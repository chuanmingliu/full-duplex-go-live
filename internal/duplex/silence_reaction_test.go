package duplex

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider/mock"
	"github.com/chuanmingliu/golive/internal/tools"
)

func enableTestSilence(e *Engine) {
	e.tools.silence.config, _ = tools.ParseSilenceReaction([]byte(tools.DefaultSilenceReaction))
}

func TestSilenceReminderSamplesOneBoundedDeadline(t *testing.T) {
	e := newSilenceRuleEngine(t)
	enableTestSilence(e)
	now := time.Now()
	seen := map[int]bool{}
	for i := 0; i < 50; i++ {
		e.tools.silence.due = time.Time{}
		e.maybeSilenceReaction(now)
		s := e.tools.silence
		gap := s.due.Sub(now)
		if gap < 8*time.Second || gap > 12*time.Second {
			t.Fatalf("gap outside configured range: %v", gap)
		}
		seen[s.waitMS] = true
		e.maybeSilenceReaction(now.Add(time.Second))
		if e.tools.silence.due != s.due {
			t.Fatal("tick resampled deadline")
		}
	}
	if len(seen) < 2 {
		t.Fatal("deadlines did not vary")
	}
}

func TestSilenceReminderWaitsForEveryFloorAndTaskOwner(t *testing.T) {
	cases := map[string]func(*Engine){
		"caller":                func(e *Engine) { e.userOpen = true },
		"recognition":           func(e *Engine) { e.asrRun = &asrRun{sendDone: true} },
		"model":                 func(e *Engine) { e.backendPending.Store(1) },
		"playback receipt":      func(e *Engine) { e.playbackPending = []*playbackCompletion{{}} },
		"interruption decision": func(e *Engine) { e.hold = &floorHold{} },
		"quotation calculating": func(e *Engine) { e.tools.tasks = map[string]*tools.Task{"q": {}} },
		"quotation ready":       func(e *Engine) { e.tools.deliveries = []taskDelivery{{}} },
		"acknowledgment":        func(e *Engine) { e.tools.ackPending = map[string]bool{"q": true} },
		"delegated reply":       func(e *Engine) { e.pending["p"] = &pendingResponse{} },
		"muted":                 func(e *Engine) { e.muted = true },
		"before answer":         func(e *Engine) { e.tools.lastCallerAt = time.Time{} },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			e := newSilenceRuleEngine(t)
			enableTestSilence(e)
			e.tools.silence.due = time.Now().Add(-time.Second)
			setup(e)
			e.maybeSilenceReaction(time.Now())
			if e.tools.silence.attempts != 0 || !e.tools.silence.due.IsZero() {
				t.Fatal("busy interval was mistaken for caller silence")
			}
		})
	}
}

func TestSilenceReminderBudgetResetsOnlyOnCallerSpeech(t *testing.T) {
	e := newSilenceRuleEngine(t)
	enableTestSilence(e)
	e.tools.silence.attempts = 2
	e.tools.silence.due = time.Now().Add(-time.Second)
	e.noteAssistantQuiet()
	e.maybeSilenceReaction(time.Now())
	if e.tools.silence.attempts != 2 {
		t.Fatal("assistant speech reset the reminder budget")
	}
	e.noteCallerSpoke()
	if e.tools.silence.attempts != 2 {
		t.Fatal("unrecognized sound reset the budget")
	}
	e.commitTurn(e.tracker.Begin("我在听", false))
	if e.tools.silence.attempts != 0 || !e.tools.silence.due.IsZero() {
		t.Fatal("caller did not reset the budget and deadline")
	}
	e.maybeSilenceReaction(time.Now())
	if e.tools.silence.due.IsZero() {
		t.Fatal("next silence did not get a new deadline")
	}
}

func TestSilenceReminderRestartsAfterBriefPlaybackAndBusyWork(t *testing.T) {
	e := newSilenceRuleEngine(t)
	enableTestSilence(e)
	now := time.Now()
	e.maybeSilenceReaction(now)
	// Even playback shorter than a tick invalidates the old deadline.
	e.noteAssistantQuiet()
	if !e.tools.silence.due.IsZero() {
		t.Fatal("playback completion left a stale reminder deadline")
	}
	e.tools.tasks = map[string]*tools.Task{"quote": {}}
	e.maybeSilenceReaction(now.Add(time.Minute))
	if !e.tools.silence.due.IsZero() {
		t.Fatal("calculation time started the quiet interval")
	}
	delete(e.tools.tasks, "quote")
	finished := now.Add(2 * time.Minute)
	e.maybeSilenceReaction(finished)
	if gap := e.tools.silence.due.Sub(finished); gap < 8*time.Second || gap > 12*time.Second {
		t.Fatalf("did not wait a full interval after tool work: %v", gap)
	}
}

func TestSilenceHangupHasPriorityOverReminder(t *testing.T) {
	e := newSilenceRuleEngine(t)
	enableTestSilence(e)
	e.tools.silence.due = time.Now().Add(-time.Second)
	e.checkHangupRules("")
	e.maybeSilenceReaction(time.Now())
	if e.tools.endCall == nil || e.tools.silence.attempts != 0 {
		t.Fatal("reminder overrode hangup")
	}
}

func TestUnavailableSilenceConfigPreservesPreviousBehavior(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer s.Close()
	e := newSilenceRuleEngine(t)
	e.opts.Cfg.HangupRules = true
	e.opts.Cfg.ToolsBaseURL = s.URL
	if e.loadSilenceReaction().Enabled {
		t.Fatal("missing config enabled reminders")
	}
}

// Real engine/player loop with mock providers, no phone or external API. The
// timer is accelerated but speech, playback completion and transcript emission
// use the same path as the Active Call session.
func TestSilenceReminderSimulationRecordsSpeechAndStopsAtLimit(t *testing.T) {
	opts := quoteConfig()
	opts.Cfg.Tools = nil
	opts.Cfg.ToolsEndCall = true
	opts.Cfg.HangupRules = false
	c := newCollector(opts.Cfg.ClientRate)
	e := startQuoteEngine(t, opts, Deps{ASR: newTrailingASR("我在"), LLM: shortLLM{}, TTS: mock.NewTTS(), Emit: c.emit}, &slowQuote{})
	ready := make(chan struct{})
	e.post(func() {
		e.tools.engine = nil
		e.tools.silence.config = tools.SilenceReaction{Enabled: true, MinWaitMS: 1000, MaxWaitMS: 1000, MaxAttempts: 1, Templates: []string{"您还在吗？"}}
		close(ready)
	})
	<-ready
	e.Say("您好。")
	waitFor(t, "reminder transcript", 8*time.Second, func() bool { return strings.Contains(c.saidAll(), "您还在吗") })
	pause(e, opts.Cfg.ClientRate, 2400)
	if n := strings.Count(c.saidAll(), "您还在吗"); n != 1 {
		t.Fatalf("expected one reminder, got %d: %s", n, c.saidAll())
	}
	if c.audioMS() == 0 {
		t.Fatal("reminder produced no recorded audio")
	}
}

func TestSilenceReminderSimulationCallerCanInterrupt(t *testing.T) {
	opts := quoteConfig()
	opts.Cfg.Tools = nil
	opts.Cfg.ToolsEndCall = true
	opts.Cfg.HangupRules = false
	c := newCollector(opts.Cfg.ClientRate)
	asr := newTrailingASR("我在听请先停一下")
	asr.msPerRune = 80
	e := startQuoteEngine(t, opts, Deps{ASR: asr, LLM: shortLLM{}, Emit: c.emit}, &slowQuote{})
	ready := make(chan struct{})
	e.post(func() {
		e.tools.engine = nil
		e.tools.silence.config = tools.SilenceReaction{Enabled: true, MinWaitMS: 1000, MaxWaitMS: 1000, MaxAttempts: 1,
			Templates: []string{"您好，您还在听吗？如果您有什么需要了解的问题，可以直接告诉我，我会继续为您解答。"}}
		close(ready)
	})
	<-ready
	e.Say("您好。")
	waitFor(t, "reminder starts", 8*time.Second, func() bool { return strings.Contains(c.saidAll(), "您还在听吗") })
	speak(e, opts.Cfg.ClientRate, 900)
	pause(e, opts.Cfg.ClientRate, 600)
	waitFor(t, "caller cuts reminder", 3*time.Second, func() bool { return c.count(live.ExtAudioTruncated) > 0 })
	if testHangingUp(t, e) {
		t.Fatal("reminder was treated as a farewell")
	}
}
