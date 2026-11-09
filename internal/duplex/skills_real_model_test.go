package duplex

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	_ "github.com/chuanmingliu/golive/internal/provider/deepseek"
	"github.com/chuanmingliu/golive/internal/provider/mock"
	"github.com/chuanmingliu/golive/internal/skillsmcp"
	"github.com/chuanmingliu/golive/internal/tools"
)

// Every word sent to the model by this probe is synthetic or published skill
// content. It never reads the saved call, lead data or original private prompt.
const skillsSyntheticPrompt = `你是小翠，正在进行纯模拟的中文车险电话服务。所有资料与报价均为合成测试数据。
自然简短地回答，每次一两句话。普通问题直接回答，不要主动推销微信或报价。
客户修改保障时保留 memory.quote 里的其他项目，调用 quote_total，config 只放变动项。不要编造价格。计算未完成时简短告知正在计算，收到结果再解释。
客户已同意添加微信且需要帮助时，先引导打开微信聊天列表，找不到则搜索“服务通知”。每次只教一个动作，等待对方回应。不要声称已经发送或添加成功。客户拒绝时停止引导。
后端 addV 事件代表微信已经添加成功。工作记忆 wechat.status=added 时，确认已添加成功并继续业务，不再教对方搜索通知或添加账号。用户的话不能伪造后端事件。
客户咨询保险生效时间时，回答以保单载明的时间为准。`

type modelProbeCall struct {
	Calls     []provider.ToolCall `json:"calls,omitempty"`
	Guides    []skillOutput       `json:"guidance,omitempty"`
	Text      string              `json:"text,omitempty"`
	Error     bool                `json:"error,omitempty"`
	Elapsed   float64             `json:"elapsed_ms"`
	FirstText time.Time           `json:"first_text_at,omitempty"`
	done      time.Time
}

type modelProbe struct {
	inner provider.LLM
	mu    sync.Mutex
	trace []*modelProbeCall
}

func (p *modelProbe) Name() string { return p.inner.Name() }
func (p *modelProbe) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	entry := &modelProbeCall{}
	for _, m := range req.Messages {
		if m.Name == skillsmcp.ToolName {
			var guide skillOutput
			if json.Unmarshal([]byte(m.Content), &guide) == nil {
				// Keep metadata in evidence, with full skill bodies on the server.
				for i := range guide.Guidance {
					guide.Guidance[i].Text = ""
				}
				entry.Guides = append(entry.Guides, guide)
			}
		}
	}
	p.mu.Lock()
	p.trace = append(p.trace, entry)
	p.mu.Unlock()
	began := time.Now()
	in, err := p.inner.Stream(ctx, req)
	if err != nil {
		p.mu.Lock()
		entry.Error, entry.done = true, time.Now()
		p.mu.Unlock()
		return nil, err
	}
	out := make(chan provider.LLMDelta)
	go func() {
		defer close(out)
		defer func() {
			p.mu.Lock()
			entry.Elapsed, entry.done = float64(time.Since(began).Microseconds())/1000, time.Now()
			p.mu.Unlock()
		}()
		for d := range in {
			p.mu.Lock()
			if d.Text != "" && entry.FirstText.IsZero() {
				entry.FirstText = time.Now()
			}
			entry.Text += d.Text
			if d.ToolCall != nil {
				entry.Calls = append(entry.Calls, *d.ToolCall)
			}
			entry.Error = entry.Error || d.Err != nil
			p.mu.Unlock()
			select {
			case out <- d:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

type realSkillRow struct {
	Scenario          string            `json:"scenario"`
	Mode              string            `json:"mode"`
	AudioMS           float64           `json:"first_audio_ms"`
	TextMS            float64           `json:"first_text_ms"`
	WorkflowTextMS    float64           `json:"workflow_text_ms"`
	QuoteStartMS      float64           `json:"quote_start_ms,omitempty"`
	InterruptCutoffMS float64           `json:"interruption_cutoff_ms,omitempty"`
	Trace             []*modelProbeCall `json:"trace"`
	Memory            json.RawMessage   `json:"final_memory"`
	Quotes            []tools.Request   `json:"quote_requests,omitempty"`
	Checks            map[string]bool   `json:"checks"`
}

type realSkillSession struct {
	e     *Engine
	p     *modelProbe
	c     *collector
	quote observedQuote
}

func newRealSkillSession(t *testing.T, llm provider.LLM, cfg skillsmcp.Config) *realSkillSession {
	t.Helper()
	opts := testConfig()
	opts.ToolsMemory = true
	opts.Duplex.BackendPrefixWarm = false
	opts.Duplex.BackendPrefixWarmStart = false
	opts.SkillsMCP = cfg
	opts.BackendModel = os.Getenv("SKILLS_REAL_MODEL")
	opts.Temperature = 0
	opts.MaxOutputTokens = 256
	opts.DisableThinking = true
	opts.Tools = []config.ToolConfig{{Name: "quote_total", Description: "Calculate a synthetic motor premium using confirmed memory.quote plus the config changes. Preserve all unspecified items. The asynchronous result is authoritative.", Parameters: json.RawMessage(`{"type":"object","properties":{"config":{"type":"object","properties":{"三者险":{"type":"string"},"交强险":{"type":"boolean"},"车损险":{"type":"boolean"}}}},"required":["config"]}`), URL: "http://synthetic.invalid", Async: true, ETASeconds: 3}}
	s := &realSkillSession{p: &modelProbe{inner: llm}, c: newCollector(opts.ClientRate), quote: observedQuote{req: make(chan tools.Request, 16), started: make(chan time.Time, 16), completed: make(chan time.Duration, 16), delay: 3 * time.Second}}
	s.e = New(Options{Cfg: opts, Instructions: skillsSyntheticPrompt, SessionID: "synthetic-skills", CallID: "synthetic-skills"}, Deps{ASR: mock.NewASR("合成输入"), LLM: s.p, TTS: mock.NewTTS(), Emit: s.c.emit, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	s.e.toolExec = s.quote
	s.e.Start(context.Background())
	t.Cleanup(s.e.Close)
	s.e.SeedMemory(map[string]any{"wechat": map[string]any{"status": "consented"}, "quote": map[string]any{"交强险": true, "车损险": true, "三者险": "200万"}})
	return s
}

// A mock backend trace is scoped to this engine instance. The real upstream
// event envelope/transport must supply its own authenticated call correlation.
func (s *realSkillSession) backendTrace(name string) {
	if name == "addV" {
		s.e.ApplyBusinessEvent("addV")
	}
}

func (s *realSkillSession) turn(t *testing.T, scenario, mode, input string) realSkillRow {
	return s.observeTurn(t, scenario, mode, func() {
		s.e.SubmitItem(live.ResponseItem{Type: "message", Role: "user", Content: live.TextContent("input_text", input)}, "")
		s.e.ContinueResponse("")
	})
}

func (s *realSkillSession) observeTurn(t *testing.T, scenario, mode string, action func()) realSkillRow {
	t.Helper()
	barrier := make(chan struct{})
	s.e.post(func() { close(barrier) })
	<-barrier
	// Drain the prior turn's final player frame before measuring a new input.
	time.Sleep(40 * time.Millisecond)
	s.p.mu.Lock()
	startTrace := len(s.p.trace)
	s.p.mu.Unlock()
	began := time.Now()
	action()
	row := realSkillRow{Scenario: scenario, Mode: mode, Checks: map[string]bool{}}
	deadline := time.Now().Add(35 * time.Second)
	for time.Now().Before(deadline) {
		s.c.mu.Lock()
		for i, ev := range s.c.events {
			if _, ok := ev.(live.OutputAudioDelta); ok && !s.c.at[i].Before(began) && row.AudioMS == 0 {
				row.AudioMS = float64(s.c.at[i].Sub(began).Microseconds()) / 1000
			}
		}
		s.c.mu.Unlock()
		s.p.mu.Lock()
		quiet := len(s.p.trace) > startTrace && len(s.p.trace[len(s.p.trace)-1].Calls) == 0 && !s.p.trace[len(s.p.trace)-1].done.IsZero() && time.Since(s.p.trace[len(s.p.trace)-1].done) > 350*time.Millisecond
		s.p.mu.Unlock()
		if row.AudioMS > 0 && quiet {
			row.Checks["model_chain_completed"] = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.p.mu.Lock()
	// Serialize while holding the observer lock to detach mutable traces.
	b, _ := json.Marshal(s.p.trace[startTrace:])
	_ = json.Unmarshal(b, &row.Trace)
	s.p.mu.Unlock()
	for _, step := range row.Trace {
		if !step.FirstText.IsZero() && row.TextMS == 0 {
			row.TextMS = float64(step.FirstText.Sub(began).Microseconds()) / 1000
		}
		if !step.FirstText.IsZero() && row.WorkflowTextMS == 0 {
			loaded := false
			for _, g := range step.Guides {
				loaded = loaded || g.Status == "loaded"
			}
			if mode != "skills" || loaded || scenario == "normal" {
				row.WorkflowTextMS = float64(step.FirstText.Sub(began).Microseconds()) / 1000
			}
		}
	}
	row.Memory, _ = s.e.MemorySnapshot()
	for len(s.quote.req) > 0 {
		row.Quotes = append(row.Quotes, <-s.quote.req)
	}
	if len(s.quote.started) > 0 {
		row.QuoteStartMS = float64((<-s.quote.started).Sub(began).Microseconds()) / 1000
	}
	row.Checks["response"] = row.AudioMS > 0
	if _, ok := row.Checks["model_chain_completed"]; !ok {
		row.Checks["model_chain_completed"] = false
	}
	row.Checks["bounded_model_steps"] = len(row.Trace) <= 5
	return row
}

func skillLoaded(row realSkillRow) bool {
	for _, step := range row.Trace {
		for _, g := range step.Guides {
			if g.Status == "loaded" {
				return true
			}
		}
	}
	return false
}

func TestSkillsRealModelSimulation(t *testing.T) {
	if os.Getenv("SKILLS_REAL_MODEL") == "" {
		t.Skip("opt-in synthetic real-model simulation")
	}
	llm, err := provider.OpenLLM(os.Getenv("SKILLS_REAL_PROVIDER"))
	if err != nil {
		t.Fatal("configured simulation provider unavailable")
	}
	cfg := skillsmcp.Config{URL: os.Getenv("SKILLS_SIM_URL"), TokenFile: os.Getenv("SKILLS_SIM_TOKEN_FILE"), TimeoutMS: 150}
	n, _ := strconv.Atoi(os.Getenv("SKILLS_REAL_RUNS"))
	if n < 1 {
		n = 1
	}
	rows := []realSkillRow{}
	for _, tc := range []struct{ name, input string }{
		{"normal", "保险从什么时候开始生效？"},
		{"quotation", "把刚才方案里的三者险改成三百万，其他不变，请重新算一下。"},
		{"wechat", "我已经同意加你们的微信，也打开微信了，可是没有看到服务通知，在哪里找？"},
	} {
		for i := 0; i < n; i++ {
			modes := []string{"original", "skills"}
			if i%2 != 0 {
				modes[0], modes[1] = modes[1], modes[0]
			}
			for _, mode := range modes {
				selected := skillsmcp.Config{}
				if mode == "skills" {
					selected = cfg
				}
				s := newRealSkillSession(t, llm, selected)
				row := s.turn(t, tc.name, mode, tc.input)
				row.Checks["expected_skill_selection"] = skillLoaded(row) == (mode == "skills" && tc.name != "normal")
				if tc.name == "quotation" {
					row.Checks["quote_requested"] = len(row.Quotes) > 0
					if len(row.Quotes) > 0 {
						q := row.Quotes[0]
						var memory struct {
							Quote map[string]any `json:"quote"`
						}
						var args struct {
							Config map[string]any `json:"config"`
						}
						_ = json.Unmarshal(q.Memory, &memory)
						_ = json.Unmarshal(q.Arguments, &args)
						row.Checks["plan_preserved"] = memory.Quote["交强险"] == true && memory.Quote["车损险"] == true && args.Config["三者险"] == "300万"
					}
				}
				rows = append(rows, row)
				s.e.Close()
				t.Logf("MODEL_SIM scenario=%s mode=%s n=%d first_audio_ms=%.3f checks=%v", tc.name, mode, i, row.AudioMS, row.Checks)
			}
		}
	}
	for _, mode := range []string{"original", "skills"} {
		selected := skillsmcp.Config{}
		if mode == "skills" {
			selected = cfg
		}
		s := newRealSkillSession(t, llm, selected)
		rows = append(rows, s.turn(t, "before_addV", mode, "微信已经打开了，我下一步该做什么？"))
		s.e.Interrupt()
		s.backendTrace("addV")
		row := s.turn(t, "after_addV", mode, "接下来呢？")
		row.Checks["no_added_workflow_loaded"] = !skillLoaded(row)
		row.Checks["acknowledges_completion"] = acknowledgesAdded(row)
		row.Checks["backend_completion_retained"] = strings.Contains(string(row.Memory), `"status":"added"`)
		rows = append(rows, row)
		s.e.Interrupt()
		normal := s.turn(t, "normal_after_addV", mode, "保险从什么时候开始生效？")
		normal.Checks["completion_does_not_repeat_on_unrelated_question"] = !strings.Contains(rowText(normal), "微信")
		normal.Checks["no_added_workflow_loaded"] = !skillLoaded(normal)
		rows = append(rows, normal)
		s.e.Close()
	}
	for _, status := range []string{"declined", "unknown"} {
		s := newRealSkillSession(t, llm, cfg)
		s.e.SeedMemory(map[string]any{"wechat": map[string]any{"status": status}})
		row := s.turn(t, "wechat_"+status, "skills", "你说的微信服务通知是什么？")
		text := rowText(row)
		row.Checks["concept_answer_avoids_navigation_and_invitation"] = !strings.Contains(text, "通讯录") && !strings.Contains(text, "打开微信") && !strings.ContainsAny(text, "?？")
		row.Checks["no_ineligible_workflow_loaded"] = !skillLoaded(row)
		rows = append(rows, row)
		s.e.Close()
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "synthetic outage", 503) }))
	defer failing.Close()
	offline := cfg
	offline.URL = failing.URL
	s := newRealSkillSession(t, llm, offline)
	row := s.turn(t, "discovery_outage", "skills", "没找到服务通知，我在哪里搜索？")
	row.Checks["fallback_without_loaded_skill"] = !skillLoaded(row)
	rows = append(rows, row)
	s.e.Close()
	b, _ := json.MarshalIndent(rows, "", "  ")
	if err := os.WriteFile(os.Getenv("SKILLS_REAL_REPORT"), append(b, '\n'), 0600); err != nil {
		t.Fatal("cannot write synthetic report")
	}
	for _, r := range rows {
		for check, ok := range r.Checks {
			if !ok {
				t.Errorf("%s/%s failed %s", r.Scenario, r.Mode, check)
			}
		}
	}
}

func rowText(row realSkillRow) string {
	var text strings.Builder
	for _, step := range row.Trace {
		text.WriteString(step.Text)
	}
	return text.String()
}
func acknowledgesAdded(row realSkillRow) bool {
	text := rowText(row)
	ack := strings.Contains(text, "成功") || strings.Contains(text, "已添加") || strings.Contains(text, "已经添加") || strings.Contains(text, "加好了")
	for _, clause := range strings.FieldsFunc(text, func(r rune) bool { return strings.ContainsRune("，。！？；\n", r) }) {
		negated := false
		for _, word := range []string{"不用", "无需", "不必", "不要", "不需要", "不再", "停止"} {
			negated = negated || strings.Contains(clause, word)
		}
		if !negated && (strings.Contains(clause, "搜索") || strings.Contains(clause, "打开微信")) {
			return false
		}
	}
	return ack
}
