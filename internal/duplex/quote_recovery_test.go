package duplex

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

const recoveryReadout = "三者险50万，保费808元，合计3973元；三者险100万，保费1093元，合计4258元；三者险300万，保费1615元，合计4780元。"

func recoveryEngine(t *testing.T) (*Engine, *Turn) {
	t.Helper()
	cfg := config.Default()
	cfg.Duplex.QuoteInterruptionRecovery = true
	e := &Engine{opts: Options{Cfg: cfg}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), tools: &toolState{memory: tools.NewMemory(nil)}, recordedUser: map[string]bool{}}
	_, err := e.tools.memory.Patch([]byte(`{"quote":{"三者险":"200万"}}`))
	if err != nil {
		t.Fatal(err)
	}
	snap, v := e.memorySnapshot()
	task := &tools.Task{ID: "quote-1", Tool: tools.Tool{Name: "quote_total"}, Request: tools.Request{Memory: snap, MemoryVersion: v}, Result: tools.Result{Output: []byte(`{"status":"quote","total":4543,"selection":{"三者险":"200万"}}`)}}
	turn := &Turn{ID: "readout", QuoteResult: true, Readout: recoveryReadout}
	e.startQuoteDelivery(task, turn)
	e.quoteRecoveryPrompt(turn)
	return e, turn
}

func TestQuoteRecoveryPartialAmountAndRepeatedInterruption(t *testing.T) {
	e, first := recoveryEngine(t)
	e.noteQuotePlayback(first, PlaybackReport{SpokenText: "保费算好了。三者险50万，保费808元，合计3973元；三者险100万，保费10"}, true)
	d := e.tools.quoteDelivery
	if !d.sections[0].Complete || d.sections[1].Complete || d.status != "interrupted" {
		t.Fatalf("partial number advanced section: %+v", d)
	}
	e.history = nil
	next := &Turn{ID: "clarification", Transcript: "医保外用药是什么意思？"}
	req := e.snapshotMessages(next, false)
	if !strings.Contains(req[len(req)-1].Content, "1093元") || !strings.Contains(req[len(req)-1].Content, `"complete":false`) {
		t.Fatal("quote lost after history trim")
	}
	e.noteQuotePlayback(next, PlaybackReport{SpokenText: "这是保障范围的解释。三者险100万，保费1093元，合计4258元；三者险300万"}, true)
	if !d.sections[1].Complete || d.sections[2].Complete || d.interruptions != 2 {
		t.Fatalf("second interruption lost progress: %+v", d)
	}
	last := &Turn{ID: "continue"}
	e.quoteRecoveryPrompt(last)
	e.noteQuotePlayback(last, PlaybackReport{SpokenText: d.sections[2].Text}, false)
	if d.status != "complete" {
		t.Fatalf("continuation did not finish: %+v", d)
	}
}

func TestQuoteRecoveryControlAndStaleReceipt(t *testing.T) {
	e, old := recoveryEngine(t)
	control := &Turn{ID: "control"}
	apply := func(args string) string { return e.applyQuoteDeliveryCall(provider.ToolCall{Arguments: args}, control) }
	for _, action := range []string{"pause", "cancel"} {
		if got := apply(`{"task_id":"quote-1","action":"` + action + `"}`); strings.Contains(got, `"error"`) {
			t.Fatal(got)
		}
		expected := map[string]string{"pause": "paused", "cancel": "cancelled"}[action]
		e.quoteRecoveryPrompt(control)
		e.noteQuotePlayback(control, PlaybackReport{SpokenText: "好的"}, false)
		if e.tools.quoteDelivery.status != expected {
			t.Fatal("control overwritten by playback")
		}
	}
	if got := apply(`{"task_id":"quote-1","action":"resume","section_ids":[2]}`); strings.Contains(got, `"error"`) {
		t.Fatal(got)
	}
	e.noteQuotePlayback(old, PlaybackReport{SpokenText: recoveryReadout}, false)
	d := e.tools.quoteDelivery
	if d.sections[1].Complete || d.sections[0].Selected || !d.sections[1].Selected || d.sections[2].Selected {
		t.Fatal("old playback advanced narrowed selection")
	}
	next := &Turn{ID: "narrowed"}
	e.quoteRecoveryPrompt(next)
	e.noteQuotePlayback(next, PlaybackReport{SpokenText: d.sections[1].Text}, false)
	if d.status != "complete" {
		t.Fatal("selected scope did not complete")
	}
}

func TestQuoteRecoveryInvalidControlsAreAtomic(t *testing.T) {
	e, _ := recoveryEngine(t)
	for _, args := range []string{`null`, `{}`, `{"task_id":"quote-1","action":"x"}`, `{"task_id":"old","action":"cancel"}`, `{"task_id":"quote-1","action":"resume","section_ids":[1,99]}`, `{"task_id":"quote-1","action":"pause","section_ids":[1]}`, `{"task_id":"quote-1","action":"resume","section_ids":[1,1]}`, `{"task_id":"quote-1","action":"cancel"} {}`, `{"task_id":"quote-1","action":"cancel","unexpected":true}`} {
		before, _ := json.Marshal(e.tools.quoteDelivery.sections)
		result := e.applyQuoteDeliveryCall(provider.ToolCall{Arguments: args}, &Turn{ID: "control"})
		after, _ := json.Marshal(e.tools.quoteDelivery.sections)
		if !strings.Contains(result, `"error"`) || string(before) != string(after) || e.tools.quoteDelivery.status != "queued" {
			t.Fatalf("invalid control changed state: %s", args)
		}
	}
}

func TestQuoteRecoveryRevisionAndBackendEvent(t *testing.T) {
	e, turn := recoveryEngine(t)
	_, _ = e.tools.memory.Patch([]byte(`{"wechat":{"status":"added"}}`))
	if e.currentQuoteDelivery().status == "superseded" {
		t.Fatal("addV invalidated quote")
	}
	_, _ = e.tools.memory.Patch([]byte(`{"quote":{"三者险":"300万"}}`))
	if !strings.Contains(e.quoteRecoveryPrompt(&Turn{ID: "revision"}), `"status":"superseded"`) {
		t.Fatal("old prices still eligible")
	}
	e.noteQuotePlayback(turn, PlaybackReport{SpokenText: recoveryReadout}, false)
	if e.tools.quoteDelivery.sections[0].Complete {
		t.Fatal("stale quote advanced")
	}
	if got := e.applyQuoteDeliveryCall(provider.ToolCall{Arguments: `{"task_id":"quote-1","action":"resume"}`}, &Turn{}); !strings.Contains(got, `"error"`) {
		t.Fatal("stale resume allowed")
	}
}

func TestQuoteRecoveryConservativeMatchingAndBounds(t *testing.T) {
	e, turn := recoveryEngine(t)
	e.tools.quoteDelivery.sections = []quoteSection{{ID: 1, Text: "保费12.3元。", Selected: true}}
	e.noteQuotePlayback(turn, PlaybackReport{SpokenText: "保费123元。"}, true)
	if e.tools.quoteDelivery.sections[0].Complete {
		t.Fatal("decimal stripped")
	}
	text := strings.Repeat("完整段落；", 100)
	sections := quoteSections(text)
	if len(sections) != 64 || strings.Join(sections, "") != text {
		t.Fatal("section bound lost tail")
	}
}

func TestQuoteRecoveryOptInAndNoOpenerRetention(t *testing.T) {
	e, turn := recoveryEngine(t)
	e.opts.Cfg.Duplex.QuoteInterruptionRecovery = false
	if e.quoteRecoveryPrompt(turn) != "" {
		t.Fatal("disabled recovery changed prompt")
	}
	cfg := config.Default()
	if cfg.Duplex.QuoteInterruptionRecovery {
		t.Fatal("recovery must be opt-in")
	}
	e.opts.Cfg = cfg
	e.opts.Cfg.ToolsMemory = true
	e.opts.Cfg.Tools = []config.ToolConfig{{Name: "quote_total", Async: true}}
	e.setupTools()
	if e.isServerTool("quote_delivery") {
		t.Fatal("disabled recovery exposed control")
	}
	e.opts.Cfg.Duplex.QuoteInterruptionRecovery = true
	e.setupTools()
	if !e.isServerTool("quote_delivery") {
		t.Fatal("enabled recovery missing control")
	}
}

func TestQuoteRecoveryFollowsSupplementarySkill(t *testing.T) {
	messages := []provider.Message{{Role: provider.RoleSystem, Content: "owner"}, {Role: provider.RoleSystem, Name: "golive_quote_delivery", Content: "progress"}, {Role: provider.RoleSystem, Name: "load_skill", Content: "concept clarification"}}
	got := quoteRecoveryLast(messages)
	if got[0].Content != "owner" || got[1].Name != "load_skill" || got[2].Content != "progress" {
		t.Fatal("skill displaced call progress or original prompt")
	}
}

func TestQuotationWithoutAnOpeningStillRetainsResult(t *testing.T) {
	e, _, task, p := newAckHandoffTest(t)
	e.opts.Cfg.Duplex.QuoteInterruptionRecovery = true
	task.Tool.Name = "quote_total"
	task.Tool.Ack = ""
	task.Result.Output = []byte(`{"status":"quote","total":123,"lines":[{"coverage":"test","premium":123}]}`)
	e.tools.latest = map[string]string{"quote_total": task.ID}
	e.tools.ackPending = map[string]bool{}
	p.scripted = ""
	e.onTaskDone(task)
	e.runPosted()
	if e.tools.lastResult != task || e.tools.quoteDelivery == nil {
		t.Fatal("quotation without opener was not retained")
	}
	if e.tools.quoteDelivery.status != "pending" {
		t.Fatal("free-form quotation was incorrectly assigned a queued fixed readout")
	}
	if e.tools.lastResultComplete || strings.Contains(e.memoryBlock(), "已完整念") {
		t.Fatal("queued readout claimed complete")
	}
}

func TestQuoteRecoveryControlShortcutRequiresUsableAnswer(t *testing.T) {
	e, _ := recoveryEngine(t)
	turn := &Turn{ID: "control"}
	p := &pendingResponse{turn: turn, calls: []provider.ToolCall{{ID: "control", Name: quoteDeliveryTool.Name}}, outputs: map[string]string{"control": "【报价讲解进度】{}"}}
	e.tools.quoteDelivery.status = "paused"
	if e.quoteControlAnswered(p) {
		t.Fatal("silent control needs an answer")
	}
	turn.AppendResponse("好的，先暂停。")
	if !e.quoteControlAnswered(p) {
		t.Fatal("acknowledged pause repeated model completion")
	}
	e.tools.quoteDelivery.status = "pending"
	if e.quoteControlAnswered(p) {
		t.Fatal("resume acknowledgement skipped unfinished prices")
	}
	turn.AppendResponse(recoveryReadout)
	if !e.quoteControlAnswered(p) {
		t.Fatal("complete readout repeated model completion")
	}
	p.outputs["control"] = `{"error":"stale"}`
	if e.quoteControlAnswered(p) {
		t.Fatal("failed control suppressed error handling")
	}
}

func TestDeliveredQuotationCoversOnlyItsCallingContinuation(t *testing.T) {
	e, _ := recoveryEngine(t)
	e.tools.lastResult = e.tools.quoteDelivery.task
	p := &pendingResponse{calls: []provider.ToolCall{{ID: "c", Name: "quote_total"}}, outputs: map[string]string{"c": `{"status":"running","task_id":"quote-1"}`}}
	if !e.deliveredQuoteCovers(p) {
		t.Fatal("delivered quote left a stale waiting response")
	}
	p.outputs["c"] = `{"status":"running","task_id":"quote-2"}`
	if e.deliveredQuoteCovers(p) {
		t.Fatal("new quotation was suppressed")
	}
	p.outputs["c"] = `{"status":"running","task_id":"quote-1"}`
	p.calls = append(p.calls, provider.ToolCall{ID: "other", Name: "update_memory"})
	if e.deliveredQuoteCovers(p) {
		t.Fatal("mixed tools lost their continuation")
	}
}

func TestQuoteRecoveryLateTruncationRevokesNaturalProgressOnce(t *testing.T) {
	e, _, task, p := newAckHandoffTest(t)
	e.opts.Cfg.Duplex.QuoteInterruptionRecovery = true
	turn := p.turn
	turn.QuoteResult, turn.Readout = true, recoveryReadout
	e.startQuoteDelivery(task, turn)
	e.quoteRecoveryPrompt(turn)
	e.tools.lastResult, e.tools.lastResultTurn = task, turn.ID
	report := PlaybackReport{owner: turn, TurnID: turn.ID, Generation: turn.Generation, Revision: turn.Revision, TotalMS: 100, PlayedMS: 100, SpokenText: recoveryReadout}
	e.completePlayback(&playbackCompletion{report: report}, true)
	if e.tools.quoteDelivery.status != "complete" {
		t.Fatal("natural receipt did not complete quotation")
	}
	report.SpokenText = e.tools.quoteDelivery.sections[0].Text + "三者险100万"
	report.PlayedMS = 40
	for range 2 {
		e.completePlayback(&playbackCompletion{report: report, truncated: true}, true)
	}
	d := e.tools.quoteDelivery
	if d.status != "interrupted" || d.interruptions != 1 || !d.sections[0].Complete || d.sections[1].Complete || d.sections[2].Complete || e.tools.lastResultComplete {
		t.Fatalf("late truncation retained unplayed sections or counted twice: %+v", d)
	}
}

func TestQuoteRecoveryControlAfterSpokenCompletion(t *testing.T) {
	e, _ := recoveryEngine(t)
	turn := &Turn{ID: "narrowed"}
	turn.SetSpoken(e.tools.quoteDelivery.sections[1].Text)
	turn.markCompletedNaturally()
	result := e.applyQuoteDeliveryCall(provider.ToolCall{Arguments: `{"task_id":"quote-1","action":"resume","section_ids":[2]}`}, turn)
	if !strings.Contains(result, `"status":"complete"`) || !e.tools.quoteDelivery.sections[1].Complete {
		t.Fatal("scope update lost an already played response")
	}
}

func TestQuoteRecoveryExplicitScopeRequiresSavedControl(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"只说一百万那一档的保费和合计，其他先不讲。", true},
		{"请只讲50万和100万这两档。", true},
		{"不要只说一百万那一档。", false},
		{"一百万这档多少钱？", false},
		{"医保外用药是什么意思？", false},
	} {
		e, _ := recoveryEngine(t)
		req := provider.LLMRequest{}
		e.requireQuoteScopeControl(&Turn{Transcript: tc.text}, &req)
		if (req.ToolChoice != nil) != tc.want {
			t.Errorf("unexpected forced control for %q", tc.text)
		}
		req.ToolChoice = "none"
		e.requireQuoteScopeControl(&Turn{Transcript: tc.text}, &req)
		if req.ToolChoice != "none" {
			t.Fatal("explicit tool policy overridden")
		}
	}
}
