package duplex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

func TestSplitDriverPassengerMedicalRevision(t *testing.T) {
	const driver = "医保外用药责任险-司机"
	const passenger = "医保外用药责任险-乘客"
	e := confirmationEngine()
	e.tools.quoteBrief = &tools.QuoteBrief{Plan: "motor", Plans: map[string]tools.QuoteSelectionSchema{"motor": {Options: map[string][]string{driver: {"2万", "20万"}, passenger: {"2万", "20万"}}}}}
	e.history = []provider.Message{{Role: provider.RoleUser, Content: "司机、乘客、医保外。"}, {Role: provider.RoleAssistant, Content: "司机和乘客各按每个座位2万保额，医保外用药三者用的是共享保额，司机和乘客各2万。您是想调高额度吗？"}}
	forced := func(turn *Turn) bool {
		req := provider.LLMRequest{Tools: []json.RawMessage{json.RawMessage(`{}`)}}
		e.applyQuoteTurnPolicy(turn, &req)
		return req.ToolChoice != nil
	}
	for _, s := range []string{"配个二十万。", "这个二十万。", "你给我，配高一点，你配个，你配个二十万，配高一点。"} {
		if !forced(&Turn{ID: "stage", Transcript: s}) {
			t.Fatalf("not routed: %s", s)
		}
	}
	for _, s := range []string{"不要配二十万。", "配个二十万还是十万？", "三者险配个二十万。", "司机二十万，乘客两万。", "配个二十万可以吗？"} {
		if forced(&Turn{ID: "other", Transcript: s}) {
			t.Fatalf("unexpected quote route: %s", s)
		}
	}
	_, initialReply := e.prepareQuoteRevision(&Turn{ID: "names", Transcript: "司机乘客医保外。"}, revisionCall(`{"config":{"司机责任险":"2万"}}`))
	if !strings.Contains(initialReply, "needs_limit") || e.tools.quoteRevision != nil {
		t.Fatal(initialReply)
	}
	stage := &Turn{ID: "stage", Transcript: "配个二十万。"}
	_, reply := e.prepareQuoteRevision(stage, revisionCall(`{"config":{"司机责任险":"20万"}}`))
	p := e.tools.quoteRevision
	if !strings.Contains(reply, "awaiting_confirmation") || p == nil || len(p.Config) != 2 || p.Config[driver] != "20万" || p.Config[passenger] != "20万" {
		t.Fatalf("staging %+v %s", p, reply)
	}
	if forced(&Turn{ID: "other", Transcript: "对。"}) {
		t.Fatal("bare assent to a different question")
	}
	if forced(&Turn{ID: "negated", Transcript: "不用直接报价。"}) {
		t.Fatal("negated request treated as consent")
	}
	e.history = append(e.history, provider.Message{Role: provider.RoleAssistant, Content: "司机和乘客的医保外用药都配二十万，对吗？"})
	if !forced(&Turn{ID: "yes", Transcript: "对。"}) {
		t.Fatal("missed assent to both amounts")
	}
	for _, text := range []string{"不用，你直接报就行了。", "确认报价。"} {
		if !forced(&Turn{ID: "approved", Transcript: text}) {
			t.Fatalf("missed confirmation: %s", text)
		}
	}
	approved, reply := e.prepareQuoteRevision(&Turn{ID: "approved", Transcript: "不用，你直接报就行了。"}, revisionCall(confirmationArgs(p.ID, "confirm")))
	if reply != "" || !strings.Contains(approved.Arguments, driver) || !strings.Contains(approved.Arguments, passenger) {
		t.Fatal(approved, reply)
	}
	e.history = append(e.history, provider.Message{Role: provider.RoleUser, Content: "改一下车损险。"})
	if forced(stage) {
		t.Fatal("rider scope leaked across a topic change")
	}
}
