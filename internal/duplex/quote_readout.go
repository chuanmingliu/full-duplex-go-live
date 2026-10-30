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
	caller := e.tracker.Current()
	if caller == nil || !e.tracker.IsCurrent(caller) || caller.ID != origin.turnID || caller.Opened == "" || caller.Scripted {
		return
	}
	timing := caller.Timings()
	if origin.speechEndAt.IsZero() || !timing.SpeechEndAt.Equal(origin.speechEndAt) || timing.FirstAudioOut.IsZero() || timing.CompletedAt.IsZero() {
		return
	}
	return "", readout, true
}
