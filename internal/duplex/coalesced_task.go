package duplex

import "github.com/chuanmingliu/golive/internal/tools"

const maxCoalescedOrigins = 32

// Coalesced metadata moves with a task into its queued delivery. Pending
// continuations also retain this object, so actual delivery remains provable
// after the queue entry is removed, without keeping a session-wide archive.
type coalescedTask struct {
	task      *tools.Task
	origins   []taskOrigin
	delivered bool
}

// noteCoalescedTask is called only after the existing exact args, memory and
// repeat-window match. A recent entry can outlive delivery; that is not proof
// that its old result will answer a newly arrived caller.
func (e *Engine) noteCoalescedTask(id string, caller *Turn) *coalescedTask {
	ts := e.tools
	var state *coalescedTask
	var primary taskOrigin
	if task := ts.tasks[id]; task != nil && !task.Cancelled() {
		if ts.coalesced == nil {
			ts.coalesced = map[string]*coalescedTask{}
		}
		state = ts.coalesced[id]
		if state == nil {
			state = &coalescedTask{task: task}
			ts.coalesced[id] = state
		}
		primary = ts.origins[id]
	} else {
		for i := range ts.deliveries {
			delivery := &ts.deliveries[i]
			if delivery.task.ID != id || delivery.task.Cancelled() {
				continue
			}
			if delivery.coalesced == nil {
				delivery.coalesced = &coalescedTask{task: delivery.task}
			}
			state, primary = delivery.coalesced, delivery.origin
			break
		}
	}
	if state == nil || caller == nil || caller.ID == "" || caller.ID == primary.turnID || primary.turnID == "" || primary.speechEndAt.IsZero() {
		return state
	}
	speechEnd := caller.Timings().SpeechEndAt
	if speechEnd.IsZero() || len(state.origins) >= maxCoalescedOrigins {
		return state
	}
	for _, origin := range state.origins {
		if origin.turnID == caller.ID {
			return state
		}
	}
	state.origins = append(state.origins, taskOrigin{turnID: caller.ID, speechEndAt: speechEnd})
	return state
}

func (e *Engine) completedCoalescedOpener(state *coalescedTask) bool {
	if state != nil {
		for _, origin := range state.origins {
			if e.completedOriginOpener(origin) {
				return true
			}
		}
	}
	return false
}

func (e *Engine) deliverCoalescedTask(delivery taskDelivery, turn *Turn) {
	state := delivery.coalesced
	if state == nil {
		return
	}
	state.delivered = true
	for _, origin := range state.origins {
		e.log.Info("tools: delivering a coalesced task result",
			"tool", delivery.task.Tool.Name, "task", delivery.task.ID, "turn", turn.ID,
			"origin_turn", origin.turnID, "primary_origin_turn", delivery.origin.turnID,
			"origin_to_result_start_ms", turn.Timings().StartedAt.Sub(origin.speechEndAt).Milliseconds())
	}
	state.origins = nil // continuations need delivery proof, not caller timings
}

func (e *Engine) discardCoalescedTask(id string) {
	if state := e.tools.coalesced[id]; state != nil {
		state.origins = nil
	}
	delete(e.tools.coalesced, id)
}

func (e *Engine) clearCoalescedTasks() {
	for id := range e.tools.coalesced {
		e.discardCoalescedTask(id)
	}
	for _, delivery := range e.tools.deliveries {
		if delivery.coalesced != nil {
			delivery.coalesced.origins = nil
		}
	}
}

func (p *pendingResponse) noteCoalescedCall(callID string, state *coalescedTask) {
	if state == nil {
		return
	}
	if p.coalescedCalls == nil {
		p.coalescedCalls = map[string]*coalescedTask{}
	}
	p.coalescedCalls[callID] = state
}

// Every call must be answered by the same task, and that task must still be
// owned or have actually been delivered. A cancelled or discarded result is
// insufficient; mixed function responses retain their usual continuation.
func (e *Engine) coveringCoalescedTask(p *pendingResponse) *tools.Task {
	if e.tools == nil || len(p.calls) == 0 || !p.complete() {
		return nil
	}
	var state *coalescedTask
	for _, call := range p.calls {
		candidate := p.coalescedCalls[call.ID]
		if candidate == nil || state != nil && candidate != state {
			return nil
		}
		state = candidate
	}
	if state.task.Cancelled() {
		return nil
	}
	if state.delivered || e.tools.tasks[state.task.ID] == state.task {
		return state.task
	}
	for _, delivery := range e.tools.deliveries {
		if delivery.task == state.task && delivery.coalesced == state {
			return state.task
		}
	}
	return nil
}
