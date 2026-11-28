package duplex

import (
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/skillsmcp"
)

func TestQuoteRecoveryConceptContinuation(t *testing.T) {
	e, initial := recoveryEngine(t)
	e.noteQuotePlayback(initial, PlaybackReport{SpokenText: e.tools.quoteDelivery.sections[0].Text}, true)
	turn := &Turn{ID: "concept", Transcript: "医保外用药是什么意思？"}
	e.quoteRecoveryPrompt(turn)
	guidance := []skillsmcp.Guidance{{URI: "skill://vehicle_insurance_concepts/SKILL.md"}}
	req := provider.LLMRequest{}
	e.prepareQuoteClarification(turn, guidance, &req)
	tail := e.quoteClarificationTail(turn, "这是概念说明。")
	if !strings.Contains(tail, "1093元") || strings.Contains(tail, "1615元") || len(req.Messages) != 1 {
		t.Fatalf("wrong next section: %q", tail)
	}
	if got := e.quoteClarificationTail(turn, tail); got != "" {
		t.Fatal("repeated an already generated complete section")
	}
	turn.Transcript += "接着把报价说完。"
	if got := e.quoteClarificationTail(turn, "这是概念说明。"); !strings.Contains(got, "1615元") {
		t.Fatal("explicit continuation omitted remaining section")
	}
	for _, status := range []string{"paused", "cancelled", "superseded", "complete"} {
		e.tools.quoteDelivery.status = status
		if got := e.quoteClarificationTail(turn, "这是概念说明。"); got != "" {
			t.Fatalf("appended quotation while %s", status)
		}
	}
}

func TestQuoteRecoveryConceptContinuationRequiresCurrentSkillAndIntent(t *testing.T) {
	for _, text := range []string{"一百万多少钱？", "先不要报价，医保外用药是什么意思？", "等一下，医保外用药是什么意思？"} {
		e, initial := recoveryEngine(t)
		e.noteQuotePlayback(initial, PlaybackReport{}, true)
		turn := &Turn{ID: "concept", Transcript: text}
		e.quoteRecoveryPrompt(turn)
		e.prepareQuoteClarification(turn, []skillsmcp.Guidance{{URI: "skill://vehicle_insurance_concepts/SKILL.md"}}, &provider.LLMRequest{})
		if e.quoteClarificationTail(turn, "解释。") != "" {
			t.Fatalf("unwanted continuation for %q", text)
		}
	}
	e, initial := recoveryEngine(t)
	e.noteQuotePlayback(initial, PlaybackReport{}, true)
	turn := &Turn{ID: "concept", Transcript: "医保外用药是什么意思？"}
	e.quoteRecoveryPrompt(turn)
	e.prepareQuoteClarification(turn, nil, &provider.LLMRequest{})
	if turn.quoteClarification {
		t.Fatal("missing skill enabled automatic continuation")
	}
	e.prepareQuoteClarification(turn, []skillsmcp.Guidance{{URI: "skill://vehicle_insurance_concepts/SKILL.md"}}, &provider.LLMRequest{})
	e.tools.quoteDelivery.epoch++
	if e.quoteClarificationTail(turn, "解释。") != "" {
		t.Fatal("old scope resumed after a newer control")
	}
}
