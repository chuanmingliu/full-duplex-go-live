package duplex

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/skillsmcp"
)

func skillFaultProxy(t *testing.T, cfg skillsmcp.Config, fault string) (skillsmcp.Config, <-chan struct{}, func()) {
	t.Helper()
	target, err := url.Parse(cfg.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &httputil.ReverseProxy{Director: func(r *http.Request) {
		r.URL.Scheme, r.URL.Host, r.URL.Path = target.Scheme, target.Host, target.Path
		r.Host = target.Host
	}}
	reached, release := make(chan struct{}), make(chan struct{})
	var signalOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var msg struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &msg)
		if fault == "unavailable" && msg.Method != "skills/list" {
			http.Error(w, "synthetic retrieval outage", 503)
			return
		}
		if fault == "blocked" && msg.Method == "resources/read" {
			signalOnce.Do(func() { close(reached) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { unblock(); srv.Close() })
	cfg.URL = srv.URL
	return cfg, reached, unblock
}

func probeBarrier(e *Engine) {
	done := make(chan struct{})
	e.post(func() { close(done) })
	<-done
}

func audioAfter(c *collector, cutoff time.Time) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for i, event := range c.events {
		if _, ok := event.(live.OutputAudioDelta); ok && c.at[i].After(cutoff) {
			n++
		}
	}
	return n
}

func lastAudioDelay(c *collector, since time.Time) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ms float64
	for i, event := range c.events {
		if _, ok := event.(live.OutputAudioDelta); ok && c.at[i].After(since) {
			ms = float64(c.at[i].Sub(since).Microseconds()) / 1000
		}
	}
	return ms
}

func TestSkillsRealModelEdges(t *testing.T) {
	if os.Getenv("SKILLS_REAL_MODEL") == "" {
		t.Skip("opt-in synthetic real-model edges")
	}
	llm, err := provider.OpenLLM(os.Getenv("SKILLS_REAL_PROVIDER"))
	if err != nil {
		t.Fatal("configured simulation provider unavailable")
	}
	cfg := skillsmcp.Config{URL: os.Getenv("SKILLS_SIM_URL"), TokenFile: os.Getenv("SKILLS_SIM_TOKEN_FILE"), TimeoutMS: 150}
	rows := []realSkillRow{}
	fallback, _, _ := skillFaultProxy(t, cfg, "unavailable")
	s := newRealSkillSession(t, llm, fallback)
	r := s.turn(t, "retrieval_outage", "skills", "我已同意加微信，微信里搜索服务通知该怎么做？")
	fallbackSeen := false
	for _, step := range r.Trace {
		for _, g := range step.Guides {
			fallbackSeen = fallbackSeen || g.Status == "unavailable"
		}
	}
	r.Checks["retrieval_fallback_observed"] = fallbackSeen
	r.Checks["no_loaded_guidance"] = !skillLoaded(r)
	r.Checks["fallback_keeps_configured_navigation"] = !strings.Contains(rowText(r), "通讯录")
	rows = append(rows, r)
	s.e.Close()

	blocked, reached, release := skillFaultProxy(t, cfg, "blocked")
	s = newRealSkillSession(t, llm, blocked)
	injected := make(chan struct{})
	go func(session *realSkillSession) {
		select {
		case <-reached:
			session.backendTrace("addV")
			probeBarrier(session.e)
			release()
			close(injected)
		case <-session.e.ctx.Done():
		}
	}(s)
	r = s.turn(t, "addV_during_retrieval", "skills", "我已同意加微信，打开微信之后怎么找服务通知？")
	select {
	case <-injected:
		r.Checks["event_injected"] = true
	default:
		r.Checks["event_injected"] = false
	}
	r.Checks["no_stale_guidance_rendered"] = !skillLoaded(r)
	r.Checks["acknowledges_completion"] = acknowledgesAdded(r)
	r.Checks["backend_completion_retained"] = strings.Contains(string(r.Memory), `"status":"added"`)
	rows = append(rows, r)
	s.e.Close()
	release()

	for _, test := range []struct {
		phase, mode   string
		clarification bool
	}{{"retrieval", "skills", false}, {"playback", "original", false}, {"playback", "skills", false}, {"retrieval", "skills", true}} {
		phase, mode := test.phase, test.mode
		selected := cfg
		if mode == "original" {
			selected = skillsmcp.Config{}
		}
		var ready <-chan struct{}
		resume := func() {}
		if phase == "retrieval" {
			selected, ready, resume = skillFaultProxy(t, cfg, "blocked")
		}
		s = newRealSkillSession(t, llm, selected)
		input := "请详细解释保险理赔流程，列出六个具体步骤。"
		if phase == "retrieval" {
			input = "我已同意加微信，怎么找服务通知？"
		}
		s.e.SubmitItem(live.ResponseItem{Type: "message", Role: "user", Content: live.TextContent("input_text", input)}, "")
		s.e.ContinueResponse("")
		observed := false
		until := time.Now().Add(20 * time.Second)
		for time.Now().Before(until) {
			if phase == "retrieval" {
				select {
				case <-ready:
					observed = true
				default:
				}
			} else {
				observed = s.c.audioMS() > 0
			}
			if observed {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		interrupted := time.Now()
		s.e.Interrupt()
		probeBarrier(s.e)
		resume()
		time.Sleep(400 * time.Millisecond)
		late := audioAfter(s.c, interrupted.Add(100*time.Millisecond))
		cutoff := lastAudioDelay(s.c, interrupted)
		next, scenario := "保险从什么时候开始生效？", "interrupt_"+phase+"_recovery"
		if test.clarification {
			next, scenario = "我打开聊天列表了，下一步呢？", "interrupt_clarification"
		}
		r = s.turn(t, scenario, mode, next)
		r.InterruptCutoffMS = cutoff
		r.Checks["interruption_phase_reached"] = observed
		r.Checks["no_old_audio_after_100ms"] = late == 0
		r.Checks["recovered_response"] = r.AudioMS > 0
		text := rowText(r)
		r.Checks["latest_request_separated"] = len(r.Trace) > 0 && r.Trace[0].Request == next && r.Trace[0].Recovery
		if test.clarification {
			r.Checks["clarification_context_preserved"] = strings.Contains(text, "服务通知")
		} else {
			r.Checks["recovery_has_no_old_skill"] = !skillLoaded(r)
			r.Checks["recovery_answers_latest_question"] = strings.Contains(text, "生效") || strings.Contains(text, "保单")
			r.Checks["recovery_does_not_resume_old_task"] = !strings.Contains(text, "服务通知") && !strings.Contains(text, "打开微信") && !strings.Contains(text, "理赔")
		}
		rows = append(rows, r)
		s.e.Close()
	}

	s = newRealSkillSession(t, llm, cfg)
	s.e.SubmitItem(live.ResponseItem{Type: "message", Role: "user", Content: live.TextContent("input_text", "我同意添加微信了，请告诉我怎么找到服务通知。")}, "")
	s.e.ContinueResponse("")
	waitFor(t, "wechat playback", 15*time.Second, func() bool { return s.c.audioMS() > 0 })
	r = s.observeTurn(t, "addV_during_playback", "skills", func() { s.backendTrace("addV") })
	r.Checks["acknowledges_completion"] = acknowledgesAdded(r)
	r.Checks["no_stale_guidance_rendered"] = !skillLoaded(r)
	r.Checks["backend_completion_retained"] = strings.Contains(string(r.Memory), `"status":"added"`)
	rows = append(rows, r)
	s.e.Close()

	for _, mode := range []string{"original", "skills"} {
		selected := skillsmcp.Config{}
		if mode == "skills" {
			selected = cfg
		}
		s = newRealSkillSession(t, llm, selected, func(e *Engine) { e.ApplyBusinessEvent("addV") })
		r = s.turn(t, "quotation_completion", mode, "请按现有的方案给我报个价。")
		probeBarrier(s.e)
		memory, _ := s.e.MemorySnapshot()
		r.Checks["addV_during_calculation_retained"] = strings.Contains(string(memory), `"status":"added"`)
		r.Checks["quote_started_before_addV"] = len(r.Quotes) > 0 && strings.Contains(string(r.Quotes[0].Memory), `"status":"consented"`)
		select {
		case duration := <-s.quote.completed:
			r.Checks["three_second_calculation_preserved"] = duration >= 3*time.Second
			t.Logf("REAL_CALCULATION mode=%s elapsed_ms=%.3f", mode, float64(duration.Microseconds())/1000)
		case <-time.After(6 * time.Second):
			r.Checks["three_second_calculation_preserved"] = false
		}
		waitFor(t, "synthetic quotation delivery", 8*time.Second, func() bool {
			for _, ev := range s.c.taskEvents() {
				if ev.Status == "delivered" {
					return true
				}
			}
			return false
		})
		probeBarrier(s.e)
		historyOK := true
		s.e.post(func() {
			for _, m := range s.e.history {
				if strings.Contains(m.Content, "方案在计算期间有改动") {
					historyOK = false
				}
			}
		})
		probeBarrier(s.e)
		r.Checks["addV_does_not_invalidate_quote"] = historyOK
		rows = append(rows, r)
		s.e.Interrupt()
		r = s.turn(t, "quotation_revision_same_session", mode, "把刚才方案里的三者险改成三百万，其他不变，再算一下。")
		r.Checks["revision_requested"] = len(r.Quotes) > 0
		if len(r.Quotes) > 0 {
			var args struct {
				Config map[string]any `json:"config"`
			}
			var memory struct {
				Quote map[string]any `json:"quote"`
			}
			_ = json.Unmarshal(r.Quotes[0].Arguments, &args)
			_ = json.Unmarshal(r.Quotes[0].Memory, &memory)
			r.Checks["revision_plan_preserved"] = args.Config["三者险"] == "300万" && memory.Quote["交强险"] == true && memory.Quote["车损险"] == true
		}
		rows = append(rows, r)
		s.e.Close()
	}
	b, _ := json.MarshalIndent(rows, "", "  ")
	path := filepath.Join(filepath.Dir(os.Getenv("SKILLS_REAL_REPORT")), "edges.json")
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		for check, ok := range r.Checks {
			if !ok {
				t.Errorf("%s/%s failed %s", r.Scenario, r.Mode, check)
			}
		}
	}
}
