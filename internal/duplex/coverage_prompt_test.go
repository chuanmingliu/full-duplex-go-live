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
	if e.quotePlanVersion() != 7 {
		t.Fatal("plan version lost")
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
