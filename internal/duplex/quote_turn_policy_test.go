package duplex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

func TestExplicitMedicalRevisionRequiresQuoteTool(t *testing.T) {
	e := confirmationEngine()
	e.tools.quoteBrief = &tools.QuoteBrief{Plan: "motor", Plans: map[string]tools.QuoteSelectionSchema{
		"motor": {Options: map[string][]string{thirdPartyMedical: {"400万", "共享保额"}, "三者险": {"200万", "400万"}}},
	}}
	request := func(id, text string) provider.LLMRequest {
		req := provider.LLMRequest{Tools: []json.RawMessage{json.RawMessage(`{}`)}}
		e.applyQuoteTurnPolicy(&Turn{ID: id, Transcript: text}, &req)
		return req
	}
	stage := request("stage", "三者，医保外，用药单独四百万。")
	if stage.ToolChoice == nil || !strings.Contains(stage.Messages[0].Content, "400万") {
		t.Fatal(stage)
	}
	unsupported := request("bad", "三者，医保外用药，单独，五百万。")
	if unsupported.ToolChoice != nil || !strings.Contains(unsupported.Messages[0].Content, "400万、共享保额") {
		t.Fatal(unsupported)
	}
	_, rejected := e.prepareQuoteRevision(&Turn{ID: "wrong"}, revisionCall(`{"config":{"三者险":"500万","医保外用药责任险-三者":"共享保额"}}`))
	if !strings.Contains(rejected, "unsupported_selection") || e.tools.quoteRevision != nil {
		t.Fatal(rejected)
	}

	e.prepareQuoteRevision(&Turn{ID: "stage"}, revisionCall(`{"config":{"医保外用药责任险-三者":"400万"}}`))
	if got := request("other", "呃，没有。"); got.ToolChoice != nil {
		t.Fatal("unrelated denial forced pricing")
	}
	e.history = []provider.Message{{Role: provider.RoleAssistant, Content: "您还有其他保障要调整吗？"}}
	if got := request("later", "呃，没有。"); got.ToolChoice == nil || !strings.Contains(got.Messages[0].Content, "revision_action=confirm") {
		t.Fatal(got)
	}
	if !strings.Contains(e.quotePricingStateNote(), "不得说还在算") {
		t.Fatal("pending state allows waiting claim")
	}
}
