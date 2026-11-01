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
// this calling turn can release it; a newer turn or a quiet synthesis gap is
// not proof that the original clause finished. Result delivery still applies
// its own caller, scripted-speech, freshness and configured-wait guards.
func (e *Engine) releaseCompletedCallingAck(taskID string) {
	ts := e.tools
	if !ts.ackPending[taskID] || len(e.continuations) == 0 {
		return
	}
	p := e.continuations[0]
	if p.scripted == "" || p.scriptedTask != taskID || !p.complete() ||
		p.turn == nil || p.turn.Timings().CompletedAt.IsZero() {
		return
	}
	e.continuations[0] = nil
	e.continuations = e.continuations[1:]
	e.recordContinuation(p)
	delete(ts.ackPending, taskID)
	delete(ts.ackGraceStarted, taskID)
	e.log.Info("tools: ready result no longer waits for its completed calling turn's ack", "task", taskID, "turn", p.turn.ID)
}
