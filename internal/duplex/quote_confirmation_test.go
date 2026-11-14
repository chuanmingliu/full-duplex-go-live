package duplex

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

func confirmationEngine() *Engine {
	t := tools.Tool{Name: "quote_total", Async: true, Ack: "fixed wait", Speech: &tools.QuoteSpeech{ConfirmRevisions: true}}
	return &Engine{log: slog.New(slog.NewTextHandler(io.Discard, nil)), deps: Deps{Emit: func(any) {}}, tools: &toolState{memory: tools.NewMemory(map[string]any{"quote": map[string]any{"三者险": "200万", "车损险": true}}), byName: map[string]tools.Tool{"quote_total": t}, lastResult: &tools.Task{Tool: t, Result: tools.Result{Output: json.RawMessage(`{"status":"quote"}`)}}}}
}
func revisionCall(args string) provider.ToolCall {
	return provider.ToolCall{Name: "quote_total", Arguments: args}
}
func confirmationArgs(id, action string) string {
	raw, _ := json.Marshal(map[string]string{"revision_id": id, "revision_action": action})
	return string(raw)
}

func TestQuoteConfirmationStagesAccumulatesAndCommits(t *testing.T) {
	e := confirmationEngine()
	before, version := e.memorySnapshot()
	first := &Turn{ID: "first", Transcript: "三者改三百万"}
	_, response := e.prepareQuoteRevision(first, revisionCall(`{"config":{"三者险":"300万"}}`))
	if !strings.Contains(response, "awaiting_confirmation") || e.tools.quoteRevision == nil {
		t.Fatal(response)
	}
	id := e.tools.quoteRevision.ID
	after, v := e.memorySnapshot()
	if string(before) != string(after) || v != version {
		t.Fatal("staging changed confirmed memory")
	}
	_, response = e.prepareQuoteRevision(first, revisionCall(confirmationArgs(id, "confirm")))
	if !strings.Contains(response, "awaiting_confirmation") {
		t.Fatal("same-turn confirmation allowed", response)
	}
	_, response = e.prepareQuoteRevision(&Turn{ID: "next", Transcript: "还要去掉车损"}, revisionCall(`{"config":{"车损险":false}}`))
	next := e.tools.quoteRevision
	if next.ID == id || next.Config["三者险"] != "300万" || next.Config["车损险"] != false {
		t.Fatalf("lost accumulated changes %+v", next)
	}
	_, response = e.prepareQuoteRevision(&Turn{ID: "later", Transcript: "没有其他了"}, revisionCall(confirmationArgs(id, "confirm")))
	if !strings.Contains(response, "expired") {
		t.Fatal("stale confirmation allowed")
	}
	confirmed, response := e.prepareQuoteRevision(&Turn{ID: "later", Transcript: "没有其他了，就这样算"}, revisionCall(confirmationArgs(next.ID, "confirm")))
	if response != "" || strings.Contains(confirmed.Arguments, "revision_") {
		t.Fatal("host fields leaked", response, confirmed)
	}
	if err := e.applyQuoteRevision(confirmed); err != nil {
		t.Fatal(err)
	}
	after, _ = e.memorySnapshot()
	if !strings.Contains(string(after), `"三者险":"300万"`) || !strings.Contains(string(after), `"车损险":false`) {
		t.Fatal(string(after))
	}
	_, response = e.prepareQuoteRevision(&Turn{ID: "retry", Transcript: "确认"}, revisionCall(confirmationArgs(next.ID, "confirm")))
	if !strings.Contains(response, "already_confirmed") {
		t.Fatal("confirmation replay was not idempotent", response)
	}
}

func TestQuoteConfirmationCancellationAndInputChange(t *testing.T) {
	for _, action := range []string{"cancel", "input_changed", "wechat_added", "empty_turn"} {
		t.Run(action, func(t *testing.T) {
			e := confirmationEngine()
			e.prepareQuoteRevision(&Turn{ID: "one", Transcript: "改三百万"}, revisionCall(`{"config":{"三者险":"300万"}}`))
			id := e.tools.quoteRevision.ID
			turn := &Turn{ID: "two", Transcript: "确认"}
			verb := "confirm"
			want := ""
			switch action {
			case "cancel":
				verb = "cancel"
				want = "cancelled"
			case "input_changed":
				e.tools.memory.Patch([]byte(`{"vehicle":{"value":80000}}`))
				want = "expired"
			case "wechat_added":
				e.tools.memory.Patch([]byte(`{"wechat":{"status":"added"}}`))
			case "empty_turn":
				turn.Transcript = ""
				want = "awaiting_confirmation"
			}
			_, response := e.prepareQuoteRevision(turn, revisionCall(confirmationArgs(id, verb)))
			if want == "" && response != "" || want != "" && !strings.Contains(response, want) {
				t.Fatal(response)
			}
		})
	}
}

func TestQuoteConfirmationGuardsAndCompatibility(t *testing.T) {
	for _, args := range []string{`{}`} {
		e := confirmationEngine()
		_, response := e.prepareQuoteRevision(&Turn{ID: "one"}, revisionCall(args))
		if response != "" {
			t.Fatal(response)
		}
	}
	e := confirmationEngine()
	e.tools.lastResult = nil
	if _, response := e.prepareQuoteRevision(&Turn{ID: "one"}, revisionCall(`{"config":{"三者险":"300万"}}`)); response != "" {
		t.Fatal("first quotation gated")
	}
	e = confirmationEngine()
	response := e.applyMemoryCall(provider.ToolCall{Arguments: `{"patch":{"quote":{"三者险":"300万"}}}`})
	if !strings.Contains(response, `"ok":false`) {
		t.Fatal("memory bypass accepted", response)
	}
	for _, args := range []string{`{"config":{"三者险":null}}`, `{"revision_action":"confirm","revision_id":"x","config":{"三者险":"300万"}}`} {
		if _, response := e.prepareQuoteRevision(&Turn{ID: "one"}, revisionCall(args)); !strings.Contains(response, "invalid") {
			t.Fatal(response)
		}
	}
	tool := e.tools.byName["quote_total"]
	if e.confirmationTool(tool, revisionCall(`{}`)).Ack != tool.Ack {
		t.Fatal("original acknowledgment changed")
	}
	if e.confirmationTool(tool, revisionCall(confirmationArgs("revision-1", "confirm"))).Ack != "" {
		t.Fatal("confirmed revision retained fixed prehook")
	}
	tool.Speech.ConfirmRevisions = false
	if _, response := e.prepareQuoteRevision(&Turn{ID: "one"}, revisionCall(`{"config":{"三者险":"300万"}}`)); response != "" {
		t.Fatal("legacy mode changed")
	}
}

func TestQuoteConfirmationQuestionMustFinishBeforeSkippingContinuation(t *testing.T) {
	e := confirmationEngine()
	turn := &Turn{ID: "ask", Transcript: "改三百万", Response: "三者改为三百万，其他保障还需要调整吗？"}
	call := revisionCall(`{"config":{"三者险":"300万"}}`)
	call.ID = "staged"
	_, output := e.prepareQuoteRevision(turn, call)
	p := &pendingResponse{turn: turn, calls: []provider.ToolCall{call}, outputs: map[string]string{call.ID: output}}
	if e.quoteConfirmationAnswered(p) {
		t.Fatal("unfinished question treated as completed")
	}
	turn.markCompletedNaturally()
	if !e.quoteConfirmationAnswered(p) {
		t.Fatal("completed question would be repeated")
	}
	turn.Response = "好的。"
	if e.quoteConfirmationAnswered(p) {
		t.Fatal("bare acknowledgement treated as question")
	}
}

func TestQuoteConfirmationToolSchemaAllowsActionWithoutConfig(t *testing.T) {
	raw := quoteConfirmationParameters(json.RawMessage(`{"type":"object","properties":{"config":{"type":"object"}},"required":["config"]}`))
	var schema struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if json.Unmarshal(raw, &schema) != nil || len(schema.Required) != 0 || schema.Properties["revision_action"] == nil || schema.Properties["revision_id"] == nil {
		t.Fatal(string(raw))
	}
}

func TestQuoteConfirmationRebasesWithoutLosingRequestedChanges(t *testing.T) {
	e := confirmationEngine()
	e.prepareQuoteRevision(&Turn{ID: "one", Transcript: "改三百万"}, revisionCall(`{"config":{"三者险":"300万"}}`))
	old := e.tools.quoteRevision.ID
	e.tools.memory.Patch([]byte(`{"vehicle":{"value":80000}}`))
	_, response := e.prepareQuoteRevision(&Turn{ID: "two", Transcript: "确认"}, revisionCall(confirmationArgs(old, "confirm")))
	if !strings.Contains(response, "expired") || e.tools.quoteRevision == nil {
		t.Fatal("stale confirmation lost pending intent")
	}
	e.prepareQuoteRevision(&Turn{ID: "three", Transcript: "再去掉车损"}, revisionCall(`{"config":{"车损险":false}}`))
	pending := e.tools.quoteRevision
	if pending.ID == old || pending.Config["三者险"] != "300万" || pending.Config["车损险"] != false {
		t.Fatal("rebased revision lost accumulated changes")
	}
}

func TestQuoteConfirmationSurvivesUnrelatedResult(t *testing.T) {
	e := confirmationEngine()
	e.tools.quotedSelection = true
	e.tools.lastResult = &tools.Task{Tool: tools.Tool{Name: "send_notification"}}
	_, response := e.prepareQuoteRevision(&Turn{ID: "next", Transcript: "改三百万"}, revisionCall(`{"config":{"三者险":"300万"}}`))
	if !strings.Contains(response, "awaiting_confirmation") {
		t.Fatal("unrelated tool result bypassed confirmation", response)
	}
}

func TestQuoteConfirmationDispatchStartsOnlyAfterApproval(t *testing.T) {
	opts := quoteConfig()
	collector := newCollector(opts.Cfg.ClientRate)
	quotes := &blockedRevisionQuote{requests: make(chan tools.Request, 4)}
	e := startQuoteEngine(t, opts, Deps{ASR: newTrailingASR("x"), LLM: &quoteLLM{}, Emit: collector.emit}, quotes)
	e.SeedMemory(map[string]any{"quote": map[string]any{"三者险": "200万", "车损险": true}})
	e.post(func() {
		tool := e.tools.byName["quote_total"]
		tool.Speech = &tools.QuoteSpeech{ConfirmRevisions: true}
		tool.Ack = "fixed wait"
		e.tools.byName[tool.Name] = tool
		e.tools.lastResult = &tools.Task{Tool: tool, Result: tools.Result{Output: json.RawMessage(`{"status":"quote"}`)}}
	})
	outcome := make(chan string, 1)
	e.post(func() {
		turn := e.tracker.Begin("三者改三百万", false)
		call := revisionCall(`{"config":{"三者险":"300万"}}`)
		_, id, _ := e.submitTask(backendJob{turn: turn}, call, e.confirmationTool(e.tools.byName[call.Name], call))
		if id != "" || e.tools.async != 0 {
			outcome <- "submitted early"
			return
		}
		outcome <- e.tools.quoteRevision.ID
	})
	id := <-outcome
	if id == "submitted early" {
		t.Fatal(id)
	}
	e.post(func() {
		turn := e.tracker.Begin("没有别的了，就这样算", false)
		call := revisionCall(confirmationArgs(id, "confirm"))
		_, taskID, _ := e.submitTask(backendJob{turn: turn}, call, e.confirmationTool(e.tools.byName[call.Name], call))
		if taskID == "" || e.tools.tasks[taskID].Tool.Ack != "" {
			outcome <- "missing submission or fixed ack"
			return
		}
		outcome <- "ok"
	})
	if result := <-outcome; result != "ok" {
		t.Fatal(result)
	}
	request := revisionRequest(t, quotes)
	if strings.Contains(string(request.Arguments), "revision_") || !strings.Contains(string(request.Memory), `"三者险":"300万"`) {
		t.Fatalf("incorrect confirmed request: %+v", request)
	}
}

func TestQuoteConfirmationComparisonWaitsWithoutSelectingPolicy(t *testing.T) {
	e := confirmationEngine()
	before, version := e.memorySnapshot()
	call := revisionCall(`{"configs":[{"三者险":"200万"},{"三者险":"300万"},{"三者险":"400万"}]}`)
	_, response := e.prepareQuoteRevision(&Turn{ID: "compare", Transcript: "对比这三档"}, call)
	if !strings.Contains(response, "awaiting_confirmation") || e.tools.quoteRevision == nil {
		t.Fatal("comparison bypassed confirmation", response)
	}
	id := e.tools.quoteRevision.ID
	confirmed, response := e.prepareQuoteRevision(&Turn{ID: "approve", Transcript: "没有其他，就对比这三档"}, revisionCall(confirmationArgs(id, "confirm")))
	if response != "" {
		t.Fatal(response)
	}
	var args struct {
		Configs []map[string]any `json:"configs"`
	}
	if json.Unmarshal([]byte(confirmed.Arguments), &args) != nil || len(args.Configs) != 3 {
		t.Fatal("comparison lost variants", confirmed.Arguments)
	}
	if err := e.applyQuoteRevision(confirmed); err != nil {
		t.Fatal(err)
	}
	after, nextVersion := e.memorySnapshot()
	if string(after) != string(before) || nextVersion != version {
		t.Fatal("confirmed comparison changed selected policy")
	}
}

func TestQuoteConfirmationComparisonPreservesPendingChangesAndInvalidatesOldApproval(t *testing.T) {
	e := confirmationEngine()
	e.prepareQuoteRevision(&Turn{ID: "change"}, revisionCall(`{"config":{"车损险":false}}`))
	old := e.tools.quoteRevision.ID
	e.prepareQuoteRevision(&Turn{ID: "compare"}, revisionCall(`{"configs":[{"三者险":"200万"},{"三者险":"300万"}]}`))
	pending := e.tools.quoteRevision
	if len(pending.Configs) != 2 || pending.Configs[0]["车损险"] != false || pending.Configs[1]["车损险"] != false {
		t.Fatal("comparison dropped pending common change", pending)
	}
	_, output := e.prepareQuoteRevision(&Turn{ID: "later", Transcript: "确认"}, revisionCall(confirmationArgs(old, "confirm")))
	if !strings.Contains(output, "expired") {
		t.Fatal("old request remained confirmable", output)
	}
	_, output = e.prepareQuoteRevision(&Turn{ID: "cancel"}, revisionCall(confirmationArgs(pending.ID, "cancel")))
	if !strings.Contains(output, "cancelled") || e.tools.quoteRevision != nil {
		t.Fatal(output)
	}
}

func TestQuoteConfirmationComparisonLegacyAndFirstQuote(t *testing.T) {
	for _, first := range []bool{true, false} {
		e := confirmationEngine()
		if first {
			e.tools.lastResult = nil
		} else {
			e.tools.byName["quote_total"].Speech.ConfirmRevisions = false
		}
		_, output := e.prepareQuoteRevision(&Turn{ID: "compare"}, revisionCall(`{"configs":[{"三者险":"200万"},{"三者险":"300万"}]}`))
		if output != "" {
			t.Fatal("first quote or legacy comparison changed", output)
		}
	}
}
