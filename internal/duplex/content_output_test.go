package duplex

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

// Drive the player separately from synthesis: the opener has gone out, and
// the answer is ready in its queue when the caller either interrupts or waits.
// Readiness alone must never establish an observed content output.
func TestContentPresenceRequiresPlayback(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, interrupted := range []bool{false, true} {
			name := "synthesized"
			if cached {
				name = "cached"
			}
			if interrupted {
				name += "/interrupted"
			} else {
				name += "/completed"
			}
			t.Run(name, func(t *testing.T) {
				cfg := testConfig()
				cfg.Duplex.PhraseCache = false
				cfg.Duplex.PlaybackPaced = false
				var logs bytes.Buffer
				c := newCollector(cfg.ClientRate)
				e := New(Options{Cfg: cfg}, Deps{
					TTS: mock.NewTTS(), Emit: c.emit,
					Log: slog.New(slog.NewJSONHandler(&logs, nil)),
				})
				e.ctx = context.Background()
				turn := e.tracker.Begin("question", false)
				turn.commit(c.emit)
				turn.MarkSpeechEnd(time.Now(), 0, 380)
				turn.Opened, turn.OpenedMS = "ack", 20
				pcm := audio.EncodePCM16([]float32{.1, .2, .1, .2})
				opener := Segment{Gen: turn.Generation, TurnID: turn.ID, Text: turn.Opened, PCM: pcm}
				e.player.beginSegment(opener)
				e.player.playAudio(&e.gen, opener)
				e.player.markSegment(opener)
				pipe := &speechPipe{e: e, turn: turn, ctx: e.ctx}
				var ok bool
				if cached {
					ok = pipe.speakCached("answer", PhraseAudio{PCM: pcm, Rate: cfg.ClientRate})
				} else {
					ok = pipe.speak("answer")
				}
				if !ok || turn.Timings().FirstContent.IsZero() {
					t.Fatal("answer was not synthesized and queued")
				}
				if got := c.allOutput(); got != turn.Opened {
					t.Fatalf("content escaped the queue before playback: %q", got)
				}
				if interrupted {
					e.player.OnTruncate(func(TruncationReport) {})
					e.gen.Bump()
					e.player.Interrupt()
				} else {
					e.player.OnTurnDone(func(string, int64, string) { e.player.Close() })
					e.player.Enqueue(Segment{Kind: SegEnd, Gen: turn.Generation, TurnID: turn.ID})
					e.player.Run(&e.gen)
					if !strings.Contains(c.allOutput(), "answer") {
						t.Fatal("the queued content was not emitted")
					}
				}
				e.emitMetrics(turn, 20, interrupted)
				var fields map[string]any
				decoder := json.NewDecoder(&logs)
				for decoder.More() {
					var row map[string]any
					if err := decoder.Decode(&row); err != nil {
						t.Fatal(err)
					}
					if row["msg"] == "turn: response latency" {
						fields = row
					}
				}
				if fields["first_audio_present"] != true || fields["first_content_present"] != !interrupted {
					t.Fatalf("incorrect output presence after interruption=%t: audio=%v, content=%v", interrupted,
						fields["first_audio_present"], fields["first_content_present"])
				}
				if interrupted && fields["first_content_ms"] != float64(0) {
					t.Fatalf("unemitted content has a latency: %v", fields["first_content_ms"])
				}
				tm := turn.Timings()
				if !interrupted {
					if tm.FirstContentOut.IsZero() || tm.FirstContentOut.Before(tm.FirstContent) {
						t.Fatalf("content output did not follow its readiness: %+v", tm)
					}
					if want := float64(tm.FirstContentOut.Sub(tm.SpeechEndAt).Milliseconds()); fields["first_content_ms"] != want {
						t.Fatalf("content latency = %v, want output boundary %v", fields["first_content_ms"], want)
					}
				}
				if e.ttsStream != nil {
					_ = e.ttsStream.Close()
				}
			})
		}
	}
}

func TestRevisedOpenerDoesNotInheritContentOutput(t *testing.T) {
	var gen Generation
	tracker := NewTracker(&gen)
	previous := tracker.Begin("question", false)
	previous.Opened = "ack"
	previous.MarkFirstAudioOut()
	previous.MarkFirstContentOut()
	revised := tracker.Revise(previous.ID, "revised question", false)
	revised.InheritOpener(previous)
	if revised.Timings().FirstAudioOut.IsZero() {
		t.Fatal("the revised turn lost the opener's output")
	}
	if !revised.Timings().FirstContentOut.IsZero() {
		t.Fatal("the revised answer inherited another revision's content output")
	}
}
