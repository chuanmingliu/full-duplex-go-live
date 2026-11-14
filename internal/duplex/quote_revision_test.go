package duplex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

type blockedRevisionQuote struct{ requests chan tools.Request }

func (q *blockedRevisionQuote) Execute(ctx context.Context, _ tools.Tool, req tools.Request) tools.Result {
	q.requests <- req
	<-ctx.Done()
	return tools.Result{Err: ctx.Err()}
}

func revisionRequest(t *testing.T, q *blockedRevisionQuote) tools.Request {
	t.Helper()
	select {
	case req := <-q.requests:
		return req
	case <-time.After(time.Second):
		t.Fatal("quotation was not submitted")
		return tools.Request{}
	}
}

func TestQuoteRevisionsCommitBeforeCalculationAndAccumulate(t *testing.T) {
	for _, seeded := range []bool{true, false} {
		name := "default_pending"
		if seeded {
			name = "existing_plan"
		}
		t.Run(name, func(t *testing.T) {
			opts := quoteConfig()
			c := newCollector(opts.Cfg.ClientRate)
			q := &blockedRevisionQuote{requests: make(chan tools.Request, 4)}
			e := startQuoteEngine(t, opts, Deps{ASR: newTrailingASR("x"), LLM: &quoteLLM{}, Emit: c.emit}, q)
			if seeded {
				e.SeedMemory(map[string]any{"quote": map[string]any{"交强险": true, "车损险": true, "三者险": "200万"}})
			}
			submit := func(id, args string) {
				e.post(func() {
					job := backendJob{turn: e.tracker.Begin("", false)}
					e.submitTask(job, provider.ToolCall{ID: id, Name: "quote_total", Arguments: args}, e.tools.byName["quote_total"])
				})
			}
			submit("first", `{"config":{"三者险":"300万"}}`)
			first := revisionRequest(t, q)
			if !strings.Contains(string(first.Memory), `"三者险":"300万"`) || first.QuoteBaseDefault == seeded {
				t.Fatalf("first request missed committed change or default provenance: %+v", first)
			}
			repeated := make(chan bool, 1)
			e.post(func() {
				job := backendJob{turn: e.tracker.Begin("", false)}
				e.submitTask(job, provider.ToolCall{ID: "repeat", Name: "quote_total", Arguments: `{"config":{"三者险":"300万"}}`}, e.tools.byName["quote_total"])
				_, version := e.memorySnapshot()
				repeated <- version == first.MemoryVersion && e.tools.async == 1
			})
			if !<-repeated {
				t.Fatal("repeating the same revision changed the version or submitted another calculation")
			}
			prompt := make(chan string, 1)
			e.post(func() { prompt <- e.systemPrompt(false) })
			if got := <-prompt; !strings.Contains(got, `"三者险":"300万"`) || strings.Contains(got, "200万") {
				t.Fatalf("follow-up prompt has stale desired state: %s", got)
			}
			submit("second", `{"config":{"车损险":false}}`)
			second := revisionRequest(t, q)
			if !strings.Contains(string(second.Memory), `"三者险":"300万"`) || !strings.Contains(string(second.Memory), `"车损险":false`) || second.MemoryVersion != first.MemoryVersion+1 {
				t.Fatalf("second change lost first change: %+v", second)
			}
			if second.QuoteBaseDefault == seeded {
				t.Fatal("second revision lost its default provenance")
			}
			checked := make(chan bool, 1)
			e.post(func() {
				current, _ := e.memorySnapshot()
				checked <- !quoteInputsUnchanged(&tools.Task{Tool: e.tools.byName["quote_total"], Request: first}, current)
			})
			if !<-checked {
				t.Fatal("obsolete result could overwrite the second revision")
			}
		})
	}
}

func TestQuotePreviewAndInvalidRevisionDoNotMutateMemory(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		wantError  bool
	}{
		{"one_preview", `{"configs":[{"三者险":"500万"}]}`, false},
		{"mixed_preview", `{"config":{"三者险":"300万"},"configs":[{"三者险":"500万"}]}`, false},
		{"invalid_json", `{"config":`, true},
		{"invalid_value", `{"config":{"三者险":"300万","车损险":null}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			memory := tools.NewMemory(map[string]any{"quote": map[string]any{"三者险": "200万"}})
			e := &Engine{tools: &toolState{memory: memory}}
			before, version := memory.Snapshot()
			err := e.applyQuoteRevision(provider.ToolCall{Name: "quote_total", Arguments: tc.args})
			after, afterVersion := memory.Snapshot()
			if (err != nil) != tc.wantError || string(before) != string(after) || version != afterVersion {
				t.Fatalf("err=%v before=%s after=%s version=%d", err, before, after, afterVersion)
			}
		})
	}
}

func TestRepeatedMemoryPatchDoesNotEmitOrExpireState(t *testing.T) {
	opts := quoteConfig()
	c := newCollector(opts.Cfg.ClientRate)
	e := startQuoteEngine(t, opts, Deps{ASR: newTrailingASR("x"), LLM: &quoteLLM{}, Emit: c.emit}, &slowQuote{})
	done := make(chan bool, 1)
	e.post(func() {
		call := provider.ToolCall{Name: "update_memory", Arguments: `{"patch":{"quote":{"三者险":"300万"}}}`}
		first := e.applyMemoryCall(call)
		second := e.applyMemoryCall(call)
		done <- first == second
	})
	if !<-done {
		t.Fatal("identical patch changed memory version")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, event := range c.events {
		if _, ok := event.(live.MemoryEvent); ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("got %d memory events, want one real change", count)
	}
}

func TestDeliveredQuotePricesAreScopedToTheirSelection(t *testing.T) {
	result := &tools.Task{Tool: tools.Tool{Name: "quote_total"}, Request: tools.Request{Memory: json.RawMessage(`{}`)},
		Result: tools.Result{Output: json.RawMessage(`{"selection":{"三者险":"200万"},"total":100}`)}}
	if !quoteResultMatchesMemory(result, json.RawMessage(`{"quote":{"三者险":"200万"}}`)) {
		t.Fatal("materialized default treated as stale")
	}
	if quoteResultMatchesMemory(result, json.RawMessage(`{"quote":{"三者险":"300万"}}`)) {
		t.Fatal("old quote price treated as the revised plan's price")
	}
}

func TestDefaultQuoteProvenanceSurvivesIntakeAndEndsAtSelection(t *testing.T) {
	opts := quoteConfig()
	c := newCollector(opts.Cfg.ClientRate)
	e := startQuoteEngine(t, opts, Deps{ASR: newTrailingASR("x"), LLM: &quoteLLM{}, Emit: c.emit}, &slowQuote{})
	done := make(chan struct{})
	e.post(func() {
		defer close(done)
		if err := e.applyQuoteRevision(provider.ToolCall{Name: "quote_total", Arguments: `{"config":{"三者险":"300万"}}`}); err != nil {
			t.Error(err)
			return
		}
		e.applyMemoryCall(provider.ToolCall{Arguments: `{"patch":{"vehicleAge":3}}`})
		if !e.tools.quoteBaseDefault {
			t.Error("missing-fact intake discarded the default base")
		}
		e.recordSelection([]byte(`{"selection":{"交强险":true,"车损险":true,"三者险":"300万"}}`))
		if e.tools.quoteBaseDefault {
			t.Error("full selection did not materialize the default base")
		}
		snapshot, _ := e.memorySnapshot()
		if !strings.Contains(string(snapshot), `"vehicleAge":3`) || !strings.Contains(string(snapshot), `"交强险":true`) {
			t.Errorf("selection reconciliation lost intake facts or coverage: %s", snapshot)
		}
		e.applyMemoryCall(provider.ToolCall{Arguments: `{"patch":{"quote":null}}`})
		if err := e.applyQuoteRevision(provider.ToolCall{Name: "quote_total", Arguments: `{"config":{"三者险":"500万"}}`}); err != nil {
			t.Error(err)
		}
		e.applyMemoryCall(provider.ToolCall{Arguments: `{"patch":{"quote":null}}`})
		if e.tools.quoteBaseDefault {
			t.Error("explicit plan deletion retained the old default base")
		}
	})
	<-done
}

func TestFullMotorAliasesAreCommittedBeforeSubmission(t *testing.T) {
	opts := quoteConfig()
	c := newCollector(opts.Cfg.ClientRate)
	q := &blockedRevisionQuote{requests: make(chan tools.Request, 2)}
	e := startQuoteEngine(t, opts, Deps{ASR: newTrailingASR("x"), LLM: &quoteLLM{}, Emit: c.emit}, q)
	e.SeedMemory(map[string]any{"quote": map[string]any{"三者险": "200万"}})
	e.post(func() {
		brief, err := tools.ParseQuoteBrief([]byte(`{"default":"motor-full","plans":{"motor-full":{"coverage_aliases":{"第三者责任险":"三者险"},"coverage_details":{"三者险":{"kind":"amount","input_unit":"万"}},"options":{"三者险":["200万","300万"]}}}}`))
		if err != nil {
			panic(err)
		}
		e.tools.quoteBrief = &brief
		e.submitTask(backendJob{turn: e.tracker.Begin("", false)}, provider.ToolCall{ID: "revision", Name: "quote_total", Arguments: `{"config":{"第三者责任险":"300w"}}`}, e.tools.byName["quote_total"])
	})
	req := revisionRequest(t, q)
	if !strings.Contains(string(req.Memory), `"三者险":"300万"`) || strings.Contains(string(req.Memory), "第三者责任险") || !strings.Contains(string(req.Arguments), `"三者险":"300万"`) {
		t.Fatalf("not canonicalized: %+v", req)
	}
}
