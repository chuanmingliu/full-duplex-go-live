package duplex

import (
	"math/rand/v2"
	"time"
)

type pendingEventResponse struct {
	event    string
	text     string
	revision int
	index    int
	received time.Time
}

// Callback speech uses the same player, transcript and guarded barge-in path
// as preset speech. MCP I/O never runs on the audio loop.
func (e *Engine) prepareEventResponse(event string, resume *Turn) {
	if e.skills == nil {
		e.resumeBusinessResponse(resume)
		return
	}
	pending := &pendingEventResponse{event: event, received: time.Now()}
	e.eventResponse = pending
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		response, err := e.skills.EventResponse(e.ctx, event)
		e.post(func() {
			if e.ctx.Err() != nil || e.eventResponse != pending {
				return
			}
			status := "ready"
			if err != nil {
				status = "unavailable"
			} else if !response.Enabled {
				status = "disabled"
			}
			e.log.Info("business: response prepared", "event", event, "status", status,
				"revision", response.Revision, "lookup_ms", time.Since(pending.received).Milliseconds())
			if status != "ready" {
				e.eventResponse = nil
				e.resumeBusinessResponse(resume)
				return
			}
			pending.index = rand.IntN(len(response.Templates))
			pending.text = response.Templates[pending.index]
			pending.revision = response.Revision
			e.maybeEventResponse()
		})
	}()
}

func (e *Engine) resumeBusinessResponse(turn *Turn) {
	if turn != nil && e.tracker.IsCurrent(turn) && !e.userOpen && e.hold == nil && !e.hangingUp() {
		e.startBackend(turn, "business_event")
	}
}

func (e *Engine) maybeEventResponse() {
	pending := e.eventResponse
	if pending == nil || pending.text == "" || e.tools == nil || e.ctx.Err() != nil {
		return
	}
	if e.hangingUp() {
		e.eventResponse = nil
		return
	}
	if e.muted || (e.tools.lastCallerAt.IsZero() && e.tools.lastQuietAt.IsZero()) || e.silenceReactionBusy() {
		return
	}
	// Consume before speech starts. A retry or interruption cannot repeat it.
	e.eventResponse = nil
	e.sayOnLoop(pending.text)
	e.log.Info("business: response started", "event", pending.event, "revision", pending.revision,
		"template_index", pending.index, "turn", e.tracker.Current().ID,
		"wait_ms", time.Since(pending.received).Milliseconds())
}
