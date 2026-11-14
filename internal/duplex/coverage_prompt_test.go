package duplex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/tools"
)

func TestCoveragePromptUsesCallPlanAndFailsClosed(t *testing.T) {
	brief, err := tools.ParseQuoteBrief([]byte(`{"default":"legacy","plans":{"legacy":{"coverages":["旧险种"],"options":{"旧险种":["500万"]}},"motor-full":{"coverages":["医保外用药责任险-司机"],"options":{"医保外用药责任险-司机":["共享保额","1万","2万"]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	brief.Version = 7
	e := &Engine{tools: &toolState{quoteBrief: &brief, memory: tools.NewMemory(map[string]any{"quote_plan": "motor-full"}), defs: []tools.Tool{{Name: "quote_total", Description: "Saved original description"}}}}
	text := string(e.toolDefinitions()[0])
	if !strings.Contains(text, "共享保额、1万、2万") || strings.Contains(text, "旧险种") || !strings.Contains(text, "Saved original description") {
		t.Fatal(text)
	}
	if e.quotePlanVersion("quote_total") != 7 {
		t.Fatal("plan version lost")
	}
	if e.quotePlanVersion("send_wechat") != 0 {
		t.Fatal("quotation metadata leaked into an unrelated tool")
	}
	e.tools.memory.Patch(json.RawMessage(`{"quote_plan":"missing"}`))
	text = string(e.toolDefinitions()[0])
	if !strings.Contains(text, "目录不可用") || strings.Contains(text, "500万") {
		t.Fatal(text)
	}
	e.tools.quoteBrief = nil
	text = string(e.toolDefinitions()[0])
	if !strings.Contains(text, "不得用常见产品知识编造") || !strings.Contains(text, "目录不可用") {
		t.Fatal(text)
	}
}

func TestCoverageFollowupSelection(t *testing.T) {
	messages := []provider.Message{{Role: provider.RoleUser, Content: "医保外用药有哪些档位？"}, {Role: provider.RoleAssistant, Content: "医保外用药分三者、司机和乘客。"}, {Role: provider.RoleUser, Content: "还有其他的吗？"}}
	if got := coverageSelectionText(messages, "还有其他的吗？"); !strings.Contains(got, "医保外用药有哪些选项") {
		t.Fatal(got)
	}
	messages = append(messages, provider.Message{Role: provider.RoleUser, Content: "微信怎么加？"}, provider.Message{Role: provider.RoleAssistant, Content: "请打开服务通知。"}, provider.Message{Role: provider.RoleUser, Content: "还有其他的吗？"})
	if got := coverageSelectionText(messages, "还有其他的吗？"); got != "还有其他的吗？" {
		t.Fatal("inherited stale topic", got)
	}
}

func TestCoverageFragmentFollowup(t *testing.T) {
	messages := []provider.Message{{Role: provider.RoleUser, Content: "请问司机医保。"}, {Role: provider.RoleAssistant, Content: "明白，司机医保外用"}}
	for _, question := range []string{"请完整列出。", "请完整列出来。", "全部列出来"} {
		if got := coverageSelectionText(messages, question); !strings.HasPrefix(got, "医保外用药责任险-司机有哪些选项？") {
			t.Fatal(got)
		}
	}
	if got := coverageSelectionText(messages, "重新报价"); got != "重新报价" {
		t.Fatal("changed business request", got)
	}
}

func TestCoverageComparisonFollowupCarriesNearbyTopic(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleUser, Content: "三者险有几档？"},
		{Role: provider.RoleAssistant, Content: "三者险可以选多个档位。"},
	}
	explicit := "对比车损五十万和七十万"
	if got := coverageSelectionText(messages, explicit); got != explicit {
		t.Fatal("explicit coverage was relabeled", got)
	}
	query := "你帮我对比一下，两百万、三百万、四百万分别是多少？"
	if got := coverageSelectionText(messages, query); !strings.HasPrefix(got, "三者险。") {
		t.Fatal("comparison lost its coverage context", got)
	}
	messages = append(messages, provider.Message{Role: provider.RoleUser, Content: "说说今天的天气"})
	if got := coverageSelectionText(messages, query); got != query {
		t.Fatal("comparison reused context across an unrelated topic", got)
	}
}

func TestImplicitCoverageChangesAndOptions(t *testing.T) {
	history := []provider.Message{{Role: provider.RoleUser, Content: "三者险还有什么档位？"}, {Role: provider.RoleAssistant, Content: "三者险有50万到400万六个档位。"}}
	for _, query := range []string{"你这样吧，帮我调整到三百万吧。", "提高到四百万。", "改成两百万。", "哎，他这个东西除了两百万，还有其他选项吗？"} {
		if got := coverageSelectionText(history, query); !strings.HasPrefix(got, "三者险") {
			t.Errorf("%s: %s", query, got)
		}
	}
	history = append(history, provider.Message{Role: provider.RoleUser, Content: "微信在哪里找？"}, provider.Message{Role: provider.RoleAssistant, Content: "请打开服务通知。"})
	query := "帮我调整到三百万。"
	if got := coverageSelectionText(history, query); got != query {
		t.Fatal("crossed unrelated topic", got)
	}
}

func TestPendingConfirmationSkillRoutingDoesNotDecideConsent(t *testing.T) {
	pending := []provider.Message{{Role: provider.RoleSystem, Content: "【待确认报价范围，尚未计算】revision-1"}}
	for _, input := range []string{"有。", "没有其他要改的了。", "没有了，就按四百万算。", "就比较这两档。", "取消这次调整。", "其他不变。"} {
		if got := coverageSelectionText(pending, input); got != "待确认报价。"+input {
			t.Fatal(got)
		}
		if got := coverageSelectionText(nil, input); got != input {
			t.Fatal("inherited an absent pending revision", got)
		}
	}
	input := "没有收到微信通知。"
	if got := coverageSelectionText(pending, input); got != input {
		t.Fatal("routed an unrelated request", got)
	}
	pending[0].Role = provider.RoleUser
	if got := coverageSelectionText(pending, "有。"); got != "有。" {
		t.Fatal("caller text created pending confirmation state", got)
	}
}
