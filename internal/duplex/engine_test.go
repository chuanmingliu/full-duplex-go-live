package duplex

import (
	"context"
	b64 "encoding/base64"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

// collector records every event the engine emits, the way a client would.
type collector struct {
	mu     sync.Mutex
	counts map[string]int
	events []any
	audio  []byte
	rate   int
}

func newCollector(rate int) *collector {
	return &collector{counts: map[string]int{}, rate: rate}
}

func (c *collector) emit(event any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	switch ev := event.(type) {
	case live.OutputAudioDelta:
		c.counts[ev.Type]++
		if pcm, err := decodeB64(ev.Audio); err == nil {
			c.audio = append(c.audio, pcm...)
		}
	case live.TranscriptDelta:
		c.counts[ev.Type]++
	case live.SpeechEvent:
		c.counts[ev.Type]++
	case live.DelegationCreatedEvent:
		c.counts[ev.Type]++
	case live.AudioTruncatedEvent:
		c.counts[ev.Type]++
	case live.ChannelStateEvent:
		c.counts[ev.Type]++
	case live.TurnMetricsEvent:
		c.counts[ev.Type]++
	case live.ErrorEvent:
		c.counts[ev.Type]++
	case live.Usage:
		c.counts[ev.Type]++
	case live.AckEvent:
		c.counts[ev.Type]++
	case live.ResponseEventEnvelope:
		c.counts[ev.Type]++
	case live.BackchannelEvent:
		c.counts[ev.Type]++
	case live.SessionStartedEvent:
		c.counts[ev.Type]++
	case live.SessionClosedEvent:
		c.counts[ev.Type]++
	default:
		// An event type the collector does not know about is a gap in the
		// harness, not something to swallow: an assertion on it would silently
		// read zero forever.
		c.counts["<uncounted>"]++
	}
}

func (c *collector) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[name]
}

func (c *collector) audioMS() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return audio.PCM16(c.rate).DurationMS(c.audio)
}

// delegationReasons returns the reasons of every delegation, in order. Order is
// the assertion that matters for speculation: a speculative delegation that
// arrives after the final transcript bought nothing.
func (c *collector) delegationReasons() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, ev := range c.events {
		if d, ok := ev.(live.DelegationCreatedEvent); ok {
			out = append(out, d.Delegation.Reason)
		}
	}
	return out
}

func (c *collector) find(match func(any) bool) any {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ev := range c.events {
		if match(ev) {
			return ev
		}
	}
	return nil
}

func (c *collector) inputTranscript() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, ev := range c.events {
		d, ok := ev.(live.TranscriptDelta)
		if !ok || d.Type != live.ServerInputTranscriptDelta {
			continue
		}
		if d.Replace {
			b.Reset()
		}
		b.WriteString(d.Content)
	}
	return b.String()
}

func decodeB64(s string) ([]byte, error) {
	return base64Decode(s)
}

// testConfig is the default profile with pacing kept on (truncation accounting
// depends on it) and the timers shortened so tests do not idle.
func testConfig() config.Config {
	cfg := config.Default()
	cfg.Duplex.Backchannel = false
	cfg.Duplex.PlaybackPaced = true
	cfg.Duplex.PlaybackLeadMS = 120
	cfg.Duplex.SpeculativeStableMS = 200
	return cfg
}

func newTestEngine(t *testing.T, cfg config.Config, c *collector) (*Engine, context.CancelFunc) {
	t.Helper()
	asrProvider := mock.NewASR("你好帮我查一下明天的天气")
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	tts := mock.NewTTS()

	e := New(Options{
		Cfg:         cfg,
		SessionID:   "test",
		ClientRate:  cfg.ClientRate,
		Delegation:  live.DelegationResponses,
		Speculative: cfg.Duplex.Speculative,
		Backchannel: cfg.Duplex.Backchannel,
	}, Deps{
		ASR:  asrProvider,
		LLM:  llm,
		TTS:  tts,
		Emit: c.emit,
	})
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)
	t.Cleanup(func() { e.Close(); cancel() })
	return e, cancel
}

// speak streams synthetic voiced audio at real time, as a microphone would.
func speak(e *Engine, rate, ms int) {
	pushPaced(e, voiced(rate, ms, 0.45), rate)
}

// pause streams silence at real time.
func pause(e *Engine, rate, ms int) {
	pushPaced(e, make([]byte, audio.PCM16(rate).BytesForMS(ms)), rate)
}

func pushPaced(e *Engine, pcm []byte, rate int) {
	frame := audio.PCM16(rate).BytesForMS(20)
	for off := 0; off < len(pcm); off += frame {
		end := off + frame
		if end > len(pcm) {
			end = len(pcm)
		}
		e.PushAudio(pcm[off:end])
		time.Sleep(20 * time.Millisecond)
	}
}

func voiced(rate, ms int, amp float64) []byte {
	n := rate * ms / 1000
	out := make([]float32, n)
	for i := range out {
		t := float64(i) / float64(rate)
		env := 0.62 + 0.38*math.Sin(2*math.Pi*4.5*t)
		v := amp * env * (math.Sin(2*math.Pi*140*t) +
			0.5*math.Sin(2*math.Pi*280*t) +
			0.25*math.Sin(2*math.Pi*560*t)) / 1.75
		out[i] = float32(v)
	}
	return audio.EncodePCM16(out)
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestEngineCompletesATurn(t *testing.T) {
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)
	e, _ := newTestEngine(t, cfg, c)

	speak(e, cfg.ClientRate, 1400)
	pause(e, cfg.ClientRate, 700)

	waitFor(t, "speech detection", 3*time.Second, func() bool {
		return c.count(live.ExtSpeechStarted) > 0
	})
	waitFor(t, "a delegation", 3*time.Second, func() bool {
		return c.count(live.ServerDelegationCreated) > 0
	})
	waitFor(t, "assistant audio", 5*time.Second, func() bool {
		return c.audioMS() > 200
	})

	if got := c.inputTranscript(); got == "" {
		t.Error("no input transcript was produced")
	}
	if c.count(live.ServerOutputTranscriptDelta) == 0 {
		t.Error("no output transcript deltas were produced")
	}
	if c.count(live.ServerError) != 0 {
		t.Errorf("session produced %d error events", c.count(live.ServerError))
	}
}

// TestEngineKeepsListeningWhileSpeaking is the property that separates this
// from a half-duplex cascade: input transcription continues during playback.
func TestEngineKeepsListeningWhileSpeaking(t *testing.T) {
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)
	e, _ := newTestEngine(t, cfg, c)

	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "assistant audio", 5*time.Second, func() bool { return c.audioMS() > 150 })

	before := c.count(live.ServerInputTranscriptDelta)
	if !e.speaking.Load() {
		t.Skip("playback finished before the interruption window; timing-dependent")
	}
	// Talk over the assistant.
	speak(e, cfg.ClientRate, 900)

	if after := c.count(live.ServerInputTranscriptDelta); after <= before {
		t.Errorf("input transcription stalled while the assistant was speaking: %d deltas before, %d after",
			before, after)
	}
}

func TestEngineTruncatesOnBargeIn(t *testing.T) {
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)
	e, _ := newTestEngine(t, cfg, c)

	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "assistant audio", 5*time.Second, func() bool { return c.audioMS() > 300 })

	speak(e, cfg.ClientRate, 1000)

	waitFor(t, "a truncation report", 4*time.Second, func() bool {
		return c.count(live.ExtAudioTruncated) > 0
	})

	ev := c.find(func(a any) bool {
		e, ok := a.(live.AudioTruncatedEvent)
		return ok && e.Type == live.ExtAudioTruncated
	}).(live.AudioTruncatedEvent)

	if ev.PlayedMS <= 0 {
		t.Errorf("truncation reported %dms played; the user heard some of the turn", ev.PlayedMS)
	}
	if ev.PlayedMS > ev.TotalMS {
		t.Errorf("reported %dms played out of %dms emitted", ev.PlayedMS, ev.TotalMS)
	}
	if ev.Text == "" {
		t.Error("truncation reported no spoken text; history would record nothing")
	}
	if full := mock.Reply(""); strings.Contains(full, ev.Text) && len(ev.Text) >= len(full) {
		t.Error("truncation claimed the whole reply was heard")
	}
}

func TestEngineSpeculatesAndCommits(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.Speculative = true
	c := newCollector(cfg.ClientRate)
	e, _ := newTestEngine(t, cfg, c)

	// Long enough that the mock recognizer's hypothesis stabilises before the
	// utterance closes, which is what triggers speculation.
	speak(e, cfg.ClientRate, 2600)
	pause(e, cfg.ClientRate, 700)

	waitFor(t, "a delegation", 5*time.Second, func() bool {
		return c.count(live.ServerDelegationCreated) > 0
	})

	ev := c.find(func(a any) bool {
		d, ok := a.(live.DelegationCreatedEvent)
		return ok && d.Delegation.Reason == "speculative"
	})
	if ev == nil {
		// Not skipped. A recognizer goes quiet once its hypothesis settles, so
		// "stable" and "no further events" are the same condition — if
		// stability is only evaluated when an event arrives, speculation can
		// never fire at all, and a skip here hides exactly that.
		t.Fatal("no speculative delegation: the transcript settled and nothing acted on it")
	}
	waitFor(t, "assistant audio", 5*time.Second, func() bool { return c.audioMS() > 100 })
}

func TestEngineClientDelegationSpeaksCommentary(t *testing.T) {
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)

	asrProvider := mock.NewASR("你好")
	e := New(Options{
		Cfg:        cfg,
		SessionID:  "test-client-delegation",
		ClientRate: cfg.ClientRate,
		Delegation: live.DelegationClient,
	}, Deps{
		ASR:  asrProvider,
		LLM:  mock.NewLLM(),
		TTS:  mock.NewTTS(),
		Emit: c.emit,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)
	defer e.Close()

	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)

	waitFor(t, "a client delegation", 4*time.Second, func() bool {
		return c.count(live.ServerDelegationCreated) > 0
	})

	// In client mode the engine must not generate anything on its own.
	time.Sleep(400 * time.Millisecond)
	if c.audioMS() > 0 {
		t.Fatalf("client delegation produced %v ms of audio without commentary", c.audioMS())
	}

	d := c.find(func(a any) bool {
		_, ok := a.(live.DelegationCreatedEvent)
		return ok
	}).(live.DelegationCreatedEvent)
	if d.Delegation.Target != live.DelegationClient {
		t.Fatalf("delegation target is %q, want %q", d.Delegation.Target, live.DelegationClient)
	}

	e.AppendCommentary(d.Delegation.ID, "好的，已经帮你订好了。")
	waitFor(t, "commentary audio", 4*time.Second, func() bool { return c.audioMS() > 100 })
}

func TestEngineRejectsUnknownToolResult(t *testing.T) {
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)
	e, _ := newTestEngine(t, cfg, c)

	e.SubmitToolOutput(live.ResponseItem{Type: "function_call_output", CallID: "nope", Output: "{}"})
	waitFor(t, "an error event", 2*time.Second, func() bool {
		return c.count(live.ServerError) > 0
	})
}

func TestEngineMuteStopsTranscription(t *testing.T) {
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)
	e, _ := newTestEngine(t, cfg, c)

	e.SetMuted(true)
	time.Sleep(100 * time.Millisecond)
	speak(e, cfg.ClientRate, 1000)
	pause(e, cfg.ClientRate, 500)

	if c.count(live.ExtSpeechStarted) != 0 {
		t.Error("a muted session still detected speech")
	}

	e.SetMuted(false)
	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "speech after unmute", 3*time.Second, func() bool {
		return c.count(live.ExtSpeechStarted) > 0
	})
}

var _ = provider.PipelineRate

func base64Decode(s string) ([]byte, error) {
	return b64.StdEncoding.DecodeString(s)
}

// newSlowEngine spaces the synthesized segments far apart, which is what opens
// the inter-segment gap these tests are about.
func newSlowEngine(t *testing.T, cfg config.Config, c *collector) *Engine {
	t.Helper()
	llm := mock.NewLLM()
	llm.DelayPerRune = 45 * time.Millisecond

	e := New(Options{
		Cfg:         cfg,
		SessionID:   "slow",
		ClientRate:  cfg.ClientRate,
		Delegation:  live.DelegationResponses,
		Speculative: false,
	}, Deps{
		ASR:  mock.NewASR("你好帮我查一下明天的天气"),
		LLM:  llm,
		TTS:  mock.NewTTS(),
		Emit: c.emit,
	})
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)
	t.Cleanup(func() { e.Close(); cancel() })
	return e
}

// waitForSegmentGap returns once the player has fallen silent between two
// synthesized sentences while the answer is still in progress. That is the
// window in which interruption used to be missed entirely.
func waitForSegmentGap(t *testing.T, e *Engine, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !e.player.Speaking() && e.assistantHasFloor() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// TestEngineYieldsFloorInTheGapBetweenSegments is the regression test for the
// bug this policy was built to fix: a new query arriving while the player is
// momentarily idle between sentences left the stale answer running, so it
// resumed and played ahead of the answer to what had just been asked.
func TestEngineYieldsFloorInTheGapBetweenSegments(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.OnNewQuery = "cut"
	c := newCollector(cfg.ClientRate)
	e := newSlowEngine(t, cfg, c)

	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "assistant audio", 6*time.Second, func() bool { return c.audioMS() > 200 })

	if !waitForSegmentGap(t, e, 4*time.Second) {
		t.Skip("never observed a gap between segments; timing-dependent")
	}
	if e.speaking.Load() {
		t.Fatal("expected the player to be idle in the gap")
	}

	// The user asks something new in that silence.
	speak(e, cfg.ClientRate, 900)

	waitFor(t, "the stale answer to be cut", 4*time.Second, func() bool {
		return c.count(live.ExtAudioTruncated) > 0
	})
}

func TestEngineQueuePolicyLetsTheAnswerFinish(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.OnNewQuery = "queue"
	c := newCollector(cfg.ClientRate)
	e := newSlowEngine(t, cfg, c)

	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "assistant audio", 6*time.Second, func() bool { return c.audioMS() > 200 })

	speak(e, cfg.ClientRate, 900)
	time.Sleep(600 * time.Millisecond)

	if c.count(live.ExtAudioTruncated) != 0 {
		t.Errorf("queue policy cut the answer short; it should let it run to the end")
	}
}

// TestEngineFinishSentencePolicyCompletesTheSentence checks the polite middle
// ground. Its truncation report is distinguishable from a hard cut: everything
// emitted was heard, so played equals total.
func TestEngineFinishSentencePolicyCompletesTheSentence(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.OnNewQuery = "finish_sentence"
	c := newCollector(cfg.ClientRate)
	e := newSlowEngine(t, cfg, c)

	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "assistant audio", 6*time.Second, func() bool { return c.audioMS() > 200 })

	speak(e, cfg.ClientRate, 900)

	waitFor(t, "the sentence to be handed over", 6*time.Second, func() bool {
		return c.count(live.ExtAudioTruncated) > 0
	})
	ev := c.find(func(a any) bool {
		x, ok := a.(live.AudioTruncatedEvent)
		return ok && x.Type == live.ExtAudioTruncated
	}).(live.AudioTruncatedEvent)

	if ev.PlayedMS != ev.TotalMS {
		t.Errorf("finish_sentence reported %d of %d ms played; everything emitted was heard, "+
			"so the two should match — a mismatch means the sentence was cut after all",
			ev.PlayedMS, ev.TotalMS)
	}
	if ev.Text == "" {
		t.Error("no spoken text reported; history would lose the sentence")
	}
}

// slowBackend delays its first token, which is what the holding filler exists
// to cover.
type slowBackend struct{ delay time.Duration }

func (s *slowBackend) Name() string { return "slow" }

func (s *slowBackend) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	out := make(chan provider.LLMDelta, 8)
	go func() {
		defer close(out)
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return
		}
		for _, r := range "好的，已经查到了。" {
			select {
			case out <- provider.LLMDelta{Text: string(r)}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// TestEngineHoldsTheFloorWhileTheBackendWorks covers the behaviour a delegating
// agent needs: the conversational layer keeps talking while the slow half runs
// elsewhere, instead of going silent long enough to sound like a dropped call.
func TestEngineHoldsTheFloorWhileTheBackendWorks(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.HoldingFiller = true
	cfg.Duplex.HoldingFillerAfterMS = 300
	cfg.Duplex.HoldingFillerPhrases = []string{"我看一下"}
	c := newCollector(cfg.ClientRate)

	e := New(Options{
		Cfg:        cfg,
		SessionID:  "filler",
		ClientRate: cfg.ClientRate,
		Delegation: live.DelegationResponses,
	}, Deps{
		ASR:  mock.NewASR("明天的天气"),
		LLM:  &slowBackend{delay: 2500 * time.Millisecond},
		TTS:  mock.NewTTS(),
		Emit: c.emit,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)
	defer e.Close()

	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)

	waitFor(t, "a holding phrase while the backend works", 4*time.Second, func() bool {
		return c.count(live.ExtBackchannel) > 0
	})
	// And audio for it, not merely the event.
	waitFor(t, "filler audio", 3*time.Second, func() bool { return c.audioMS() > 100 })
}

// TestEngineSkipsTheFillerOnAFastAnswer guards the cost: the filler must not
// fire on turns that were already going to answer quickly, where it would only
// delay the real reply.
func TestEngineSkipsTheFillerOnAFastAnswer(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.HoldingFiller = true
	cfg.Duplex.HoldingFillerAfterMS = 1500
	cfg.Duplex.HoldingFillerPhrases = []string{"我看一下"}
	c := newCollector(cfg.ClientRate)
	e, _ := newTestEngine(t, cfg, c)

	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "assistant audio", 5*time.Second, func() bool { return c.audioMS() > 200 })

	if c.count(live.ExtBackchannel) != 0 {
		t.Error("the filler fired on a fast answer, where it can only delay the real reply")
	}
}

// leakyTTS imitates a provider whose persistent connection is left
// desynchronized by an abandoned synthesis: the next call replays the tail of
// the interrupted sentence before the new one. That is exactly the failure a
// real vendor stream produces after a barge-in, and no amount of care in the
// engine can detect it — the audio arrives on the new turn's channel, correctly
// formed and completely wrong.
type leakyTTS struct {
	mu    sync.Mutex
	opens int
}

func (l *leakyTTS) Name() string { return "leaky" }

func (l *leakyTTS) Open(ctx context.Context, opts provider.TTSOptions) (provider.TTSStream, error) {
	l.mu.Lock()
	l.opens++
	l.mu.Unlock()
	rate := opts.SampleRate
	if rate <= 0 {
		rate = provider.PipelineRate
	}
	return &leakyStream{rate: rate}, nil
}

func (l *leakyTTS) openCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.opens
}

type leakyStream struct {
	rate      int
	abandoned bool
}

func (s *leakyStream) SampleRate() int { return s.rate }
func (s *leakyStream) Close() error    { return nil }

func (s *leakyStream) Synthesize(ctx context.Context, text string) (<-chan provider.TTSChunk, error) {
	leak := s.abandoned
	s.abandoned = false
	out := make(chan provider.TTSChunk, 8)
	go func() {
		defer close(out)
		if leak {
			// The tail of the sentence nobody wanted.
			select {
			case out <- provider.TTSChunk{PCM: make([]byte, s.rate)}:
			case <-ctx.Done():
				return
			}
		}
		for i := 0; i < 8; i++ {
			select {
			case out <- provider.TTSChunk{PCM: make([]byte, s.rate/4)}:
			case <-ctx.Done():
				s.abandoned = true
				return
			}
			select {
			case <-time.After(60 * time.Millisecond):
			case <-ctx.Done():
				s.abandoned = true
				return
			}
		}
	}()
	return out, nil
}

// TestEngineResetsTTSOnInterrupt covers the fallback for such a provider: with
// the setting on, an interruption drops the session so the next turn cannot
// inherit its state.
func TestEngineResetsTTSOnInterrupt(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.OnNewQuery = "cut"
	cfg.Duplex.ResetTTSOnInterrupt = true
	c := newCollector(cfg.ClientRate)

	tts := &leakyTTS{}
	llm := mock.NewLLM()
	llm.DelayPerRune = 30 * time.Millisecond
	e := New(Options{
		Cfg:        cfg,
		SessionID:  "leaky",
		ClientRate: cfg.ClientRate,
		Delegation: live.DelegationResponses,
	}, Deps{
		ASR:  mock.NewASR("你好帮我查一下明天的天气"),
		LLM:  llm,
		TTS:  tts,
		Emit: c.emit,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)
	defer e.Close()

	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "assistant audio", 6*time.Second, func() bool { return c.audioMS() > 200 })

	opensBefore := tts.openCount()
	speak(e, cfg.ClientRate, 900)
	waitFor(t, "the interruption", 4*time.Second, func() bool {
		return c.count(live.ExtAudioTruncated) > 0
	})
	pause(e, cfg.ClientRate, 700)

	waitFor(t, "a fresh synthesis session", 5*time.Second, func() bool {
		return tts.openCount() > opensBefore
	})
}

// TestEngineSpeculatesInsideTheEndOfTurnPause covers what speculation is for:
// starting the backend during the silence the VAD is still waiting out, so the
// caller never pays the full time-to-first-token.
//
// The pause here is shorter than vad.min_silence_ms, so the utterance is still
// open when the delegation must appear. If it appears only after the final
// transcript, the head start was zero and the feature is doing nothing.
func TestEngineSpeculatesInsideTheEndOfTurnPause(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.Speculative = true
	cfg.Duplex.SpeculativeStableMS = 200
	cfg.Duplex.SpeculativeMinChars = 6
	cfg.VAD.MinSilenceMS = 900 // a wide window, so the assertion is not a race
	c := newCollector(cfg.ClientRate)
	e, _ := newTestEngine(t, cfg, c)

	speak(e, cfg.ClientRate, 1600)
	go pause(e, cfg.ClientRate, 2000)

	waitFor(t, "a speculative delegation", 2*time.Second, func() bool {
		reasons := c.delegationReasons()
		return len(reasons) > 0 && reasons[0] == "speculative"
	})

	// Audio must already be flowing before the VAD has closed the turn: that
	// head start is the whole benefit.
	waitFor(t, "audio inside the pause", 3*time.Second, func() bool {
		return c.audioMS() > 150
	})
}

// TestEngineDoesNotSpeculateWhileTheSpeakerIsStillTalking is the regression
// test for the version that guessed on transcript churn alone.
//
// A recognizer emits only when its hypothesis changes, so it also falls silent
// whenever it is running behind — which mid-sentence it usually is. Treating
// that as "the speaker has finished" fired the watch on a prefix, and nearly
// every speculative turn was immediately revised: a wasted generation, a
// cancelled synthesis, and a turn that restarted from zero. Continuous speech
// must produce no speculation at all.
func TestEngineDoesNotSpeculateWhileTheSpeakerIsStillTalking(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.Speculative = true
	cfg.Duplex.SpeculativeStableMS = 200
	cfg.Duplex.SpeculativeMinChars = 6
	c := newCollector(cfg.ClientRate)
	e, _ := newTestEngine(t, cfg, c)

	// Far longer than the stability window: the hypothesis reaches full length
	// early and then stops changing while the speaker keeps going.
	speak(e, cfg.ClientRate, 2800)

	for _, reason := range c.delegationReasons() {
		if reason == "speculative" {
			t.Fatal("speculated on a prefix while the speaker was still talking")
		}
	}

	pause(e, cfg.ClientRate, 700)
	waitFor(t, "the turn to be delegated once speech ends", 3*time.Second, func() bool {
		return len(c.delegationReasons()) > 0
	})
}
