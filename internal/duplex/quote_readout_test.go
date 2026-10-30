package duplex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/provider/mock"
	"github.com/chuanmingliu/golive/internal/tools"
)

func TestReadyQuoteSkipsOnlyRedundantDefaultPosthook(t *testing.T) {
	e, _, task, p := readyQuoteHandoff(t)
	qs, err := tools.ParseQuoteSpeech([]byte(tools.DefaultQuoteSpeech))
	if err != nil {
		t.Fatal(err)
	}
	task.Tool = task.Tool.WithSpeech(qs)
	task.Result.Output = []byte(`{"status":"quote","total":100,"lines":[]}`)
	// The same caller's opener really completed before this result arrived.
	p.turn.MarkFirstAudioOut()
	p.turn.markCompletedNaturally()
	e.tools.deliveries = []taskDelivery{{task: task, origin: e.tools.origins[task.ID]}}
	delete(e.tools.ackPending, task.ID)
	e.maybeDeliver()
	result := e.tracker.Current()
	if result.Opening != "" {
		t.Fatalf("redundant default posthook delayed the figures: %q", result.Opening)
	}
	if result.Readout == "" || !result.QuoteResult {
		t.Fatal("skipping the posthook lost the substantive readout")
	}
}

func TestDefaultPosthookNeedsAnUninterruptedSameOriginOpener(t *testing.T) {
	for _, guard := range []string{"eligible", "custom", "unheard", "unfinished", "interrupted", "different_origin", "stale", "busy", "no_opener", "no_readout", "failed", "not_quote"} {
		t.Run(guard, func(t *testing.T) {
			e, _, task, p := readyQuoteHandoff(t)
			qs, err := tools.ParseQuoteSpeech([]byte(tools.DefaultQuoteSpeech))
			if err != nil {
				t.Fatal(err)
			}
			task.Tool = task.Tool.WithSpeech(qs)
			task.Result.Output = []byte(`{"status":"quote","total":100,"lines":[]}`)
			p.turn.MarkFirstAudioOut()
			p.turn.markCompletedNaturally()
			delivery := taskDelivery{task: task, origin: e.tools.origins[task.ID]}
			busy := false
			switch guard {
			case "custom":
				task.Tool.ResultOpening = "Custom terms apply."
			case "unheard":
				p.turn.firstAudioOut = time.Time{}
			case "unfinished":
				p.turn.completedAt = time.Time{}
			case "interrupted":
				e.gen.Bump()
			case "different_origin":
				delivery.origin.turnID = "different"
			case "stale":
				task.Request.MemoryVersion = 1
			case "busy":
				busy = true
			case "no_opener":
				p.turn.Opened = ""
			case "no_readout":
				task.Tool.Speech = nil
			case "failed":
				task.Result.Err = errors.New("lookup failed")
			case "not_quote":
				task.Result.Output = []byte(`{"status":"need"}`)
			}
			opening, readout, skipped := e.quoteResultPrefix(delivery, 0, busy)
			if guard == "eligible" {
				if opening != "" || readout == "" || !skipped {
					t.Fatal("eligible result retained the redundant posthook")
				}
			} else if skipped || (guard != "stale" && guard != "failed" && opening != task.Tool.OpeningFor(task.Result.Output)) {
				t.Fatalf("posthook changed across %s guard", guard)
			}
		})
	}
}

func TestQuoteWithoutPosthookGetsFirstSegmentUrgency(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.PhraseCache = false
	cfg.Duplex.StreamMaxChunkChars = 20
	tts := &urgencyTTS{inner: mock.NewTTS()}
	c := newCollector(cfg.ClientRate)
	e := New(Options{Cfg: cfg}, Deps{TTS: tts, Emit: c.emit})
	e.ctx = context.Background()
	t.Cleanup(e.Close)
	turn := e.tracker.Begin("", false)
	e.commitTurn(turn)
	turn.Readout = "First coverage 100 dollars. Second coverage 200 dollars."
	turn.QuoteResult = true
	pipe := e.newSpeechPipe(turn, false)
	pipe.PushFixed(turn.Readout, true)
	pipe.Push("Anything else?")
	pipe.Close()
	awaitHandoffSignal(t, pipe.done)
	tts.mu.Lock()
	marks := append([]bool(nil), tts.marks...)
	tts.mu.Unlock()
	if len(marks) < 3 || !marks[0] {
		t.Fatalf("readout did not receive first-segment urgency: %v", marks)
	}
	for _, urgent := range marks[1:] {
		if urgent {
			t.Fatalf("later readout/model segments received first-segment urgency: %v", marks)
		}
	}
	// Every readout segment carries its role independently of the pipe's
	// later model text. Invoke each queued output observer in isolation.
	inReadout := false
	readoutSegments := 0
	for _, seg := range e.player.queue {
		if seg.Kind == SegBegin {
			inReadout = seg.Text != "Anything else?"
			if inReadout {
				readoutSegments++
			}
		}
		if seg.Kind != SegAudio || seg.onFirstAudio == nil {
			continue
		}
		turn.firstReadoutOut = time.Time{}
		seg.onFirstAudio()
		if present := !turn.Timings().FirstReadoutOut.IsZero(); present != inReadout {
			t.Fatal("queued audio lost its readout role after the pipe advanced")
		}
	}
	if readoutSegments < 2 {
		t.Fatal("test did not exercise a multi-segment readout")
	}
}

func TestQuoteReadoutPresenceRequiresItsOwnAudioOutput(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, cancelled := range []bool{false, true} {
			name := "synthesized"
			if cached {
				name = "cached"
			}
			if cancelled {
				name += "_cancelled"
			}
			t.Run(name, func(t *testing.T) {
				cfg := testConfig()
				cfg.Duplex.PlaybackPaced = false
				cfg.Duplex.PhraseCache = cached
				cfg.Duplex.PhraseCacheLearnChars = 200
				c := newCollector(cfg.ClientRate)
				var logs bytes.Buffer
				e := New(Options{Cfg: cfg}, Deps{TTS: mock.NewTTS(), Emit: c.emit, Log: slog.New(slog.NewJSONHandler(&logs, nil))})
				e.ctx = context.Background()
				t.Cleanup(e.Close)
				turn := e.tracker.Begin("", false)
				e.commitTurn(turn)
				turn.Opening, turn.Readout, turn.QuoteResult = "The result is ready.", "Total 100 dollars.", true
				if cached {
					pcm := make([]byte, audio.PCM16(cfg.ClientRate).BytesForMS(40))
					for _, text := range []string{turn.Opening, turn.Readout} {
						Phrases().Put(e.phraseKey(text), PhraseAudio{PCM: pcm, Rate: cfg.ClientRate})
					}
				}
				pipe := e.newSpeechPipe(turn, false)
				pipe.PushFixed(turn.Opening, false)
				pipe.PushFixed(turn.Readout, true)
				pipe.Push("A follow-up.")
				pipe.Close()
				awaitHandoffSignal(t, pipe.done)
				if !turn.Timings().FirstReadoutOut.IsZero() {
					t.Fatal("synthesized/queued figures were counted as output")
				}
				// Drain exactly the posthook through the real player. Readout
				// audio is already queued behind it but must not be observed yet.
				for len(e.player.queue) > 0 {
					seg := e.player.queue[0]
					if seg.Kind == SegBegin && strings.Contains(seg.Text, "100") {
						break
					}
					e.player.queue = e.player.queue[1:]
					switch seg.Kind {
					case SegBegin:
						e.player.beginSegment(seg)
					case SegAudio:
						e.player.playAudio(&e.gen, seg)
					case SegMark:
						e.player.markSegment(seg)
					}
				}
				if c.allOutput() != turn.Opening || turn.Timings().FirstContentOut.IsZero() || !turn.Timings().FirstReadoutOut.IsZero() {
					t.Fatal("posthook was confused with readout output")
				}
				if cancelled {
					e.gen.Bump()
					e.player.Interrupt()
				} else {
					e.player.OnTurnDone(func(string, int64, string) { e.player.Close() })
					e.player.Run(&e.gen)
				}
				e.emitMetrics(turn, int64(c.audioMS()), cancelled)
				var metric map[string]any
				decoder := json.NewDecoder(&logs)
				for decoder.More() {
					var row map[string]any
					if err := decoder.Decode(&row); err != nil {
						t.Fatal(err)
					}
					if row["msg"] == "turn: response latency" {
						metric = row
					}
				}
				if metric["quote_result"] != true || metric["readout_expected"] != true || metric["first_readout_present"] != !cancelled || metric["first_content_present"] != true {
					t.Fatalf("incorrect readout evidence: %v", metric)
				}
				if !cancelled {
					want := turn.Timings().FirstReadoutOut.Sub(turn.Timings().StartedAt).Milliseconds()
					if metric["first_readout_ms"] != float64(want) || !strings.Contains(c.allOutput(), "100") {
						t.Fatal("readout timestamp does not identify actual figure output")
					}
				} else if metric["first_readout_ms"] != float64(0) || strings.Contains(c.allOutput(), "100") {
					t.Fatal("cancelled figures were counted as output")
				}
			})
		}
	}
}
