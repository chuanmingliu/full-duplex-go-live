package duplex

import "github.com/chuanmingliu/golive/internal/provider"

// recordContinuation keeps function calls and outputs together, even when a
// ready task makes its waiting acknowledgment unnecessary.
func (e *Engine) recordContinuation(p *pendingResponse) {
	e.history = append(e.history, provider.Message{Role: provider.RoleAssistant, ToolCalls: p.calls})
	for _, c := range p.calls {
		e.history = append(e.history, provider.Message{
			Role: provider.RoleTool, Name: c.Name, ToolCallID: c.ID, Content: p.outputs[c.ID],
		})
	}
	e.trimHistory()
}

// releaseCompletedCallingAck removes bookkeeping that otherwise waits for the
// entire conversational floor to become free. Only a playback completion of
// this calling turn can release it, unless its generation is invalid and it
// never emitted audio or retained playback permission. A quiet synthesis gap
// alone is not proof that the original clause finished. Result delivery applies
// its own caller, scripted-speech, freshness and configured-wait guards.
func (e *Engine) releaseCompletedCallingAck(taskID string) {
	ts := e.tools
	if !ts.ackPending[taskID] || len(e.continuations) == 0 {
		return
	}
	e.retireDeliveredCoalescedContinuations()
	if len(e.continuations) == 0 {
		return
	}
	p := e.continuations[0]
	if p.scripted == "" || p.scriptedTask != taskID || !p.complete() || p.turn == nil {
		return
	}
	reason := "playback_completed"
	if p.turn.Timings().CompletedAt.IsZero() {
		if !e.player.supersededWithoutAudio(p.turn, &e.gen) {
			return
		}
		reason = "superseded_without_audio"
	}
	e.continuations[0] = nil
	e.continuations = e.continuations[1:]
	e.recordContinuation(p)
	delete(ts.ackPending, taskID)
	delete(ts.ackGraceStarted, taskID)
	e.log.Info("tools: ready result no longer waits for its completed calling turn's ack", "task", taskID, "turn", p.turn.ID, "reason", reason)
}

// supersededWithoutAudio checks under the output lock so an in-flight chunk
// cannot race the first-audio check. Invalid generations cannot regain output
// permission; protect finish-sentence grace and an explicitly inherited opener.
// This releases bookkeeping only, never synthesizes a playback receipt.
func (p *Player) supersededWithoutAudio(turn *Turn, gen *Generation) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if gen.Valid(turn.Generation) || p.graceTurn == turn.ID ||
		p.drainingTurn == turn.ID || p.adoptedFrom == turn || p.adoptedTo == turn {
		return false
	}
	return turn.Timings().FirstAudioOut.IsZero()
}
