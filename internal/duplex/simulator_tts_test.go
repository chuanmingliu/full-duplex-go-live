package duplex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
)

// The failing provider deliberately includes sensitive-looking content: none of
// its raw message belongs in the simulator's structured warning/report.
var simulatorTTSError = errors.New("vendor rejected SECRET_TOKEN: private sentence")

type recoveringSimulatorTTS struct {
	mode string
	fail atomic.Bool
}

func (*recoveringSimulatorTTS) Name() string { return "recovering" }
func (p *recoveringSimulatorTTS) Open(_ context.Context, opts provider.TTSOptions) (provider.TTSStream, error) {
	if p.mode == "open" && p.fail.Load() {
		return nil, simulatorTTSError
	}
	return &recoveringSimulatorStream{owner: p, rate: opts.SampleRate}, nil
}

type recoveringSimulatorStream struct {
	owner  *recoveringSimulatorTTS
	rate   int
	broken bool
}

func (s *recoveringSimulatorStream) SampleRate() int { return s.rate }
func (s *recoveringSimulatorStream) Close() error    { return nil }
func (s *recoveringSimulatorStream) Synthesize(_ context.Context, text string) (<-chan provider.TTSChunk, error) {
	if s.broken {
		return nil, simulatorTTSError
	}
	failing := strings.Contains(text, "Broken") && s.owner.fail.Load()
	if failing && s.owner.mode == "synthesis" {
		s.broken = true
		return nil, simulatorTTSError
	}
	chunks := make(chan provider.TTSChunk, 2)
	if !failing || s.owner.mode == "partial" {
		chunks <- provider.TTSChunk{PCM: voiced(s.rate, 40, .4)}
	}
	if failing && s.owner.mode != "empty" {
		s.broken = true
		chunks <- provider.TTSChunk{Err: simulatorTTSError}
	}
	close(chunks)
	return chunks, nil
}

func newRecoveringSimulator(t *testing.T, mode string, enabled bool) (*Engine, *collector, *recoveringSimulatorTTS) {
	t.Helper()
	cfg := testConfig()
	cfg.Duplex.PlaybackPaced = false
	cfg.Duplex.StreamFirstChunkChars = 1
	cfg.Duplex.StreamMinChunkChars = 1
	cfg.Duplex.StreamMaxChunkChars = 1000
	c := newCollector(cfg.ClientRate)
	provider := &recoveringSimulatorTTS{mode: mode}
	provider.fail.Store(true)
	e := New(Options{Cfg: cfg, ClientRate: cfg.ClientRate, SimulatorEnabled: enabled}, Deps{TTS: provider, Emit: c.emit, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.Start(context.Background())
	t.Cleanup(e.Close)
	return e, c, provider
}

func simulatorSay(t *testing.T, e *Engine, text string) *Turn {
	t.Helper()
	var turn *Turn
	var pipe *speechPipe
	simulatorLoop(t, e, func() {
		turn = e.tracker.Begin("question", false)
		turn.commit(e.deps.Emit)
		pipe = e.newSpeechPipe(turn, false)
	})
	pipe.Push(text)
	pipe.Close()
	select {
	case <-pipe.done:
	case <-time.After(2 * time.Second):
		t.Fatal("speech pipe stalled")
	}
	return turn
}

func simulatorTTSWarnings(c *collector) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, ev := range c.events {
		b, _ := json.Marshal(ev)
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		if v["type"] == "simulator.warning" {
			out = append(out, v)
		}
	}
	return out
}

func TestSimulatorTTSFailureDropsOnlyTheSegment(t *testing.T) {
	for _, mode := range []string{"chunk", "partial", "synthesis", "empty"} {
		t.Run(mode, func(t *testing.T) {
			e, c, p := newRecoveringSimulator(t, mode, true)
			turn := simulatorSay(t, e, "Broken sentence. Healthy sentence.")
			waitFor(t, "healthy segment after failure", time.Second, func() bool {
				var heard string
				simulatorLoop(t, e, func() {
					for _, m := range e.history {
						if m.Role == provider.RoleAssistant {
							heard += m.Content
						}
					}
				})
				return strings.Contains(heard, "Healthy sentence.")
			})
			simulatorLoop(t, e, func() {
				for _, m := range e.history {
					if m.Role == provider.RoleAssistant && strings.Contains(m.Content, "Broken") {
						t.Error("incomplete segment was recorded as spoken")
					}
				}
			})
			if e.ctx.Err() != nil {
				t.Fatal("provider failure ended engine")
			}
			if c.count(live.ServerError) != 0 {
				t.Fatal("recoverable provider failure emitted fatal-protocol error")
			}
			warnings := simulatorTTSWarnings(c)
			if len(warnings) != 1 {
				t.Fatalf("warning count=%d, want 1", len(warnings))
			}
			wantCode := map[string]string{"chunk": "tts_chunk_failed", "partial": "tts_chunk_failed", "synthesis": "tts_synthesis_failed", "empty": "tts_empty_segment"}[mode]
			if warnings[0]["code"] != wantCode {
				t.Fatalf("warning code=%v, want %s", warnings[0]["code"], wantCode)
			}
			b, _ := json.Marshal(warnings)
			if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "private sentence") {
				t.Fatal("raw provider content escaped into warning")
			}
			if warnings[0]["turn_id"] != turn.ID || warnings[0]["segment_id"] != float64(1) {
				t.Fatalf("warning did not identify failed segment: %s", b)
			}
			p.fail.Store(false)
			later := simulatorSay(t, e, "Later turn.")
			waitFor(t, "subsequent turn completes", time.Second, func() bool { return !later.Timings().CompletedAt.IsZero() })
		})
	}
}

func TestSimulatorTTSOpenFailureKeepsTheNextTurnAlive(t *testing.T) {
	e, c, p := newRecoveringSimulator(t, "open", true)
	simulatorSay(t, e, "Broken sentence.")
	if c.count(live.ServerError) != 0 {
		t.Fatal("TTS dial failure emitted fatal-protocol error")
	}
	if len(simulatorTTSWarnings(c)) != 1 {
		t.Fatal("missing warning for failed synthesis connection")
	}
	p.fail.Store(false)
	turn := simulatorSay(t, e, "Later turn.")
	waitFor(t, "subsequent turn completes", time.Second, func() bool { return !turn.Timings().CompletedAt.IsZero() })
}

func TestOrdinaryTTSFailureKeepsExistingErrorBehavior(t *testing.T) {
	e, c, _ := newRecoveringSimulator(t, "synthesis", false)
	simulatorSay(t, e, "Broken sentence. Healthy sentence.")
	if c.count(live.ServerError) != 1 {
		t.Fatal("ordinary session lost its TTS error event")
	}
	if len(simulatorTTSWarnings(c)) != 0 {
		t.Fatal("ordinary session emitted simulator warning")
	}
	if c.audioMS() != 0 {
		t.Fatal("ordinary session behavior changed after failed segment")
	}
}

func TestSimulatorTTSPartialFinalSegmentIsNotCompletedText(t *testing.T) {
	e, c, _ := newRecoveringSimulator(t, "partial", true)
	turn := simulatorSay(t, e, "Broken sentence.")
	waitFor(t, "partial audio drains", time.Second, func() bool { return !turn.Timings().CompletedAt.IsZero() })
	if c.audioMS() != 40 {
		t.Fatalf("real partial audio evidence = %vms, want 40", c.audioMS())
	}
	simulatorLoop(t, e, func() {
		for _, m := range e.history {
			if m.Role == provider.RoleAssistant {
				t.Fatalf("failed partial segment claimed complete text: %q", m.Content)
			}
		}
	})
}

func TestSimulatorTTSConsecutiveFailuresKeepLaterSegments(t *testing.T) {
	e, c, _ := newRecoveringSimulator(t, "chunk", true)
	turn := simulatorSay(t, e, "Broken one. Broken two. Broken three. Healthy sentence.")
	waitFor(t, "response completes after three failed segments", time.Second, func() bool { return !turn.Timings().CompletedAt.IsZero() })
	warnings := simulatorTTSWarnings(c)
	if len(warnings) != 3 {
		t.Fatalf("warnings=%d, want 3", len(warnings))
	}
	for i, w := range warnings {
		if w["segment_id"] != float64(i+1) {
			t.Fatalf("warning segment id = %v, want %d", w["segment_id"], i+1)
		}
	}
	simulatorLoop(t, e, func() {
		var said string
		for _, m := range e.history {
			if m.Role == provider.RoleAssistant {
				said += m.Content
			}
		}
		if said != "Healthy sentence." {
			t.Fatalf("spoken history=%q", said)
		}
	})
}

type cancelledSimulatorTTS struct{ entered chan struct{} }

func (*cancelledSimulatorTTS) Name() string { return "cancelled" }
func (p *cancelledSimulatorTTS) Open(context.Context, provider.TTSOptions) (provider.TTSStream, error) {
	return p, nil
}
func (*cancelledSimulatorTTS) SampleRate() int { return 24000 }
func (*cancelledSimulatorTTS) Close() error    { return nil }
func (p *cancelledSimulatorTTS) Synthesize(ctx context.Context, _ string) (<-chan provider.TTSChunk, error) {
	close(p.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSimulatorTTSCancelIsNotAProviderWarning(t *testing.T) {
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)
	tts := &cancelledSimulatorTTS{entered: make(chan struct{})}
	e := New(Options{Cfg: cfg, ClientRate: cfg.ClientRate, SimulatorEnabled: true}, Deps{TTS: tts, Emit: c.emit, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.Start(context.Background())
	t.Cleanup(e.Close)
	var pipe *speechPipe
	simulatorLoop(t, e, func() {
		turn := e.tracker.Begin("question", false)
		turn.commit(e.deps.Emit)
		pipe = e.newSpeechPipe(turn, false)
	})
	pipe.Push("A sentence.")
	pipe.Close()
	select {
	case <-tts.entered:
	case <-time.After(time.Second):
		t.Fatal("synthesis did not start")
	}
	pipe.Abort()
	select {
	case <-pipe.done:
	case <-time.After(time.Second):
		t.Fatal("cancelled synthesis stalled")
	}
	if c.count(live.ServerError) != 0 || len(simulatorTTSWarnings(c)) != 0 {
		t.Fatal("interruption became a provider failure")
	}
	if c.audioMS() != 0 {
		t.Fatal("cancelled synthesis emitted audio")
	}
}
