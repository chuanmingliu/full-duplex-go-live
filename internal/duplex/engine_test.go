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
		t.Skip("the recognizer never stabilised before the utterance closed; speculation is opportunistic")
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
