package duplex

import "github.com/chuanmingliu/golive/internal/tools"

type speechInput struct {
	text    string
	fixed   bool
	readout bool
}

// PushFixed preserves a complete prefix's boundary. In particular a posthook
// must never share its output timestamp with the readout that follows it.
func (p *speechPipe) PushFixed(text string, readout bool) {
	if text != "" {
		p.push(speechInput{text: text, fixed: true, readout: readout})
	}
}

// outputObserver captures the segment's role now, not when queued audio is
// eventually emitted and the pipe may already be processing model text.
func (p *speechPipe) outputObserver() func() {
	readout := p.readout
	return func() {
		p.turn.MarkFirstContentOut()
		if readout {
			p.turn.MarkFirstReadoutOut()
		}
	}
}

func (e *Engine) quoteResultPrefix(delivery taskDelivery, version int, busy bool) (opening, readout string, skipped bool) {
	task := delivery.task
	if task.Result.Err != nil || task.Request.MemoryVersion != version {
		return
	}
	opening = task.Tool.OpeningFor(task.Result.Output)
	if opening == "" {
		return
	}
	readout = task.Tool.ReadoutFor(task.Result.Output)
	if tools.ResultIsDefault(task.Result.Output) {
		return "", readout, true // preserve the existing precomputed-quote rule
	}
	if busy || e.hold != nil || opening != tools.DefaultQuotePosthook || readout == "" || tools.ResultStatus(task.Result.Output) != "quote" {
		return
	}
	origin := delivery.origin
	if e.completedOriginOpener(origin) {
		return "", readout, true
	}
	ack := e.tracker.Current()
	if origin.ackCompleted && ack != nil && ack.Scripted && ack.ID == origin.ackTurnID && e.tracker.IsCurrent(ack) {
		timing := ack.Timings()
		if !timing.FirstAudioOut.IsZero() && !timing.CompletedAt.IsZero() {
			return "", readout, true
		}
	}
	return
}

// noteAckPlayback records only natural completion as permission to omit the
// posthook. The task may have finished while its ack still held the floor, so
// update both running origins and the immutable-origin copies queued to speak.
// Called on the engine loop, before a completion can release result delivery.
func (e *Engine) noteAckPlayback(turnID string, natural bool) {
	if e.tools == nil {
		return
	}
	update := func(origin *taskOrigin) {
		if origin.ackTurnID != turnID {
			return
		}
		origin.ackCompleted = natural
		if !natural {
			origin.ackTurnID = "" // a later stale completion cannot restore proof
		}
	}
	for id, origin := range e.tools.origins {
		update(&origin)
		e.tools.origins[id] = origin
	}
	for i := range e.tools.deliveries {
		update(&e.tools.deliveries[i].origin)
	}
}

// completedOriginOpener must run before an ack replaces the caller's turn:
// Begin invalidates its generation, so later delivery needs the explicit link.
func (e *Engine) completedOriginOpener(origin taskOrigin) bool {
	caller := e.tracker.Current()
	if caller == nil || !e.tracker.IsCurrent(caller) || caller.ID != origin.turnID || caller.Opened == "" || caller.Scripted {
		return false
	}
	timing := caller.Timings()
	return !origin.speechEndAt.IsZero() && timing.SpeechEndAt.Equal(origin.speechEndAt) &&
		!timing.FirstAudioOut.IsZero() && !timing.CompletedAt.IsZero() && timing.CompletedNaturally
}
