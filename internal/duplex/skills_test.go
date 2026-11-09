package duplex

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
	"github.com/chuanmingliu/golive/internal/skillsmcp"
	"github.com/chuanmingliu/golive/internal/tools"
)

func skillFixture(t *testing.T, rewrite ...func(string, any) any) skillsmcp.Config {
	t.Helper()
	text := "---\nname: \"test-skill\"\ndescription: \"Quotation and WeChat fixture\"\n---\n\nAsk the caller to open WeChat and locate 服务通知, one step at a time."
	uri := "skill://test-skill/SKILL.md"
	manifest := map[string]any{"uri": uri, "size": len(text), "digest": fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text)))}
	entry := map[string]any{"uri": uri, "frontmatter": map[string]string{"name": "test-skill", "description": "Quotation and WeChat fixture"}, "resources": []any{manifest}, "_meta": map[string]any{"ai.liuchuanming.golive/routing": map[string]any{"events": []string{"quote.revised", "wechat.notification_help"}}}}
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-test-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		var msg struct {
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&msg) != nil {
			http.Error(w, "bad", 400)
			return
		}
		var result any
		switch msg.Method {
		case "skills/list":
			result = map[string]any{"skills": []any{entry}}
		case "skills/get":
			result = map[string]any{"skill": entry}
		case "resources/read":
			result = map[string]any{"contents": []any{map[string]any{"uri": uri, "mimeType": "text/markdown", "text": text}}}
		case "prompts/get":
			var args map[string]string
			_ = json.Unmarshal(msg.Params["arguments"], &args)
			var state map[string]string
			_ = json.Unmarshal([]byte(args["state"]), &state)
			messages := []any{}
			if args["event"] == "quote.revised" || (args["event"] == "auto" && strings.Contains(args["utterance"], "报价")) || (state["wechat.status"] == "consented" && (args["event"] != "auto" || strings.Contains(args["utterance"], "微信"))) {
				messages = append(messages, map[string]any{"content": map[string]any{"type": "resource_link", "uri": uri, "_meta": map[string]any{"ai.liuchuanming.golive/turn": manifest}}})
			}
			result = map[string]any{"_meta": map[string]any{"ai.liuchuanming.golive/turn": map[string]any{"revision": 1, "auto_selection": true}}, "messages": messages}
		default:
			http.Error(w, "unknown", 400)
			return
		}
		if len(rewrite) > 0 {
			result = rewrite[0](msg.Method, result)
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	t.Cleanup(svr.Close)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("private-test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	return skillsmcp.Config{URL: svr.URL, TokenFile: token, TimeoutMS: 150, StateKeys: []string{"wechat.status"}}
}

// This scripted provider isolates engine/MCP scheduling from model variability.
type skillScenarioLLM struct {
	mu        sync.Mutex
	event     string
	loaded    bool
	submitted bool
	requests  []provider.LLMRequest
}

func (s *skillScenarioLLM) Name() string { return "skill-simulation" }
func (s *skillScenarioLLM) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	hasLoader := false
	for _, def := range req.Tools {
		if strings.Contains(string(def), `"name":"load_business_skill"`) {
			hasLoader = true
		}
	}
	var call *provider.ToolCall
	text := "您好，请问您想了解什么？"
	if hasLoader && s.event != "" && !s.loaded {
		s.loaded = true
		args, _ := json.Marshal(map[string]string{"event": s.event})
		call = &provider.ToolCall{ID: "guidance", Name: skillsmcp.ToolName, Arguments: string(args)}
		text = ""
	} else if s.event == "quote.revised" && !s.submitted {
		s.submitted = true
		text = "我按刚才的方案为您计算。"
		call = &provider.ToolCall{ID: "quote", Name: "quote_total", Arguments: `{"config":{"三者险":"300万"}}`}
	} else if s.event == "wechat.notification_help" {
		text = "请打开微信，找到服务通知。"
	}
	s.mu.Unlock()
	out := make(chan provider.LLMDelta, 2)
	go func() {
		defer close(out)
		select {
		case <-time.After(120 * time.Millisecond):
		case <-ctx.Done():
			return
		}
		if text != "" {
			out <- provider.LLMDelta{Text: text}
		}
		if call != nil {
			out <- provider.LLMDelta{ToolCall: call}
		}
	}()
	return out, nil
}

type observedQuote struct {
	req       chan tools.Request
	delay     time.Duration
	completed chan time.Duration
	started   chan time.Time
}

func (q observedQuote) Execute(ctx context.Context, _ tools.Tool, r tools.Request) tools.Result {
	began := time.Now()
	if q.started != nil {
		q.started <- began
	}
	q.req <- r
	select {
	case <-time.After(q.delay):
	case <-ctx.Done():
		return tools.Result{Err: ctx.Err()}
	}
	if q.completed != nil {
		q.completed <- time.Since(began)
	}
	return tools.Result{Output: json.RawMessage(`{"status":"quote","total":3200,"currency":"CNY"}`), Took: q.delay}
}
func runSkillScenario(t *testing.T, cfg skillsmcp.Config, event string, finishQuote bool) (time.Duration, []provider.LLMRequest) {
	t.Helper()
	opts := testConfig()
	opts.ToolsMemory = true
	opts.Duplex.BackendPrefixWarm = false
	opts.Duplex.BackendPrefixWarmStart = false
	opts.SkillsMCP = cfg
	opts.Tools = []config.ToolConfig{{Name: "quote_total", Description: "Calculate premium", URL: "http://fixture", Async: true, ETASeconds: 3}}
	collector := newCollector(opts.ClientRate)
	llm := &skillScenarioLLM{event: event}
	quote := observedQuote{req: make(chan tools.Request, 2), delay: 3 * time.Second, completed: make(chan time.Duration, 1)}
	engine := New(Options{Cfg: opts, SessionID: "skills-sim", Instructions: "原始业务提示", ClientRate: opts.ClientRate}, Deps{ASR: mock.NewASR(""), LLM: llm, TTS: mock.NewTTS(), Emit: collector.emit, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	engine.toolExec = quote
	ctx, cancel := context.WithCancel(context.Background())
	engine.Start(ctx)
	defer func() { engine.Close(); cancel() }()
	engine.SeedMemory(map[string]any{"wechat": map[string]any{"status": "consented"}, "quote": map[string]any{"交强险": true, "三者险": "200万"}})
	text := "普通问题"
	if event == "quote.revised" {
		text = "修改报价"
	}
	if event == "wechat.notification_help" {
		text = "怎么添加微信"
	}
	began := time.Now()
	engine.SubmitItem(live.ResponseItem{Type: "message", Role: "user", Content: live.TextContent("input_text", text)}, "")
	engine.ContinueResponse("")
	waitFor(t, "first simulated audio", 4*time.Second, func() bool { return collector.audioMS() > 0 })
	collector.mu.Lock()
	var first time.Time
	for i, ev := range collector.events {
		if _, ok := ev.(live.OutputAudioDelta); ok {
			first = collector.at[i]
			break
		}
	}
	collector.mu.Unlock()
	if event == "quote.revised" {
		select {
		case req := <-quote.req:
			if !strings.Contains(string(req.Memory), "交强险") || !strings.Contains(string(req.Arguments), "300万") {
				t.Fatal("lost quotation context")
			}
		case <-time.After(time.Second):
			t.Fatal("quotation was not submitted")
		}
	}
	if finishQuote {
		select {
		case elapsed := <-quote.completed:
			t.Logf("QUOTATION_CALCULATION_MS %.3f", float64(elapsed.Microseconds())/1000)
		case <-time.After(4 * time.Second):
			t.Fatal("quotation did not complete")
		}
		waitFor(t, "quotation delivered", time.Second, func() bool {
			for _, event := range collector.taskEvents() {
				if event.Status == "delivered" {
					return true
				}
			}
			return false
		})
	}
	llm.mu.Lock()
	requests := append([]provider.LLMRequest(nil), llm.requests...)
	llm.mu.Unlock()
	if len(requests) == 0 || !strings.HasPrefix(requests[0].Messages[0].Content, "原始业务提示") {
		t.Fatal("original prompt lost")
	}
	return first.Sub(began), requests
}
func TestSkillsActiveCallRendering(t *testing.T) {
	cfg := skillFixture(t)
	for _, event := range []string{"", "quote.revised", "wechat.notification_help"} {
		t.Run(event, func(t *testing.T) {
			_, requests := runSkillScenario(t, cfg, event, false)
			if event == "" && len(requests) != 1 {
				t.Fatal("normal response gained an inference")
			}
			if event != "" {
				loaded := false
				for _, req := range requests {
					for _, m := range req.Messages {
						if m.Name == skillsmcp.ToolName && strings.Contains(m.Content, `"status":"loaded"`) {
							loaded = true
						}
					}
				}
				if !loaded {
					t.Fatal("guidance did not reach LLM continuation")
				}
			}
		})
	}
}
func TestSkillsExpireAtTurnAndMemoryBoundary(t *testing.T) {
	h := []provider.Message{{Role: provider.RoleTool, Name: skillsmcp.ToolName, ToolCallID: "c", Content: `{"status":"loaded","scope":"turn-a","memory_version":1,"guidance":[{"text":"old"}]}`}}
	for _, tc := range []struct {
		scope   string
		version int
	}{{"turn-b", 1}, {"turn-a", 2}} {
		got := expiredSkillHistory(h, tc.scope, tc.version)
		if !strings.Contains(got[0].Content, "expired") || got[0].ToolCallID != "c" {
			t.Fatal(got)
		}
	}
	if !strings.Contains(expiredSkillHistory(h, "turn-a", 1)[0].Content, "old") {
		t.Fatal("same-turn instruction expired")
	}
	if !strings.Contains(h[0].Content, "old") {
		t.Fatal("mutated canonical history")
	}
}

func TestSkillsDisabledPreservesExistingToolHistory(t *testing.T) {
	legacy := provider.Message{Role: provider.RoleTool, Name: skillsmcp.ToolName, ToolCallID: "legacy", Content: `{"existing":"application tool"}`}
	e := New(Options{Cfg: testConfig(), History: []provider.Message{legacy}}, Deps{})
	defer e.Close()
	messages := e.snapshotMessages(nil, false)
	for _, message := range messages {
		if message.ToolCallID == legacy.ToolCallID && message.Content == legacy.Content {
			return
		}
	}
	t.Fatal("disabled integration changed existing application history")
}
func TestSkillsNoMatchAndDeadlineFallback(t *testing.T) {
	cfg := skillFixture(t)
	client, err := skillsmcp.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := client.Load(context.Background(), "wechat.notification_help", json.RawMessage(`{"wechat":{"status":"added"}}`))
	if err != nil || len(docs) != 0 {
		t.Fatal("completed workflow still loaded", err)
	}
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(150 * time.Millisecond) }))
	defer stalled.Close()
	cfg.URL = stalled.URL
	cfg.TimeoutMS = 30
	elapsed, reqs := runSkillScenario(t, cfg, "", false)
	if elapsed > time.Second || len(reqs) != 1 {
		t.Fatal("fallback blocked response", elapsed)
	}
	for _, def := range reqs[0].Tools {
		if strings.Contains(string(def), skillsmcp.ToolName) {
			t.Fatal("unavailable discovery exposed loader")
		}
	}
}

func TestSkillsBoxLatencySimulation(t *testing.T) {
	url := os.Getenv("SKILLS_SIM_URL")
	if url == "" {
		t.Skip("requires the deployed skills service")
	}
	cfg := skillsmcp.Config{URL: url, TokenFile: os.Getenv("SKILLS_SIM_TOKEN_FILE"), TimeoutMS: 150, StateKeys: []string{"wechat.status"}}
	for _, event := range []string{"", "quote.revised", "wechat.notification_help"} {
		timings := map[string][]float64{"baseline": {}, "skills": {}}
		for n := 0; n < 20; n++ {
			for _, mode := range []string{"baseline", "skills"} {
				selected := skillsmcp.Config{}
				if mode == "skills" {
					selected = cfg
				}
				elapsed, requests := runSkillScenario(t, selected, event, false)
				if mode == "skills" && event != "" {
					loaded := false
					for _, req := range requests {
						for _, m := range req.Messages {
							if m.Name == skillsmcp.ToolName && strings.Contains(m.Content, `"status":"loaded"`) {
								loaded = true
							}
						}
					}
					if !loaded {
						t.Fatal("candidate silently fell back")
					}
				}
				timings[mode] = append(timings[mode], float64(elapsed.Microseconds())/1000)
			}
		}
		for mode, values := range timings {
			sort.Float64s(values)
			t.Logf("SKILLS_SIM event=%q mode=%s n=%d first_content_p50_ms=%.3f first_content_p95_ms=%.3f", event, mode, len(values), values[9], values[18])
		}
	}
	runSkillScenario(t, skillsmcp.Config{}, "quote.revised", true)
	runSkillScenario(t, cfg, "quote.revised", true)
}

func TestSkillsRejectChangedContentAndPublication(t *testing.T) {
	for _, target := range []string{"resources/read", "prompts/get"} {
		t.Run(target, func(t *testing.T) {
			reads := 0
			cfg := skillFixture(t, func(method string, result any) any {
				if method != target {
					return result
				}
				reads++
				object := result.(map[string]any)
				if method == "resources/read" {
					object["contents"].([]any)[0].(map[string]any)["text"] = "changed body"
				} else if reads > 1 {
					object["_meta"] = map[string]any{"ai.liuchuanming.golive/turn": map[string]any{"revision": 2}}
				}
				return object
			})
			client, err := skillsmcp.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			guidance, err := client.Load(context.Background(), "quote.revised", nil)
			if err == nil || len(guidance) != 0 {
				t.Fatal("inconsistent guidance accepted")
			}
		})
	}
}

func TestSkillsInterruptedFetchDoesNotResumeAnswer(t *testing.T) {
	fetching, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	cfg := skillFixture(t, func(method string, result any) any {
		if method == "resources/read" {
			once.Do(func() { close(fetching) })
			<-release
		}
		return result
	})
	defer close(release)
	opts := testConfig()
	opts.ToolsMemory = true
	opts.SkillsMCP = cfg
	opts.Duplex.BackendPrefixWarm = false
	opts.Duplex.BackendPrefixWarmStart = false
	llm := &skillScenarioLLM{event: "quote.revised"}
	collector := newCollector(opts.ClientRate)
	e := New(Options{Cfg: opts}, Deps{ASR: mock.NewASR(""), LLM: llm, TTS: mock.NewTTS(), Emit: collector.emit, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.Start(context.Background())
	defer e.Close()
	e.SubmitItem(live.ResponseItem{Type: "message", Role: "user", Content: live.TextContent("input_text", "修改报价")}, "")
	e.ContinueResponse("")
	select {
	case <-fetching:
	case <-time.After(2 * time.Second):
		t.Fatal("fetch did not begin")
	}
	e.Interrupt()
	// Wait beyond the fetch deadline so a late fallback has had time to resume.
	time.Sleep(300 * time.Millisecond)
	llm.mu.Lock()
	count := len(llm.requests)
	llm.mu.Unlock()
	if count != 0 || collector.audioMS() != 0 {
		t.Fatal("interrupted skill request resumed speech")
	}
}

func TestSkillsLegacyServiceFallsBack(t *testing.T) {
	cfg := skillFixture(t, func(method string, result any) any {
		if method == "prompts/get" {
			result.(map[string]any)["_meta"] = map[string]any{"ai.liuchuanming.golive/turn": map[string]any{"revision": 1}}
		}
		return result
	})
	client, err := skillsmcp.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := client.LoadForTurn(context.Background(), "修改报价", nil)
	if err == nil || len(docs) != 0 {
		t.Fatal("old service silently accepted automatic selection")
	}
}
