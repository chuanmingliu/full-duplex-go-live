package duplex

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/requestlog"
	"github.com/chuanmingliu/golive/internal/skillsmcp"
)

type heldRecallLLM struct {
	provider.LLM
	started chan struct{}
	release chan struct{}
}

func (h heldRecallLLM) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	if requestlog.ScopeFrom(ctx).Purpose == "working_memory_recall" {
		select {
		case h.started <- struct{}{}:
		default:
		}
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else {
		select {
		case <-h.release:
		default:
			close(h.release)
		}
	}
	return h.LLM.Stream(ctx, req)
}

// Uses isolated published skills, synthetic ASR events and mock TTS and CRM.
// Both the voice response and enabled selector use the configured real LLM.
func TestWorkingMemoryRecallRealModel(t *testing.T) {
	if os.Getenv("SKILLS_SIM_URL") != "http://127.0.0.1:18795/mcp" || os.Getenv("SKILLS_REAL_PROVIDER") == "" {
		t.Skip("requires isolated MCP fixture and a real-model provider")
	}
	llm, err := provider.OpenLLM(os.Getenv("SKILLS_REAL_PROVIDER"))
	if err != nil {
		t.Fatal("configured simulation provider unavailable")
	}
	n, _ := strconv.Atoi(os.Getenv("SKILLS_REAL_RUNS"))
	if n < 1 || n > 20 {
		t.Fatal("use 1–20 paired trials")
	}
	cfg := skillsmcp.Config{URL: os.Getenv("SKILLS_SIM_URL"), TokenFile: os.Getenv("SKILLS_SIM_TOKEN_FILE"), TimeoutMS: 150}
	panel := &http.Client{Timeout: 5 * time.Second}
	admin := func(method string, data []byte) map[string]any {
		t.Helper()
		req, err := http.NewRequest(method, "http://127.0.0.1:18795/admin/catalog", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer simulation-admin-token")
		req.Header.Set("Content-Type", "application/json")
		res, err := panel.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var catalog map[string]any
		if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&catalog) != nil {
			t.Fatalf("simulation panel returned %s", res.Status)
		}
		return catalog
	}
	publish := func(enabled bool) {
		catalog := admin(http.MethodGet, nil)
		catalog["prompt_rendering"] = map[string]int{"version": 2}
		catalog["working_memory_recall"] = map[string]any{
			"enabled": enabled, "function": skillsmcp.RecallFunction,
			"selector_prompt": "Select every directly relevant skill. Pick motor-quotation for a premium or coverage change. Pick wechat-service-notification for help finding the WeChat service notification. Select both if the caller asks both topics. Abstain on unrelated requests.",
			"candidates":      []string{"motor-quotation", "wechat-service-notification"},
		}
		raw, _ := json.Marshal(catalog)
		admin(http.MethodPut, raw)
	}
	feed := func(e *Engine, text string, final bool) {
		e.post(func() {
			e.asrRun = &asrRun{id: 1, openedAt: time.Now()}
			e.onASREvent(asrEvent{run: 1, res: provider.ASRResult{Text: text, Final: final}})
			e.asrRun = nil
		})
	}
	type result struct {
		realSkillRow
		DispatchMS    float64 `json:"dispatch_ms"`
		CalculationMS float64 `json:"mock_calculation_ms,omitempty"`
	}
	var results []result
	// observeTurn adds 40 ms. Keep the final close to a live ASR update.
	lead := 60 * time.Millisecond
	for _, scenario := range []struct {
		name, input string
		skills      int
		quote       bool
	}{
		{"quotation", "我确认三者险改为三百万，交强险和车损险保持不变，没有其他调整，请按这个方案计算报价", 1, true},
		{"wechat", "我已经同意加微信，请问微信服务通知在哪里找", 1, false},
		{"combined", "我确认三者险改为三百万，交强险和车损险保持不变，没有其他调整，请按这个方案计算报价。也请告诉我微信服务通知在哪里找", 2, true},
		{"held", "请告诉我微信服务通知在哪里找", 1, false},
	} {
		for i := 0; i < n; i++ {
			for _, enabled := range []bool{i%2 == 1, i%2 == 0} {
				if scenario.name == "held" && !enabled {
					continue
				}
				publish(enabled)
				var model provider.LLM = llm
				held := heldRecallLLM{LLM: llm, started: make(chan struct{}, 1), release: make(chan struct{})}
				if scenario.name == "held" {
					model = held
				}
				s := newRealSkillSession(t, model, cfg)
				if enabled {
					waitFor(t, "async catalog registration", time.Second, func() bool {
						ready := make(chan bool, 1)
						s.e.post(func() { ready <- s.e.workingMemoryRecall.registration.Enabled })
						return <-ready
					})
				}
				partial := scenario.input
				if scenario.name == "combined" {
					partial, _, _ = strings.Cut(scenario.input, "。")
				}
				feed(s.e, partial, false)
				if scenario.name == "held" {
					select {
					case <-held.started:
					case <-time.After(time.Second):
						t.Fatal("selector did not reach the hold")
					}
				} else {
					time.Sleep(lead)
				}
				mode := "off"
				if enabled {
					mode = "on"
				}
				var began time.Time
				row := s.observeTurn(t, scenario.name, mode, func() {
					began = time.Now()
					feed(s.e, scenario.input+"。", true)
				})
				r := result{realSkillRow: row}
				if len(row.Trace) > 0 {
					first := row.Trace[0]
					r.DispatchMS = float64(first.Started.Sub(began).Microseconds()) / 1000
					row.Checks["original_prompt"] = first.OriginalPrompt
					if enabled {
						row.Checks["nonblocking_dispatch"] = r.DispatchMS >= 0 && r.DispatchMS < 50
					}
					if scenario.skills == 2 {
						row.Checks["multiple_skill_headings"] = len(first.SkillHeadings) == 2
					}
					for _, out := range first.Guides {
						if scenario.name == "held" {
							continue
						}
						row.Checks["skill_injection"] = out.RenderingVersion == 2 && first.Slot && len(out.Guidance) == scenario.skills
						if enabled {
							row.Checks["ready_guidance"] = out.Source == "published_rules" || out.Source == "rules_or_embedding" || out.Source == skillsmcp.RecallFunction
						}
					}
					if scenario.name == "held" {
						row.Checks["generation_started_while_selector_held"] = r.DispatchMS >= 0 && r.DispatchMS < 50
					} else if _, ok := row.Checks["skill_injection"]; !ok {
						row.Checks["skill_injection"] = false
					}
				}
				row.Checks["response"] = len(row.Trace) > 0 && row.AudioMS > 0 && row.Checks["model_chain_completed"]
				if scenario.quote {
					row.Checks["quotation"] = len(row.Quotes) == 1 && strings.Contains(string(row.Quotes[0].Arguments), "300万") && strings.Contains(string(row.Quotes[0].Memory), "车损险")
					select {
					case elapsed := <-s.quote.completed:
						r.CalculationMS = float64(elapsed.Microseconds()) / 1000
					default:
						row.Checks["quotation"] = false
					}
				}
				if scenario.name == "wechat" {
					row.Checks["wechat_direction"] = strings.Contains(row.ResponseText, "微信") || strings.Contains(row.ResponseText, "服务通知")
					row.Checks["wechat_direction"] = row.Checks["wechat_direction"] && len(row.Quotes) == 0
				}
				s.e.Close()
				var sources []string
				if len(row.Trace) > 0 {
					for _, guide := range row.Trace[0].Guides {
						sources = append(sources, guide.Source)
					}
				}
				t.Logf("RECALL_SIM %s %s dispatch_ms=%.3f audio_ms=%.3f first_text_ms=%.3f mock_calculation_ms=%.3f sources=%v checks=%v", scenario.name, mode, r.DispatchMS, row.AudioMS, row.TextMS, r.CalculationMS, sources, row.Checks)
				results = append(results, r)
				for check, ok := range row.Checks {
					if !ok {
						t.Errorf("%s %s trial %d failed %s", scenario.name, mode, i+1, check)
					}
				}
			}
		}
	}
	raw, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(os.Getenv("SKILLS_REAL_REPORT"), raw, 0600); err != nil {
		t.Fatal("could not write synthetic simulation report")
	}
}
