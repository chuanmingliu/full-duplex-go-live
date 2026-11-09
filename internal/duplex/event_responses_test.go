package duplex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider/mock"
	"github.com/chuanmingliu/golive/internal/skillsmcp"
	"github.com/chuanmingliu/golive/internal/tools"
)

func eventResponseClient(t *testing.T, enabled bool, calls *atomic.Int32) *skillsmcp.Client {
	t.Helper()
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Method string }
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Method != "resources/read" {
			http.Error(w, "synthetic skill outage", 503)
			return
		}
		calls.Add(1)
		text, _ := json.Marshal(skillsmcp.EventResponse{Event: "addV", Revision: 7, Enabled: enabled, Templates: []string{"微信已经添加成功了，我们继续刚才的话题。您可以先告诉我还有什么需要了解，我会继续为您解答。"}})
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"contents": []any{map[string]any{"uri": "golive://event-responses/addV", "mimeType": "application/json", "text": string(text)}}}})
	}))
	t.Cleanup(svr.Close)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("private-test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := skillsmcp.New(skillsmcp.Config{URL: svr.URL, TokenFile: token})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestEventResponseWaitsForFloorAndQuotation(t *testing.T) {
	cases := map[string]func(*Engine){
		"caller":      func(e *Engine) { e.userOpen = true },
		"recognition": func(e *Engine) { e.asrRun = &asrRun{sendDone: true} },
		"model":       func(e *Engine) { e.backendPending.Store(1) },
		"receipt":     func(e *Engine) { e.playbackPending = []*playbackCompletion{{}} },
		"guard":       func(e *Engine) { e.hold = &floorHold{} },
		"quote":       func(e *Engine) { e.tools.tasks = map[string]*tools.Task{"q": {}} },
		"result":      func(e *Engine) { e.tools.deliveries = []taskDelivery{{}} },
		"ack":         func(e *Engine) { e.tools.ackPending = map[string]bool{"q": true} },
		"delegation":  func(e *Engine) { e.pending["p"] = &pendingResponse{} },
		"muted":       func(e *Engine) { e.muted = true },
		"unanswered":  func(e *Engine) { e.tools.lastCallerAt = time.Time{} },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			e := newSilenceRuleEngine(t)
			p := &pendingEventResponse{event: "addV", text: "微信添加成功了。", received: time.Now()}
			e.eventResponse = p
			setup(e)
			e.maybeEventResponse()
			if e.eventResponse != p {
				t.Fatal("response took a busy floor")
			}
		})
	}
}

func TestEventResponseSimulationOnceAndInterruptible(t *testing.T) {
	var calls atomic.Int32
	client := eventResponseClient(t, true, &calls)
	opts := quoteConfig()
	opts.Cfg.Tools = nil
	opts.Cfg.ToolsEndCall = true
	opts.Cfg.HangupRules = false
	c := newCollector(opts.Cfg.ClientRate)
	asr := newTrailingASR("停一下我想先问保险什么时候生效")
	asr.msPerRune = 80
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: shortLLM{}, TTS: mock.NewTTS(), Emit: c.emit}, &slowQuote{})
	e.post(func() { e.skills = client; e.tools.engine = nil; e.tools.lastCallerAt = time.Now(); e.userOpen = true })
	probeBarrier(e)
	if !e.ApplyBusinessEvent("addV") {
		t.Fatal("event rejected")
	}
	waitFor(t, "callback resource fetched", time.Second, func() bool { return calls.Load() == 1 })
	probeBarrier(e)
	if strings.Contains(c.saidAll(), "微信已经") {
		t.Fatal("spoke over caller")
	}
	e.post(func() { e.userOpen = false })
	waitFor(t, "callback transcript", 3*time.Second, func() bool { return strings.Contains(c.saidAll(), "微信已经") })
	if !e.ApplyBusinessEvent("addV") {
		t.Fatal("duplicate rejected")
	}
	speak(e, opts.Cfg.ClientRate, 900)
	pause(e, opts.Cfg.ClientRate, 600)
	waitFor(t, "callback interrupted", 3*time.Second, func() bool { return c.count(live.ExtAudioTruncated) > 0 })
	if calls.Load() != 1 || strings.Count(c.saidAll(), "微信已经") != 1 {
		t.Fatal("duplicate callback speech or lookup")
	}
	if c.audioMS() == 0 {
		t.Fatal("callback produced no audio")
	}
	state, _ := e.MemorySnapshot()
	if !strings.Contains(string(state), `"status":"added"`) {
		t.Fatal("state lost after interruption")
	}
}

func TestDisabledEventResponseRetainsState(t *testing.T) {
	var calls atomic.Int32
	client := eventResponseClient(t, false, &calls)
	opts := quoteConfig()
	opts.Cfg.Tools = nil
	opts.Cfg.ToolsEndCall = true
	opts.Cfg.HangupRules = false
	c := newCollector(opts.Cfg.ClientRate)
	e := startQuoteEngine(t, opts, Deps{ASR: mock.NewASR(""), LLM: shortLLM{}, TTS: mock.NewTTS(), Emit: c.emit}, &slowQuote{})
	e.post(func() { e.skills = client; e.tools.engine = nil; e.tools.lastCallerAt = time.Now() })
	probeBarrier(e)
	e.ApplyBusinessEvent("addV")
	waitFor(t, "disabled resource read", time.Second, func() bool { return calls.Load() == 1 })
	waitFor(t, "disabled response handled", time.Second, func() bool {
		done := make(chan bool, 1)
		e.post(func() { done <- e.eventResponse == nil })
		return <-done
	})
	if c.audioMS() != 0 {
		t.Fatal("disabled binding spoke")
	}
	state, _ := e.MemorySnapshot()
	if !strings.Contains(string(state), `"status":"added"`) {
		t.Fatal("disabled binding lost state")
	}
}
