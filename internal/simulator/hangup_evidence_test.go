package simulator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func assertHangupStillOccupied(t *testing.T, c *RunController, b Binding) []byte {
	t.Helper()
	ctx := context.Background()
	snapshot, err := c.Get(ctx, b.RunID)
	if err != nil || snapshot.Phase != Ending {
		t.Errorf("unsettled hangup exposed terminal snapshot: %+v %v", snapshot, err)
	}
	runs, err := c.List(ctx)
	if err != nil || len(runs) != 1 || runs[0].Phase != Ending {
		t.Errorf("unsettled hangup exposed terminal list: %+v %v", runs, err)
	}
	if _, err = c.Report(ctx, b.RunID); !errors.Is(err, ErrNotReady) {
		t.Errorf("unsettled hangup exposed report: %v", err)
	}
	if _, err = c.Transcript(ctx, b.RunID); !errors.Is(err, ErrNotReady) {
		t.Errorf("unsettled hangup exposed transcript: %v", err)
	}
	if w := apiRequest(c, "GET", "/sim/v1/runs/"+b.RunID+"/report", "test-token", nil); w.Code != 409 {
		t.Errorf("unsettled HTTP report status=%d", w.Code)
	}
	if _, err = os.Stat(filepath.Join(c.opts.DataDir, b.RunID, "report.json")); !os.IsNotExist(err) {
		t.Errorf("premature terminal report persisted: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(c.opts.DataDir, b.RunID, "state.json"))
	if err != nil {
		t.Error(err)
	} else {
		var saved persistedRun
		if err = json.Unmarshal(raw, &saved); err != nil || saved.Phase != Ending {
			t.Errorf("unsettled snapshot persisted terminal phase: %+v %v", saved, err)
		}
	}
	if next, err := c.Prepare(ctx, sampleScenario()); !errors.Is(err, ErrConflict) {
		t.Errorf("unsettled hangup released reservation: %+v %v", next, err)
		if err == nil {
			_ = c.Stop(ctx, next.RunID)
		}
	}
	return raw
}

func assertPendingHangupRestoresIndeterminate(t *testing.T, c *RunController, id string, pendingState, finalReport []byte) {
	t.Helper()
	opts := c.opts
	opts.DataDir = t.TempDir()
	dir := filepath.Join(opts.DataDir, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), pendingState, 0600); err != nil {
		t.Fatal(err)
	}
	// This also simulates interruption after a final report write but before
	// the final snapshot publication. Pending metadata must dominate the report.
	if finalReport != nil {
		if err := os.WriteFile(filepath.Join(dir, "report.json"), finalReport, 0600); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := NewController(opts)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := restored.Get(context.Background(), id)
	if err != nil || snap.Phase != Aborted {
		t.Fatal("pending stop restored terminal success", snap, err)
	}
	rep, err := restored.Report(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range rep.Checks {
		if check.Result != "indeterminate" {
			t.Fatal("pending stop restored conclusive evidence", check)
		}
	}
	for _, action := range rep.Actions {
		if action.Result == "completed" {
			t.Fatal("pending stop restored completed action", action)
		}
	}
}

func TestScriptedHangupDefersTerminalPublicationUntilControlsSettle(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		for _, commandFails := range []bool{false, true} {
			name := map[bool]string{false: "synchronous", true: "concurrent"}[concurrent] + "/" + map[bool]string{false: "success", true: "command-error"}[commandFails]
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				c, f, base := testController(t)
				c.opts.DataDir = t.TempDir()
				p := &actionReturnPorts{fakePorts: base}
				c.opts.Telephony = p
				if commandFails {
					base.hangupErr = ErrNotReady
				}
				s := hangupScenario()
				s.RetainTranscript = true
				snap, err := c.Prepare(ctx, s)
				if err != nil {
					t.Fatal(err)
				}
				b := connect(t, c, f, snap)
				confirmed, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var pendingState []byte
				p.hangupHook = func() {
					if err := c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS()}); err != nil {
						t.Error(err)
					}
					// This point must be reachable without waiting on Hangup's own return.
					if concurrent {
						close(confirmed)
						<-release
					} else {
						pendingState = assertHangupStillOccupied(t, c, b)
						assertPendingHangupRestoresIndeterminate(t, c, b.RunID, pendingState, nil)
					}
				}
				go func() { c.performAction(b.RunID, s.Actions[0]); close(done) }()
				if concurrent {
					select {
					case <-confirmed:
					case <-time.After(time.Second):
						t.Fatal("native end callback blocked on Hangup return")
					}
					pendingState = assertHangupStillOccupied(t, c, b)
					assertPendingHangupRestoresIndeterminate(t, c, b.RunID, pendingState, nil)
					close(release)
				}
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("scripted stop did not settle")
				}
				state, err := c.Get(ctx, b.RunID)
				if err != nil || !state.Phase.Terminal() {
					t.Fatal("settled hangup remained nonterminal", state, err)
				}
				rep, err := c.Report(ctx, b.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if commandFails {
					if len(rep.Failures) == 0 || actionCount(rep.Actions, s.Actions[0].ID, "completed") != 0 {
						t.Fatal("command error published successful evidence", rep)
					}
				} else if len(rep.Failures) != 0 || actionCount(rep.Actions, s.Actions[0].ID, "completed") != 1 {
					t.Fatal("settled success lacked completion", rep)
				}
				assertArtifactMatches(t, c, b.RunID)
				finalReport, err := os.ReadFile(filepath.Join(c.opts.DataDir, b.RunID, "report.json"))
				if err != nil {
					t.Fatal(err)
				}
				assertPendingHangupRestoresIndeterminate(t, c, b.RunID, pendingState, finalReport)
				if _, err := c.Prepare(ctx, sampleScenario()); err != nil {
					t.Fatal("settled controls kept reservation occupied", err)
				}
			})
		}
	}
}

func hangupScenario() Scenario {
	s := sampleScenario()
	s.Actions = []Action{{ID: "scripted-leave", Kind: "hangup", Trigger: "connected", Occurrence: 1, AfterMS: 5000}}
	return s
}

func actionCount(outcomes []ActionOutcome, id, result string) int {
	count := 0
	for _, outcome := range outcomes {
		if outcome.ID == id && outcome.Result == result {
			count++
		}
	}
	return count
}

func TestScriptedHangupRequiresMatchingObservedEnd(t *testing.T) {
	ctx := context.Background()
	c, f, _ := testController(t)
	c.opts.DataDir = t.TempDir()
	s := hangupScenario()
	snap, err := c.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	b := connect(t, c, f, snap)
	c.performAction(b.RunID, s.Actions[0])
	state, err := c.Get(ctx, b.RunID)
	if err != nil || state.Phase != Ending {
		t.Fatal("command acceptance became terminal", err, state.Phase)
	}
	c.mu.Lock()
	completed := actionCount(c.runs[b.RunID].actions, s.Actions[0].ID, "completed")
	c.mu.Unlock()
	if completed != 0 {
		t.Fatal("hangup command acceptance counted as completion")
	}
	for _, wrong := range []Binding{
		{RunID: b.RunID, CallID: "other", SessionID: b.SessionID},
		{RunID: b.RunID, CallID: b.CallID, SessionID: "other"},
	} {
		if err := c.Handle(ctx, Event{Kind: "ended", Binding: wrong, AtMS: c.RelativeMS()}); !errors.Is(err, ErrInvalidTransition) {
			t.Fatal("foreign end accepted", err)
		}
	}
	f.advance(25 * time.Millisecond)
	if err := c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS(), Reason: "gateway confirmed normal end"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	rep, err := c.Report(ctx, b.RunID)
	if err != nil || actionCount(rep.Actions, s.Actions[0].ID, "completed") != 1 {
		t.Fatalf("missing/duplicate hangup completion: %+v %v", rep.Actions, err)
	}
	var requested, ended int64
	for _, action := range rep.Actions {
		if action.ID == s.Actions[0].ID {
			if action.Result == "requested" {
				requested = action.AtMS
			}
			if action.Result == "completed" {
				ended = action.AtMS
			}
		}
	}
	if ended-requested != 25 {
		t.Fatalf("completion timestamp is not confirmed end: request=%d complete=%d", requested, ended)
	}
	assertArtifactMatches(t, c, b.RunID)
}

type closeCallbackPorts struct {
	*actionReturnPorts
	closeHook func()
}

func (p *closeCallbackPorts) Close(ctx context.Context, b Binding) error {
	if p.closeHook != nil {
		p.closeHook()
	}
	return p.fakePorts.Close(ctx, b)
}

func TestScriptedHangupLifecycleEvidence(t *testing.T) {
	ctx := context.Background()
	for _, variant := range []string{"synchronous-confirmation", "native-zero-timestamp", "hangup-error", "close-error", "synchronous-end-then-command-error", "end-before-hangup-dispatched", "missing-end-timeout", "premature-end", "unrelated-stop", "backdated-end", "future-end", "existing-failure", "restart"} {
		t.Run(variant, func(t *testing.T) {
			c, f, base := testController(t)
			c.opts.DataDir = t.TempDir()
			p := &closeCallbackPorts{actionReturnPorts: &actionReturnPorts{fakePorts: base}}
			c.opts.Telephony = p
			c.opts.Voice = p
			s := hangupScenario()
			snap, err := c.Prepare(ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			b := connect(t, c, f, snap)
			confirm := func() {
				if err := c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS()}); err != nil {
					t.Error(err)
				}
			}
			switch variant {
			case "synchronous-confirmation":
				p.hangupHook = confirm
			case "hangup-error":
				base.hangupErr = ErrNotReady
			case "close-error":
				base.closeErr = ErrNotReady
				p.hangupHook = confirm
			case "synchronous-end-then-command-error":
				p.hangupHook = confirm
				base.hangupErr = ErrNotReady
			case "end-before-hangup-dispatched":
				p.closeHook = confirm
			case "premature-end":
				confirm()
			case "unrelated-stop":
				if err := c.Stop(ctx, b.RunID); err != nil {
					t.Fatal(err)
				}
			case "existing-failure":
				if err := c.Observe(ctx, b, Observation{Kind: "assistant_audio", AtMS: -1, NonSilent: true}); err == nil {
					t.Fatal("missing invalid observation failure")
				}
			}
			c.performAction(b.RunID, s.Actions[0])
			switch variant {
			case "native-zero-timestamp":
				err = c.Handle(ctx, Event{Kind: "ended", Binding: Binding{RunID: b.RunID, CallID: b.CallID}})
			case "missing-end-timeout":
				f.advance(c.opts.SetupTimeout)
			case "unrelated-stop", "existing-failure":
				confirm()
			case "backdated-end":
				err = c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS() - 1})
			case "future-end":
				err = c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS() + 1})
			case "restart":
				c, err = NewController(c.opts)
			}
			if err != nil {
				t.Fatal(err)
			}
			rep, err := c.Report(ctx, b.RunID)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if variant == "synchronous-confirmation" || variant == "native-zero-timestamp" {
				want = 1
			}
			if got := actionCount(rep.Actions, s.Actions[0].ID, "completed"); got != want {
				t.Fatalf("%s completion=%d want=%d actions=%+v failures=%v", variant, got, want, rep.Actions, rep.Failures)
			}
			assertArtifactMatches(t, c, b.RunID)
		})
	}
}

func TestHangupCannotUseEngineCompletionAsGatewayEvidence(t *testing.T) {
	c, f, _ := testController(t)
	s := hangupScenario()
	snap, err := c.Prepare(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	b := connect(t, c, f, snap)
	if err = c.ActionObservation(context.Background(), b, ActionOutcome{ID: s.Actions[0].ID, Result: "completed", AtMS: c.RelativeMS()}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("engine accepted control completion without gateway evidence", err)
	}
}

func TestScriptedHangupScheduledBeforeDurationLimit(t *testing.T) {
	ctx := context.Background()
	c, f, base := testController(t)
	c.opts.DataDir = t.TempDir()
	p := &actionReturnPorts{fakePorts: base}
	c.opts.Telephony = p
	s := hangupScenario()
	s.MaxDurationMS = 20000
	s.IdleTimeoutMS = 0
	snap, err := c.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	b := connect(t, c, f, snap)
	p.hangupHook = func() {
		if err := c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS()}); err != nil {
			t.Error(err)
		}
	}
	f.advance(5 * time.Second)
	rep, err := c.Report(ctx, b.RunID)
	if err != nil || rep.Phase != Completed || actionCount(rep.Actions, s.Actions[0].ID, "completed") != 1 || len(rep.Failures) != 0 {
		t.Fatal("scheduled scripted hangup did not complete", rep, err)
	}
	for _, name := range []string{"answered_once", "greeting_once", "call_limits", "clean_termination"} {
		if reportChecks(rep)[name] != "pass" {
			t.Fatal("lifecycle check did not pass", name)
		}
	}
	assertArtifactMatches(t, c, b.RunID)
}
