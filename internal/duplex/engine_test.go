package duplex

import (
	"context"
	b64 "encoding/base64"
	"io"
	"log/slog"
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
	case live.InputBackchannelEvent:
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

// outputTranscript reassembles what the assistant is recorded as having said
// for one item. Locked, because the engine is still emitting.
func (c *collector) outputTranscript(itemID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, ev := range c.events {
		d, ok := ev.(live.TranscriptDelta)
		if ok && d.Type == live.ServerOutputTranscriptDelta && d.ItemID == itemID {
			b.WriteString(d.Content)
		}
	}
	return b.String()
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

// variableBackend takes a different amount of time on each turn, which is what
// separates a wait worth covering from the way this session simply sounds.
type variableBackend struct {
	mu     sync.Mutex
	delays []time.Duration
	calls  int
}

func (v *variableBackend) Name() string { return "variable" }

func (v *variableBackend) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	v.mu.Lock()
	i := v.calls
	v.calls++
	if i >= len(v.delays) {
		i = len(v.delays) - 1
	}
	delay := v.delays[i]
	v.mu.Unlock()

	out := make(chan provider.LLMDelta, 8)
	go func() {
		defer close(out)
		select {
		case <-time.After(delay):
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
//
// The session has to have been quick first. A filler means "this is taking
// longer than usual", and on the first delegation of a call there is no usual —
// see TestHoldingFillerSaysNothingBeforeItKnowsWhatIsNormal.
func TestEngineHoldsTheFloorWhileTheBackendWorks(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.Speculative = false
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
		ASR: mock.NewASR("明天的天气"),
		LLM: &variableBackend{delays: []time.Duration{
			50 * time.Millisecond, 50 * time.Millisecond, 2500 * time.Millisecond,
		}},
		TTS:  mock.NewTTS(),
		Emit: c.emit,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)
	defer e.Close()

	for turn := 0; turn < 2; turn++ {
		speak(e, cfg.ClientRate, 1200)
		pause(e, cfg.ClientRate, 700)
		waitFor(t, "a prompt answer", 8*time.Second, func() bool {
			return c.count(live.ExtTurnMetrics) > turn
		})
	}
	if n := c.count(live.ExtBackchannel); n != 0 {
		t.Fatalf("%d filler(s) on turns the backend answered in 50 ms", n)
	}

	// Now one that takes fifty times as long as everything before it.
	audioBefore := c.audioMS()
	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)

	waitFor(t, "a holding phrase while the backend works", 4*time.Second, func() bool {
		return c.count(live.ExtBackchannel) > 0
	})
	// And audio for it, not merely the event.
	waitFor(t, "filler audio", 3*time.Second, func() bool { return c.audioMS() > audioBefore+100 })
}

// TestHoldingFillerSaysNothingBeforeItKnowsWhatIsNormal is the regression test
// for a filler that broke into the first exchange of a call.
//
// A real session: a 12k-character persona prompt put time-to-first-token at
// about three seconds on every turn, holding_filler_after_ms was the default
// 1.5 s, and the adaptive gate needed three observations before it would engage.
// A short call never gets three. So the gate stayed off for turns one, two and
// three and the filler fired on all of them — the tic it exists to prevent,
// relocated to the start of the call, where it does the most damage. The caller
// said 走走走 and heard 我看一下 back.
//
// A backend that is uniformly slow is a backend that is never unusually slow.
func TestHoldingFillerSaysNothingBeforeItKnowsWhatIsNormal(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.HoldingFiller = true
	cfg.Duplex.HoldingFillerAfterMS = 1500
	cfg.Duplex.HoldingFillerPhrases = []string{"我看一下"}
	c := newCollector(cfg.ClientRate)
	e := New(Options{
		Cfg:        cfg,
		SessionID:  "uniform",
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

	for turn := 0; turn < 3; turn++ {
		speak(e, cfg.ClientRate, 1200)
		pause(e, cfg.ClientRate, 700)
		waitFor(t, "the answer", 12*time.Second, func() bool {
			return c.count(live.ExtTurnMetrics) > turn
		})
	}
	if n := c.count(live.ExtBackchannel); n != 0 {
		t.Errorf("%d filler(s) on a backend that is slow on every turn; none of those waits "+
			"was unusual, and the first is the worst possible place to say so", n)
	}
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

// silentASR is a recognizer that hears nothing at all: a noisy line, a far
// microphone, a provider having a bad minute. It exists to prove the hold is
// bounded — that the assistant does not get unlimited licence to keep talking
// just because no evidence arrived.
type silentASR struct{}

func (silentASR) Name() string { return "silent" }

func (silentASR) Open(ctx context.Context, _ provider.ASROptions) (provider.ASRStream, error) {
	return &silentASRStream{results: make(chan provider.ASRResult)}, nil
}

type silentASRStream struct {
	once    sync.Once
	results chan provider.ASRResult
}

func (s *silentASRStream) Write([]byte) error                 { return nil }
func (s *silentASRStream) Results() <-chan provider.ASRResult { return s.results }
func (s *silentASRStream) CloseSend() error                   { s.stop(); return nil }
func (s *silentASRStream) Close() error                       { s.stop(); return nil }
func (s *silentASRStream) stop()                              { s.once.Do(func() { close(s.results) }) }

// bcEngine builds an engine with the backchannel list active. A nil asr uses
// the mock recognizer hearing `heard`.
func bcEngine(t *testing.T, c *collector, asr provider.ASR, phrases []string, greeting string) *Engine {
	t.Helper()
	cfg := testConfig()
	cfg.Duplex.Speculative = false
	cfg.Duplex.AllowBargeIn = true
	cfg.Duplex.OnNewQuery = "cut"
	cfg.Duplex.UserBackchannelPhrases = phrases
	// Deliberately shorter than vad.min_silence_ms, and that is the point. The
	// final transcript cannot arrive until the VAD has closed the turn, so a
	// hold that simply expires this long after the utterance opened can never
	// see a verdict — which is exactly the bug this value reproduces. Only the
	// distinction between "is anything arriving" and "what is the answer"
	// makes these tests pass.
	cfg.Duplex.UserBackchannelHoldMS = 200

	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	e := New(Options{
		Cfg:        cfg,
		SessionID:  "backchannel",
		ClientRate: cfg.ClientRate,
		Delegation: live.DelegationResponses,
		Greeting:   greeting,
	}, Deps{
		ASR:  asr,
		LLM:  llm,
		TTS:  mock.NewTTS(),
		Emit: c.emit,
	})
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)
	t.Cleanup(func() { e.Close(); cancel() })
	return e
}

// TestEngineKeepsTheFloorThroughAnAcknowledgement is the behaviour the whole
// phrase list exists for: murmur agreement over the answer and the answer keeps
// going.
//
// A full-duplex model does this natively, having learned that "嗯" is not a
// request to stop. golive cannot hear the difference, so it holds the floor for
// a moment and lets the recognizer settle it — and the assertion here is that
// the murmur produces no truncation at all.
func TestEngineKeepsTheFloorThroughAnAcknowledgement(t *testing.T) {
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)
	e := bcEngine(t, c, mock.NewASR("嗯"), []string{"嗯", "对", "好的"}, "")

	speak(e, cfg.ClientRate, 900)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "the assistant to take the floor", 5*time.Second, func() bool {
		return c.audioMS() > 250
	})

	// Murmur over it.
	speak(e, cfg.ClientRate, 500)
	pause(e, cfg.ClientRate, 700)

	waitFor(t, "the backchannel to be recognised", 4*time.Second, func() bool {
		return c.count(live.ExtInputBackchannel) > 0
	})
	if n := c.count(live.ExtAudioTruncated); n != 0 {
		t.Fatalf("an acknowledgement truncated the answer %d time(s); it must not interrupt", n)
	}
	// And it is not a question: nothing was delegated for it.
	if reasons := c.delegationReasons(); len(reasons) != 1 {
		t.Fatalf("delegations = %v; the acknowledgement must not be answered", reasons)
	}
}

// TestEngineYieldsToRealSpeech is the other half, and the one that keeps the
// feature honest. A phrase list that swallowed genuine interruptions would be a
// far worse bug than the one it fixes.
func TestEngineYieldsToRealSpeech(t *testing.T) {
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)
	// Begins with a listed phrase and continues — the case the incremental
	// prefix check exists for.
	e := bcEngine(t, c, mock.NewASR("嗯等一下我改主意了"), []string{"嗯", "对", "好的"}, "")

	speak(e, cfg.ClientRate, 900)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "the assistant to take the floor", 5*time.Second, func() bool {
		return c.audioMS() > 250
	})

	speak(e, cfg.ClientRate, 900)
	waitFor(t, "the interruption", 4*time.Second, func() bool {
		return c.count(live.ExtAudioTruncated) > 0
	})
	if n := c.count(live.ExtInputBackchannel); n != 0 {
		t.Fatalf("real speech was treated as a backchannel %d time(s)", n)
	}
}

// TestEngineYieldsWhenTheTranscriptNeverArrives pins the failure direction.
//
// A recognizer that says nothing — a noisy line, a provider having a bad
// minute — must not buy the assistant unlimited licence to keep talking. The
// hold is bounded and expires toward yielding, because talking over someone is
// worse than stopping for nothing.
func TestEngineYieldsWhenTheTranscriptNeverArrives(t *testing.T) {
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)
	// A long greeting gives the assistant the floor without needing a turn,
	// which a recognizer that hears nothing could never produce.
	e := bcEngine(t, c, silentASR{}, []string{"嗯", "对"},
		"你好，我是语音助手，今天有什么可以帮你的吗，随时打断我都可以")
	e.Greet()

	waitFor(t, "the greeting to start", 5*time.Second, func() bool {
		return c.audioMS() > 250
	})

	speak(e, cfg.ClientRate, 1400)
	waitFor(t, "the hold to expire and yield the floor", 4*time.Second, func() bool {
		return c.count(live.ExtAudioTruncated) > 0
	})
	if n := c.count(live.ExtInputBackchannel); n != 0 {
		t.Fatalf("silence was reported as a backchannel %d time(s); no evidence is not evidence", n)
	}
}

// TestHoldingFillerFiresOnlyWhenATurnIsLateForThisSession is the regression
// test for a filler that became a verbal tic.
//
// A real call showed 稍等一下 before literally every answer: the backend's
// time-to-first-token had settled around 2.1 s under a long system prompt,
// holding_filler_after_ms was still the 1.5 s that suited a shorter one, and so
// the threshold was crossed on every single turn. A filler that always fires is
// not covering an unusual wait — it is just something the agent says, and it
// costs a synthesis and delays the real answer each time.
func TestHoldingFillerFiresOnlyWhenATurnIsLateForThisSession(t *testing.T) {
	e := &Engine{}
	e.opts.Cfg.Duplex.HoldingFillerAfterMS = 1500

	// Nothing observed yet: "late" has no meaning, and the caller must treat
	// that as a reason to stay quiet rather than as a reason to fall back on
	// the configured floor.
	if _, known := e.lateThreshold(); known {
		t.Fatal("lateThreshold claimed to know what is late before a single answer had been measured")
	}

	// One observation is enough, because three is more than a short call ever
	// reaches. A session whose every turn takes about three seconds fired the
	// filler on turns one, two and three while waiting for a third sample.
	e.ttfa.Add(3000)
	if _, known := e.lateThreshold(); !known {
		t.Fatal("lateThreshold still did not know after an answer had been measured")
	}
	e.ttfa = rollingMS{}

	// A backend that consistently takes about 2.1 s. Every one of those turns
	// crosses the 1.5 s floor, which is exactly how the tic happened.
	for _, ms := range []int64{2100, 2050, 2150, 2120} {
		e.ttfa.Add(ms)
	}
	late, known := e.lateThreshold()
	if !known {
		t.Fatal("lateThreshold stayed unknown after four observations")
	}
	if late <= 2100*time.Millisecond {
		t.Errorf("lateThreshold = %v; a typical 2.1s turn must not count as late", late)
	}
	// And a genuinely slow turn still must.
	if late >= 4*time.Second {
		t.Errorf("lateThreshold = %v; a turn twice the usual wait must still get a filler", late)
	}
}

func TestRollingMSKeepsRecentHistory(t *testing.T) {
	var r rollingMS
	if r.N() != 0 || r.P50() != 0 {
		t.Fatal("an empty window must report nothing rather than a made-up number")
	}
	r.Add(-1)
	if r.N() != 0 {
		t.Fatal("a negative duration is a clock artefact, not an observation")
	}
	for i := 1; i <= 20; i++ {
		r.Add(int64(i * 100))
	}
	// The window is short on purpose: a backend's latency changes within a
	// call, and a long window would still be describing the first few turns.
	if r.N() > 8 {
		t.Errorf("window held %d observations; it is meant to stay short", r.N())
	}
	if got := r.P50(); got < 1500 {
		t.Errorf("P50 = %d; the window must have dropped the early, unrepresentative values", got)
	}
}

// TestGreetingKeepsItsOwnOrigin is the regression test for negative latencies.
//
// A real call reported first_segment_ms: -742 and first_audio_out_ms: -480 on
// the greeting. The numbers were real; the zero was wrong. The caller said
// "好。" nine hundred milliseconds into a greeting that was already playing, and
// the engine stamped that utterance's speech-end onto whatever turn was current
// — which was the greeting. Audio already spoken was then dated to a moment
// still in the future, so every figure came out negative.
//
// A turn the caller is not waiting for has no origin, and a turn already live
// when they started talking belongs to an earlier utterance or to none.
func TestGreetingKeepsItsOwnOrigin(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.Speculative = false
	c := newCollector(cfg.ClientRate)
	e := bcEngine(t, c, mock.NewASR("好"), []string{"好", "嗯"},
		"您好，我是保险管家小翠儿，看到您的爱车快到报价期了，给您来电做个报价")
	e.Greet()

	waitFor(t, "the greeting to start", 5*time.Second, func() bool {
		return c.audioMS() > 300
	})
	// Talk over it, exactly as the caller did.
	speak(e, cfg.ClientRate, 700)
	pause(e, cfg.ClientRate, 700)

	waitFor(t, "the greeting's metrics", 6*time.Second, func() bool {
		return c.count(live.ExtTurnMetrics) > 0
	})

	ev := c.find(func(a any) bool {
		m, ok := a.(live.TurnMetricsEvent)
		return ok && m.TurnID == "item_1"
	})
	if ev == nil {
		t.Fatal("no metrics for the greeting turn")
	}
	m := ev.(live.TurnMetricsEvent)
	for name, v := range map[string]int64{
		"first_segment_ms":   m.FirstSegmentMS,
		"tts_first_audio_ms": m.TTSFirstAudioMS,
		"first_audio_out_ms": m.FirstAudioOutMS,
	} {
		if v < 0 {
			t.Errorf("%s = %d; the greeting was dated to a later utterance's speech end", name, v)
		}
	}
}

// TestFillerYieldsToTheAnswerItWasCovering is the regression test for a filler
// that made the wait longer.
//
// A real turn synthesized its answer at 3652 ms and did not get it onto the
// wire until 5620 ms, because 我看一下 was still occupying the floor. A filler
// exists to cover a wait; one that outlives the wait is pure added delay, and
// nearly two seconds of it.
func TestFillerYieldsToTheAnswerItWasCovering(t *testing.T) {
	var mu sync.Mutex
	heard := map[string]int{}
	p := NewPlayer(PlayerConfig{Rate: 24000, ChunkMS: 40, Paced: true, LeadMS: 40},
		func(id string, pcm []byte) {
			mu.Lock()
			heard[id] += len(pcm)
			mu.Unlock()
		})
	bytesFor := func(id string) int {
		mu.Lock()
		defer mu.Unlock()
		return heard[id]
	}

	var gen Generation
	go p.Run(&gen)
	defer p.Close()

	// Nothing playing: there is nothing to preempt, and saying so matters —
	// the engine calls this on every first segment.
	if p.PreemptBackchannel() {
		t.Fatal("preempted a filler that was not there")
	}

	// A filler takes the floor. Paced, and a second of audio, so there is a
	// real tail to cut rather than a race with the run loop.
	oneSecond := make([]byte, 2*24000)
	p.Enqueue(Segment{Kind: SegBegin, Gen: gen.Current(), TurnID: "bc_1", Text: "我看一下"})
	p.Enqueue(Segment{Kind: SegAudio, Gen: gen.Current(), TurnID: "bc_1", PCM: oneSecond})
	waitFor(t, "the filler to be heard", 2*time.Second, func() bool { return bytesFor("bc_1") > 0 })

	if !p.PreemptBackchannel() {
		t.Fatal("a playing filler was not preempted")
	}
	cut := bytesFor("bc_1")
	if cut >= len(oneSecond) {
		t.Fatal("the filler had already finished; nothing was actually cut")
	}
	waitFor(t, "the floor to be released", 2*time.Second, func() bool { return !p.Active() })

	// And it stays cut: the rest of that second must never reach the wire.
	time.Sleep(200 * time.Millisecond)
	if now := bytesFor("bc_1"); now > cut {
		t.Errorf("filler audio kept flowing after preemption: %d bytes then %d now", cut, now)
	}

	// An answer must never be preempted this way, whatever else is true.
	p.Enqueue(Segment{Kind: SegBegin, Gen: gen.Current(), TurnID: "item_9", Text: "明天下午两点"})
	p.Enqueue(Segment{Kind: SegAudio, Gen: gen.Current(), TurnID: "item_9", PCM: oneSecond})
	waitFor(t, "the answer to be heard", 2*time.Second, func() bool { return bytesFor("item_9") > 0 })
	if p.PreemptBackchannel() {
		t.Fatal("PreemptBackchannel cut a real answer; only bc_ turns may be dropped")
	}
}

// TestSpokenFillersReachTheTranscript is the regression test for a written
// record that did not match the call.
//
// A real call spoke four holding fillers — 稍等一下, 让我查一下, 我看一下,
// 让我查一下 — and the transcript contained none of them. Two of those belonged
// to turns the caller interrupted before any answer arrived, so the transcript
// showed two user questions in a row with no reply, when what actually happened
// was the agent saying "one moment" and then being cut off. The record read as
// an agent ignoring its caller.
//
// What was said aloud and what the model is told are different records.
// Conflating them is what hid this, so the test pins both directions.
func TestSpokenFillersReachTheTranscript(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.Backchannel = true
	cfg.Duplex.BackchannelPhrases = []string{"我在听"}
	c := newCollector(cfg.ClientRate)
	e, _ := newTestEngine(t, cfg, c)

	filler := &Turn{ID: "bc_1", Generation: e.gen.Current(), State: TurnCommitted}
	e.speakBackchannel(filler, "稍等一下")

	said := c.outputTranscript("bc_1")
	if said != "稍等一下" {
		t.Errorf("transcript for the filler = %q, want %q; the caller heard it, so the record must show it",
			said, "稍等一下")
	}

	// The other direction: it must not become something the model reasons
	// from. An agent that reads its own "one moment" back as conversation will
	// answer it.
	e.recordTurn("bc_1", "稍等一下")
	done := make(chan struct{})
	e.post(func() { close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("engine loop did not drain")
	}
	for _, m := range e.history {
		if strings.Contains(m.Content, "稍等一下") {
			t.Fatal("a holding filler reached conversation history")
		}
	}
}

// TestAnswerOwnsTheFloorUntilItEnds is the regression test for the defect
// behind an agent that repeated itself.
//
// maybeHoldingFiller checks the floor is free before deciding to speak, but
// speakBackchannel then synthesizes on its own goroutine — a few hundred
// milliseconds during which the answer it was covering for can arrive and start
// playing. Its audio then landed mid-answer.
//
// The audible damage is obvious. The invisible damage is what actually hurt: a
// turn switch resets the player's per-turn accounting, so the answer's
// emittedMS restarted and its spoken spans were discarded. A real call reported
// output_audio_ms: 801 for a forty-seven character answer and wrote a
// five-character fragment into conversation history — after which the model,
// with no record of having explained itself, explained itself again. Two
// near-identical answers in one short call, and nothing in the log naming the
// cause.
func TestAnswerOwnsTheFloorUntilItEnds(t *testing.T) {
	var mu sync.Mutex
	heard := map[string]int{}
	var doneID, doneText string
	var doneMS int64

	p := NewPlayer(PlayerConfig{Rate: 24000, ChunkMS: 40, Paced: true, LeadMS: 40},
		func(id string, pcm []byte) {
			mu.Lock()
			heard[id] += len(pcm)
			mu.Unlock()
		})
	p.OnTurnDone(func(id string, ms int64, text string) {
		mu.Lock()
		doneID, doneMS, doneText = id, ms, text
		mu.Unlock()
	})

	var gen Generation
	go p.Run(&gen)
	defer p.Close()

	sec := make([]byte, 2*24000)
	first, second := "费用看方案。", "您先看下微信服务通知。"

	p.Enqueue(Segment{Kind: SegBegin, Gen: gen.Current(), TurnID: "item_3", Text: first})
	p.Enqueue(Segment{Kind: SegAudio, Gen: gen.Current(), TurnID: "item_3", PCM: sec})
	p.Enqueue(Segment{Kind: SegMark, Gen: gen.Current(), TurnID: "item_3", Text: first})
	waitFor(t, "the answer to take the floor", 3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return heard["item_3"] > 0
	})

	// A filler whose synthesis began while the floor was free now arrives.
	if p.Enqueue(Segment{Kind: SegBegin, Gen: gen.Current(), TurnID: "bc_1", Text: "让我查一下"}) {
		t.Error("a filler was accepted while an answer held the floor")
	}
	p.Enqueue(Segment{Kind: SegAudio, Gen: gen.Current(), TurnID: "bc_1", PCM: sec})
	p.Enqueue(Segment{Kind: SegMark, Gen: gen.Current(), TurnID: "bc_1", Text: "让我查一下"})

	p.Enqueue(Segment{Kind: SegBegin, Gen: gen.Current(), TurnID: "item_3", Text: second})
	p.Enqueue(Segment{Kind: SegAudio, Gen: gen.Current(), TurnID: "item_3", PCM: sec})
	p.Enqueue(Segment{Kind: SegMark, Gen: gen.Current(), TurnID: "item_3", Text: second})
	p.Enqueue(Segment{Kind: SegEnd, Gen: gen.Current(), TurnID: "item_3"})

	waitFor(t, "the answer to finish", 15*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return doneID != ""
	})

	mu.Lock()
	defer mu.Unlock()
	if heard["bc_1"] != 0 {
		t.Errorf("%d bytes of filler played inside the answer", heard["bc_1"])
	}
	// Two seconds were emitted and two seconds must be reported. Under-reporting
	// here is what made a truncation-free turn look like a cut one.
	if doneMS < 1900 {
		t.Errorf("output_audio_ms = %d, want ~2000; the turn's accounting was reset mid-answer", doneMS)
	}
	// And the spoken text must be whole, because this is what reaches history.
	if !strings.Contains(doneText, first) || !strings.Contains(doneText, second) {
		t.Errorf("spoken text = %q; history would lose what the assistant actually said, "+
			"and the model would repeat it", doneText)
	}
}

// TestBackchannelHoldDoesNotBlockSpeculation pins that a pending floor hold —
// created whenever the caller speaks over the assistant, which with a holding
// filler playing is most turns — does not stop the backend being started early.
//
// Deciding whether to yield the floor and deciding whether to guess at an
// answer are different questions, and a turn that waits for the final
// transcript pays the backend's full time-to-first-token. On a 3 s backend that
// is the difference between answering and being interrupted before answering.
func TestBackchannelHoldDoesNotBlockSpeculation(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.Speculative = true
	cfg.Duplex.SpeculativeStableMS = 200
	cfg.Duplex.SpeculativeMinChars = 4
	cfg.VAD.MinSilenceMS = 900
	cfg.Duplex.UserBackchannelPhrases = []string{"嗯", "啊", "好的"}
	cfg.Duplex.UserBackchannelHoldMS = 600
	c := newCollector(cfg.ClientRate)

	// A question that opens with a listed acknowledgement, which is exactly the
	// case the hold exists for — and must still be speculated on.
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	e := New(Options{
		Cfg:         cfg,
		SessionID:   "hold-vs-spec",
		ClientRate:  cfg.ClientRate,
		Delegation:  live.DelegationResponses,
		Speculative: true,
		Greeting:    "您好，我是车险管家小翠儿，看到您的爱车快到报价期了给您来电",
	}, Deps{
		ASR:  mock.NewASR("啊啊怎么了这是什么"),
		LLM:  llm,
		TTS:  mock.NewTTS(),
		Emit: c.emit,
	})
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)
	t.Cleanup(func() { e.Close(); cancel() })
	e.Greet()
	waitFor(t, "the greeting to hold the floor", 5*time.Second, func() bool {
		return c.audioMS() > 200
	})

	// Talk over it. A hold is created, because the assistant has the floor.
	speak(e, cfg.ClientRate, 1800)
	go pause(e, cfg.ClientRate, 2000)

	waitFor(t, "a speculative delegation", 3*time.Second, func() bool {
		for _, r := range c.delegationReasons() {
			if r == "speculative" {
				return true
			}
		}
		return false
	})
}

// TestSpeculationStillSkipsPlainAcknowledgements is the other half: the narrow
// condition that is genuinely not worth guessing on.
func TestSpeculationStillSkipsPlainAcknowledgements(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.SpeculativeMinChars = 2
	e := &Engine{}
	e.opts.Cfg = cfg
	e.opts.Speculative = true
	e.bcSet = NewPhraseSet([]string{"嗯", "好的"})
	e.asrRun = &asrRun{lastText: "嗯嗯"}
	e.watch = NewStabilityWatch(200*time.Millisecond, 2)

	e.maybeSpeculate()
	if e.specWhy == "" {
		t.Fatal("a pure acknowledgement was treated as worth speculating on")
	}
	if !strings.Contains(e.specWhy, "acknowledgement") {
		t.Errorf("specWhy = %q, want it to name the acknowledgement", e.specWhy)
	}
}

// TestFillerDoesNotRepeatItself covers the other thing that call made obvious.
//
// Independent random choice from three phrases repeats about a third of the
// time, and the call duly produced 让我查一下 on three turns running. A person
// filling a silence varies what they say; the same four syllables repeated is
// how a caller works out the machine is stuck.
func TestFillerDoesNotRepeatItself(t *testing.T) {
	e := &Engine{}
	phrases := []string{"我看一下", "稍等一下", "让我查一下"}
	prev := ""
	for i := 0; i < 200; i++ {
		got := e.pickPhrase(phrases, &e.lastFiller)
		if got == prev {
			t.Fatalf("phrase %q repeated on consecutive turns", got)
		}
		prev = got
	}

	// A single-phrase list has no choice, and must not spin looking for one.
	e2 := &Engine{}
	one := []string{"稍等"}
	for i := 0; i < 3; i++ {
		if got := e2.pickPhrase(one, &e2.lastFiller); got != "稍等" {
			t.Fatalf("single-phrase list returned %q", got)
		}
	}
	if got := e2.pickPhrase(nil, &e2.lastFiller); got != "" {
		t.Errorf("empty list returned %q", got)
	}
}

// TestOneFillerBetweenAnswers is the regression test for the worst thing a
// holding filler can do.
//
// When the backend is slower than the caller's patience, every turn is
// superseded before it speaks and the only thing the caller ever hears is the
// filler. A real call went "让我查一下" — question — "让我查一下" — question —
// "让我查一下", three turns deep, with no answer at any point. Each one was
// individually justified by its own turn running long; together they were an
// agent that appeared to have nothing to say but that.
//
// One filler is a reassurance that work is happening. The second, with no
// answer in between, is evidence that it is not.
func TestOneFillerBetweenAnswers(t *testing.T) {
	e := &Engine{}
	e.opts.Cfg.Duplex.HoldingFiller = true
	e.opts.Cfg.Duplex.HoldingFillerAfterMS = 1
	e.opts.Cfg.Duplex.HoldingFillerPhrases = []string{"我看一下", "稍等一下"}

	// Nothing heard yet: a filler is allowed.
	if e.fillerSinceAnswer != 0 {
		t.Fatal("a fresh session already owes the caller an answer")
	}
	e.fillerSinceAnswer++ // as maybeHoldingFiller does when it speaks one

	// The turn is then superseded without speaking, and the next slow turn must
	// not repeat the promise.
	if e.fillerSinceAnswer == 0 {
		t.Fatal("a spoken filler was not counted")
	}

	// An answer reaching the caller clears it.
	turn := &Turn{ID: "item_9"}
	turn.MarkFirstAudio()
	e.delegatedTurn = turn
	e.delegatedAt = time.Now().Add(-time.Second)
	e.observeTTFA()
	if e.fillerSinceAnswer != 0 {
		t.Error("an answer was heard but the filler budget was not restored")
	}
	// And the observation feeds the lateness threshold, so a session that never
	// answers never learns what normal looks like — which is why the floor
	// still applies.
	if e.ttfa.N() != 1 {
		t.Errorf("ttfa observations = %d, want 1", e.ttfa.N())
	}
}

// TestHistoryIsBoundedBySizeNotJustTurns covers the largest single term in the
// cascade.
//
// Two logs from the same machine, model and endpoint: a 79-character system
// prompt gave a time-to-first-token of 259–546 ms, and a long persona prompt at
// the same history_turns gave 2441–3385 ms. Prompt size is what the backend
// charges for, and history_turns does not constrain it — sixteen exchanges can
// be four hundred characters or four thousand.
func TestHistoryIsBoundedBySizeNotJustTurns(t *testing.T) {
	e := &Engine{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	e.opts.Cfg.HistoryTurns = 16
	e.opts.Cfg.HistoryMaxChars = 100

	long := strings.Repeat("字", 60)
	for i := 0; i < 8; i++ {
		e.history = append(e.history,
			provider.Message{Role: provider.RoleUser, Content: "多少钱"},
			provider.Message{Role: provider.RoleAssistant, Content: long})
	}
	// Sixteen messages is inside history_turns, so that cap alone changes
	// nothing — which is the point.
	before := len(e.history)
	e.trimHistory()
	if len(e.history) >= before {
		t.Fatalf("history stayed at %d messages; the size limit did nothing", len(e.history))
	}

	total := 0
	for _, m := range e.history {
		total += len([]rune(m.Content))
	}
	if total > e.opts.Cfg.HistoryMaxChars {
		t.Errorf("history is %d characters, limit is %d", total, e.opts.Cfg.HistoryMaxChars)
	}
	// The newest exchange must survive: dropping that would leave the model
	// answering without the question.
	if last := e.history[len(e.history)-1]; last.Content != long {
		t.Error("trimming dropped from the wrong end; the newest turn must be kept")
	}

	// Zero disables it, for anyone who would rather pay the latency.
	e2 := &Engine{log: e.log}
	e2.opts.Cfg.HistoryTurns = 16
	e2.opts.Cfg.HistoryMaxChars = 0
	e2.history = append([]provider.Message(nil), e.history...)
	e2.history = append(e2.history, provider.Message{Role: provider.RoleUser, Content: long})
	n := len(e2.history)
	e2.trimHistory()
	if len(e2.history) != n {
		t.Errorf("history_max_chars: 0 trimmed anyway (%d -> %d)", n, len(e2.history))
	}
}

// TestNoiseDoesNotCostTheAssistantItsTurn is the regression test for a greeting
// that played half and stopped.
//
// A real call cut the greeting 768 ms in, to an utterance that produced no
// transcript at all: a speech.stopped with no delegation behind it, and an
// empty row in the transcript. The VAD heard something; the recognizer found no
// words in it. A cough, a door, or the assistant's own voice returning through
// a speaker — barge_in_min_speech_ms had already let it through, because
// acoustics cannot tell a door closing from a syllable.
//
// The transcript can tell them apart, and deferring the interrupt is what makes
// that usable: an interrupt taken the moment the VAD opens cannot be given back
// once the recognizer reports silence.
func TestNoiseDoesNotCostTheAssistantItsTurn(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.AllowBargeIn = true
	cfg.Duplex.OnNewQuery = "cut"
	cfg.Duplex.Speculative = false
	// Deliberately empty: this has nothing to do with acknowledgements, and
	// must work for a deployment that configured no phrase list at all.
	cfg.Duplex.UserBackchannelPhrases = nil
	cfg.Duplex.UserBackchannelHoldMS = 600

	c := newCollector(cfg.ClientRate)
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	e := New(Options{
		Cfg:        cfg,
		SessionID:  "noise",
		ClientRate: cfg.ClientRate,
		Delegation: live.DelegationResponses,
		Greeting:   "您好，我是众安保险的车险管家小翠儿，看到您的爱车快到报价期了给您来电做个最低的报价",
	}, Deps{
		ASR:  silentASR{}, // hears the noise, finds no words in it
		LLM:  llm,
		TTS:  mock.NewTTS(),
		Emit: c.emit,
	})
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)
	t.Cleanup(func() { e.Close(); cancel() })
	e.Greet()

	waitFor(t, "the greeting to start", 5*time.Second, func() bool {
		return c.audioMS() > 250
	})
	before := c.audioMS()

	// A noise loud enough to open the VAD, which the recognizer cannot
	// transcribe.
	speak(e, cfg.ClientRate, 500)
	pause(e, cfg.ClientRate, 800)

	// The greeting must still be going.
	waitFor(t, "the greeting to keep playing through the noise", 6*time.Second, func() bool {
		return c.audioMS() > before+400
	})
	if n := c.count(live.ExtAudioTruncated); n != 0 {
		t.Errorf("the greeting was truncated %d time(s) by a noise that produced no words", n)
	}
	// And nothing the caller never said should appear as a turn.
	if n := c.count(live.ServerDelegationCreated); n != 0 {
		t.Errorf("%d delegation(s) for an utterance with no transcript", n)
	}
}

func TestIsBackchannel(t *testing.T) {
	for id, want := range map[string]bool{
		"bc_1": true, "bc_42": true,
		"item_1": false, "item_12": false, "": false,
	} {
		if got := IsBackchannel(id); got != want {
			t.Errorf("IsBackchannel(%q) = %v, want %v", id, got, want)
		}
	}
}

// TestEnginePrewarmsBeforeTheCriticalPath pins the two moments the engine
// pays for handshakes.
//
// This is a latency contract with no functional symptom: drop the prewarm calls
// and every other test still passes, because the connections are made anyway —
// just later, with the caller waiting. The only way it shows up is as a first
// turn that is a few hundred milliseconds worse than the rest, which is exactly
// the kind of regression that survives for months.
func TestEnginePrewarmsBeforeTheCriticalPath(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.Speculative = false
	c := newCollector(cfg.ClientRate)

	tts := mock.NewTTS()
	e := New(Options{
		Cfg:        cfg,
		SessionID:  "prewarm",
		ClientRate: cfg.ClientRate,
		Delegation: live.DelegationResponses,
	}, Deps{
		ASR:  mock.NewASR("你好帮我查一下明天的天气"),
		LLM:  mock.NewLLM(),
		TTS:  tts,
		Emit: c.emit,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)
	defer e.Close()

	waitFor(t, "a prewarm at session open", 2*time.Second, func() bool {
		return tts.Prewarms() > 0
	})
	atOpen := tts.Prewarms()

	// The microphone opening is a second or more of warning that a reply is
	// coming, and the moment an idle-dropped connection must be rebuilt.
	speak(e, cfg.ClientRate, 700)
	waitFor(t, "a prewarm when the utterance opens", 2*time.Second, func() bool {
		return tts.Prewarms() > atOpen
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

// mutedTTS imitates the provider outcome behind `speak: segment synthesized …
// audio_ms=0 ms=0`, which a real session log showed twice: Synthesize returns
// successfully and its channel closes without a single frame of audio.
//
// MiniMax does exactly this. readAudio's first act is to check the context, and
// on a cancelled one it abandons the task and returns — no chunk, no error, an
// empty closed channel. The engine's chunk loop then never runs, which is the
// trap: the supersession check lives inside the loop body, so a segment with no
// audio skipped it and fell through to the closing mark.
//
// The mark is what puts the sentence into conversation history. An answer whose
// later segments were silent therefore told the model it had said things the
// caller never heard, and the next turn was generated as a continuation of
// them. That is what a caller hears as an answer arriving in pieces.
type mutedTTS struct {
	mu   sync.Mutex
	rate int
	segs int
}

func (m *mutedTTS) Name() string { return "muted" }

func (m *mutedTTS) Open(ctx context.Context, opts provider.TTSOptions) (provider.TTSStream, error) {
	rate := opts.SampleRate
	if rate <= 0 {
		rate = provider.PipelineRate
	}
	m.mu.Lock()
	m.rate = rate
	m.mu.Unlock()
	return m, nil
}

func (m *mutedTTS) SampleRate() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rate <= 0 {
		return provider.PipelineRate
	}
	return m.rate
}

func (m *mutedTTS) Close() error { return nil }

func (m *mutedTTS) Synthesize(ctx context.Context, text string) (<-chan provider.TTSChunk, error) {
	m.mu.Lock()
	m.segs++
	first := m.segs == 1
	rate := m.rate
	m.mu.Unlock()

	out := make(chan provider.TTSChunk, 4)
	go func() {
		defer close(out)
		// The first segment is audible so the failure is partial, which is what
		// the log shows: the caller hears the opening and nothing after it.
		if !first {
			return
		}
		select {
		case out <- provider.TTSChunk{PCM: make([]byte, rate)}: // one second
		case <-ctx.Done():
		}
	}()
	return out, nil
}

// TestSilentSegmentsDoNotEnterHistory is the regression test for that fall-through.
func TestSilentSegmentsDoNotEnterHistory(t *testing.T) {
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)
	tts := &mutedTTS{}
	e := New(Options{
		Cfg:        cfg,
		SessionID:  "muted",
		ClientRate: cfg.ClientRate,
		Delegation: live.DelegationResponses,
	}, Deps{
		ASR:  mock.NewASR("明天的天气"),
		LLM:  mock.NewLLM(),
		TTS:  tts,
		Emit: c.emit,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)
	defer e.Close()

	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)

	waitFor(t, "every segment to be attempted", 8*time.Second, func() bool {
		tts.mu.Lock()
		defer tts.mu.Unlock()
		return tts.segs >= 2
	})
	// Let the player drain and the turn be recorded.
	waitFor(t, "the answer to finish", 8*time.Second, func() bool {
		return !e.player.Active()
	})
	done := make(chan struct{})
	e.post(func() { close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("engine loop did not drain")
	}

	reply := mock.Reply("明天的天气")
	audible := []rune(reply)
	var said string
	for _, m := range e.history {
		if m.Role == provider.RoleAssistant {
			said += m.Content
		}
	}
	// Whatever was audible is a prefix of the reply; the point is that history
	// must not run past the audio. One second of speech cannot cover a reply
	// this long, so a history entry holding all of it is the bug.
	if len([]rune(said)) >= len(audible) {
		t.Errorf("history records %d characters of a %d-character reply, but only the first "+
			"segment produced audio; the model would answer sentences the caller never heard\n  history: %q",
			len([]rune(said)), len(audible), said)
	}
}

// TestAbandonedSegmentIsNotReportedAsSpoken pins the player half of the silent
// segment fix at the level where it matters: what OnTurnDone hands to history.
//
// The last segment is the one that catches a regression, because pendingText is
// only counted when no mark follows it.
func TestAbandonedSegmentIsNotReportedAsSpoken(t *testing.T) {
	var mu sync.Mutex
	var doneID, doneText string

	p := NewPlayer(PlayerConfig{Rate: 24000, ChunkMS: 40, Paced: true, LeadMS: 40},
		func(id string, pcm []byte) {})
	p.OnTurnDone(func(id string, ms int64, text string) {
		mu.Lock()
		doneID, doneText = id, text
		mu.Unlock()
	})

	var gen Generation
	go p.Run(&gen)
	defer p.Close()

	heard, silent := "费用看方案。", "您先看下微信服务通知。"
	sec := make([]byte, 2*24000)

	p.Enqueue(Segment{Kind: SegBegin, Gen: gen.Current(), TurnID: "item_3", Text: heard})
	p.Enqueue(Segment{Kind: SegAudio, Gen: gen.Current(), TurnID: "item_3", PCM: sec})
	p.Enqueue(Segment{Kind: SegMark, Gen: gen.Current(), TurnID: "item_3", Text: heard})

	// Announced, then synthesized to nothing.
	p.Enqueue(Segment{Kind: SegBegin, Gen: gen.Current(), TurnID: "item_3", Text: silent})
	p.Enqueue(Segment{Kind: SegAbandon, Gen: gen.Current(), TurnID: "item_3"})
	p.Enqueue(Segment{Kind: SegEnd, Gen: gen.Current(), TurnID: "item_3"})

	waitFor(t, "the answer to finish", 15*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return doneID != ""
	})

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(doneText, heard) {
		t.Errorf("spoken text = %q; it must keep the segment that did play", doneText)
	}
	if strings.Contains(doneText, silent) {
		t.Errorf("spoken text = %q; it contains a sentence that produced no audio, so the "+
			"model would carry on from something the caller never heard", doneText)
	}
}

// audioRuns returns the turn ids of the audio deltas in the order they went
// out, collapsed to runs. ["item_1","item_2"] is one answer then the next;
// ["item_1","item_2","item_1"] is two answers interleaved.
func (c *collector) audioRuns() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, ev := range c.events {
		d, ok := ev.(live.OutputAudioDelta)
		if !ok {
			continue
		}
		if len(out) == 0 || out[len(out)-1] != d.ItemID {
			out = append(out, d.ItemID)
		}
	}
	return out
}

// TestOnNewQueryQueueDoesNotInterleave covers the policy the page describes as
// "say everything first".
//
// The worry is structural: the player holds one FIFO queue and one active turn,
// and under queue nothing stops the interrupted answer — so two pipes can be
// pushing into the same queue at once. If the second answer's segments landed
// between the first answer's, the two would interleave, and every turn switch
// runs ensureTurnLocked, which resets the per-turn accounting and drops the
// spoken spans of whichever answer was mid-flight.
//
// It holds up, including with a provider that serializes synthesis on one
// connection the way a real one does (TestOnNewQueryQueueSaysEverything), and
// this pins it.
func TestOnNewQueryQueueDoesNotInterleave(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.OnNewQuery = "queue"
	cfg.Duplex.Speculative = false
	c := newCollector(cfg.ClientRate)
	e, _ := newTestEngine(t, cfg, c)

	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "the first answer to be speaking", 6*time.Second, func() bool {
		return len(c.audioRuns()) > 0
	})

	// Ask something else while it is still talking.
	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "the second answer", 10*time.Second, func() bool {
		return len(c.audioRuns()) > 1
	})
	// Let both run to completion.
	waitFor(t, "the floor to clear", 20*time.Second, func() bool {
		return !e.player.Active()
	})

	runs := c.audioRuns()
	seen := map[string]bool{}
	for _, id := range runs {
		if seen[id] {
			t.Fatalf("audio order %v: %s came back after another answer had started; "+
				"queue is supposed to finish one answer before beginning the next", runs, id)
		}
		seen[id] = true
	}
}

// serialTTS models the one property of a real vendor stream that the mock does
// not have: a single persistent connection that serializes synthesis, so two
// turns speaking at once have to take turns through the same lock. It is the
// shape in which "say everything first" is most likely to come apart.
type serialTTS struct {
	mu    sync.Mutex
	rate  int
	delay time.Duration

	cmu   sync.Mutex
	calls []string
}

func (s *serialTTS) Name() string { return "serial" }

func (s *serialTTS) Open(ctx context.Context, opts provider.TTSOptions) (provider.TTSStream, error) {
	rate := opts.SampleRate
	if rate <= 0 {
		rate = provider.PipelineRate
	}
	s.rate = rate
	return s, nil
}

func (s *serialTTS) SampleRate() int { return s.rate }
func (s *serialTTS) Close() error    { return nil }

func (s *serialTTS) spoken() []string {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *serialTTS) Synthesize(ctx context.Context, text string) (<-chan provider.TTSChunk, error) {
	s.cmu.Lock()
	s.calls = append(s.calls, text)
	s.cmu.Unlock()

	out := make(chan provider.TTSChunk, 4)
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		defer close(out)
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return
		}
		select {
		case out <- provider.TTSChunk{PCM: make([]byte, s.rate/2)}:
		case <-ctx.Done():
		}
	}()
	return out, nil
}

// TestOnNewQueryQueueSaysEverything is the other half of the promise, and the
// half that is easy to lose: not just that the answers do not interleave, but
// that the interrupted one is finished rather than abandoned.
//
// newSpeechPipe aborts the previous pipe unconditionally, so if a turn's text
// were still being synthesized when the next turn began, the rest of it would
// never be spoken — and, because no generation is bumped and no truncation is
// reported under queue, conversation history would still record the whole
// answer as said. Synthesis here is slow enough that the first answer is still
// in the provider when the second question arrives.
func TestOnNewQueryQueueSaysEverything(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.OnNewQuery = "queue"
	cfg.Duplex.Speculative = false
	c := newCollector(cfg.ClientRate)
	tts := &serialTTS{delay: 300 * time.Millisecond}
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	e := New(Options{
		Cfg:        cfg,
		SessionID:  "queue",
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
	waitFor(t, "the first answer to be speaking", 8*time.Second, func() bool {
		return len(c.audioRuns()) > 0
	})
	speak(e, cfg.ClientRate, 1200)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "the second answer", 15*time.Second, func() bool {
		return len(c.audioRuns()) > 1
	})
	waitFor(t, "the floor to clear", 30*time.Second, func() bool { return !e.player.Active() })

	// Every segment of the first answer reached the provider, and what the
	// model generated is what the caller was given.
	said := strings.Join(tts.spoken(), "")
	first := c.outputTranscript("item_1")
	if first == "" {
		t.Fatal("the first answer produced no transcript at all")
	}
	if !strings.Contains(said, first) {
		t.Errorf("the first answer was abandoned when the second question arrived.\n"+
			"  generated: %q\n  synthesized: %q", first, said)
	}
}
