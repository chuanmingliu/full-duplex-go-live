package duplex

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"

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
	prompt := skillsSyntheticPrompt
	if path := os.Getenv("QUOTE_CONFIRMATION_PROMPT_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		prompt = string(raw)
	}
	var rows []realSkillRow
	for _, enabled := range []bool{false, true} {
		mode := "baseline"
		if enabled {
			mode = "confirmation"
		}
		for _, scenario := range []string{"accumulate", "clarify", "cancel", "compare", "interrupt"} {
			cfg := skillsmcp.Config{}
			if enabled && os.Getenv("QUOTE_CONFIRMATION_MCP_URL") != "" {
				cfg = skillsmcp.Config{URL: os.Getenv("QUOTE_CONFIRMATION_MCP_URL"), TokenFile: os.Getenv("QUOTE_CONFIRMATION_MCP_TOKEN_FILE"), TimeoutMS: 150}
			} else if enabled && os.Getenv("QUOTE_CONFIRMATION_SKILL") != "" {
				cfg = quotationSkillFixture(t, os.Getenv("QUOTE_CONFIRMATION_SKILL"))
			}
			s := newSkillSessionWithPrompt(t, llm, cfg, prompt)
			s.e.post(func() {
				tool := s.e.tools.byName["quote_total"]
				var schema map[string]any
				_ = json.Unmarshal(tool.Parameters, &schema)
				properties := schema["properties"].(map[string]any)
				properties["configs"] = map[string]any{"type": "array", "items": properties["config"]}
				delete(schema, "required")
				tool.Parameters, _ = json.Marshal(schema)
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
			request := "三者险改成三百万，其他暂时不变。"
			if scenario == "compare" {
				request = "先比较一下三者险二百万、三百万、四百万的价格，其他保障不变。"
			}
			initial := s.turn(t, scenario+"_request", mode, request)
			if enabled {
				if cfg.URL != "" {
					initial.Checks["quotation_skill_loaded"] = skillLoaded(initial)
				}
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
				more := s.turn(t, "more_changes", mode, "我还想去掉车损险。")
				more.Checks["yes_means_more_changes"] = len(more.Quotes) == 0
				if cfg.URL != "" {
					more.Checks["removal_skill_loaded"] = skillLoaded(more)
				}
				s.e.post(func() {
					p := s.e.tools.quoteRevision
					more.Checks["changes_accumulated"] = p != nil && p.Config["三者险"] == "300万" && p.Config["车损险"] == false
				})
				probeBarrier(s.e)
				more.Checks["no_invented_change_count"] = !strings.Contains(more.ResponseText, "三项") && !strings.Contains(more.ResponseText, "三处")
				more.Checks["confirmation_wording_varied"] = confirmationQuestion(more.ResponseText) != confirmationQuestion(initial.ResponseText)
				rows = append(rows, more)
				final := s.turn(t, "confirm_all", mode, "没有其他要改的了，就按三者三百万、不带车损的方案算吧。")
				final.Checks["one_confirmed_calculation"] = len(final.Quotes) == 1
				if cfg.URL != "" {
					final.Checks["confirmation_skill_loaded"] = skillLoaded(final)
				}
				if len(final.Quotes) > 0 {
					final.Checks["merged_selection_submitted"] = strings.Contains(string(final.Quotes[0].Memory), `"三者险":"300万"`) && strings.Contains(string(final.Quotes[0].Memory), `"车损险":false`)
				}
				rows = append(rows, final)
				repeat := s.turn(t, "repeat_confirmation", mode, "对，就按刚刚确认好的方案，不要重复计算。")
				repeat.Checks["no_duplicate_calculation"] = len(repeat.Quotes) == 0
				rows = append(rows, repeat)
				revise := s.turn(t, "second_revision", mode, "再把三者险提高到四百万，其他不动。")
				revise.Checks["second_revision_waits"] = len(revise.Quotes) == 0
				revise.Checks["confirmation_wording_varied"] = confirmationQuestion(revise.ResponseText) != confirmationQuestion(initial.ResponseText)
				rows = append(rows, revise)
				confirmed := s.turn(t, "second_confirmation", mode, "没有其他要改的，按四百万这个方案算吧。")
				confirmed.Checks["second_revision_calculated_once"] = len(confirmed.Quotes) == 1
				confirmed.Checks["second_revision_memory"] = strings.Contains(string(confirmed.Memory), `"三者险":"400万"`) && strings.Contains(string(confirmed.Memory), `"车损险":false`)
				rows = append(rows, confirmed)
			case "interrupt":
				cut := make(chan bool, 1)
				go func() {
					deadline := time.Now().Add(12 * time.Second)
					for time.Now().Before(deadline) {
						playing := make(chan bool, 1)
						s.e.post(func() {
							cur := s.e.tracker.Current()
							playing <- cur != nil && cur.waitingTask != "" && !cur.Timings().FirstAudioOut.IsZero()
						})
						if <-playing {
							s.e.Interrupt()
							probeBarrier(s.e)
							stopped := time.Now()
							time.Sleep(200 * time.Millisecond)
							cut <- audioAfter(s.c, stopped.Add(100*time.Millisecond)) == 0
							return
						}
						time.Sleep(5 * time.Millisecond)
					}
					cut <- false
				}()
				final := s.observeTurn(t, "interrupt_waiting_phrase", mode, func() {
					s.e.SubmitItem(live.ResponseItem{Type: "message", Role: "user", Content: live.TextContent("input_text", "没有其他要改的了，就按三百万算吧。")}, "")
					s.e.ContinueResponse("")
				})
				final.Checks["waiting_phrase_interrupted_without_stale_audio"] = <-cut
				final.Checks["one_quote_despite_interruption"] = len(final.Quotes) == 1
				final.Checks["confirmed_memory_retained"] = strings.Contains(string(final.Memory), `"三者险":"300万"`)
				rows = append(rows, final)
				recover := s.turn(t, "recover_after_interruption", mode, "刚才确认的三者险保额是多少？不要重新计算。")
				recover.Checks["no_duplicate_after_interruption"] = len(recover.Quotes) == 0
				recover.Checks["recalls_confirmed_selection"] = strings.Contains(recover.ResponseText, "三百万") || strings.Contains(recover.ResponseText, "300万")
				rows = append(rows, recover)
			case "clarify":
				pause := s.turn(t, "clarification", mode, "先等一下，三者险一般是赔谁的？先解释，别计算。")
				pause.Checks["clarification_did_not_calculate"] = len(pause.Quotes) == 0
				s.e.post(func() { pause.Checks["pending_survived"] = s.e.tools.quoteRevision != nil })
				probeBarrier(s.e)
				rows = append(rows, pause)
				final := s.turn(t, "confirm_after_clarification", mode, "明白了，没有其他调整，就按刚才三百万的方案计算吧。")
				final.Checks["confirmed_after_clarification"] = len(final.Quotes) == 1
				rows = append(rows, final)
			case "compare":
				unclear := s.turn(t, "unspecified_damage_change", mode, "车损也想再调整一下。")
				unclear.Checks["no_pricing_on_unspecified_change"] = len(unclear.Quotes) == 0
				unclear.Checks["asks_for_details"] = strings.ContainsAny(unclear.ResponseText, "？?")
				rows = append(rows, unclear)
				more := s.turn(t, "comparison_damage_removed", mode, "把三个比较方案都去掉车损险，三者那三个档位保留。")
				more.Checks["no_pricing_before_scope_confirmation"] = len(more.Quotes) == 0
				more.Checks["confirmation_wording_varied"] = confirmationQuestion(more.ResponseText) != confirmationQuestion(initial.ResponseText)
				rows = append(rows, more)
				final := s.turn(t, "confirm_comparison", mode, "没有其他的了，就比较这三个方案。")
				final.Checks["one_confirmed_comparison"] = len(final.Quotes) == 1
				if len(final.Quotes) == 1 {
					var args struct {
						Configs []map[string]any `json:"configs"`
					}
					_ = json.Unmarshal(final.Quotes[0].Arguments, &args)
					final.Checks["three_alternatives"] = len(args.Configs) == 3
					tiers := map[string]bool{}
					for _, variant := range args.Configs {
						tier, _ := variant["三者险"].(string)
						tiers[tier] = variant["车损险"] == false
					}
					final.Checks["comparison_preserves_requested_scope"] = tiers["200万"] && tiers["300万"] && tiers["400万"]
				}
				final.Checks["comparison_kept_original_policy"] = strings.Contains(string(final.Memory), `"三者险":"200万"`) && strings.Contains(string(final.Memory), `"车损险":true`)
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

func confirmationQuestion(text string) string {
	end := strings.LastIndexAny(text, "？?")
	if end < 0 {
		return ""
	}
	parts := strings.FieldsFunc(text[:end], func(r rune) bool { return strings.ContainsRune("。；！？，,;!?\n", r) })
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}
