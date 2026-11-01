package duplex

import (
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
)

// A calling clause can finish after the tracker has moved to a newer answer.
// Its ready task must not keep waiting for that newer answer merely to decide
// that the now-unnecessary acknowledgment should be skipped.
func TestReadyTaskReleasesAckAfterItsOwnCallingTurnEnds(t *testing.T) {
	e, _, task, p := newAckHandoffTest(t)
	e.continuations = append(e.continuations, p)
	p.turn.MarkCompleted() // the interrupted calling clause's playback receipt
	followup := e.tracker.Begin("Another question", false)
	e.commitTurn(followup)
	e.backendPending.Store(1) // newer answer owns the floor
	t.Cleanup(func() { e.backendPending.Store(0) })
	historyBefore := len(e.history)
	e.onTaskDone(task)
	if e.tools.ackPending[task.ID] || len(e.continuations) != 0 {
		t.Fatal("ready result still depends on a discarded acknowledgment")
	}
	if e.tools.delivered != 0 {
		t.Fatal("result bypassed the configured wait for the newer answer")
	}
	if len(e.history) != historyBefore+2 || len(e.history[historyBefore].ToolCalls) != 1 || e.history[historyBefore+1].ToolCallID != "call1" {
		t.Fatal("releasing the acknowledgment lost its function-call history")
	}
	task.Submitted = time.Now().Add(-2 * time.Second)
	e.maybeDeliver()
	if e.tools.delivered != 1 {
		t.Fatal("ready result is still blocked after the configured delivery wait")
	}
}

func TestReadyTaskDoesNotReleaseAckBeforeCallingPlaybackEnds(t *testing.T) {
	e, _, task, p := newAckHandoffTest(t)
	e.continuations = append(e.continuations, p)
	e.backendPending.Store(1)
	t.Cleanup(func() { e.backendPending.Store(0) })
	task.Submitted = time.Now().Add(-2 * time.Second)
	e.onTaskDone(task)
	if !e.tools.ackPending[task.ID] || len(e.continuations) != 1 || e.tools.delivered != 0 {
		t.Fatal("the ready task cut an unfinished calling clause")
	}
}

func TestReadyTaskWaitDeadlineNeverTakesTheCallersFloor(t *testing.T) {
	for _, state := range []string{"speaking", "awaiting_final", "focus_hold"} {
		t.Run(state, func(t *testing.T) {
			e, _, task, _ := newAckHandoffTest(t)
			delete(e.tools.ackPending, task.ID) // ack already skipped
			task.Submitted = time.Now().Add(-2 * time.Second)
			switch state {
			case "speaking":
				e.userOpen = true
			case "focus_hold":
				e.hold = &floorHold{}
			case "awaiting_final":
				e.asrRun = &asrRun{sendDone: true}
			}
			e.onTaskDone(task)
			e.drainDeferred()
			if e.tools.delivered != 0 {
				t.Fatal("ready result took the caller's floor after its wait deadline")
			}
			e.userOpen, e.hold = false, nil
			e.asrRun = nil
			e.maybeDeliver()
			if e.tools.delivered != 1 {
				t.Fatal("result was not delivered after the caller released the floor")
			}
		})
	}
}

// A calling turn cancelled before any audio reaches the player has no
// playback-completion receipt. Its acknowledgement must not wait forever for
// a receipt that cannot arrive while a later response holds the floor.
func TestReadyTaskReleasesAckForSupersededSilentCallingTurn(t *testing.T) {
	e, c, task, p := newAckHandoffTest(t)
	e.continuations = append(e.continuations, p)
	next := e.tracker.Begin("Follow-up", false)
	e.commitTurn(next)
	e.backendPending.Store(1)
	t.Cleanup(func() { e.backendPending.Store(0) })
	task.Submitted = time.Now().Add(-2 * time.Second)
	e.onTaskDone(task)
	if e.tools.ackPending[task.ID] || e.tools.delivered != 1 || len(e.continuations) != 0 {
		t.Fatal("cancelled silent calling turn still blocks its ready result")
	}
	if !p.turn.Timings().CompletedAt.IsZero() || c.count(live.ExtTurnMetrics) != 0 {
		t.Fatal("releasing bookkeeping invented a playback completion")
	}
}

func TestReadyTaskPreservesCallingPlaybackWithoutCompletion(t *testing.T) {
	for _, state := range []string{"audio_emitted", "sentence_grace", "draining", "adopted_from", "adopted_to"} {
		t.Run(state, func(t *testing.T) {
			e, _, task, p := newAckHandoffTest(t)
			e.continuations = append(e.continuations, p)
			next := e.tracker.Begin("Follow-up", false)
			e.commitTurn(next)
			switch state {
			case "audio_emitted":
				p.turn.markOutput(false, false)
			case "sentence_grace":
				e.player.graceTurn = p.turn.ID
			case "draining":
				e.player.drainingTurn = p.turn.ID
			case "adopted_from":
				e.player.adoptedFrom = p.turn
			case "adopted_to":
				e.player.adoptedTo = p.turn
			}
			task.Submitted = time.Now().Add(-2 * time.Second)
			e.onTaskDone(task)
			if !e.tools.ackPending[task.ID] || e.tools.delivered != 0 || len(e.continuations) != 1 {
				t.Fatal("released a calling turn that still needs its playback receipt")
			}
		})
	}
}
