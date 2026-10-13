package duplex

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

func simulatorEngine(t *testing.T, enabled bool) (*Engine, *collector) {
	t.Helper()
	cfg := testConfig()
	cfg.Duplex.PlaybackPaced = false
	c := newCollector(cfg.ClientRate)
	e := New(Options{Cfg: cfg, ClientRate: cfg.ClientRate, SimulatorEnabled: enabled, Delegation: live.DelegationClient}, Deps{ASR: mock.NewASR("remote question"), TTS: mock.NewTTS(), Emit: c.emit})
	e.Start(context.Background())
	t.Cleanup(e.Close)
	return e, c
}

func simulatorLoop(t *testing.T, e *Engine, f func()) {
	t.Helper()
	done := make(chan struct{})
	e.post(func() { f(); close(done) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("engine loop stalled")
	}
}

func TestSimulatorCapability(t *testing.T) {
	e, _ := simulatorEngine(t, false)
	err := e.PrepareSimulatorSpeech(context.Background(), "action", "hello")
	if !errors.Is(err, ErrSimulatorDisabled) {
		t.Fatalf("disabled prepare: %v", err)
	}
	if err = e.ScheduleSimulatorSpeech(context.Background(), SimulatorSpeechRequest{ActionID: "action", Text: "hello"}); !errors.Is(err, ErrSimulatorDisabled) {
		t.Fatalf("disabled schedule: %v", err)
	}
}

// simulatorRecordingASR counts samples throughout scripted playback without
// finalizing input or relying on recognizer/provider timing.
type simulatorRecordingASR struct{ samples atomic.Int64 }

func (*simulatorRecordingASR) Name() string { return "simulator-recording" }
func (a *simulatorRecordingASR) Open(context.Context, provider.ASROptions) (provider.ASRStream, error) {
	return &simulatorRecordingStream{asr: a, results: make(chan provider.ASRResult)}, nil
}

type simulatorRecordingStream struct {
	asr     *simulatorRecordingASR
	results chan provider.ASRResult
	once    sync.Once
}

func (s *simulatorRecordingStream) Write(pcm []byte) error {
	s.asr.samples.Add(int64(len(pcm) / 2))
	return nil
}
func (s *simulatorRecordingStream) Results() <-chan provider.ASRResult { return s.results }
func (s *simulatorRecordingStream) CloseSend() error                   { return s.Close() }
func (s *simulatorRecordingStream) Close() error                       { s.once.Do(func() { close(s.results) }); return nil }

func TestSimulatorOutputWhileRemoteActive(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.PlaybackPaced = false
	c := newCollector(cfg.ClientRate)
	asr := &simulatorRecordingASR{}
	e := New(Options{Cfg: cfg, ClientRate: cfg.ClientRate, SimulatorEnabled: true, Delegation: live.DelegationClient}, Deps{ASR: asr, TTS: mock.NewTTS(), Emit: c.emit})
	e.Start(context.Background())
	t.Cleanup(e.Close)
	if err := e.PrepareSimulatorSpeech(context.Background(), "action", "hello"); err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(time.Hour)
	e.PushAudio(voiced(cfg.ClientRate, 800, 0.8))
	waitFor(t, "active ASR samples", time.Second, func() bool { return asr.samples.Load() > 0 })
	before := asr.samples.Load()
	req := SimulatorSpeechRequest{ActionID: "action", Text: "hello", DueAt: due, ExpiresAt: due.Add(time.Second), MaxHold: time.Second, RequireRemoteSpeaking: true}
	if err := e.ScheduleSimulatorSpeech(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	simulatorLoop(t, e, func() { e.advanceSimulator(due); e.onAudio(voiced(cfg.ClientRate, 400, 0.8)) })
	waitFor(t, "scripted audio", time.Second, func() bool { return c.audioMS() > 0 })
	waitFor(t, "ASR during output", time.Second, func() bool { return asr.samples.Load() > before })
	simulatorLoop(t, e, func() {
		if !e.userOpen {
			t.Error("remote speech ended before output")
		}
	})
	if e.inputMS.Load() != 1200 {
		t.Fatalf("input sample duration=%d, want 1200ms", e.inputMS.Load())
	}
	if err := e.ScheduleSimulatorSpeech(context.Background(), req); err != nil {
		t.Fatalf("duplicate schedule: %v", err)
	}
	if !simulatorStatus(c, "action", "started") {
		t.Fatal("missing actual audio boundary")
	}
}

func TestSimulatorFinalDoesNotRetireScript(t *testing.T) {
	e, c := simulatorEngine(t, true)
	due := time.Now().Add(time.Hour)
	if err := e.PrepareSimulatorSpeech(context.Background(), "action", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := e.ScheduleSimulatorSpeech(context.Background(), SimulatorSpeechRequest{ActionID: "action", Text: "hello", DueAt: due, ExpiresAt: due.Add(time.Second), MaxHold: time.Second}); err != nil {
		t.Fatal(err)
	}
	simulatorLoop(t, e, func() {
		e.advanceSimulator(due)
		turn := e.tracker.Current()
		run := &asrRun{id: 1, itemID: "remote", openedAt: time.Now()}
		e.asrRun = run
		e.onASREvent(asrEvent{run: 1, res: provider.ASRResult{Text: "remote question", Final: true}})
		e.asrRun = nil
		if e.tracker.Current() != turn {
			t.Error("remote final retired script")
		}
		if len(e.simulator.finals) != 1 {
			t.Error("remote final was not deferred")
		}
	})
	waitFor(t, "input transcript", time.Second, func() bool { return c.count(live.ServerInputTranscriptDelta) > 0 })
	waitFor(t, "ordinary delegation", time.Second, func() bool { return c.count(live.ServerDelegationCreated) == 1 })
}

func simulatorStatus(c *collector, actionID, status string) bool {
	return c.find(func(ev any) bool {
		v, ok := ev.(SimulatorSpeechEvent)
		return ok && v.ActionID == actionID && v.Status == status
	}) != nil
}

func simulatorReservation(t *testing.T, e *Engine, id string, due time.Time) SimulatorSpeechRequest {
	t.Helper()
	if err := e.PrepareSimulatorSpeech(context.Background(), id, "hello"); err != nil {
		t.Fatal(err)
	}
	return SimulatorSpeechRequest{ActionID: id, Text: "hello", DueAt: due, ExpiresAt: due.Add(time.Second), MaxHold: time.Second}
}

func TestSimulatorCancelAtScheduledInstantSuppressesOutput(t *testing.T) {
	e, c := simulatorEngine(t, true)
	due := time.Now().Add(time.Hour)
	req := simulatorReservation(t, e, "cancel", due)
	if err := e.ScheduleSimulatorSpeech(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := e.CancelSimulatorSpeech(context.Background(), req.ActionID); err != nil {
		t.Fatal(err)
	}
	simulatorLoop(t, e, func() { e.advanceSimulator(due) })
	// Even a stale player callback reaching the output boundary is suppressed.
	e.emitAudio("simulator_cancel", make([]byte, 480), time.Now())
	if c.audioMS() != 0 {
		t.Fatal("cancelled scheduled action emitted audio")
	}
	if !simulatorStatus(c, "cancel", "cancelled") {
		t.Fatal("missing cancellation outcome")
	}
	if err := e.ScheduleSimulatorSpeech(context.Background(), req); err != nil {
		t.Fatalf("idempotent terminal reservation: %v", err)
	}
}

func TestSimulatorExpiredAndRemoteEndedSkip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		expired bool
	}{{"expired", true}, {"remote-ended", false}} {
		t.Run(tc.name, func(t *testing.T) {
			e, c := simulatorEngine(t, true)
			due := time.Now().Add(time.Hour)
			req := simulatorReservation(t, e, tc.name, due)
			req.RequireRemoteSpeaking = true
			if err := e.ScheduleSimulatorSpeech(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			at := due
			status := "skipped"
			if tc.expired {
				at = req.ExpiresAt
				status = "expired"
			}
			simulatorLoop(t, e, func() { e.advanceSimulator(at) })
			if !simulatorStatus(c, tc.name, status) {
				t.Fatalf("missing %s outcome", status)
			}
			if c.audioMS() != 0 {
				t.Fatal("terminal action emitted audio")
			}
		})
	}
}

func TestSimulatorMaxHoldSuppressesLateOutput(t *testing.T) {
	e, c := simulatorEngine(t, true)
	due := time.Now().Add(time.Hour)
	req := simulatorReservation(t, e, "hold", due)
	req.MaxHold = time.Millisecond
	if err := e.ScheduleSimulatorSpeech(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	simulatorLoop(t, e, func() { e.advanceSimulator(due); e.advanceSimulator(due.Add(req.MaxHold)) })
	before := c.audioMS()
	e.emitAudio("simulator_hold", make([]byte, 480), time.Now())
	if c.audioMS() != before {
		t.Fatal("late audio after maximum hold")
	}
	if !simulatorStatus(c, "hold", "timed_out") {
		t.Fatal("missing maximum hold outcome")
	}
	simulatorLoop(t, e, func() {
		if e.simulator.active != nil || e.simulator.pcmBytes != 0 {
			t.Error("ownership/buffer retained after timeout")
		}
	})
}

func TestSimulatorContextCancellationAndCloseSuppressLateOutput(t *testing.T) {
	for _, closeSession := range []bool{false, true} {
		t.Run(map[bool]string{true: "close", false: "context"}[closeSession], func(t *testing.T) {
			e, c := simulatorEngine(t, true)
			due := time.Now().Add(time.Hour)
			req := simulatorReservation(t, e, "stop", due)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := e.ScheduleSimulatorSpeech(ctx, req); err != nil {
				t.Fatal(err)
			}
			if closeSession {
				e.Close()
			} else {
				cancel()
				simulatorLoop(t, e, func() { e.advanceSimulator(due) })
			}
			e.emitAudio("simulator_stop", make([]byte, 480), time.Now())
			if c.audioMS() != 0 {
				t.Fatal("late audio after stop")
			}
			if !simulatorStatus(c, "stop", "cancelled") {
				t.Fatal("missing stopped action outcome")
			}
		})
	}
}

func TestSimulatorPreparationFailureIsExplicit(t *testing.T) {
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)
	e := New(Options{Cfg: cfg, ClientRate: cfg.ClientRate, SimulatorEnabled: true}, Deps{Emit: c.emit})
	e.Start(context.Background())
	t.Cleanup(e.Close)
	if err := e.PrepareSimulatorSpeech(context.Background(), "broken", "hello"); err == nil {
		t.Fatal("preparation succeeded without TTS")
	}
	if !simulatorStatus(c, "broken", "failed") {
		t.Fatal("missing preparation failure")
	}
	if err := e.ScheduleSimulatorSpeech(context.Background(), SimulatorSpeechRequest{ActionID: "broken", Text: "hello"}); err == nil {
		t.Fatal("failed preparation was scheduled")
	}
	if c.audioMS() != 0 {
		t.Fatal("failed preparation emitted audio")
	}
}

func TestSimulatorPendingFinalOverflowFailsSession(t *testing.T) {
	e, c := simulatorEngine(t, true)
	due := time.Now().Add(time.Hour)
	req := simulatorReservation(t, e, "overflow", due)
	if err := e.ScheduleSimulatorSpeech(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	simulatorLoop(t, e, func() {
		e.advanceSimulator(due)
		for i := 0; i <= maxSimulatorQueued; i++ {
			e.onFinalTranscript(&asrRun{}, "remote final")
		}
	})
	if e.ctx.Err() == nil {
		t.Fatal("final overflow did not fail session")
	}
	if c.count(live.ServerError) == 0 {
		t.Fatal("final overflow was not reported")
	}
}

func TestSimulatorOrdinaryCommentaryStillWaitsForSilence(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "enabled"}[enabled], func(t *testing.T) {
			e, c := simulatorEngine(t, enabled)
			simulatorLoop(t, e, func() {
				e.userOpen = true
				e.commentary = append(e.commentary, queuedCommentary{text: "ordinary commentary"})
				e.drainDeferred()
				if len(e.commentary) != 1 {
					t.Error("commentary bypassed input silence")
				}
			})
			if c.audioMS() != 0 {
				t.Fatal("commentary emitted during remote input")
			}
			simulatorLoop(t, e, func() { e.userOpen = false; e.drainDeferred() })
			waitFor(t, "ordinary commentary output", time.Second, func() bool { return c.audioMS() > 0 })
		})
	}
}

func TestSimulatorCancellationPreservesHeardPrefix(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.PlaybackLeadMS = 20
	c := newCollector(cfg.ClientRate)
	e := New(Options{Cfg: cfg, ClientRate: cfg.ClientRate, SimulatorEnabled: true, Delegation: live.DelegationClient}, Deps{TTS: mock.NewTTS(), Emit: c.emit})
	e.Start(context.Background())
	t.Cleanup(e.Close)
	text := "These are the scripted words whose unheard tail must not enter conversation history."
	if err := e.PrepareSimulatorSpeech(context.Background(), "prefix", text); err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(time.Hour)
	req := SimulatorSpeechRequest{ActionID: "prefix", Text: text, DueAt: due, ExpiresAt: due.Add(maxSimulatorHold), MaxHold: maxSimulatorHold}
	if err := e.ScheduleSimulatorSpeech(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	simulatorLoop(t, e, func() { e.advanceSimulator(due) })
	waitFor(t, "first paced audio", time.Second, func() bool { return c.audioMS() >= 160 })
	// Advance the player's heard clock without waiting for the whole sentence.
	e.player.mu.Lock()
	e.player.turnStarted = time.Now().Add(-120 * time.Millisecond)
	actualTurn := e.player.activeTurn
	e.player.mu.Unlock()
	if err := e.CancelSimulatorSpeech(context.Background(), "prefix"); err != nil {
		t.Fatal(err)
	}
	before := c.audioMS()
	e.emitAudio(actualTurn, make([]byte, 480), time.Now())
	if c.audioMS() != before {
		t.Fatal("stale real turn callback emitted after cancellation")
	}
	simulatorLoop(t, e, func() {
		var heard string
		for _, msg := range e.history {
			if msg.Role == provider.RoleAssistant {
				heard += msg.Content
			}
		}
		if heard == "" || len(heard) >= len(text) {
			t.Errorf("heard prefix=%q, full=%q", heard, text)
		}
	})
}

func TestSimulatorInputOverflowFailsExplicitly(t *testing.T) {
	e, c := simulatorEngine(t, true)
	blocked := make(chan struct{})
	release := make(chan struct{})
	e.post(func() { close(blocked); <-release })
	<-blocked
	for i := 0; i <= cap(e.audioCh); i++ {
		e.PushAudio(make([]byte, 480))
	}
	close(release)
	if e.ctx.Err() == nil {
		t.Fatal("input overflow did not fail simulator session")
	}
	if c.count(live.ServerError) == 0 {
		t.Fatal("input overflow was not reported")
	}
}

func TestSimulatorRejectedOutputDoesNotEnterHeardHistory(t *testing.T) {
	p := NewPlayer(PlayerConfig{Rate: 24000, Paced: false}, func(string, []byte, time.Time) { t.Error("unchecked output invoked") })
	var rejected atomic.Int64
	var completed atomic.Int64
	p.emitChecked = func(string, []byte, time.Time) bool { rejected.Add(1); return false }
	p.OnTurnDone(func(string, int64, string) { completed.Add(1) })
	gen := &Generation{}
	done := make(chan struct{})
	go func() { defer close(done); p.Run(gen) }()
	defer func() { p.Close(); <-done }()
	p.Enqueue(Segment{Kind: SegBegin, TurnID: "script", Gen: gen.Current(), Text: "unheard scripted sentence"})
	p.Enqueue(Segment{Kind: SegAudio, TurnID: "script", Gen: gen.Current(), PCM: make([]byte, 480)})
	p.Enqueue(Segment{Kind: SegMark, TurnID: "script", Gen: gen.Current(), Text: "unheard scripted sentence"})
	p.Enqueue(Segment{Kind: SegEnd, TurnID: "script", Gen: gen.Current()})
	waitFor(t, "rejected audio", time.Second, func() bool { return rejected.Load() == 1 })
	p.mu.Lock()
	emitted := p.emittedMS
	p.mu.Unlock()
	if emitted != 0 {
		t.Fatalf("rejected audio counted as emitted: %v", emitted)
	}
	_, heard := p.Heard()
	if heard != "" {
		t.Fatalf("unheard text entered history: %q", heard)
	}
	if completed.Load() != 0 {
		t.Fatal("rejected audio completed turn")
	}
	p.Interrupt()
}

func TestSimulatorTimerStartsPreparedSpeechAtDeadline(t *testing.T) {
	e, c := simulatorEngine(t, true)
	due := time.Now().Add(10 * time.Millisecond)
	req := simulatorReservation(t, e, "timer", due)
	if err := e.ScheduleSimulatorSpeech(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "timer-driven start", time.Second, func() bool { return simulatorStatus(c, "timer", "started") })
	event := c.find(func(ev any) bool {
		v, ok := ev.(SimulatorSpeechEvent)
		return ok && v.ActionID == "timer" && v.Status == "started"
	}).(SimulatorSpeechEvent)
	if event.At.Before(due) || !event.At.Before(req.ExpiresAt) {
		t.Fatalf("actual start %s outside [%s,%s)", event.At, due, req.ExpiresAt)
	}
}

func TestSimulatorRecognitionFailureStopsScript(t *testing.T) {
	e, c := simulatorEngine(t, true)
	due := time.Now().Add(time.Hour)
	req := simulatorReservation(t, e, "recognition", due)
	if err := e.ScheduleSimulatorSpeech(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	simulatorLoop(t, e, func() {
		e.advanceSimulator(due)
		recorder := &simulatorRecordingASR{}
		e.asrRun = &asrRun{id: 1, stream: &simulatorRecordingStream{asr: recorder, results: make(chan provider.ASRResult)}}
		e.onASREvent(asrEvent{run: 1, res: provider.ASRResult{Err: errors.New("recognizer failed")}})
	})
	e.Close()
	before := c.audioMS()
	e.emitAudio("simulator_recognition", make([]byte, 480), time.Now())
	if c.audioMS() != before {
		t.Fatal("recognition failure allowed late audio")
	}
	if !simulatorStatus(c, "recognition", "failed") {
		t.Fatal("recognition failure was not an explicit action failure")
	}
}

func TestSimulatorEarlyFinalKeepsScriptOwnership(t *testing.T) {
	e, c := simulatorEngine(t, true)
	due := time.Now().Add(time.Hour)
	req := simulatorReservation(t, e, "early-final", due)
	if err := e.ScheduleSimulatorSpeech(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	simulatorLoop(t, e, func() {
		e.opts.Speculative = true
		e.opts.Cfg.Duplex.EarlyFinalMS = 80
		e.advanceSimulator(due)
		turn := e.tracker.Current()
		generation := e.gen.Current()
		e.userOpen = true
		recording := &simulatorRecordingASR{}
		e.earlyRun = &asrRun{id: 1, itemID: "remote", lastText: "What time is the appointment?", sendDone: true, stream: &simulatorRecordingStream{asr: recording, results: make(chan provider.ASRResult)}}
		e.asrRun = &asrRun{id: 2, itemID: "remote", continuation: true, stream: &simulatorRecordingStream{asr: recording, results: make(chan provider.ASRResult)}}
		e.onASREvent(asrEvent{run: 1, res: provider.ASRResult{Final: true, Text: "What time is the appointment?"}})
		if e.tracker.Current() != turn || e.gen.Current() != generation {
			t.Error("early final retired scripted ownership")
		}
		if c.count(live.ServerDelegationCreated) != 0 {
			t.Error("early final started ordinary generation during script")
		}
		if e.asrRun.prefix != "What time is the appointment?" {
			t.Error("early-final transcript/continuation was gated")
		}
	})
}

func TestSimulatorEarlyRecognitionFailureIsFatal(t *testing.T) {
	e, c := simulatorEngine(t, true)
	due := time.Now().Add(time.Hour)
	req := simulatorReservation(t, e, "early-error", due)
	if err := e.ScheduleSimulatorSpeech(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	simulatorLoop(t, e, func() {
		e.advanceSimulator(due)
		recording := &simulatorRecordingASR{}
		e.earlyRun = &asrRun{id: 1, lastText: "old hypothesis", stream: &simulatorRecordingStream{asr: recording, results: make(chan provider.ASRResult)}}
		e.asrRun = &asrRun{id: 2, continuation: true, stream: &simulatorRecordingStream{asr: recording, results: make(chan provider.ASRResult)}}
		e.onASREvent(asrEvent{run: 1, res: provider.ASRResult{Err: errors.New("early recognizer failed")}})
		if e.ctx.Err() == nil {
			t.Error("early-run recognition failure did not stop simulator")
		}
	})
	e.Close()
	if !simulatorStatus(c, "early-error", "failed") {
		t.Fatal("early-run error did not produce failed action outcome")
	}
	if c.count(live.ServerError) == 0 {
		t.Fatal("early-run error was not reported")
	}
}

type simulatorFixedTTS struct {
	pcm  []byte
	rate int
}

func (*simulatorFixedTTS) Name() string { return "fixed-script" }
func (t *simulatorFixedTTS) Open(context.Context, provider.TTSOptions) (provider.TTSStream, error) {
	return &simulatorFixedStream{pcm: t.pcm, rate: t.rate}, nil
}

type simulatorFixedStream struct {
	pcm  []byte
	rate int
}

func (s *simulatorFixedStream) SampleRate() int { return s.rate }
func (*simulatorFixedStream) Close() error      { return nil }
func (s *simulatorFixedStream) Synthesize(context.Context, string) (<-chan provider.TTSChunk, error) {
	ch := make(chan provider.TTSChunk, 1)
	ch <- provider.TTSChunk{PCM: s.pcm}
	close(ch)
	return ch, nil
}

func TestSimulatorPreparedPrefixUsesWholeAudioDuration(t *testing.T) {
	for _, terminal := range []string{"cancelled", "timed_out", "close"} {
		t.Run(terminal, func(t *testing.T) {
			cfg := testConfig()
			cfg.Duplex.PlaybackPaced = false
			cfg.Duplex.PlaybackChunkMS = 1000
			c := newCollector(cfg.ClientRate)
			pcm := make([]byte, cfg.ClientRate*2*10)
			for i := 0; i < len(pcm); i += 2 {
				pcm[i] = 1
			}
			e := New(Options{Cfg: cfg, ClientRate: cfg.ClientRate, SimulatorEnabled: true, Delegation: live.DelegationClient}, Deps{TTS: &simulatorFixedTTS{pcm: pcm, rate: cfg.ClientRate}, Emit: c.emit})
			var chunks atomic.Int64
			e.player.emitChecked = func(id string, body []byte, at time.Time) bool {
				if chunks.Add(1) == 1 {
					return e.emitAudioChecked(id, body, at)
				}
				return false
			}
			e.Start(context.Background())
			t.Cleanup(e.Close)
			text := strings.Repeat("0123456789", 10)
			if err := e.PrepareSimulatorSpeech(context.Background(), "proportional", text); err != nil {
				t.Fatal(err)
			}
			due := time.Now().Add(time.Hour)
			req := SimulatorSpeechRequest{ActionID: "proportional", Text: text, DueAt: due, ExpiresAt: due.Add(15 * time.Second), MaxHold: 2 * time.Second}
			if err := e.ScheduleSimulatorSpeech(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			simulatorLoop(t, e, func() { e.advanceSimulator(due) })
			waitFor(t, "one accepted second then rejection", time.Second, func() bool { return chunks.Load() >= 2 })
			if terminal == "close" {
				e.Close()
			} else if terminal == "cancelled" {
				if err := e.CancelSimulatorSpeech(context.Background(), req.ActionID); err != nil {
					t.Fatal(err)
				}
			} else {
				simulatorLoop(t, e, func() { e.advanceSimulator(due.Add(req.MaxHold)) })
			}
			check := func() {
				var heard string
				for _, msg := range e.history {
					if msg.Role == provider.RoleAssistant {
						heard += msg.Content
					}
				}
				if heard != text[:10] {
					t.Errorf("one of ten seconds heard: got %q, want %q", heard, text[:10])
				}
			}
			if terminal == "close" {
				check()
			} else {
				simulatorLoop(t, e, check)
			}
		})
	}
}

func TestSimulatorResumePreservesFinalArrivalOrder(t *testing.T) {
	for _, terminal := range []string{"cancelled", "timed_out"} {
		t.Run(terminal, func(t *testing.T) {
			e, c := simulatorEngine(t, true)
			due := time.Now().Add(time.Hour)
			req := simulatorReservation(t, e, "drain", due)
			if err := e.ScheduleSimulatorSpeech(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			simulatorLoop(t, e, func() {
				e.advanceSimulator(due)
				e.onFinalTranscript(&asrRun{id: 1}, "older final")
				if terminal == "cancelled" {
					e.finishSimulatorAction(e.simulator.active, "cancelled", "test cancellation")
				} else {
					e.advanceSimulator(due.Add(req.MaxHold))
				}
				e.onFinalTranscript(&asrRun{id: 2}, "newer final")
				e.opts.Speculative = true
				current := e.tracker.Current()
				generation := e.gen.Current()
				e.speculateOnFinal("What time is the appointment?")
				e.startSpeculativeTurn("What time is the appointment?")
				if e.tracker.Current() != current || e.gen.Current() != generation {
					t.Error("speculation bypassed pending resume")
				}
				if c.count(live.ServerDelegationCreated) != 0 {
					t.Error("newer final bypassed posted resume")
				}
			})
			waitFor(t, "one resumed ordinary delegation", time.Second, func() bool { return c.count(live.ServerDelegationCreated) > 0 })
			simulatorLoop(t, e, func() {
				if c.count(live.ServerDelegationCreated) != 1 {
					t.Errorf("resumed %d times, want once", c.count(live.ServerDelegationCreated))
				}
				if got := e.tracker.Current().Transcript; got != "older final\nnewer final" {
					t.Errorf("final arrival order=%q", got)
				}
			})
		})
	}
}
