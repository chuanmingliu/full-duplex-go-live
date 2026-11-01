package duplex

import (
	"context"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

// newLateContinuationTest is an engine with no loop running, a committed
// caller turn, and the continuation a quick tool's result would post back
// for it, complete and ready to resume the model. drainDeferred is called
// from the test goroutine in the loop's place.
func newLateContinuationTest(t *testing.T) (*Engine, *pendingResponse) {
	t.Helper()
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)
	llm := mock.NewLLM()
	e := New(Options{Cfg: cfg, Delegation: live.DelegationResponses, Speculative: true, ClientRate: cfg.ClientRate},
		Deps{Emit: c.emit, LLM: llm, TTS: mock.NewTTS()})
	e.ctx = context.Background()
	t.Cleanup(e.Close)
	turn := e.tracker.Begin("帮我把三者险改成两百万", false)
	e.commitTurn(turn)
	turn.MarkCompleted()
	p := &pendingResponse{
		turn: turn, createdAt: time.Now(), server: true,
		calls:   []provider.ToolCall{{ID: "m1", Name: "update_memory", Arguments: `{}`}},
		outputs: map[string]string{"m1": `{"status":"ok","version":2}`},
	}
	return e, p
}

func toolOutputsInHistory(e *Engine) map[string]int {
	outputs := map[string]int{}
	for _, msg := range e.history {
		if msg.Role == provider.RoleTool {
			outputs[msg.ToolCallID]++
		}
	}
	return outputs
}

// A quick tool's continuation comes back off the loop whenever the tool
// returns. If the caller has asked something new by then, resuming the
// model on the old results took the floor from the new question's answer
// and dropped its head start. The results go into the history; the model
// is not resumed on them.
func TestALateContinuationDoesNotResumeOverANewerCallerTurn(t *testing.T) {
	e, p := newLateContinuationTest(t)
	e.continuations = append(e.continuations, p)
	next := e.tracker.Begin("那保费是多少", false)
	e.commitTurn(next)
	e.drainDeferred()
	if len(e.continuations) != 0 {
		t.Fatal("the superseded continuation stayed queued")
	}
	if e.tracker.Current() != next {
		t.Fatal("a late continuation took the floor from the caller's newer turn")
	}
	if e.backendPending.Load() != 0 {
		t.Fatal("a late continuation resumed the model")
	}
	if outputs := toolOutputsInHistory(e); outputs["m1"] != 1 {
		t.Fatalf("the function results were not recorded once in the history: %v", outputs)
	}
}

// The same for a continuation whose turn was killed (a vetoed or abandoned
// turn), whatever is current now.
func TestAContinuationOfAKilledTurnIsNotResumed(t *testing.T) {
	e, p := newLateContinuationTest(t)
	e.continuations = append(e.continuations, p)
	p.turn.kill()
	e.drainDeferred()
	if len(e.continuations) != 0 || e.backendPending.Load() != 0 {
		t.Fatal("the continuation of a killed turn was kept or resumed")
	}
}

// While a newer utterance already has a speculative answer under way, the
// continuation waits: starting it would throw that head start away
// (startContinuation clears specTurn) and put a stale answer first. Once
// the speculation is committed as the caller's turn, the continuation is
// superseded; once it is abandoned, the continuation resumes.
func TestAContinuationWaitsForALiveSpeculation(t *testing.T) {
	for _, outcome := range []string{"committed", "abandoned"} {
		t.Run(outcome, func(t *testing.T) {
			e, p := newLateContinuationTest(t)
			e.continuations = append(e.continuations, p)
			spec := e.tracker.Begin("那保费", true)
			e.specTurn = spec
			e.drainDeferred()
			if len(e.continuations) != 1 || e.specTurn != spec || e.tracker.Current() != spec {
				t.Fatal("a continuation discarded a live speculation")
			}
			switch outcome {
			case "committed":
				e.specTurn = nil
				e.commitTurn(spec)
				e.drainDeferred()
				if len(e.continuations) != 0 || e.tracker.Current() != spec || e.backendPending.Load() != 0 {
					t.Fatal("a continuation resumed over the committed newer turn")
				}
			case "abandoned":
				e.abandonSpeculation("test")
				e.drainDeferred()
				if len(e.continuations) != 0 {
					t.Fatal("the continuation did not resume once the speculation was abandoned")
				}
				cur := e.tracker.Current()
				if cur == nil || cur == spec || cur == p.turn || cur.Transcript != "" {
					t.Fatal("the continuation did not begin a turn of its own")
				}
			}
		})
	}
}

// A barge-in being judged (floorHold) holds the continuation too: the
// sound over the line may be the caller's next question.
func TestAContinuationWaitsWhileABargeInIsJudged(t *testing.T) {
	e, p := newLateContinuationTest(t)
	e.continuations = append(e.continuations, p)
	e.hold = &floorHold{started: time.Now(), until: time.Now().Add(900 * time.Millisecond)}
	e.drainDeferred()
	if len(e.continuations) != 1 {
		t.Fatal("a continuation started while a barge-in was being judged")
	}
	e.hold = nil
	e.drainDeferred()
	if len(e.continuations) != 0 {
		t.Fatal("the continuation did not resume once the hold was decided")
	}
}

// An application's function result (client delegation) is read back
// however late it comes: the caller asking something else in the meantime
// does not retire it (the parity test TestConversationContinuesWhileAFunctionIsPending
// is the full-path version of this).
func TestAClientsLateFunctionResultIsStillResumed(t *testing.T) {
	e, p := newLateContinuationTest(t)
	p.server = false
	e.continuations = append(e.continuations, p)
	next := e.tracker.Begin("今天星期几", false)
	e.commitTurn(next)
	next.MarkCompleted()
	e.drainDeferred()
	if len(e.continuations) != 0 {
		t.Fatal("the client's continuation stayed queued")
	}
	if cur := e.tracker.Current(); cur == next || cur == nil || cur.Transcript != "" {
		t.Fatal("the client's late function result was not resumed on a turn of its own")
	}
}

// The scripted ack of an async tool is not held by these guards: it has its
// own (sayAck), and its task result is what answers the caller.
func TestAScriptedAckContinuationIsNotHeldAsStale(t *testing.T) {
	e, _, task, p := newAckHandoffTest(t)
	e.continuations = append(e.continuations, p)
	next := e.tracker.Begin("A newer question", false)
	e.commitTurn(next)
	e.drainDeferred()
	if len(e.continuations) != 0 || !e.tools.ackGraceStarted[task.ID] {
		t.Fatal("the scripted ack did not start its grace")
	}
}
