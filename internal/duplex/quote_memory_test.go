package duplex

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/chuanmingliu/golive/internal/crm"
	"github.com/chuanmingliu/golive/internal/tools"
)

// These prices are synthetic. A custom spoken comparison omits individual
// premiums, but those source amounts must survive a later history trim.
func TestRecentQuoteMemoryRetainsPricesBeyondTheCustomReadout(t *testing.T) {
	qs, err := tools.ParseQuoteSpeech([]byte(`{"posthook_status":"quote","template":"CUSTOM total {{ total }}","compare_template":"CUSTOM comparison"}`))
	if err != nil {
		t.Fatal(err)
	}
	lines := func(choice string, premium float64) []crm.QuoteLine {
		return []crm.QuoteLine{{Coverage: "liability", Choice: choice, Premium: premium}, {Coverage: "other", Premium: 50}}
	}
	raw, err := json.Marshal(map[string]any{"status": "quote", "currency": "CNY", "compare": true, "total": 150,
		"lines": lines("low", 100), "variants": []crm.Variant{
			{Label: "liability medium", Total: 170, Lines: lines("medium", 120)},
			{Label: "liability high", Total: 230, Lines: lines("high", 180)},
		}})
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip any
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(roundTrip)
	task := &tools.Task{Tool: tools.Tool{Name: "quote_total"}.WithSpeech(qs), Result: tools.Result{Output: raw}}
	e := &Engine{tools: &toolState{lastResult: task, lastResultCut: true, lastResultHeard: "CUSTOM"}}
	block := e.memoryBlock() // No conversation history remains.
	for _, want := range []string{"CUSTOM comparison", "liability", "low", "100", "medium", "120", "high", "180"} {
		if !strings.Contains(block, want) {
			t.Errorf("recent-result memory lost %q after serialization/history trim: %s", want, block)
		}
	}
	if strings.Contains(block, "先把总价和改动的险种复述一遍") {
		t.Error("an interrupted result still forces total-price repetition regardless of the question")
	}
	if got := task.Tool.ReadoutFor(raw); got != "CUSTOM comparison" {
		t.Fatalf("retaining prices changed the custom spoken readout: %q", got)
	}
	t.Logf("synthetic two-option comparison: extra price facts=%d characters; retained block=%d characters", utf8.RuneCountInString(quoteMemoryFacts(raw)), utf8.RuneCountInString(block))
}

func TestQuoteMemoryDoesNotInventMissingOrFailedPrices(t *testing.T) {
	raw := []byte(`{"status":"quote","total":20,"lines":[{"coverage":"known free","premium":0},{"coverage":"missing"},{"coverage":"declined","premium":0}],"skipped":["declined"],"variants":[{"label":"failed option","error":"service unavailable","total":0,"lines":[{"coverage":"untrusted failed line","premium":0}]},{"label":"missing total","lines":[{"coverage":"known","premium":12.34}]},{"label":"same option","same":true,"total":20,"lines":[{"coverage":"known free","premium":0}]}]}`)
	got := quoteMemoryFacts(raw)
	for _, want := range []string{"已报价部分合计=20", "known free=0", "missing=未返回保费", "未报价=declined", "failed option：未能报价", "known=12.34", "same option：合计=20"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing accurate fact %q: %s", want, got)
		}
	}
	for _, wrong := range []string{"missing=0", "declined=0", "untrusted failed line", "failed option：合计=0", "missing total：合计=0"} {
		if strings.Contains(got, wrong) {
			t.Errorf("unavailable price represented as valid: %q in %s", wrong, got)
		}
	}
}

func TestQuoteMemoryPreservesReadoutAndNonQuoteFallbacks(t *testing.T) {
	for _, raw := range []string{`{"status":"need","missing":["vehicle"]}`, `{"status":"error","error":"offline"}`, `not-json`} {
		task := &tools.Task{Result: tools.Result{Output: []byte(raw)}}
		if got := lastResultLine(task); got != raw {
			t.Errorf("non-quote result changed: %q -> %q", raw, got)
		}
	}
	// No speech template: retain the compact quote facts rather than losing
	// the structured prices or bringing service identifiers into memory.
	raw := []byte(`{"status":"quote","total":12.34,"lines":[{"coverage":"sample","premium":12.34}],"insure_flow_code":"not-needed-in-memory"}`)
	got := lastResultLine(&tools.Task{Result: tools.Result{Output: raw}})
	if !strings.Contains(got, "sample=12.34") || strings.Contains(got, "not-needed-in-memory") {
		t.Fatalf("untemplated quote lost prices or retained irrelevant metadata: %s", got)
	}
}

func TestExtraQuoteMemoryHasAnExplicitSizeBound(t *testing.T) {
	variant := map[string]any{"label": "sample option", "total": 100, "lines": []any{map[string]any{"coverage": "liability", "choice": "high", "premium": 100}}}
	var variants []any
	for i := 0; i < 500; i++ {
		variants = append(variants, variant)
	}
	raw, err := json.Marshal(map[string]any{"status": "quote", "total": 50, "lines": []any{map[string]any{"coverage": "liability", "choice": "low", "premium": 50}}, "variants": variants})
	if err != nil {
		t.Fatal(err)
	}
	got := quoteMemoryFacts(raw)
	if utf8.RuneCountInString(got) > maxQuoteMemoryChars || !strings.Contains(got, "其余明细未保留") || !strings.Contains(got, "liability[low]=50") {
		t.Fatalf("oversized facts were not bounded and marked: chars=%d", utf8.RuneCountInString(got))
	}
	if strings.Contains(got, "liability[high]=100") && !strings.Contains(got, "sample option：合计=100；liability[high]=100。") {
		t.Fatal("an option was cut into an ambiguous partial record")
	}
	t.Logf("oversized comparison: retained facts=%d characters; limit=%d", utf8.RuneCountInString(got), maxQuoteMemoryChars)
}
