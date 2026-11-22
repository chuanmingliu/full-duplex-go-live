package duplex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

func TestUserPromptRendersCurrentRequestAfterProductPageEvent(t *testing.T) {
	const business = "指导用户在已打开的产品页调整保障。"
	cfgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/configs/user-prompt" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"version": 4, "body": map[string]any{
			"enabled": true, "template": "{% if events.product_page_opened %}" + business + "{% endif %}",
		}}})
	}))
	defer cfgServer.Close()
	cfg := testConfig()
	cfg.ToolsBaseURL = cfgServer.URL
	cfg.Duplex.Opener = false
	cfg.Duplex.Speculative = false
	collector := newCollector(cfg.ClientRate)
	llm := &recordingLLM{inner: mock.NewLLM()}
	e := startEngine(t, Options{Cfg: cfg, Instructions: "已保存的系统提示"}, Deps{LLM: llm, TTS: mock.NewTTS(), Emit: collector.emit})

	const first = "我已经打开微信页面了。"
	const second = "现在怎么改保障？"
	e.post(func() { e.onFinalTranscript(nil, first) })
	waitFor(t, "first model request", time.Second, func() bool { return len(llm.requests()) >= 1 })
	before := llm.requests()[0].Messages
	if latestUserText(before) != first || before[0].Role != provider.RoleSystem || !strings.Contains(before[0].Content, "已保存的系统提示") {
		t.Fatalf("unexpected initial message roles or content: %+v", before)
	}
	if !e.ApplyBusinessEvent(ProductPageOpenedEvent) {
		t.Fatal("product page event was rejected")
	}
	e.post(func() {
		e.ensureUserRecorded(e.tracker.Current())
		e.onFinalTranscript(nil, second)
	})
	waitFor(t, "second model request", time.Second, func() bool { return len(llm.requests()) >= 2 })
	after := llm.requests()[1].Messages
	if next := latestUserText(after); !strings.Contains(next, second) || !strings.HasSuffix(next, business) {
		t.Fatalf("current user message = %q", next)
	}
	if after[0].Content != before[0].Content {
		t.Fatal("user prompt changed the system prefix")
	}
	var stored string
	e.post(func() {
		e.ensureUserRecorded(e.tracker.Current())
		stored = latestUserText(e.history)
	})
	probeBarrier(e)
	if !strings.HasSuffix(stored, second) || strings.Contains(stored, business) {
		t.Fatalf("templated guidance leaked into history: %q", stored)
	}
}
