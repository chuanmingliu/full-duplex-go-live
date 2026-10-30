package duplex

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

// Drive the loop's transitions directly: completion must release a ready
// result from the event that makes it ready, without another periodic tick.
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
			assertTaskDeliveredWithoutAck(t, e, c, task, p)
		})
	}
}

func TestReadyTaskResultKeepsDeliveryGuards(t *testing.T) {
	for _, guard := range []string{"caller", "muted", "scripted_ack", "hangup", "closed_context"} {
		t.Run(guard, func(t *testing.T) {
			e, c, task, p := newAckHandoffTest(t)
			e.continueWith(p)
			var clause Segment
			switch guard {
			case "caller":
				e.userOpen = true
			case "muted":
				e.muted = true
			case "scripted_ack":
				p.turn.Scripted = true
				clause = Segment{TurnID: p.turn.ID, owner: p.turn, Gen: p.turn.Generation, Text: "Please wait."}
				e.player.beginSegment(&e.gen, clause)
				task.Submitted = time.Now().Add(-2 * time.Second)
			case "hangup":
				e.tools.endCall = &endCallRequest{at: time.Now()}
			case "closed_context":
				ctx, cancel := context.WithCancel(e.ctx)
				cancel()
				e.ctx = ctx
			}
			e.onTaskDone(task)
			if e.tools.delivered != 0 {
				t.Fatal("task delivery bypassed its guard")
			}
			if guard == "hangup" || guard == "closed_context" {
				return
			}
			e.userOpen, e.muted = false, false
			if guard == "scripted_ack" {
				e.player.finishTurn(&e.gen, clause)
			} else {
				e.wake()
			}
			e.runPosted()
			assertTaskDeliveredWithoutAck(t, e, c, task, p)
		})
	}
}

func TestTaskResultLogsOriginalMonotonicOffsetForCachedOutput(t *testing.T) {
	e, _, task, p := newAckHandoffTest(t)
	var logs bytes.Buffer
	e.log = slog.New(slog.NewJSONHandler(&logs, nil))
	origin := time.Now().Add(-time.Second)
	p.turn.MarkSpeechEnd(origin, 0, 380)
	e.tools.origins = map[string]taskOrigin{task.ID: {turnID: p.turn.ID, speechEndAt: origin}}
	e.speechEndAt = time.Now() // an unrelated, newer caller boundary cannot replace the origin
	e.onTaskDone(task)
	if len(e.tools.origins) != 0 || len(e.tools.deliveries) != 1 {
		t.Fatal("completion did not move the origin from running task to pending delivery")
	}
	e.continueWith(p)
	result := e.tracker.Current()
	if result == p.turn || !result.Timings().SpeechEndAt.IsZero() || len(e.tools.deliveries) != 0 {
		t.Fatal("result did not remain a separate non-speech turn after delivery")
	}
	// A cache hit at the result's start is legitimately zero on that turn's
	// clock. The logged origin offset preserves the caller's nonzero wait.
	result.mu.Lock()
	result.firstAudioOut = result.startedAt
	result.mu.Unlock()
	e.emitMetrics(result, 20, false)
	var delivery, metric map[string]any
	decoder := json.NewDecoder(&logs)
	for decoder.More() {
		var row map[string]any
		if err := decoder.Decode(&row); err != nil {
			t.Fatal(err)
		}
		switch row["msg"] {
		case "tools: delivering a task's result":
			delivery = row
		case "turn: response latency":
			metric = row
		}
	}
	want := result.Timings().StartedAt.Sub(origin).Milliseconds()
	if delivery["origin_turn"] != p.turn.ID || delivery["turn"] != result.ID ||
		delivery["origin_to_result_start_ms"] != float64(want) || want < 1000 {
		t.Fatalf("wrong caller origin: %v, expected offset %d", delivery, want)
	}
	if metric["response_to_speech"] != false || metric["first_content_present"] != true || metric["first_content_ms"] != float64(0) {
		t.Fatalf("cached child metrics changed their origin or lost measured zero: %v", metric)
	}
}

// These workers never finish spontaneously; cancellation releases them without
// posting a result, so submission/supersession can be examined deterministically.
type handoffBlockedExecutor struct{}

func (handoffBlockedExecutor) Execute(ctx context.Context, _ tools.Tool, _ tools.Request) tools.Result {
	<-ctx.Done()
	return tools.Result{Err: ctx.Err()}
}

func TestTaskOriginMetadataFollowsTaskLifetime(t *testing.T) {
	e, _, _, p := newAckHandoffTest(t)
	ts := e.tools
	clear(ts.tasks)
	clear(ts.latest)
	clear(ts.ackPending)
	ts.pool = tools.NewPool(handoffBlockedExecutor{}, 2)
	p.turn.MarkSpeechEnd(time.Now().Add(-time.Second), 0, 380)
	tool := tools.Tool{Name: "lookup", Ack: "Please wait."}
	submit := func(turn *Turn, args string) string {
		_, id := e.submitTask(backendJob{turn: turn}, provider.ToolCall{ID: args, Name: tool.Name, Arguments: args}, tool)
		return id
	}
	first := submit(p.turn, `{"n":1}`)
	old := ts.tasks[first]
	origin := ts.origins[first]
	origin.ackTurnID = "old_ack"
	origin.ackCompleted = true
	ts.origins[first] = origin
	ts.ackPending[first] = true
	ts.ackGraceStarted = map[string]bool{first: true}
	second := submit(p.turn, `{"n":2}`)
	if !old.Cancelled() || len(ts.origins) != 1 || ts.origins[second].turnID != p.turn.ID ||
		ts.origins[second].ackTurnID != "" || ts.origins[second].ackCompleted ||
		ts.ackPending[first] || ts.ackGraceStarted[first] {
		t.Fatal("supersession retained the old task's origin or acknowledgement")
	}
	// A non-caller task cannot borrow the engine's latest speech origin.
	third := submit(e.tracker.Begin("", false), `{"n":3}`)
	if len(ts.origins) != 0 || ts.tasks[third] == nil {
		t.Fatal("a task without a caller origin inherited one")
	}
	submit(p.turn, `{"n":4}`)
	e.closeTools()
	if len(ts.tasks)+len(ts.origins)+len(ts.deliveries)+len(ts.ackPending)+len(ts.ackGraceStarted) != 0 {
		t.Fatal("shutdown retained task metadata")
	}
	e.onTaskDone(old)
	if len(ts.deliveries) != 0 {
		t.Fatal("a late superseded completion was accepted after shutdown")
	}
}

func TestReadyTaskResultWaitsForTheCallingClause(t *testing.T) {
	e, c, task, p := newAckHandoffTest(t)
	clause := Segment{TurnID: p.turn.ID, owner: p.turn, Gen: p.turn.Generation, Text: "Let me check that."}
	e.player.beginSegment(&e.gen, clause)
	// Even an overdue result must wait for the clause that called its tool.
	// Releasing ackPending unconditionally on completion would interrupt it.
	task.Submitted = time.Now().Add(-2 * time.Second)
	e.continueWith(p)
	e.onTaskDone(task)
	e.maybeDeliver()
	if e.tools.delivered != 0 || !e.player.Active() || e.tracker.Current() != p.turn {
		t.Fatal("the ready result interrupted the calling clause")
	}
	e.player.finishTurn(&e.gen, clause)
	e.runPosted() // the player's completion callback, with no periodic tick
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
