package duplex

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
	"github.com/chuanmingliu/golive/internal/tools"
)

type voiceConfigSimLLM struct {
	requests atomic.Int32
}

func (*voiceConfigSimLLM) Name() string { return "voice-config-call-sim" }

func (l *voiceConfigSimLLM) Stream(_ context.Context, _ provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	lines := []string{"三者险最高支持四百万。"}
	if l.requests.Add(1) == 1 {
		lines = []string{"我", "不", "猜。", "乘客的医保外用药有共享保额、一万、两万、五万、十万和二十万。", strings.Repeat("您可以按自己的需求考虑。", 16)}
	}
	out := make(chan provider.LLMDelta, len(lines))
	for _, line := range lines {
		out <- provider.LLMDelta{Text: line}
	}
	close(out)
	return out, nil
}

func TestCallSimStreamingGuardAndTurnTaking(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/configs/streaming-guard":
			fmt.Fprintf(w, `{"config":{"version":2,"body":%s}}`, tools.DefaultStreamingGuard)
		case "/api/configs/turn-taking":
			fmt.Fprintf(w, `{"config":{"version":2,"body":%s}}`, tools.DefaultTurnTaking)
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	cfg := testConfig()
	cfg.ToolsBaseURL = s.URL
	cfg.Duplex.AllowBargeIn = true
	cfg.Duplex.PlaybackPaced = true
	cfg.Duplex.Opener = false
	cfg.Duplex.Backchannel = false
	cfg.Duplex.PhraseCache = false
	c := newCollector(cfg.ClientRate)
	llm := &voiceConfigSimLLM{}
	tts := mock.NewTTS()
	tts.MSPerRune = 45
	e := New(Options{Cfg: cfg, SessionID: "voice-config-call-sim", OnNewQuery: "queue"}, Deps{
		ASR: mock.NewASR(""), LLM: llm, TTS: tts, Emit: c.emit,
	})
	e.Start(context.Background())
	t.Cleanup(e.Close)
	feed := func(id uint64, partial, final string) {
		e.post(func() {
			e.asrRun = &asrRun{id: id, openedAt: time.Now()}
			if partial != "" {
				e.userOpen = true
				e.onASREvent(asrEvent{run: id, res: provider.ASRResult{Text: partial}})
				e.userOpen = false
			}
			e.onASREvent(asrEvent{run: id, res: provider.ASRResult{Text: final, Final: true}})
			e.asrRun = nil
		})
	}
	feed(1, "", "乘客的医保外用药有哪些档位？")
	waitFor(t, "first synthesized audio", 3*time.Second, func() bool {
		return strings.Contains(c.allOutput(), "乘客的医保外用药") && c.audioMS() > 0
	})
	feed(2, "你先别说话", "你先别说话。")
	waitFor(t, "playback cut and preset acknowledgment", 4*time.Second, func() bool {
		return c.count(live.ExtAudioTruncated) == 1 && strings.Contains(c.allOutput(), "好的，您先说")
	})
	if llm.requests.Load() != 1 {
		t.Fatal("the explicit cue went to the model")
	}
	feed(3, "", "那三者险最高多少？")
	waitFor(t, "follow-up without the configured five-second wait", 3*time.Second, func() bool {
		return strings.Contains(c.allOutput(), "三者险最高支持四百万。")
	})
	if transcript := c.allOutput(); strings.Contains(transcript, "我不猜") ||
		strings.Count(transcript, "好的，您先说") != 1 || llm.requests.Load() != 2 {
		t.Fatalf("guard, acknowledgment, or model requests were incorrect: %q, requests=%d", transcript, llm.requests.Load())
	}
}

func TestTurnTakingCutsOnAcceptedPartialEvenWithQueuePolicy(t *testing.T) {
	e := newSilenceRuleEngine(t)
	e.turnTaking.config, _ = tools.ParseTurnTaking([]byte(tools.DefaultTurnTaking))
	e.turnTaking.config.Acknowledgment = ""
	e.opts.OnNewQuery = "queue"
	e.opts.Cfg.Duplex.AllowBargeIn = true
	e.opts.Speculative = true
	current := e.tracker.Begin("previous question", false)
	e.backendPending.Store(1)
	e.userOpen = true
	e.asrRun = &asrRun{id: 1}
	e.hold = &floorHold{started: time.Now()}
	e.onASREvent(asrEvent{run: 1, res: provider.ASRResult{Text: "你听我说"}})
	if e.tracker.IsCurrent(current) || !e.asrRun.cueSeen || !e.waitingForCallerCue() {
		t.Fatal("accepted partial did not cut the previous turn or reserve the floor")
	}
	e.startSpeculativeTurn("你听我说")
	if e.liveSpeculation() != nil {
		t.Fatal("the cue started a speculative answer")
	}
	e.userOpen = false
	e.backendPending.Store(0)
	e.onASREvent(asrEvent{run: 1, res: provider.ASRResult{Text: "你听我说。", Final: true}})
	e.asrRun = nil
	if !e.waitingForCallerCue() || time.Until(e.turnTaking.until) < 4*time.Second {
		t.Fatal("final cue did not start the configured wait")
	}
	e.commentary = []queuedCommentary{{text: "queued"}}
	e.drainDeferred()
	if len(e.commentary) != 1 {
		t.Fatal("queued speech occupied the caller's reserved floor")
	}
	if text, consumed := e.acceptCallerCue(nil, "等下，三者险有哪些档位？"); consumed || text != "三者险有哪些档位？" || e.waitingForCallerCue() {
		t.Fatalf("same-utterance request did not release the hold: %q consumed=%v", text, consumed)
	}
}

func TestTurnTakingPlaysSavedAcknowledgmentOnce(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/configs/turn-taking" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"config":{"version":3,"body":{"enabled":true,"wait_ms":5000,
			"phrases":["请听我说"],"acknowledgment":"好，您说。"}}}`)
	}))
	defer s.Close()
	cfg := testConfig()
	cfg.ToolsBaseURL = s.URL
	c := newCollector(cfg.ClientRate)
	e, _ := newTestEngine(t, cfg, c)
	e.post(func() {
		e.asrRun = &asrRun{id: 1}
		e.onASREvent(asrEvent{run: 1, res: provider.ASRResult{Text: "请听我说。", Final: true}})
		e.asrRun = nil
	})
	waitFor(t, "saved acknowledgment", 2*time.Second, func() bool { return c.allOutput() == "好，您说。" && c.audioMS() > 0 })
	done := make(chan struct{})
	e.post(func() {
		e.maybeTurnTakingAck()
		e.turnTaking.until = time.Now().Add(-time.Millisecond)
		e.expireCallerWait()
		e.maybeTurnTakingAck()
		close(done)
	})
	awaitHandoffSignal(t, done)
	if got := c.allOutput(); strings.Count(got, "好，您说。") != 1 || got != "好，您说。" {
		t.Fatalf("expected just one acknowledgment, got %q", got)
	}
}

func TestTurnTakingDoesNotAcceptCueFromRejectedVoice(t *testing.T) {
	e := newSilenceRuleEngine(t)
	e.turnTaking.config, _ = tools.ParseTurnTaking([]byte(tools.DefaultTurnTaking))
	e.hold = &floorHold{until: time.Now().Add(-time.Millisecond), kept: true}
	run := &asrRun{lastText: "等下。", finalSeen: true}
	if !e.deferCallerCue(run, run.lastText) {
		t.Fatal("cue did not follow the existing speaker hold")
	}
	e.expireHold()
	e.settleCallerCue()
	if e.waitingForCallerCue() || e.turnTaking.ackPending {
		t.Fatal("a rejected cue held the floor or queued speech")
	}
}
