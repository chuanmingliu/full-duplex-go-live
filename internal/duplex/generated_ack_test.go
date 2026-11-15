package duplex

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/chuanmingliu/golive/internal/provider/mock"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestSilentContinuationCancellationEmitsOneMetric(t *testing.T) {
	e, _, _, p := newAckHandoffTest(t)
	var log bytes.Buffer
	e.log = slog.New(slog.NewTextHandler(&log, nil))
	p.turn.MarkSpeechEnd(time.Now(), 1000, 380)
	turn := e.tracker.Begin("", false)
	turn.continuationOrigin = taskOriginFromTurn(p.turn)
	e.commitTurn(turn)
	e.interrupt()
	e.interrupt()
	text := log.String()
	if strings.Count(text, "turn: response latency") != 1 ||
		!strings.Contains(text, "first_audio_present=false") || !strings.Contains(text, "truncated=true") {
		t.Fatal(text)
	}
}

func TestGeneratedAckSkipsReadyResults(t *testing.T) {
	for _, readyFirst := range []bool{true, false} {
		e, c, task, p := newAckHandoffTest(t)
		p.scripted, p.generatedAck = "", true
		if readyFirst {
			e.onTaskDone(task)
			e.continueWith(p)
		} else {
			e.continueWith(p)
			e.onTaskDone(task)
		}
		assertTaskDeliveredWithoutAck(t, e, c, task, p)
	}
}

func TestGeneratedAckCompletesBeforeItsResult(t *testing.T) {
	e, _, task, p := newAckHandoffTest(t)
	delete(e.tools.ackPending, task.ID)
	turn := e.tracker.Begin("", false)
	turn.waitingTask = task.ID
	e.commitTurn(turn)
	segment := Segment{TurnID: turn.ID, owner: turn, Gen: turn.Generation, Text: "I will calculate that plan."}
	e.player.beginSegment(&e.gen, segment)
	task.Submitted = time.Now().Add(-3 * time.Second)
	e.onTaskDone(task)
	if e.tools.delivered != 0 || e.tracker.Current() != turn {
		t.Fatal("result cut a generated acknowledgement")
	}
	e.player.finishTurn(&e.gen, segment)
	e.maybeDeliver()
	if e.tools.delivered != 1 {
		t.Fatal("completed acknowledgement did not release result")
	}
	// A late timer never resurrects the old phrase.
	p.generatedAck = true
	current := e.tracker.Current()
	e.sayAck(p)
	if e.tracker.Current() != current {
		t.Fatal("old acknowledgement returned")
	}
}

func TestQuoteResultFinishesRepeatedConfirmationSpeech(t *testing.T) {
	e, _, task, _ := newAckHandoffTest(t)
	delete(e.tools.ackPending, task.ID)
	task.Tool.Name = "quote_total"
	task.Submitted = time.Now().Add(-3 * time.Second)
	turn := e.tracker.Begin("The same selection, please.", false)
	e.commitTurn(turn)
	segment := Segment{TurnID: turn.ID, owner: turn, Gen: turn.Generation, Text: "That selection is being calculated."}
	e.player.beginSegment(&e.gen, segment)
	e.speaking.Store(true)
	e.onTaskDone(task)
	if e.tools.delivered != 0 || e.tracker.Current() != turn {
		t.Fatal("quote result cut the repeated confirmation response")
	}
	e.player.finishTurn(&e.gen, segment)
	e.speaking.Store(false)
	e.maybeDeliver()
	if e.tools.delivered != 1 {
		t.Fatal("completed response did not release the quote result")
	}
}

func TestContinuationOriginSurvivesChain(t *testing.T) {
	first := &Turn{ID: "item_1"}
	first.MarkSpeechEnd(time.Now().Add(-time.Second), 1000, 380)
	second := &Turn{ID: "item_2", continuationOrigin: taskOriginFromTurn(first)}
	third := &Turn{ID: "item_3", continuationOrigin: taskOriginFromTurn(second)}
	origin := taskOriginFromTurn(third)
	if origin.turnID != first.ID || !origin.speechEndAt.Equal(first.Timings().SpeechEndAt) || origin.endSilenceMS != 380 {
		t.Fatal("lost caller clock")
	}
}

func TestGeneratedAckCannotRepeatTools(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		cfg := testConfig()
		rec := &recordLLM{}
		c := newCollector(cfg.ClientRate)
		e := New(Options{Cfg: cfg, Instructions: "合成应答测试", Tools: []json.RawMessage{json.RawMessage(`{"type":"function","function":{"name":"quote_total"}}`)}}, Deps{ASR: mock.NewASR(""), LLM: rec, TTS: mock.NewTTS(), Emit: c.emit})
		e.Start(context.Background())
		t.Cleanup(e.Close)
		e.post(func() {
			turn := e.tracker.Begin("", false)
			if waiting {
				turn.waitingTask = "t1"
			}
			e.commitTurn(turn)
			e.startBackend(turn, "tool_result")
		})
		waitFor(t, "backend request", time.Second, func() bool {
			rec.mu.Lock()
			defer rec.mu.Unlock()
			return len(rec.last.Messages) > 0
		})
		rec.mu.Lock()
		toolCount := len(rec.last.Tools)
		rec.mu.Unlock()
		if (toolCount == 0) != waiting {
			t.Fatalf("waiting=%v toolCount=%v", waiting, toolCount)
		}
	}
}
