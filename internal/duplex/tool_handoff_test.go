package duplex

import (
	"context"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

// Drive the loop's transitions directly: completion must release a ready
// result on the next delivery check, without waiting for a wall-clock timer.
func TestReadyTaskResultDoesNotWaitForAckGrace(t *testing.T) {
	for _, doneBeforeContinuation := range []bool{true, false} {
		name := "done_during_grace"
		if doneBeforeContinuation {
			name = "done_before_continuation"
		}
		t.Run(name, func(t *testing.T) {
			e, c, task, p := newAckHandoffTest(t)
			if doneBeforeContinuation {
				e.onTaskDone(task)
				e.continueWith(p)
			} else {
				e.continueWith(p)
				e.onTaskDone(task)
			}
			e.maybeDeliver()
			assertTaskDeliveredWithoutAck(t, e, c, task, p)
		})
	}
}

func TestReadyTaskResultWaitsForTheCallingClause(t *testing.T) {
	e, c, task, p := newAckHandoffTest(t)
	clause := Segment{TurnID: p.turn.ID, Gen: p.turn.Generation, Text: "Let me check that."}
	e.player.beginSegment(clause)
	// Even an overdue result must wait for the clause that called its tool.
	// Releasing ackPending unconditionally on completion would interrupt it.
	task.Submitted = time.Now().Add(-2 * time.Second)
	e.continueWith(p)
	e.onTaskDone(task)
	e.maybeDeliver()
	if e.tools.delivered != 0 || !e.player.Active() || e.tracker.Current() != p.turn {
		t.Fatal("the ready result interrupted the calling clause")
	}
	e.player.finishTurn(clause)
	e.drainDeferred()
	e.maybeDeliver()
	assertTaskDeliveredWithoutAck(t, e, c, task, p)
}

func newAckHandoffTest(t *testing.T) (*Engine, *collector, *tools.Task, *pendingResponse) {
	t.Helper()
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)
	e := New(Options{Cfg: cfg, Delegation: live.DelegationClient, ClientDelegation: "manual"}, Deps{Emit: c.emit})
	e.ctx = context.Background()
	t.Cleanup(e.Close)
	turn := e.tracker.Begin("Check the result.", false)
	e.commitTurn(turn)
	task := &tools.Task{
		ID: "t1", Submitted: time.Now(),
		Tool:   tools.Tool{Name: "lookup", Ack: "Please wait."},
		Result: tools.Result{Output: []byte(`{"status":"ready"}`)},
	}
	e.tools = &toolState{
		tasks:      map[string]*tools.Task{task.ID: task},
		latest:     map[string]string{task.Tool.Name: task.ID},
		ackPending: map[string]bool{task.ID: true},
	}
	p := &pendingResponse{
		turn: turn, createdAt: time.Now(), scriptedTask: task.ID, scripted: task.Tool.Ack,
		calls:   []provider.ToolCall{{ID: "call1", Name: task.Tool.Name, Arguments: `{}`}},
		outputs: map[string]string{"call1": `{"status":"running","task_id":"t1"}`},
	}
	return e, c, task, p
}

func assertTaskDeliveredWithoutAck(t *testing.T, e *Engine, c *collector, task *tools.Task, p *pendingResponse) {
	t.Helper()
	events := c.taskEvents()
	if len(events) != 2 || events[0].Status != "done" || events[1].Status != "delivered" || events[1].TaskID != task.ID {
		t.Fatalf("a ready result must be delivered without waiting for the ack timer; events: %+v", events)
	}
	resultTurn := e.tracker.Current()
	if resultTurn == p.turn || resultTurn.Scripted {
		t.Fatal("expected a result turn without an acknowledgement")
	}
	// A grace callback already scheduled before completion cannot say an ack
	// after the result has been delivered.
	e.sayAck(p)
	if e.tracker.Current() != resultTurn {
		t.Fatal("a late grace callback started an acknowledgement")
	}
}
