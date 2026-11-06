package duplex

import (
	"context"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/tools"
)

// newSilenceRuleEngine is an engine with no loop running, a 2 s silence
// rule in force and the caller's last word 10 s in the past: every call to
// checkHangupRules is one tick, run from the test goroutine in the loop's
// place. The farewell is empty so firing leaves only endCall set.
func newSilenceRuleEngine(t *testing.T) *Engine {
	t.Helper()
	allowImmediateModelHangup(t)
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)
	e := New(Options{Cfg: cfg, Delegation: live.DelegationClient, ClientDelegation: "manual"}, Deps{Emit: c.emit})
	e.ctx = context.Background()
	t.Cleanup(e.Close)
	rules := []tools.HangupRule{{ID: "quiet", Kind: "silence", Seconds: 2}}
	e.tools = &toolState{
		started:      time.Now().Add(-10 * time.Second),
		lastCallerAt: time.Now().Add(-10 * time.Second),
		engine:       rules,
	}
	e.tools.rules.Rules = rules
	return e
}

// The player lets go of the floor under its own lock and only then posts
// the completion that stamps the assistant's quiet; a tick in between used
// to see a free floor and a clock still running from the caller's last
// word, and the silence rule hung up as the answer ended (the race behind
// ISS-051, which its fix left open). The clock now also runs from the last
// tick that saw the floor held.
func TestTheSilenceClockRunsFromTheLastTickThatSawTheFloorHeld(t *testing.T) {
	e := newSilenceRuleEngine(t)
	// A tick during the answer: the backend has the floor.
	e.backendPending.Store(1)
	e.checkHangupRules("")
	if e.tools.endCall != nil {
		t.Fatal("the silence rule fired while the assistant held the floor")
	}
	// The floor is let go; the quiet has not been stamped yet (lastQuietAt
	// is zero: the completion receipt has not reached the loop).
	e.backendPending.Store(0)
	e.checkHangupRules("")
	if e.tools.endCall != nil {
		t.Fatal("the silence rule fired on the tick between the floor's release and the quiet being stamped")
	}
	// Two seconds of real silence after the floor was last seen held: it fires.
	e.tools.floorHeldAt = time.Now().Add(-3 * time.Second)
	e.checkHangupRules("")
	if e.tools.endCall == nil {
		t.Fatal("the silence rule did not fire after the caller's real silence")
	}
}

// A completion receipt posted to the loop and not yet run is the answer
// still ending: the rule waits for it rather than counting the answer as
// the caller's silence.
func TestAPostedPlaybackReceiptKeepsTheSilenceClock(t *testing.T) {
	e := newSilenceRuleEngine(t)
	e.tools.floorHeldAt = time.Now().Add(-3 * time.Second)
	e.playbackMu.Lock()
	e.playbackPending = append(e.playbackPending, &playbackCompletion{})
	e.playbackMu.Unlock()
	e.checkHangupRules("")
	if e.tools.endCall != nil {
		t.Fatal("the silence rule fired with the answer's completion receipt still on its way to the loop")
	}
	if time.Since(e.tools.floorHeldAt) > time.Second {
		t.Fatal("a pending receipt did not count as the floor being held")
	}
	e.playbackMu.Lock()
	e.playbackPending = nil
	e.playbackMu.Unlock()
	e.tools.floorHeldAt = time.Now().Add(-3 * time.Second)
	e.checkHangupRules("")
	if e.tools.endCall == nil {
		t.Fatal("the silence rule did not fire once the receipt had been handled and the silence was real")
	}
}

// The assistant's own quiet, once stamped, still starts the clock on its own.
func TestTheSilenceClockStartsFromWhoeverWasLastHeard(t *testing.T) {
	e := newSilenceRuleEngine(t)
	e.tools.lastQuietAt = time.Now().Add(-time.Second)
	if since := e.silenceClockStart(); !since.Equal(e.tools.lastQuietAt) {
		t.Fatalf("clock start %v; wanted the assistant's quiet %v", since, e.tools.lastQuietAt)
	}
	e.tools.floorHeldAt = time.Now()
	if since := e.silenceClockStart(); !since.Equal(e.tools.floorHeldAt) {
		t.Fatal("a later tick that saw the floor held did not move the clock")
	}
	e.tools.lastCallerAt = time.Now().Add(time.Second)
	if since := e.silenceClockStart(); !since.Equal(e.tools.lastCallerAt) {
		t.Fatal("the caller's later word did not move the clock")
	}
}
