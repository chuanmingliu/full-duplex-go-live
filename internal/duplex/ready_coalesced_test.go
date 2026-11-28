package duplex

import (
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

func TestDeliveredCoalescedContinuationDoesNotBlockNextReadyAck(t *testing.T) {
	e, _, task, pending := newAckHandoffTest(t)
	pending.turn.MarkCompleted()
	priorTask := &tools.Task{ID: "prior"}
	state := &coalescedTask{task: priorTask, delivered: true}
	covered := &pendingResponse{
		turn:           pending.turn,
		calls:          []provider.ToolCall{{ID: "covered", Name: "lookup", Arguments: `{}`}},
		outputs:        map[string]string{"covered": `{"status":"running"}`},
		coalescedCalls: map[string]*coalescedTask{"covered": state},
	}
	e.continuations = []*pendingResponse{covered, pending}
	next := e.tracker.Begin("The earlier result is being read", false)
	e.commitTurn(next)
	e.backendPending.Store(1)
	t.Cleanup(func() { e.backendPending.Store(0) })
	task.Submitted = time.Now().Add(-2 * time.Second)
	e.onTaskDone(task)
	if e.tools.delivered != 1 || e.tools.ackPending[task.ID] || len(e.continuations) != 0 {
		t.Fatal("already-delivered coalesced bookkeeping blocked a ready result")
	}
	outputs := map[string]int{}
	for _, msg := range e.history {
		if msg.Role == provider.RoleTool {
			outputs[msg.ToolCallID]++
		}
	}
	if outputs["covered"] != 1 || outputs["call1"] != 1 {
		t.Fatalf("lost or duplicated function output history: %v", outputs)
	}
}

func TestReadyAckKeepsUncoveredContinuationsInOrder(t *testing.T) {
	for _, guard := range []string{"running", "cancelled", "mixed", "incomplete", "scripted", "caller_speaking"} {
		t.Run(guard, func(t *testing.T) {
			e, _, task, pending := newAckHandoffTest(t)
			pending.turn.MarkCompleted()
			prior := &tools.Task{ID: "prior"}
			state := &coalescedTask{task: prior, delivered: true}
			covered := &pendingResponse{
				turn:           pending.turn,
				calls:          []provider.ToolCall{{ID: "covered", Name: "lookup", Arguments: `{}`}},
				outputs:        map[string]string{"covered": `{"status":"running"}`},
				coalescedCalls: map[string]*coalescedTask{"covered": state},
			}
			switch guard {
			case "running":
				state.delivered = false
				e.tools.tasks[prior.ID] = prior
			case "cancelled":
				prior.Cancel()
			case "mixed":
				covered.calls = append(covered.calls, provider.ToolCall{ID: "other", Name: "other"})
				covered.outputs["other"] = `{}`
			case "incomplete":
				delete(covered.outputs, "covered")
			case "scripted":
				covered.scripted = "An acknowledgment still belongs here."
			case "caller_speaking":
				e.userOpen = true
			}
			e.continuations = []*pendingResponse{covered, pending}
			task.Submitted = time.Now().Add(-2 * time.Second)
			e.onTaskDone(task)
			if len(e.continuations) != 2 || !e.tools.ackPending[task.ID] || e.tools.delivered != 0 || len(e.history) != 0 {
				t.Fatal("ready result retired work without an already-delivered covering task")
			}
		})
	}
}
