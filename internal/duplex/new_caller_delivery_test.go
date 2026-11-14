package duplex

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestReadyResultDoesNotStealNewCallerAnswerAtFinalization(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_audio", true: "answer_started"}[started], func(t *testing.T) {
			e, _, task, p := newAckHandoffTest(t)
			var log bytes.Buffer
			e.log = slog.New(slog.NewTextHandler(&log, nil))
			e.continuations = append(e.continuations, p)
			p.turn.MarkCompleted()
			next := e.tracker.Begin("A new coverage question", false)
			e.commitTurn(next)
			next.MarkSpeechEnd(time.Now(), 1200, 380)
			if started {
				next.MarkFirstAudioOut()
			}
			e.backendPending.Store(1)
			t.Cleanup(func() { e.backendPending.Store(0) })
			task.Submitted = time.Now().Add(-10 * time.Second)
			e.onTaskDone(task)
			if e.tools.delivered != 0 || e.tracker.Current() != next {
				t.Fatal("old ready result stole a newly finalized caller answer")
			}
			e.maybeDeliver()
			if strings.Count(log.String(), "ready result yields to a newer caller answer") != 1 {
				t.Fatal("new caller protection diagnostic missing or repeated")
			}
			if e.tools.ackPending[task.ID] || len(e.continuations) != 0 {
				t.Fatal("new caller protection regressed old acknowledgment cleanup")
			}
			// Protection is bounded by the existing configured delivery wait. An
			// unresponsive generation must not indefinitely withhold a ready result.
			next.mu.Lock()
			next.speechEndAt = time.Now().Add(-2 * time.Second)
			next.mu.Unlock()
			e.maybeDeliver()
			if e.tools.delivered != 1 {
				t.Fatal("new-caller grace did not expire")
			}
		})
	}
}

func TestNewCallerAnswerProtectionEndsWithPlayback(t *testing.T) {
	e, _, task, _ := newAckHandoffTest(t)
	delete(e.tools.ackPending, task.ID)
	next := e.tracker.Begin("A short question", false)
	e.commitTurn(next)
	next.MarkSpeechEnd(time.Now(), 1200, 380)
	next.MarkFirstAudioOut()
	next.MarkCompleted()
	e.backendPending.Store(1)
	t.Cleanup(func() { e.backendPending.Store(0) })
	task.Submitted = time.Now().Add(-10 * time.Second)
	e.onTaskDone(task)
	if e.tools.delivered != 1 {
		t.Fatal("finished answer retained unnecessary protection")
	}
}

func TestNewCallerProtectionDoesNotDelayItsOwnReadyResult(t *testing.T) {
	for _, coalesced := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "coalesced"}[coalesced], func(t *testing.T) {
			e, _, task, p := newAckHandoffTest(t)
			delete(e.tools.ackPending, task.ID)
			next := e.tracker.Begin("The same quote, please", false)
			e.commitTurn(next)
			next.MarkSpeechEnd(time.Now(), 1200, 380)
			e.tools.origins = map[string]taskOrigin{task.ID: taskOriginFromTurn(next)}
			if coalesced {
				e.tools.origins[task.ID] = taskOrigin{turnID: p.turn.ID}
				e.tools.coalesced = map[string]*coalescedTask{task.ID: {task: task, origins: []taskOrigin{taskOriginFromTurn(next)}}}
			}
			e.backendPending.Store(1)
			t.Cleanup(func() { e.backendPending.Store(0) })
			task.Submitted = time.Now().Add(-10 * time.Second)
			e.onTaskDone(task)
			if e.tools.delivered != 1 {
				t.Fatal("ready result for this caller was unnecessarily delayed")
			}
		})
	}
}

func TestNewCallerProtectionHonorsConfiguredWait(t *testing.T) {
	for _, tc := range []struct {
		wait int
		age  time.Duration
		held bool
	}{{5000, 2 * time.Second, true}, {250, time.Second, false}} {
		e, _, task, _ := newAckHandoffTest(t)
		delete(e.tools.ackPending, task.ID)
		e.opts.Cfg.ToolsDeliverWaitMS = tc.wait
		next := e.tracker.Begin("A new question", false)
		e.commitTurn(next)
		next.MarkSpeechEnd(time.Now().Add(-tc.age), 1200, 380)
		e.backendPending.Store(1)
		t.Cleanup(func() { e.backendPending.Store(0) })
		task.Submitted = time.Now().Add(-20 * time.Second)
		e.onTaskDone(task)
		if (e.tools.delivered == 0) != tc.held {
			t.Fatalf("caller protection ignored configured wait %d", tc.wait)
		}
	}
}
