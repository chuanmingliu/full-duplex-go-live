package duplex

import (
	"os"
	"testing"
	"time"
)

// The same fixture runs on baseline and candidate: the original clause has
// ended, a task is ready, and an unrelated answer holds the floor for 3s.
// Report task-ready-to-delivery time, not handset or quotation-readout latency.
func TestCompletedCallingAckLatencySimulation(t *testing.T) {
	runCallingAckLatencySimulation(t, true)
}

func TestSilentCallingAckLatencySimulation(t *testing.T) {
	runCallingAckLatencySimulation(t, false)
}

func runCallingAckLatencySimulation(t *testing.T, completed bool) {
	t.Helper()
	if os.Getenv("GOLIVE_TOOL_LATENCY_SIM") != "1" {
		t.Skip("opt-in controlled scheduling simulation")
	}
	e, _, task, p := newAckHandoffTest(t)
	e.opts.Cfg.ToolsDeliverWaitMS = 1500
	e.continuations = append(e.continuations, p)
	if completed {
		p.turn.MarkCompleted()
	}
	followup := e.tracker.Begin("Unrelated response", false)
	e.commitTurn(followup)
	e.backendPending.Store(1)
	t.Cleanup(func() { e.backendPending.Store(0) })
	start := time.Now()
	task.Submitted = start
	e.onTaskDone(task)
	for e.tools.delivered == 0 && time.Since(start) < 5*time.Second {
		if time.Since(start) >= 3*time.Second {
			e.backendPending.Store(0)
		}
		e.maybeDeliver()
		e.drainDeferred()
		if e.tools.delivered == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if e.tools.delivered != 1 {
		t.Fatal("simulation did not deliver its result")
	}
	t.Logf("LATENCY_SIM calling_completed=%t configured_wait_ms=1500 unrelated_floor_ms=3000 ready_to_delivery_ms=%d", completed, time.Since(start).Milliseconds())
}
