package duplex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

func coalescedQuote(t *testing.T) (*Engine, *tools.Task, *pendingResponse, *Turn) {
	t.Helper()
	e, _, task, original := readyQuoteHandoff(t)
	speech, err := tools.ParseQuoteSpeech([]byte(tools.DefaultQuoteSpeech))
	if err != nil {
		t.Fatal(err)
	}
	task.Tool = task.Tool.WithSpeech(speech)
	task.Result.Output = []byte(`{"status":"quote","total":100,"lines":[]}`)
	e.tools.byName = map[string]tools.Tool{task.Tool.Name: task.Tool}
	e.tools.recent = map[string]recentCall{task.Tool.Name: {id: task.ID, args: `{}`, at: time.Now()}}
	followup := e.tracker.Begin("The same quotation, please.", false)
	e.commitTurn(followup)
	followup.MarkSpeechEnd(time.Now().Add(-100*time.Millisecond), 0, 380)
	followup.Opened = "I am checking that."
	return e, task, original, followup
}

func foldQuote(e *Engine, task *tools.Task, turn *Turn) {
	e.submitTask(backendJob{turn: turn}, provider.ToolCall{ID: "repeat", Name: task.Tool.Name, Arguments: `{}`}, task.Tool)
}

func TestCoalescedQuoteRetainsBothOriginsAndSkipsOnlyRedundantPosthook(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "queued"}[queued], func(t *testing.T) {
			e, task, original, followup := coalescedQuote(t)
			var logs bytes.Buffer
			e.log = slog.New(slog.NewJSONHandler(&logs, nil))
			if queued {
				e.onTaskDone(task)
			}
			foldQuote(e, task, followup)
			foldQuote(e, task, followup) // one caller origin despite repeated tool calls
			if !queued {
				e.onTaskDone(task)
			}
			followup.MarkFirstAudioOut()
			followup.markCompletedNaturally()
			e.speechEndAt = time.Now().Add(-time.Hour) // not either caller's clock
			delete(e.tools.ackPending, task.ID)
			e.maybeDeliver()
			result := e.tracker.Current()
			if result.Opening != "" || result.Readout == "" || !result.QuoteResult {
				t.Errorf("completed coalesced opener retained redundant posthook or lost readout: opening=%q readout=%q", result.Opening, result.Readout)
			}
			if e.tools.delivered != 1 || len(e.tools.deliveries) != 0 {
				t.Fatal("coalescing did not produce exactly one result")
			}
			counts := map[string]int{}
			decoder := json.NewDecoder(&logs)
			for decoder.More() {
				var row map[string]any
				if err := decoder.Decode(&row); err != nil {
					t.Fatal(err)
				}
				var origin *Turn
				switch row["msg"] {
				case "tools: delivering a task's result":
					origin = original.turn
				case "tools: delivering a coalesced task result":
					origin = followup
					if row["primary_origin_turn"] != original.turn.ID {
						t.Errorf("lost primary origin: %v", row)
					}
				default:
					continue
				}
				counts[origin.ID]++
				want := result.Timings().StartedAt.Sub(origin.Timings().SpeechEndAt).Milliseconds()
				if row["origin_turn"] != origin.ID || row["turn"] != result.ID || row["origin_to_result_start_ms"] != float64(want) || row["task"] != task.ID || row["tool"] != task.Tool.Name {
					t.Errorf("wrong origin relationship: %v, expected offset %d", row, want)
				}
			}
			if counts[original.turn.ID] != 1 || counts[followup.ID] != 1 {
				t.Fatalf("expected one original and one additional relationship: %v", counts)
			}
		})
	}
}

func TestCoalescedContinuationDoesNotResumeAfterItsResult(t *testing.T) {
	e, task, original, followup := coalescedQuote(t)
	e.userOpen = true
	e.continueWith(original)
	e.onTaskDone(task)
	e.onServerCalls(backendJob{turn: followup}, []provider.ToolCall{{ID: "followup", Name: task.Tool.Name, Arguments: `{}`}})
	followup.MarkFirstAudioOut()
	followup.markCompletedNaturally()
	e.userOpen = false
	e.drainDeferred() // primary continuation releases the already-finished result
	result := e.tracker.Current()
	if e.tools.delivered != 1 || result == followup {
		t.Fatal("original continuation failed to deliver result")
	}
	result.markCompletedNaturally()
	e.drainDeferred()
	if e.tracker.Current() != result || len(e.continuations) != 0 {
		t.Error("folded follow-up resumed the backend after its result")
	}
	calls, outputs := 0, 0
	for _, msg := range e.history {
		for _, call := range msg.ToolCalls {
			if call.ID == "followup" {
				calls++
			}
		}
		if msg.Role == provider.RoleTool && msg.ToolCallID == "followup" {
			outputs++
		}
	}
	if calls != 1 || outputs != 1 {
		t.Fatalf("folded call lost its complete function history: calls=%d outputs=%d", calls, outputs)
	}
}

func TestCoalescedPosthookRequiresCurrentNaturalOpener(t *testing.T) {
	for _, guard := range []string{"eligible", "unheard", "unfinished", "truncated", "invalid_generation", "different_current", "different_speech_end", "scripted", "no_opener", "busy", "hold", "memory_changed", "failed", "custom_prefix", "no_readout", "not_quote"} {
		t.Run(guard, func(t *testing.T) {
			e, task, _, followup := coalescedQuote(t)
			foldQuote(e, task, followup)
			e.onTaskDone(task)
			followup.MarkFirstAudioOut()
			followup.markCompletedNaturally()
			busy := false
			switch guard {
			case "unheard":
				followup.firstAudioOut = time.Time{}
			case "unfinished":
				followup.completedAt = time.Time{}
			case "truncated":
				followup.completedNaturally = false
			case "invalid_generation":
				e.gen.Bump()
			case "different_current":
				e.tracker.Begin("Another question", false)
			case "different_speech_end":
				followup.speechEndAt = time.Now()
			case "scripted":
				followup.Scripted = true
			case "no_opener":
				followup.Opened = ""
			case "busy":
				busy = true
			case "hold":
				e.hold = &floorHold{}
			case "memory_changed":
				task.Request.MemoryVersion = 1
			case "failed":
				task.Result.Err = errors.New("quote failed")
			case "custom_prefix":
				task.Tool.ResultOpening = "These figures exclude taxes."
			case "no_readout":
				task.Tool.Speech = nil
			case "not_quote":
				task.Result.Output = []byte(`{"status":"need"}`)
			}
			opening, readout, skipped := e.quoteResultPrefix(e.tools.deliveries[0], 0, busy)
			if guard == "eligible" {
				if opening != "" || readout == "" || !skipped {
					t.Fatal("eligible coalesced opener retained generic posthook")
				}
			} else if skipped || (guard != "memory_changed" && guard != "failed" && opening != task.Tool.OpeningFor(task.Result.Output)) {
				t.Fatalf("coalesced origin bypassed %s guard", guard)
			}
		})
	}
}

func TestCoalescedOriginsAreBoundedAndFollowTaskLifetime(t *testing.T) {
	e, task, original, followup := coalescedQuote(t)
	primary := e.tools.origins[task.ID]
	foldQuote(e, task, followup)
	state := e.tools.coalesced[task.ID]
	if state == nil || len(state.origins) != 1 {
		t.Fatal("running task did not retain its coalesced origin")
	}
	foldQuote(e, task, followup)
	foldQuote(e, task, original.turn)
	foldQuote(e, task, e.tracker.Begin("no speech boundary", false))
	if len(state.origins) != 1 || e.tools.origins[task.ID] != primary {
		t.Fatal("duplicate/non-caller fold changed origins")
	}
	for n := 0; n < maxCoalescedOrigins+5; n++ {
		turn := e.tracker.Begin(fmt.Sprintf("follow-up %d", n), false)
		turn.MarkSpeechEnd(time.Now(), 0, 380)
		foldQuote(e, task, turn)
	}
	if len(state.origins) != maxCoalescedOrigins {
		t.Fatalf("coalesced origin bound not respected: %d", len(state.origins))
	}
	e.onTaskDone(task)
	if len(e.tools.coalesced) != 0 || e.tools.deliveries[0].coalesced != state || e.tools.deliveries[0].origin != primary {
		t.Fatal("completion did not move metadata with unchanged primary origin")
	}
	delete(e.tools.ackPending, task.ID)
	e.maybeDeliver()
	if len(state.origins) != 0 || !state.delivered {
		t.Fatal("delivery retained caller timings or lost its delivery proof")
	}
	_, ackID, after := e.submitTask(backendJob{turn: followup}, provider.ToolCall{ID: "after", Name: task.Tool.Name, Arguments: `{}`}, task.Tool)
	if after != nil || ackID != "" || len(e.tools.coalesced) != 0 || e.tools.delivered != 1 {
		t.Fatal("a fold after delivery linked to an already-delivered result or rearmed an ack")
	}
}

func TestCoalescedContinuationKeepsMixedCallsAndDroppedResults(t *testing.T) {
	for _, condition := range []string{"running", "queued", "delivered", "same_task_calls", "mixed", "different_tasks", "cancelled", "queued_closed", "dropped", "closed"} {
		t.Run(condition, func(t *testing.T) {
			e, task, _, followup := coalescedQuote(t)
			e.userOpen = true
			e.onServerCalls(backendJob{turn: followup}, []provider.ToolCall{{ID: "followup", Name: task.Tool.Name, Arguments: `{}`}})
			if len(e.continuations) != 1 {
				t.Fatal("follow-up was not deferred")
			}
			p := e.continuations[0]
			state := p.coalescedCalls["followup"]
			if state == nil || p.scriptedTask != "" || p.scripted != "" {
				t.Fatal("fold did not retain explicit coverage or rearmed scripted acknowledgement")
			}
			switch condition {
			case "queued", "delivered", "dropped", "queued_closed":
				e.onTaskDone(task)
			case "cancelled":
				task.Cancel()
			case "closed":
				e.closeTools()
			case "same_task_calls", "mixed", "different_tasks":
				p.calls = append(p.calls, provider.ToolCall{ID: "other", Name: "other_tool"})
				p.outputs["other"] = `{"ok":true}`
				if condition == "different_tasks" {
					p.coalescedCalls["other"] = &coalescedTask{task: &tools.Task{ID: "other"}, delivered: true}
				} else if condition == "same_task_calls" {
					p.coalescedCalls["other"] = state
				}
			}
			if condition == "queued_closed" {
				e.closeTools()
			}
			if condition == "dropped" {
				e.tools.deliveries = nil
			}
			e.userOpen = false
			if condition == "delivered" {
				delete(e.tools.ackPending, task.ID)
				e.maybeDeliver()
			}
			before := e.tracker.Current()
			e.drainDeferred()
			wantSkip := condition == "running" || condition == "queued" || condition == "delivered" || condition == "same_task_calls"
			if (e.tracker.Current() == before) != wantSkip {
				t.Fatalf("wrong continuation decision for %s", condition)
			}
			if (condition == "closed" || condition == "queued_closed") && (len(e.tools.coalesced) != 0 || len(state.origins) != 0) {
				t.Fatal("close retained coalesced caller metadata")
			}
		})
	}
}

func TestCoalescedOriginsAreClearedOnQueuedShutdown(t *testing.T) {
	e, task, _, followup := coalescedQuote(t)
	foldQuote(e, task, followup)
	state := e.tools.coalesced[task.ID]
	e.onTaskDone(task)
	e.closeTools()
	if len(e.tools.coalesced) != 0 || len(e.tools.deliveries) != 0 || len(state.origins) != 0 || state.delivered {
		t.Fatal("shutdown retained a queued origin or fabricated a delivery")
	}
}

func TestCoalescedOriginsRequireExactLiveRepeat(t *testing.T) {
	for _, change := range []string{"arguments", "argument_whitespace", "memory", "expired"} {
		t.Run(change, func(t *testing.T) {
			e, _, _, original := newAckHandoffTest(t)
			clear(e.tools.tasks)
			clear(e.tools.latest)
			clear(e.tools.ackPending)
			e.tools.pool = tools.NewPool(handoffBlockedExecutor{}, 2)
			original.turn.MarkSpeechEnd(time.Now().Add(-time.Second), 0, 380)
			tool := tools.Tool{Name: "quote_total", Async: true}
			call := provider.ToolCall{ID: "original", Name: tool.Name, Arguments: `{"n":1}`}
			_, id, _ := e.submitTask(backendJob{turn: original.turn}, call, tool)
			first := e.tools.tasks[id]
			followup := e.tracker.Begin("repeat", false)
			followup.MarkSpeechEnd(time.Now(), 0, 380)
			_, _, state := e.submitTask(backendJob{turn: followup}, call, tool)
			if state == nil || len(state.origins) != 1 {
				t.Fatal("exact running repeat was not coalesced")
			}
			switch change {
			case "arguments":
				call.Arguments = `{"n":2}`
			case "argument_whitespace":
				call.Arguments = `{"n": 1}`
			case "memory":
				e.tools.memory = tools.NewMemory(nil)
				if _, err := e.tools.memory.Patch([]byte(`{"n":2}`)); err != nil {
					t.Fatal(err)
				}
			case "expired":
				recent := e.tools.recent[tool.Name]
				recent.at = time.Now().Add(-repeatWindow - time.Second)
				e.tools.recent[tool.Name] = recent
			}
			_, nextID, nextState := e.submitTask(backendJob{turn: followup}, call, tool)
			if nextState != nil || nextID == id || e.tools.async != 2 || !first.Cancelled() || len(e.tools.coalesced) != 0 || len(state.origins) != 0 {
				t.Fatal("changed request coalesced or supersession retained old caller metadata")
			}
		})
	}
}

type coalescedExecutor struct {
	release chan struct{}
	calls   atomic.Int32
}

func (x *coalescedExecutor) Execute(ctx context.Context, _ tools.Tool, _ tools.Request) tools.Result {
	x.calls.Add(1)
	select {
	case <-x.release:
		return tools.Result{Output: []byte(`{"status":"quote","total":100,"lines":[]}`)}
	case <-ctx.Done():
		return tools.Result{Err: ctx.Err()}
	}
}

func TestCoalescedQuoteExecutesAndDeliversOnlyOnce(t *testing.T) {
	e, _, fixture, original := readyQuoteHandoff(t)
	clear(e.tools.tasks)
	clear(e.tools.latest)
	clear(e.tools.origins)
	clear(e.tools.ackPending)
	speech, err := tools.ParseQuoteSpeech([]byte(tools.DefaultQuoteSpeech))
	if err != nil {
		t.Fatal(err)
	}
	tool := fixture.Tool.WithSpeech(speech)
	e.tools.byName = map[string]tools.Tool{tool.Name: tool}
	executor := &coalescedExecutor{release: make(chan struct{})}
	e.tools.pool = tools.NewPool(executor, 1)
	e.userOpen = true
	e.onServerCalls(backendJob{turn: original.turn}, []provider.ToolCall{{ID: "primary", Name: tool.Name, Arguments: `{}`}})
	var task *tools.Task
	for _, running := range e.tools.tasks {
		task = running
	}
	if task == nil {
		t.Fatal("primary call did not submit a task")
	}
	t.Cleanup(task.Cancel)
	close(executor.release)
	select {
	case <-task.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("task did not finish")
	}
	e.onTaskDone(task) // keep deterministic loop ordering; duplicate callback is ignored
	followup := e.tracker.Begin("Same quote, please", false)
	e.commitTurn(followup)
	followup.MarkSpeechEnd(time.Now(), 0, 380)
	followup.Opened = "Checking."
	e.onServerCalls(backendJob{turn: followup}, []provider.ToolCall{{ID: "followup", Name: tool.Name, Arguments: `{}`}})
	followup.MarkFirstAudioOut()
	followup.markCompletedNaturally()
	e.userOpen = false
	e.drainDeferred()
	result := e.tracker.Current()
	result.markCompletedNaturally()
	e.drainDeferred()
	if executor.calls.Load() != 1 || e.tools.async != 1 || e.tools.delivered != 1 || len(e.continuations) != 0 || e.tracker.Current() != result || result.Opening != "" || result.Readout == "" {
		t.Fatalf("coalescing duplicated work or delayed the result: executions=%d async=%d delivered=%d opening=%q", executor.calls.Load(), e.tools.async, e.tools.delivered, result.Opening)
	}
}
