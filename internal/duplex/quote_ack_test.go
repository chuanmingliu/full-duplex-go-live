package duplex

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/tools"
)

// Play the real scripted acknowledgement to its normal completion. The tool
// stays pending until the caller of this helper explicitly completes it.
func completedQuoteAck(t *testing.T, beforeAck func(*Engine, *tools.Task, *pendingResponse)) (*Engine, *tools.Task, *Turn) {
	t.Helper()
	e, task, ack := queuedQuoteAck(t, beforeAck)
	e.player.OnTurnDone(func(id string, ms int64, text string) {
		e.onTurnDone(id, ms, text)
		e.player.Close()
	})
	e.player.Run(&e.gen)
	e.runPosted()
	if ack.Timings().FirstAudioOut.IsZero() || ack.Timings().CompletedAt.IsZero() {
		t.Fatal("acknowledgement did not complete actual playback")
	}
	return e, task, ack
}

func queuedQuoteAck(t *testing.T, beforeAck func(*Engine, *tools.Task, *pendingResponse)) (*Engine, *tools.Task, *Turn) {
	t.Helper()
	e, _, task, p := readyQuoteHandoff(t)
	qs, err := tools.ParseQuoteSpeech([]byte(tools.DefaultQuoteSpeech))
	if err != nil {
		t.Fatal(err)
	}
	task.Tool = task.Tool.WithSpeech(qs)
	task.Result.Output = []byte(`{"status":"quote","total":100,"lines":[]}`)
	p.turn.MarkFirstAudioOut()
	p.turn.markCompletedNaturally()
	if beforeAck != nil {
		beforeAck(e, task, p)
	}
	pcm := make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(40))
	pcm[0], pcm[1] = 0xff, 0x7f
	Phrases().Put(e.phraseKey(p.scripted), PhraseAudio{PCM: pcm, Rate: e.opts.ClientRate})
	e.sayAck(p)
	ack := e.tracker.Current()
	if !ack.Scripted {
		t.Fatal("acknowledgement was not created")
	}
	awaitHandoffSignal(t, e.speech.Load().done)
	e.runPosted()
	return e, task, ack
}

func TestSoftlyTruncatedAckKeepsDefaultPosthook(t *testing.T) {
	for _, readyDuringAck := range []bool{false, true} {
		t.Run(map[bool]string{false: "task_running", true: "result_queued"}[readyDuringAck], func(t *testing.T) {
			e, task, ack := queuedQuoteAck(t, func(e *Engine, _ *tools.Task, p *pendingResponse) {
				e.opts.Cfg.Duplex.StreamFirstChunkChars = 1
				p.scripted = "Please wait. Still working."
				pcm := make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(40))
				pcm[0], pcm[1] = 0xff, 0x7f
				for _, text := range []string{"Please wait.", "Still working."} {
					Phrases().Put(e.phraseKey(text), PhraseAudio{PCM: pcm, Rate: e.opts.ClientRate})
				}
			})
			e.player.OnTruncate(func(r TruncationReport) {
				e.onTruncated(r)
				e.player.Close()
			})
			e.player.OnTurnDone(func(string, int64, string) {
				e.player.Close()
				t.Error("softly interrupted acknowledgement completed naturally")
			})
			e.player.beginSegment(e.player.queue[0])
			e.softInterrupt() // no new caller text and no generation bump
			if readyDuringAck {
				e.onTaskDone(task)
				if e.tools.delivered != 0 {
					t.Fatal("result cut the acknowledgement's current clause")
				}
			}
			e.player.Run(&e.gen)
			e.runPosted()
			if !e.tracker.IsCurrent(ack) || ack.Timings().CompletedAt.IsZero() || ack.Timings().FirstAudioOut.IsZero() {
				t.Fatal("test did not reproduce a current, emitted, truncated acknowledgement")
			}
			if ack.Spoken != "Please wait." {
				t.Fatalf("soft stop did not discard the acknowledgement's tail: %q", ack.Spoken)
			}
			// Even an out-of-order natural callback cannot restore an ack
			// link that the truncation has invalidated.
			e.noteAckPlayback(ack.ID, true)
			if readyDuringAck {
				e.maybeDeliver()
			} else {
				e.onTaskDone(task)
			}
			if e.tracker.Current().Opening != tools.DefaultQuotePosthook {
				t.Fatal("truncated acknowledgement authorized posthook omission")
			}
		})
	}
}

func TestQueuedQuoteWaitsForAckNaturalCompletion(t *testing.T) {
	e, task, ack := queuedQuoteAck(t, nil)
	e.onTaskDone(task)
	if e.tools.delivered != 0 || len(e.tools.deliveries) != 1 {
		t.Fatal("result did not wait for the active acknowledgement")
	}
	e.noteAckPlayback("unrelated_ack", true)
	if e.tools.deliveries[0].origin.ackCompleted {
		t.Fatal("an unrelated callback certified this task's acknowledgement")
	}
	e.player.OnTurnDone(func(id string, ms int64, text string) {
		e.onTurnDone(id, ms, text)
		e.player.Close()
	})
	e.player.Run(&e.gen)
	e.runPosted()
	result := e.tracker.Current()
	if result == ack || result.Opening != "" || result.Readout == "" || e.tools.delivered != 1 {
		t.Fatal("queued result lost its acknowledgement's natural completion proof")
	}
}

func TestCompletedSameTaskAckOmitsOnlyTheDefaultPosthook(t *testing.T) {
	e, task, ack := completedQuoteAck(t, nil)
	e.onTaskDone(task)
	result := e.tracker.Current()
	if result == ack || result.Opening != "" || result.Readout == "" {
		t.Fatal("a completed same-task acknowledgement retained the redundant default posthook")
	}
	if len(e.tools.origins) != 0 || len(e.tools.deliveries) != 0 {
		t.Fatal("delivered task retained acknowledgement provenance")
	}
}

func TestCompletedAckPosthookOmissionKeepsItsGuards(t *testing.T) {
	for _, guard := range []string{"custom", "unfinished", "unheard", "interrupted", "caller_changed", "unrelated_ack", "stale_memory", "origin_unheard", "origin_unfinished", "origin_changed", "origin_interrupted"} {
		t.Run(guard, func(t *testing.T) {
			e, task, ack := completedQuoteAck(t, func(e *Engine, task *tools.Task, p *pendingResponse) {
				switch guard {
				case "origin_unheard":
					p.turn.firstAudioOut = time.Time{}
				case "origin_unfinished":
					p.turn.completedAt = time.Time{}
				case "origin_changed":
					e.tracker.Begin("a newer question", false)
				case "origin_interrupted":
					e.gen.Bump()
				}
			})
			switch guard {
			case "custom":
				task.Tool.ResultOpening = "Custom terms apply."
			case "unfinished":
				ack.completedAt = time.Time{}
			case "unheard":
				ack.firstAudioOut = time.Time{}
			case "interrupted":
				e.gen.Bump()
			case "caller_changed":
				e.tracker.Begin("another question", false)
			case "unrelated_ack":
				other := e.tracker.Begin("", false)
				other.Scripted = true
				other.MarkFirstAudioOut()
				other.MarkCompleted()
			case "stale_memory":
				task.Request.MemoryVersion++
			}
			delivery := taskDelivery{task: task, origin: e.tools.origins[task.ID]}
			opening, _, skipped := e.quoteResultPrefix(delivery, 0, false)
			if skipped || (guard != "stale_memory" && opening != task.Tool.ResultOpening) {
				t.Fatal("posthook omitted without an intact same-task acknowledgement chain")
			}
		})
	}
}

func TestQuoteDeliveryLogsDefaultFromTheResult(t *testing.T) {
	for _, kind := range []string{"default", "nondefault", "not_quote"} {
		t.Run(kind, func(t *testing.T) {
			e, task, _ := completedQuoteAck(t, nil)
			if kind == "default" {
				task.Result.Output = []byte(`{"status":"quote","default":true,"total":100,"lines":[]}`)
			} else if kind == "not_quote" {
				task.Result.Output = []byte(`{"status":"need"}`)
			}
			var logs bytes.Buffer
			e.log = slog.New(slog.NewJSONHandler(&logs, nil))
			e.onTaskDone(task)
			decoder := json.NewDecoder(&logs)
			for decoder.More() {
				var row map[string]any
				if err := decoder.Decode(&row); err != nil {
					t.Fatal(err)
				}
				if row["msg"] != "tools: delivering a task's result" {
					continue
				}
				value, present := row["quote_result_default"]
				if kind == "not_quote" {
					if present {
						t.Fatal("non-quote classified as a default/nondefault quote")
					}
				} else if !present || value != (kind == "default") {
					t.Fatal("delivery did not preserve the quote's default classification")
				}
				return
			}
			t.Fatal("missing task delivery log")
		})
	}
}
