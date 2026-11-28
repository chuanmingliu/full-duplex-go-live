package duplex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

func TestFullTierComparisonKeepsLowestAndPreservesSelection(t *testing.T) {
	collector := newCollector(16000)
	q := &blockedRevisionQuote{requests: make(chan tools.Request, 4)}
	e := startQuoteEngine(t, quoteConfig(), Deps{ASR: newTrailingASR("x"), LLM: &quoteLLM{}, Emit: collector.emit}, q)
	e.SeedMemory(map[string]any{"quote": map[string]any{"三者险": "200万", "交强险": true}})
	seeded := make(chan struct{})
	e.post(func() { close(seeded) })
	<-seeded
	before, _ := e.MemorySnapshot()
	ready := make(chan struct{})
	e.post(func() {
		e.tools.quoteBrief = &tools.QuoteBrief{Plan: "active", Plans: map[string]tools.QuoteSelectionSchema{"active": {Aliases: map[string]string{"第三者责任险": "三者险"}, Options: map[string][]string{"三者险": {"50万", "100万", "150万", "200万", "300万", "400万"}}}}}
		e.submitTask(backendJob{turn: e.tracker.Begin("", false)}, provider.ToolCall{ID: "full", Name: "quote_total", Arguments: `{"compare_coverage":"第三者责任险"}`}, e.tools.byName["quote_total"])
		close(ready)
	})
	<-ready
	r := revisionRequest(t, q)
	var args struct {
		Configs []map[string]any `json:"configs"`
	}
	if err := json.Unmarshal(r.Arguments, &args); err != nil {
		t.Fatal(err)
	}
	if len(args.Configs) != 6 || args.Configs[0]["三者险"] != "50万" || args.Configs[5]["三者险"] != "400万" {
		t.Fatalf("incomplete comparison: %s", r.Arguments)
	}
	after, _ := e.MemorySnapshot()
	if string(after) != string(before) {
		t.Fatal("comparison changed selection")
	}
	if strings.Contains(string(r.Arguments), "compare_coverage") {
		t.Fatal("engine shorthand leaked to backend")
	}
}
func TestComparisonValidationAndExplicitSubsets(t *testing.T) {
	e := &Engine{tools: &toolState{quoteBrief: &tools.QuoteBrief{Plan: "a", Plans: map[string]tools.QuoteSelectionSchema{"a": {Options: map[string][]string{"三者险": {"50万", "100万"}}}}}}}
	for _, args := range []string{`{"compare_coverage":"missing"}`, `{"compare_coverage":null}`, `{"compare_coverage":"三者险","config":{}}`, `{"compare_coverage":"三者险","configs":[]}`} {
		if _, err := e.expandQuoteComparison(provider.ToolCall{Name: "quote_total", Arguments: args}); err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
	for _, args := range []string{`{"configs":[{"三者险":"100万"}]}`, `{"config":{"三者险":"100万"}}`} {
		call, err := e.expandQuoteComparison(provider.ToolCall{Name: "quote_total", Arguments: args})
		if err != nil || call.Arguments != args {
			t.Fatal("explicit selection changed")
		}
	}
}
