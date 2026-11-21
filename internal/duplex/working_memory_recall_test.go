package duplex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
	"github.com/chuanmingliu/golive/internal/skillsmcp"
	"github.com/chuanmingliu/golive/internal/tools"
)

func TestWorkingMemoryRecallRejectsInvalidSelectorOutput(t *testing.T) {
	candidates := map[string]string{"quote": "quotation", "wechat": "WeChat"}
	for _, raw := range []string{`["quote","unknown"]`, `"quote"`, `["quote"] explanation`, `["quote","quote","quote","quote","quote"]`} {
		if ids := parseRecallCandidates(raw, candidates); len(ids) > 0 {
			t.Fatalf("accepted invalid candidates: %q", raw)
		}
	}
	if ids := parseRecallCandidates(`["quote","wechat","quote"]`, candidates); strings.Join(ids, ",") != "quote,wechat" {
		t.Fatalf("lost selection order or deduplication: %v", ids)
	}
}

type recallSimLLM struct {
	started chan struct{}
	pending chan struct{}
	turns   chan provider.LLMRequest
}

func (*recallSimLLM) Name() string { return "working-memory-sim" }

func (p *recallSimLLM) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	out := make(chan provider.LLMDelta, 2)
	policy := req.Messages[0].Content
	if strings.Contains(policy, "simulation selector policy") {
		payload := req.Messages[1].Content
		if strings.Contains(payload, "先看看") {
			select {
			case p.started <- struct{}{}:
			default:
			}
			select {
			case <-p.pending:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		ids := `[]`
		switch {
		case strings.Contains(payload, "两件事"):
			ids = `["motor-quotation","wechat-service-notification"]`
		case strings.Contains(payload, "微信") || strings.Contains(payload, "先看看"):
			ids = `["wechat-service-notification"]`
		case strings.Contains(payload, "三者"):
			ids = `["tool:quote_total"]`
		}
		out <- provider.LLMDelta{Text: ids}
	} else {
		p.turns <- req
		text := latestUserText(req.Messages)
		switch {
		case strings.Contains(text, "三者想调整"):
			out <- provider.LLMDelta{ToolCall: &provider.ToolCall{ID: "quote", Name: "quote_total", Arguments: `{"config":{"三者险":"300万"}}`}}
		case strings.Contains(policy, "<additional_skill_instructions>") && strings.Contains(text, "微信"):
			out <- provider.LLMDelta{Text: "请打开微信，找到服务通知。"}
		default:
			out <- provider.LLMDelta{Text: "好的。"}
		}
	}
	close(out)
	return out, nil
}

// Uses the actual MCP service and stable scripted model, ASR and TTS outputs.
// Start TestSkillsSimulationService with a fresh SKILLS_FIXTURE_DIR first.
func TestWorkingMemoryRecallCallSim(t *testing.T) {
	if os.Getenv("GOLIVE_WM_SIM") != "1" {
		t.Skip("requires the isolated local Skills MCP simulation service")
	}
	const serverURL = "http://127.0.0.1:18795"
	const prompt = "原始业务提示：我是小翠。"
	httpClient := &http.Client{Timeout: 3 * time.Second}
	admin := func(method string, data []byte) map[string]any {
		t.Helper()
		req, err := http.NewRequest(method, serverURL+"/admin/catalog", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer simulation-admin-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var catalog map[string]any
		if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&catalog) != nil {
			t.Fatalf("simulation panel returned %s", resp.Status)
		}
		return catalog
	}
	catalog := admin(http.MethodGet, nil)
	catalog["working_memory_recall"] = map[string]any{
		"enabled": true, "function": skillsmcp.RecallFunction, "selector_prompt": "simulation selector policy",
		"candidates": []string{"motor-quotation", "wechat-service-notification", "tool:quote_total"},
	}
	body, _ := json.Marshal(catalog)
	admin(http.MethodPut, body)
	token := filepath.Join(t.TempDir(), "runtime")
	if err := os.WriteFile(token, []byte("simulation-runtime-token"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, partial, final, state string
		ready, slow, changeState    bool
		guidance                    []string
	}{
		{name: "quotation", partial: "三者想调整到三百万", state: "consented", ready: true, guidance: []string{"motor-quotation"}},
		{name: "wechat", partial: "微信服务通知如何找", final: "微信服务通知如何找？", state: "consented", ready: true, guidance: []string{"wechat-service-notification"}},
		{name: "multiple", partial: "还有两件事，分别怎么处理", state: "consented", ready: true, guidance: []string{"motor-quotation", "wechat-service-notification"}},
		{name: "slow", partial: "我想先看看", state: "consented", slow: true},
		{name: "revised-transcript", partial: "微信服务通知如何找", final: "微信服务通知如何找……先不用了", state: "consented", ready: true},
		{name: "state-change", partial: "微信服务通知如何找", state: "consented", ready: true, changeState: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.SkillsMCP = skillsmcp.Config{URL: serverURL + "/mcp", TokenFile: token, TimeoutMS: 300}
			mcpStarted := make(chan struct{}, 1)
			mcpRelease := make(chan struct{})
			if c.slow {
				upstream, _ := url.Parse(serverURL)
				proxy := httputil.NewSingleHostReverseProxy(upstream)
				gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Mcp-Name") == "outbound-turn" {
						select {
						case mcpStarted <- struct{}{}:
						default:
						}
						select {
						case <-mcpRelease:
						case <-r.Context().Done():
							return
						}
					}
					proxy.ServeHTTP(w, r)
				}))
				t.Cleanup(gate.Close)
				cfg.SkillsMCP.URL = gate.URL + "/mcp"
				cfg.SkillsMCP.TimeoutMS = 1500
			}
			cfg.Duplex.BackendPrefixWarm = false
			cfg.Duplex.BackendPrefixWarmStart = false
			cfg.ToolsMemory = true
			cfg.Tools = []config.ToolConfig{{Name: "quote_total", Description: "Calculate premium", URL: "http://fixture", Async: true}}
			llm := &recallSimLLM{started: make(chan struct{}, 1), pending: make(chan struct{}), turns: make(chan provider.LLMRequest, 16)}
			quote := observedQuote{req: make(chan tools.Request, 2), delay: 200 * time.Millisecond, completed: make(chan time.Duration, 1)}
			collector := newCollector(cfg.ClientRate)
			e := New(Options{Cfg: cfg, SessionID: "wm-" + c.name, Instructions: prompt, ClientRate: cfg.ClientRate}, Deps{
				ASR: mock.NewASR(""), LLM: llm, TTS: mock.NewTTS(), Emit: collector.emit, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			e.toolExec = quote
			e.Start(context.Background())
			t.Cleanup(e.Close)
			seed := func(state string) {
				e.SeedMemory(map[string]any{"wechat": map[string]any{"status": state}, "quote": map[string]any{"交强险": true}})
			}
			seed(c.state)
			loopValue := func(f func() bool) bool {
				ch := make(chan bool, 1)
				e.post(func() { ch <- f() })
				select {
				case result := <-ch:
					return result
				case <-time.After(time.Second):
					t.Fatal("engine loop stalled")
					return false
				}
			}
			waitFor(t, "published recall registration", 2*time.Second, func() bool {
				return loopValue(func() bool { return e.workingMemoryRecall.registration.Enabled })
			})
			e.post(func() {
				// Feed the same hypotheses that the production ASR event path receives.
				run := &asrRun{id: 1, openedAt: time.Now()}
				e.asrRun = run
				e.onASREvent(asrEvent{run: 1, res: provider.ASRResult{Text: c.partial}})
				e.asrRun = nil
			})
			if c.ready {
				waitFor(t, "verified selector result", 2*time.Second, func() bool {
					return loopValue(func() bool {
						job := e.workingMemoryRecall.job
						return job != nil && job.source == skillsmcp.RecallFunction && len(job.guidance) > 0
					})
				})
			}
			if c.slow {
				select {
				case <-llm.started:
				case <-time.After(time.Second):
					t.Fatal("selector never started")
				}
				select {
				case <-mcpStarted:
				case <-time.After(time.Second):
					t.Fatal("MCP preparation never started")
				}
			}
			if c.changeState {
				seed("declined")
			}
			final := c.final
			if final == "" {
				final = c.partial + "。"
			}
			began := time.Now()
			e.post(func() {
				e.asrRun = &asrRun{id: 1, openedAt: began}
				e.onASREvent(asrEvent{run: 1, res: provider.ASRResult{Text: final, Final: true}})
				e.asrRun = nil
			})
			var req provider.LLMRequest
			select {
			case req = <-llm.turns:
			case <-time.After(time.Second):
				t.Fatal("response did not start while selector could still be pending")
			}
			t.Logf("ASR final to response request: %s", time.Since(began))
			close(llm.pending)
			close(mcpRelease)
			system := req.Messages[0].Content
			if !strings.HasPrefix(system, prompt) || strings.Contains(system, "simulation selector policy") {
				t.Fatal("original prompt was replaced or selector prompt leaked")
			}
			if strings.Contains(system, "<additional_skill_instructions>") != (len(c.guidance) > 0) {
				t.Fatalf("unexpected prompt slot presence for %s", c.name)
			}
			for _, name := range c.guidance {
				var found bool
				for _, message := range req.Messages {
					if message.Name == skillsmcp.ToolName && strings.Contains(message.Content, "skill://"+name+"/SKILL.md") {
						found = true
					}
				}
				if !found || (len(c.guidance) > 1 && strings.Count(system, "### "+name+"\n\n") != 1) {
					t.Fatalf("missing guidance or heading for %s", name)
				}
			}
			if c.name == "quotation" {
				select {
				case call := <-quote.req:
					if !strings.Contains(string(call.Arguments), "300万") || !strings.Contains(string(call.Memory), "交强险") {
						t.Fatal("quotation lost its arguments or memory")
					}
				case <-time.After(time.Second):
					t.Fatal("registered quotation tool did not run")
				}
				select {
				case elapsed := <-quote.completed:
					t.Logf("mock quotation calculation: %s", elapsed)
				case <-time.After(time.Second):
					t.Fatal("mock quotation did not finish")
				}
			} else {
				waitFor(t, "simulated speech", 2*time.Second, func() bool { return collector.audioMS() > 0 })
			}
		})
	}
}
