package simulator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestScenarioCustomerHangupDefaultAndOptOut(t *testing.T) {
	for _, setting := range []string{"", `,"hangup_after_goal":true`, `,"hangup_after_goal":false`} {
		raw, _ := json.Marshal(sampleScenario())
		input := strings.TrimSuffix(string(raw), "}") + setting + "}"
		s, err := DecodeScenario(strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		want := !strings.Contains(setting, "false")
		if s.CustomerHangupEnabled() != want || cloneScenario(s).CustomerHangupEnabled() != want {
			t.Fatalf("setting %q did not survive decoding and cloning", setting)
		}
		s.Goal = "Complete the synthetic task."
		if strings.Contains(s.Prompt(), CustomerGoalToolName) != want {
			t.Fatalf("goal tool instructions do not match setting %q", setting)
		}
	}
	raw, _ := json.Marshal(sampleScenario())
	if _, err := DecodeScenario(strings.NewReader(strings.TrimSuffix(string(raw), "}") + `,"hangup_after_goal":null}`)); !errors.Is(err, ErrInvalidScenario) {
		t.Fatal("null is not an explicit boolean opt-out")
	}
}

func TestCustomerGoalHangupWaitsForFarewellThenAssistantReply(t *testing.T) {
	ctx := context.Background()
	c, clock, ports := testController(t)
	s := sampleScenario()
	s.Goal = "Complete the synthetic task."
	s.IdleTimeoutMS = 1000
	snap, _ := c.Prepare(ctx, s)
	b := connect(t, c, clock, snap)
	wrong := b
	wrong.SessionID = "other-session"
	if err := c.GoalReached(ctx, wrong); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("accepted another session's goal")
	}
	for range 2 {
		if err := c.GoalReached(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	clock.advance(0)
	if ports.speaks != 1 || ports.hangups != 0 {
		t.Fatalf("farewell/hangup counts: %+v", ports)
	}
	// No reply timer or stale inactivity stop may cut off the farewell.
	clock.advance(4 * time.Second)
	if ports.hangups != 0 {
		t.Fatal("hung up before farewell completed")
	}
	observeCustomerHangup(t, c, b, Observation{Kind: "customer_audio", NonSilent: true})
	clock.advance(50 * time.Millisecond)
	observeCustomerHangup(t, c, b, Observation{Kind: "customer_audio", End: true})
	if err := c.ActionObservation(ctx, b, ActionOutcome{ID: CustomerFarewellActionID, Result: "completed", AtMS: c.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	clock.advance(100 * time.Millisecond)
	observeCustomerHangup(t, c, b, Observation{Kind: "assistant_audio", NonSilent: true})
	clock.advance(50 * time.Millisecond)
	observeCustomerHangup(t, c, b, Observation{Kind: "assistant_audio", End: true})
	clock.advance(0)
	if ports.hangups != 1 {
		t.Fatal("assistant reply completion did not request gateway BYE")
	}
	if err := c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS(), Termination: &Termination{NativeInitiator: "callee", NativeReason: "ByCallee"}}); err != nil {
		t.Fatal(err)
	}
	report, err := c.Report(ctx, b.RunID)
	if err != nil || report.Phase != Completed || report.TerminationReason != "customer_hangup" {
		t.Fatalf("missing completed customer report: %+v, %v", report, err)
	}
	if len(report.AssistantGaps) != 1 || !report.AssistantGaps[0].Valid || report.AssistantGaps[0].ValueMS != 100 {
		t.Fatalf("farewell/reply gap lost: %+v", report.AssistantGaps)
	}
	for _, check := range report.Checks {
		if check.Name == "clean_termination" && check.Result != "pass" {
			t.Fatalf("clean termination not evaluated: %+v", check)
		}
	}
	clock.advance(5 * time.Second)
	if ports.hangups != 1 {
		t.Fatal("reply timeout sent duplicate BYE")
	}
}

func TestCustomerGoalNearDurationLimitCompletesFarewellAndReplyWait(t *testing.T) {
	ctx := context.Background()
	c, clock, ports := testController(t)
	s := sampleScenario()
	s.Goal = "Complete the synthetic task."
	s.MaxDurationMS = 295000
	s.IdleTimeoutMS = 0
	snap, err := c.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	b := connect(t, c, clock, snap)
	clock.advance(294912 * time.Millisecond)
	c.mu.Lock()
	callCtx := c.runs[b.RunID].ctx
	c.mu.Unlock()
	if err := c.GoalReached(ctx, b); err != nil {
		t.Fatal(err)
	}
	clock.advance(0)
	if ports.speaks != 1 {
		t.Fatal("customer farewell was not started")
	}
	clock.advance(88 * time.Millisecond)
	current, _ := c.Get(ctx, b.RunID)
	if current.Phase != Connected || ports.hangups != 0 || callCtx.Err() != nil {
		t.Fatalf("duration limit cancelled an active farewell: phase=%s hangups=%d context=%v", current.Phase, ports.hangups, callCtx.Err())
	}
	clock.advance(4912 * time.Millisecond)
	if err := c.ActionObservation(ctx, b, ActionOutcome{ID: CustomerFarewellActionID, Result: "completed", AtMS: c.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	clock.advance(2999 * time.Millisecond)
	if ports.hangups != 0 {
		t.Fatal("duration limit cut short the farewell reply wait")
	}
	clock.advance(time.Millisecond)
	if ports.hangups != 1 {
		t.Fatal("farewell completion did not hang up within the eight-second grace")
	}
	if err := c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	report, err := c.Report(ctx, b.RunID)
	if err != nil || report.Phase != Completed || report.TerminationReason != "customer_hangup" {
		t.Fatalf("farewell lost its completed termination: %+v %v", report, err)
	}
	for _, check := range report.Checks {
		if check.Name == "call_limits" && check.Result != "pass" {
			t.Fatalf("bounded farewell grace failed the duration check: %+v", check)
		}
	}
}

func TestCustomerGoalDurationGraceIsBoundedDespiteRepeatedCompletion(t *testing.T) {
	ctx := context.Background()
	c, clock, ports := testController(t)
	s := sampleScenario()
	s.Goal = "Complete the synthetic task."
	s.MaxDurationMS = 1000
	s.IdleTimeoutMS = 0
	snap, err := c.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	b := connect(t, c, clock, snap)
	clock.advance(999 * time.Millisecond)
	if err := c.GoalReached(ctx, b); err != nil {
		t.Fatal(err)
	}
	clock.advance(7999 * time.Millisecond)
	if ports.hangups != 0 {
		t.Fatal("unfinished farewell was cut off before its grace elapsed")
	}
	if err := c.GoalReached(ctx, b); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Millisecond)
	if ports.hangups != 1 || ports.speaks != 1 {
		t.Fatalf("repeated goal extended the bounded farewell grace: hangups=%d speaks=%d", ports.hangups, ports.speaks)
	}
	if err := c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	report, err := c.Report(ctx, b.RunID)
	if err != nil || report.Phase != Completed || report.TerminationReason != "duration_limit" {
		t.Fatalf("stalled farewell did not settle at the duration limit: %+v %v", report, err)
	}
}

func TestCustomerGoalDurationGraceStillAllowsRemoteHangup(t *testing.T) {
	ctx := context.Background()
	c, clock, ports := testController(t)
	s := sampleScenario()
	s.Goal = "Complete the synthetic task."
	s.MaxDurationMS = 1000
	s.IdleTimeoutMS = 0
	snap, err := c.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	b := connect(t, c, clock, snap)
	clock.advance(999 * time.Millisecond)
	if err := c.GoalReached(ctx, b); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Millisecond)
	if ports.hangups != 0 {
		t.Fatal("duration limit interrupted the farewell before remote BYE")
	}
	if err := c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS(), Termination: &Termination{NativeInitiator: "caller", NativeReason: "ByCaller"}}); err != nil {
		t.Fatal(err)
	}
	clock.advance(10 * time.Second)
	report, err := c.Report(ctx, b.RunID)
	if err != nil || report.Phase != Completed || report.TerminationReason != "remote_hangup" || ports.hangups != 0 {
		t.Fatalf("deferred duration callback overrode remote BYE: phase=%s reason=%s hangups=%d error=%v", report.Phase, report.TerminationReason, ports.hangups, err)
	}
}

func TestCustomerHangupReplyTimeoutDoesNotConfusePausesWithCompletion(t *testing.T) {
	for _, reply := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_reply", true: "debounced_reply"}[reply], func(t *testing.T) {
			c, clock, ports := testController(t)
			s := sampleScenario()
			s.Goal = "Complete the synthetic task."
			snap, _ := c.Prepare(context.Background(), s)
			b := connect(t, c, clock, snap)
			_ = c.GoalReached(context.Background(), b)
			clock.advance(0)
			_ = c.ActionObservation(context.Background(), b, ActionOutcome{ID: CustomerFarewellActionID, Result: "completed", AtMS: c.RelativeMS()})
			if reply {
				observeCustomerHangup(t, c, b, Observation{Kind: "assistant_audio", NonSilent: true})
				clock.advance(250 * time.Millisecond)
				if ports.hangups != 0 {
					t.Fatal("intra-reply pause caused premature customer BYE")
				}
				observeCustomerHangup(t, c, b, Observation{Kind: "assistant_audio", NonSilent: true})
				clock.advance(2749 * time.Millisecond)
			} else {
				clock.advance(2999 * time.Millisecond)
			}
			if ports.hangups != 0 {
				t.Fatal("customer hung up before reply finished or deadline")
			}
			clock.advance(time.Millisecond)
			if ports.hangups != 1 {
				t.Fatal("customer did not request gateway BYE")
			}
		})
	}
}

func TestRemoteHangupDuringCustomerActionCancellation(t *testing.T) {
	for _, farewell := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending_script", true: "pending_farewell"}[farewell], func(t *testing.T) {
			ctx := context.Background()
			c, clock, ports := testController(t)
			s := sampleScenario()
			s.Goal = "Complete the synthetic task."
			s.Actions = []Action{{ID: "last", Trigger: "connected", Occurrence: 1, Kind: "speak", Text: "Synthetic action.", MaxHoldMS: 1000}}
			snap, err := c.Prepare(ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			b := connect(t, c, clock, snap)
			clock.advance(0)
			id := "last"
			if farewell {
				if err := c.GoalReached(ctx, b); err != nil {
					t.Fatal(err)
				}
				clock.advance(0)
				id = CustomerFarewellActionID
			}
			speaks := ports.speaks
			media, cancel := context.WithCancel(ctx)
			cancel()
			if err := c.ActionObservation(media, b, ActionOutcome{ID: id, Result: "cancelled", AtMS: c.RelativeMS()}); !errors.Is(err, context.Canceled) {
				t.Fatalf("missing cancelled media context: %v", err)
			}
			clock.advance(0)
			current, _ := c.Get(ctx, b.RunID)
			if ports.speaks != speaks || ports.hangups != 0 || current.Phase != Connected {
				t.Fatal("cancelled media callback raced native hangup confirmation")
			}
			if err := c.Handle(ctx, Event{Kind: "ended", Binding: b, Termination: &Termination{NativeInitiator: "caller", NativeReason: "ByCaller"}}); err != nil {
				t.Fatal(err)
			}
			report, err := c.Report(ctx, b.RunID)
			if err != nil || report.Phase != Completed || report.TerminationReason != "remote_hangup" || len(report.Failures) != 0 {
				t.Fatalf("remote BYE during action teardown lost normal report: %+v %v", report, err)
			}
		})
	}
}

func TestCustomerScriptExhaustionAndOptOut(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "opt_out"}[enabled], func(t *testing.T) {
			c, clock, ports := testController(t)
			s := sampleScenario()
			s.HangupAfterGoal = &enabled
			s.Actions = []Action{
				{ID: "first", Trigger: "connected", Occurrence: 1, Kind: "speak", Text: "First synthetic action.", MaxHoldMS: 1000},
				{ID: "last", Trigger: "connected", Occurrence: 1, AfterMS: 2000, Kind: "speak", Text: "Last synthetic action.", MaxHoldMS: 1000},
			}
			snap, _ := c.Prepare(context.Background(), s)
			b := connect(t, c, clock, snap)
			clock.advance(0)
			_ = c.ActionObservation(context.Background(), b, ActionOutcome{ID: "first", Result: "completed", AtMS: c.RelativeMS()})
			clock.advance(0)
			if ports.speaks != 1 {
				t.Fatal("farewell before script exhaustion")
			}
			clock.advance(2 * time.Second)
			_ = c.ActionObservation(context.Background(), b, ActionOutcome{ID: "last", Result: "completed", AtMS: c.RelativeMS()})
			clock.advance(0)
			want := 2
			if enabled {
				want++
			}
			if ports.speaks != want || ports.hangups != 0 {
				t.Fatalf("script exhaustion controls: speaks=%d, hangups=%d", ports.speaks, ports.hangups)
			}
			if !enabled {
				clock.advance(4 * time.Second)
				if ports.hangups != 0 {
					t.Fatal("opt-out did not preserve open conversation")
				}
			}
		})
	}
}

func observeCustomerHangup(t *testing.T, c *RunController, b Binding, observation Observation) {
	t.Helper()
	observation.AtMS = c.RelativeMS()
	if err := c.Observe(context.Background(), b, observation); err != nil {
		t.Fatal(err)
	}
}

func TestCustomerScriptExhaustionAfterUnsuccessfulAction(t *testing.T) {
	for _, result := range []string{"failed", "timed_out", "cancelled"} {
		t.Run(result, func(t *testing.T) {
			c, clock, ports := testController(t)
			s := sampleScenario()
			s.Actions = []Action{{ID: "last", Trigger: "connected", Occurrence: 1, Kind: "speak", Text: "Synthetic action.", MaxHoldMS: 1000}}
			snap, _ := c.Prepare(context.Background(), s)
			b := connect(t, c, clock, snap)
			clock.advance(0)
			if err := c.ActionObservation(context.Background(), b, ActionOutcome{ID: "last", Result: result, AtMS: c.RelativeMS()}); err != nil {
				t.Fatal(err)
			}
			clock.advance(0)
			if ports.speaks != 2 {
				t.Fatalf("consumed final %s action did not trigger farewell", result)
			}
			_ = c.ActionObservation(context.Background(), b, ActionOutcome{ID: CustomerFarewellActionID, Result: "completed", AtMS: c.RelativeMS()})
			clock.advance(3 * time.Second)
			if ports.hangups != 1 {
				t.Fatal("exhausted script did not request hangup")
			}
			_ = c.Handle(context.Background(), Event{Kind: "ended", Binding: b})
			report, err := c.Report(context.Background(), b.RunID)
			if err != nil || report.Phase != Completed || report.TerminationReason != "customer_hangup" {
				t.Fatalf("missing exhausted-script terminal report: %+v %v", report, err)
			}
			if result == "failed" && len(report.Failures) == 0 {
				t.Fatal("normal hangup erased failed action evidence")
			}
		})
	}
}

type failingScriptVoice struct{ *fakePorts }

func (v failingScriptVoice) Speak(ctx context.Context, b Binding, id, text string, hold time.Duration) error {
	if id != CustomerFarewellActionID {
		return errors.New("synthetic voice control failure")
	}
	return v.fakePorts.Speak(ctx, b, id, text, hold)
}

func TestCustomerScriptExhaustionAfterControlFailure(t *testing.T) {
	c, clock, ports := testController(t)
	c.opts.Voice = failingScriptVoice{ports}
	s := sampleScenario()
	s.Actions = []Action{{ID: "last", Trigger: "connected", Occurrence: 1, Kind: "speak", Text: "Synthetic action.", MaxHoldMS: 1000}}
	snap, _ := c.Prepare(context.Background(), s)
	_ = connect(t, c, clock, snap)
	clock.advance(0)
	if ports.speaks != 1 {
		t.Fatal("failed final control did not trigger farewell")
	}
}
