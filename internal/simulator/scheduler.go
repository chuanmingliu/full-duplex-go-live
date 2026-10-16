package simulator

import (
	"context"
	"errors"
	"time"
)

type pendingHangup struct {
	id                                                        string
	binding                                                   Binding
	requestedAtMS, endedAtMS                                  int64
	endConfirmed, effectsSettled, effectsSucceeded, completed bool
}

// terminalReady keeps observed termination private while its scripted stop is
// still executing. Callbacks can record evidence without waiting on that stop.
// The caller holds the controller lock.
func (r *run) terminalReady() bool {
	return r.state.Phase.Terminal() && (r.pendingHangup == nil || r.pendingHangup.effectsSettled)
}

// completePendingHangup requires both successful controls and observed native
// termination. Synchronous callbacks during Hangup cannot finish it prematurely.
// The caller holds the controller lock.
func (c *RunController) completePendingHangup(r *run) bool {
	p := r.pendingHangup
	if p == nil || p.completed || !p.effectsSucceeded || !p.endConfirmed || r.state.Phase != Completed || r.observer.ended != 1 || len(r.failures) != 0 || !exactBinding(p.binding, r.state.Binding) {
		return false
	}
	p.completed = true
	r.actions = append(r.actions, ActionOutcome{ID: p.id, Result: "completed", AtMS: p.endedAtMS, Reason: "gateway ended confirmation after scripted hangup"})
	c.record(r, "action_completed", p.id)
	return true
}

func (c *RunController) schedule(r *run, d time.Duration, f func()) {
	r.timers = append(r.timers, c.clock.AfterFunc(d, f))
}
func (c *RunController) cancelTimers(r *run) {
	for _, t := range r.timers {
		t.Stop()
	}
	r.timers = nil
	if r.customerIdle != nil {
		r.customerIdle.Stop()
		r.customerIdle = nil
	}
	r.customerAudioGeneration++
	if r.remoteIdle != nil {
		r.remoteIdle.Stop()
		r.remoteIdle = nil
	}
	r.remoteAudioGeneration++
	r.idleGeneration++
	if r.idle != nil {
		r.idle.Stop()
		r.idle = nil
	}
}
func (c *RunController) resetIdle(r *run) {
	if r.idle != nil {
		r.idle.Stop()
	}
	if r.scenario.IdleTimeoutMS <= 0 {
		return
	}
	r.idleGeneration++
	generation := r.idleGeneration
	b := r.state.Binding
	r.idle = c.clock.AfterFunc(time.Duration(r.scenario.IdleTimeoutMS)*time.Millisecond, func() {
		c.mu.Lock()
		valid := r.state.Phase == Connected && generation == r.idleGeneration
		c.mu.Unlock()
		if valid {
			_ = c.Handle(context.Background(), Event{Kind: "stop", Binding: b, AtMS: c.RelativeMS(), Reason: "inactivity limit"})
		}
	})
}
func (c *RunController) trigger(r *run, kind string) {
	r.occurrences[kind]++
	for _, a := range r.scenario.Actions {
		if a.Trigger == kind && a.Occurrence == r.occurrences[kind] && !r.actionScheduled[a.ID] {
			r.actionScheduled[a.ID] = true
			action := a
			id := r.state.Binding.RunID
			c.schedule(r, time.Duration(a.AfterMS)*time.Millisecond, func() { c.performAction(id, action) })
		}
	}
}
func (c *RunController) performAction(id string, a Action) {
	c.mu.Lock()
	r, ok := c.runs[id]
	if !ok || r.state.Phase != Connected {
		c.mu.Unlock()
		return
	}
	b := r.state.Binding
	if a.RequireRemoteSpeaking && !r.observer.assistantSpeaking {
		r.actions = append(r.actions, ActionOutcome{ID: a.ID, Result: "skipped", AtMS: c.RelativeMS(), Reason: "remote assistant is no longer speaking"})
		c.mu.Unlock()
		return
	}
	callctx := r.ctx
	r.actions = append(r.actions, ActionOutcome{ID: a.ID, Result: "requested", AtMS: c.RelativeMS()})
	c.mu.Unlock()

	var err, actionContextErr error
	if callctx.Err() != nil {
		err = callctx.Err()
	} else {
		switch a.Kind {
		case "speak":
			deadline, cancel := context.WithTimeout(callctx, time.Duration(a.MaxHoldMS)*time.Millisecond)
			err = c.opts.Voice.Speak(deadline, b, a.ID, a.Text, time.Duration(a.MaxHoldMS)*time.Millisecond)
			actionContextErr = deadline.Err()
			cancel()
		case "silence":
			deadline, cancel := context.WithTimeout(callctx, time.Duration(a.MaxHoldMS)*time.Millisecond)
			err = c.opts.Voice.(VoiceSilencer).Silence(deadline, b, a.ID, time.Duration(a.MaxHoldMS)*time.Millisecond)
			actionContextErr = deadline.Err()
			cancel()
		case "hangup":
			err = c.handle(context.Background(), Event{Kind: "stop", Binding: b, AtMS: c.RelativeMS(), Reason: "scripted hangup"}, a.ID)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		result, reason := "failed", "voice action failed"
		switch {
		case errors.Is(err, context.Canceled) && callctx.Err() != nil:
			result, reason = "cancelled", "run ended or stopped"
		case errors.Is(err, context.DeadlineExceeded) && errors.Is(actionContextErr, context.DeadlineExceeded):
			result, reason = "timed_out", "action hold deadline"
		default:
			c.addFailure(r, reason)
		}
		r.actions = append(r.actions, ActionOutcome{ID: a.ID, Result: result, AtMS: c.RelativeMS(), Reason: reason})
	}
	if r.state.Phase.Terminal() {
		if err := c.persistReport(r); err != nil {
			c.addFailure(r, "terminal action artifact update failed")
		}
	}
}
