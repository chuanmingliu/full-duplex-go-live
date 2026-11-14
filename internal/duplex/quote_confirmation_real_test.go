package duplex

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/skillsmcp"
	"github.com/chuanmingliu/golive/internal/tools"
)

func TestQuoteConfirmationRealModel(t *testing.T) {
	report := os.Getenv("QUOTE_CONFIRMATION_REPORT")
	if report == "" {
		t.Skip("opt-in quotation confirmation conversation")
	}
	llm, err := provider.OpenLLM(os.Getenv("SKILLS_REAL_PROVIDER"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []realSkillRow
	for _, enabled := range []bool{false, true} {
		mode := "baseline"
		if enabled {
			mode = "confirmation"
		}
		for _, scenario := range []string{"accumulate", "clarify", "cancel"} {
			s := newRealSkillSession(t, llm, skillsmcp.Config{})
			s.e.post(func() {
				tool := s.e.tools.byName["quote_total"]
				tool.Ack = "这会儿正在给您计算保费，请您稍等。"
				tool.Speech = &tools.QuoteSpeech{ConfirmRevisions: enabled}
				s.e.tools.byName[tool.Name] = tool
				for i, d := range s.e.tools.defs {
					if d.Name == tool.Name {
						s.e.tools.defs[i] = tool
					}
				}
				memory, version := s.e.memorySnapshot()
				s.e.tools.lastResult = &tools.Task{ID: "prior", Tool: tool, Request: tools.Request{Memory: memory, MemoryVersion: version}, Result: tools.Result{Output: json.RawMessage(`{"status":"quote","total":4500,"selection":{"三者险":"200万","车损险":true,"交强险":true},"lines":[{"coverage":"三者险","choice":"200万","premium":1300}]}`)}}
				s.e.history = []provider.Message{{Role: provider.RoleUser, Content: "之前那份报价我知道了，我们看看怎么调整。"}, {Role: provider.RoleAssistant, Content: "目前是三者险两百万，加车损和交强险，总共4500元。"}}
			})
			probeBarrier(s.e)
			initial := s.turn(t, scenario+"_request", mode, "三者险改成三百万，其他暂时不变。")
			if enabled {
				initial.Checks["no_pricing_before_confirmation"] = len(initial.Quotes) == 0
				initial.Checks["confirmed_memory_unchanged"] = strings.Contains(string(initial.Memory), `"三者险":"200万"`)
				s.e.post(func() { initial.Checks["revision_staged"] = s.e.tools.quoteRevision != nil })
				probeBarrier(s.e)
			} else {
				initial.Checks["baseline_calculates_immediately"] = len(initial.Quotes) == 1
			}
			rows = append(rows, initial)
			if !enabled {
				s.e.Close()
				continue
			}
			switch scenario {
			case "accumulate":
				yes := s.turn(t, "yes_means_more", mode, "有。")
				yes.Checks["no_calculation_on_more_changes"] = len(yes.Quotes) == 0
				rows = append(rows, yes)
				more := s.turn(t, "more_changes", mode, "有，还想把车损险去掉。")
				more.Checks["yes_means_more_changes"] = len(more.Quotes) == 0
				s.e.post(func() {
					p := s.e.tools.quoteRevision
					more.Checks["changes_accumulated"] = p != nil && p.Config["三者险"] == "300万" && p.Config["车损险"] == false
				})
				probeBarrier(s.e)
				more.Checks["no_invented_change_count"] = !strings.Contains(more.ResponseText, "三项") && !strings.Contains(more.ResponseText, "三处")
				more.Checks["confirmation_wording_varied"] = strings.TrimSpace(more.ResponseText) != strings.TrimSpace(initial.ResponseText)
				rows = append(rows, more)
				final := s.turn(t, "confirm_all", mode, "没有其他要改的了，就按三者三百万、不带车损的方案算吧。")
				final.Checks["one_confirmed_calculation"] = len(final.Quotes) == 1
				if len(final.Quotes) > 0 {
					final.Checks["merged_selection_submitted"] = strings.Contains(string(final.Quotes[0].Memory), `"三者险":"300万"`) && strings.Contains(string(final.Quotes[0].Memory), `"车损险":false`)
				}
				rows = append(rows, final)
			case "clarify":
				pause := s.turn(t, "clarification", mode, "先等一下，三者险一般是赔谁的？先解释，别计算。")
				pause.Checks["clarification_did_not_calculate"] = len(pause.Quotes) == 0
				s.e.post(func() { pause.Checks["pending_survived"] = s.e.tools.quoteRevision != nil })
				probeBarrier(s.e)
				rows = append(rows, pause)
				final := s.turn(t, "confirm_after_clarification", mode, "明白了，没有其他调整，就按刚才三百万的方案计算吧。")
				final.Checks["confirmed_after_clarification"] = len(final.Quotes) == 1
				rows = append(rows, final)
			case "cancel":
				cancelled := s.turn(t, "cancel_pending", mode, "先不改了，取消这次调整，还是保留原来的两百万。")
				cancelled.Checks["cancel_did_not_calculate"] = len(cancelled.Quotes) == 0
				s.e.post(func() { cancelled.Checks["pending_removed"] = s.e.tools.quoteRevision == nil })
				probeBarrier(s.e)
				rows = append(rows, cancelled)
			}
			s.e.Close()
		}
	}
	raw, _ := json.MarshalIndent(rows, "", "  ")
	if err := os.WriteFile(report, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		for check, ok := range row.Checks {
			if !ok {
				t.Errorf("%s/%s: %s", row.Mode, row.Scenario, check)
			}
		}
	}
}
