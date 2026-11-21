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

// Optional real-model replay of the context captured at an interruption.
// Audio is mock TTS; the paced playback regression tests exercise the cut itself.
func TestQuoteRecoveryRealModel(t *testing.T) {
	report := os.Getenv("QUOTE_RECOVERY_REPORT")
	if report == "" {
		t.Skip("opt-in real-model quotation recovery")
	}
	llm, err := provider.OpenLLM(os.Getenv("SKILLS_REAL_PROVIDER"))
	if err != nil {
		t.Fatal(err)
	}
	prompt := skillsSyntheticPrompt
	if path := os.Getenv("COVERAGE_PROMPT_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var saved struct {
			Instructions string `json:"instructions"`
		}
		if json.Unmarshal(raw, &saved) != nil || saved.Instructions == "" {
			t.Fatal("saved prompt missing")
		}
		prompt = saved.Instructions
	}
	cfg := skillsmcp.Config{URL: os.Getenv("SKILLS_SIM_URL"), TokenFile: os.Getenv("SKILLS_SIM_TOKEN_FILE"), TimeoutMS: 400}
	var rows []realSkillRow
	cases := []struct{ name, input string }{
		{"clarification", "医保外用药是什么意思？解释完接着刚才的报价往下说。"},
		{"implicit_clarification", "医保外用药是什么意思？"},
		{"pause", "等一下，先不要继续讲报价。"},
		{"cancel", "不用报价了，我们结束这个话题。"},
		{"narrow", "只说一百万那一档的保费和合计，其他先不讲。"},
		{"revision", "三者改成三百万，其他不变，请重新报价。"},
	}
	for _, tc := range cases {
		for _, enabled := range []bool{false, true} {
			mode := "baseline"
			if enabled {
				mode = "recovery"
			}
			s := newSkillSessionWithPrompt(t, llm, cfg, prompt)
			s.e.post(func() {
				s.e.opts.Cfg.Duplex.QuoteInterruptionRecovery = enabled
				s.e.opts.Cfg.MaxOutputTokens = 768
				if enabled {
					s.e.tools.byName[quoteDeliveryTool.Name] = quoteDeliveryTool
					s.e.tools.defs = append(s.e.tools.defs, quoteDeliveryTool)
				}
				memory, v := s.e.memorySnapshot()
				raw := json.RawMessage(`{"status":"quote","total":4543,"selection":{"交强险":true,"车损险":true,"三者险":"200万"},"lines":[{"coverage":"三者险","choice":"200万","premium":1378}],"compare":true,"variants":[{"label":"三者险50万","total":3973,"lines":[{"coverage":"三者险","choice":"50万","premium":808}]},{"label":"三者险100万","total":4258,"lines":[{"coverage":"三者险","choice":"100万","premium":1093}]},{"label":"三者险300万","total":4780,"lines":[{"coverage":"三者险","choice":"300万","premium":1615}]}]}`)
				task := &tools.Task{ID: "recovery-quote", Tool: tools.Tool{Name: "quote_total"}, Request: tools.Request{Memory: memory, MemoryVersion: v}, Result: tools.Result{Output: raw}}
				turn := &Turn{ID: "prior-quote", QuoteResult: true, Readout: recoveryReadout}
				s.e.tools.lastResult = task
				s.e.tools.lastResultTurn = turn.ID
				s.e.tools.lastResultCut = true
				s.e.tools.lastResultHeard = "三者险50万，保费808元，合计3973元；三者险100万，保费10"
				s.e.startQuoteDelivery(task, turn)
				if enabled {
					s.e.quoteRecoveryPrompt(turn)
					s.e.noteQuotePlayback(turn, PlaybackReport{SpokenText: s.e.tools.lastResultHeard}, true)
				}
				s.e.history = []provider.Message{{Role: provider.RoleUser, Content: "请比较各档三者险的保费和总价。"}, {Role: provider.RoleAssistant, Content: s.e.tools.lastResultHeard}}
			})
			probeBarrier(s.e)
			row := s.turn(t, tc.name, mode, tc.input)
			text := normalizeCoverageNumbers(row.ResponseText)
			switch tc.name {
			case "clarification", "implicit_clarification":
				row.Checks["explains_medical"] = strings.Contains(text, "医保")
				row.Checks["resumes_next_unfinished_premium"] = strings.Contains(text, "1093") && strings.Contains(text, "4258")
				if tc.name == "clarification" {
					row.Checks["resumes_remaining_comparison"] = strings.Contains(text, "1615") && strings.Contains(text, "4780")
				}
				row.Checks["no_recalculation"] = len(row.Quotes) == 0
				row.Checks["concept_skill_loaded"] = skillLoaded(row)
			case "pause", "cancel":
				row.Checks["no_price_readout"] = !strings.Contains(text, "4258") && !strings.Contains(text, "4780")
				if enabled {
					probeBarrier(s.e)
					s.e.post(func() {
						want := "paused"
						if tc.name == "cancel" {
							want = "cancelled"
						}
						row.Checks["control_saved"] = s.e.tools.quoteDelivery.status == want
					})
					probeBarrier(s.e)
				}
			case "narrow":
				row.Checks["only_requested_tier"] = strings.Contains(text, "1093") && strings.Contains(text, "4258") && !strings.Contains(text, "4780")
				if enabled {
					s.e.post(func() {
						sections := s.e.tools.quoteDelivery.sections
						row.Checks["scope_saved"] = !sections[0].Selected && sections[1].Selected && !sections[2].Selected
					})
					probeBarrier(s.e)
				}
			case "revision":
				row.Checks["new_quote_requested"] = len(row.Quotes) > 0
				row.Checks["confirmed_revision_retained"] = strings.Contains(string(row.Memory), `"三者险":"300万"`)
			}
			rows = append(rows, row)
			if enabled && tc.name == "pause" {
				next := s.turn(t, "resume_after_pause", mode, "现在继续把刚才没讲完的两个档位说完。")
				text := normalizeCoverageNumbers(next.ResponseText)
				next.Checks["resumed_prices"] = strings.Contains(text, "1093") && strings.Contains(text, "1615")
				next.Checks["no_recalculation"] = len(next.Quotes) == 0
				rows = append(rows, next)
				for check, ok := range next.Checks {
					if !ok {
						t.Errorf("resume_after_pause: %s", check)
					}
				}
			}
			if enabled && tc.name == "cancel" {
				next := s.turn(t, "clarification_after_cancel", mode, "医保外用药是什么意思？")
				text := normalizeCoverageNumbers(next.ResponseText)
				next.Checks["cancel_respected"] = !strings.Contains(text, "1093") && !strings.Contains(text, "1615")
				rows = append(rows, next)
				for check, ok := range next.Checks {
					if !ok {
						t.Errorf("clarification_after_cancel: %s", check)
					}
				}
			}
			s.e.Close()
			t.Logf("RECOVERY_MODEL scenario=%s mode=%s first_text_ms=%.2f first_audio_ms=%.2f checks=%v", tc.name, mode, row.TextMS, row.AudioMS, row.Checks)
			if enabled {
				for check, ok := range row.Checks {
					if !ok {
						t.Errorf("%s: %s", tc.name, check)
					}
				}
			}
		}
	}
	raw, _ := json.MarshalIndent(rows, "", "  ")
	if err := os.WriteFile(report, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
