package simulator

import (
	"context"
	"testing"
	"time"
)

func TestReceiverLimitsCompleteAndSettleReport(t *testing.T) {
	for _, limit := range []string{"duration_limit", "remote_turn_limit", "inactivity"} {
		for _, ending := range []string{"disconnect", "native", "deadline"} {
			t.Run(limit+"/"+ending, func(t *testing.T) {
				ctx := context.Background()
				c, clock, ports := testController(t)
				c.opts.DataDir = t.TempDir()
				s := sampleScenario()
				s.IdleTimeoutMS = 0
				if limit == "duration_limit" {
					s.MaxDurationMS = 1000
				}
				if limit == "remote_turn_limit" {
					s.MaxRemoteTurns = 1
				}
				if limit == "inactivity" {
					s.IdleTimeoutMS = 1000
				}
				snap, err := c.Prepare(ctx, s)
				if err != nil {
					t.Fatal(err)
				}
				b := connect(t, c, clock, snap)
				// Leave the silence debounce pending when the remote-turn stop fires.
				for i, kind := range []string{"customer_audio", "customer_audio", "assistant_audio"} {
					if i > 0 {
						clock.advance(20 * time.Millisecond)
					}
					if err = c.Observe(ctx, b, Observation{Kind: kind, NonSilent: true, AtMS: c.RelativeMS()}); err != nil {
						t.Fatal(err)
					}
				}
				if limit == "remote_turn_limit" {
					clock.advance(0)
				} else {
					clock.advance(time.Second)
				}
				if snap, _ := c.Get(ctx, b.RunID); snap.Phase != Ending || ports.hangups != 1 || ports.closes != 0 {
					t.Fatalf("limit did not request orderly stop: %+v hangups=%d closes=%d", snap, ports.hangups, ports.closes)
				}
				switch ending {
				case "disconnect":
					err = c.Handle(ctx, Event{Kind: "failed", Binding: b, Reason: "Active Call control disconnected"})
				case "native":
					err = c.Handle(ctx, Event{Kind: "ended", Binding: b, Termination: &Termination{NativeInitiator: "caller", NativeReason: "ByCaller"}})
				case "deadline":
					clock.advance(c.opts.SetupTimeout)
				}
				if err != nil {
					t.Fatal(err)
				}
				rep, err := c.Report(ctx, b.RunID)
				if err != nil || rep.Phase != Completed || rep.TerminationReason != limit || len(rep.Failures) != 0 {
					t.Fatalf("own stop became failure: phase=%s reason=%s failures=%v err=%v", rep.Phase, rep.TerminationReason, rep.Failures, err)
				}
				if rep.Termination.GatewayEnded != (ending == "native") {
					t.Fatalf("invented/lost native evidence: %+v", rep.Termination)
				}
				if len(rep.AssistantGaps) != 1 || !rep.AssistantGaps[0].Valid || rep.AssistantGaps[0].ValueMS != 20 {
					t.Fatalf("stop invalidated measured response: %+v", rep.AssistantGaps)
				}
				for _, name := range []string{"answered_once", "greeting_once", "call_limits", "clean_termination"} {
					if reportChecks(rep)[name] != "pass" {
						t.Fatalf("%s did not settle: %+v", name, rep.Checks)
					}
				}
				assertArtifactMatches(t, c, b.RunID)
				restored, err := NewController(c.opts)
				if err != nil {
					t.Fatal(err)
				}
				saved, err := restored.Report(ctx, b.RunID)
				if err != nil || saved.Phase != Completed || saved.TerminationReason != limit || !saved.AssistantGaps[0].Valid {
					t.Fatalf("settled report lost after restore: %+v %v", saved, err)
				}
			})
		}
	}
}

func TestReceiverStopWaitsForControlEffects(t *testing.T) {
	for _, ending := range []string{"disconnect", "native"} {
		for _, commandFails := range []bool{false, true} {
			t.Run(ending+"/"+map[bool]string{false: "success", true: "error"}[commandFails], func(t *testing.T) {
				ctx := context.Background()
				c, clock, base := testController(t)
				c.opts.DataDir = t.TempDir()
				p := &actionReturnPorts{fakePorts: base}
				c.opts.Telephony = p
				if commandFails {
					base.hangupErr = ErrNotReady
				}
				snap, err := c.Prepare(ctx, sampleScenario())
				if err != nil {
					t.Fatal(err)
				}
				b := connect(t, c, clock, snap)
				p.hangupHook = func() {
					e := Event{Kind: "failed", Binding: b, Reason: "Active Call control disconnected"}
					if ending == "native" {
						e.Kind, e.Reason = "ended", "Active Call ended"
					}
					if err := c.Handle(ctx, e); err != nil {
						t.Error(err)
					}
					assertHangupStillOccupied(t, c, b)
				}
				err = c.Handle(ctx, Event{Kind: "stop", Binding: b, Reason: "duration limit"})
				if (err != nil) != commandFails {
					t.Fatalf("control result: %v", err)
				}
				rep, err := c.Report(ctx, b.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if commandFails {
					if rep.Phase != Failed || len(rep.Failures) == 0 {
						t.Fatalf("command error masked: %+v", rep)
					}
				} else if rep.Phase != Completed || rep.TerminationReason != "duration_limit" || len(rep.Failures) != 0 {
					t.Fatalf("successful own stop failed: %+v", rep)
				}
				assertArtifactMatches(t, c, b.RunID)
			})
		}
	}
}

func TestReceiverStopWaitsForMediaCleanup(t *testing.T) {
	for _, closeFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "close-error"}[closeFails], func(t *testing.T) {
			ctx := context.Background()
			c, clock, base := testController(t)
			c.opts.DataDir = t.TempDir()
			p := &closeCallbackPorts{actionReturnPorts: &actionReturnPorts{fakePorts: base}}
			c.opts.Voice = p
			if closeFails {
				base.closeErr = ErrNotReady
			}
			snap, err := c.Prepare(ctx, sampleScenario())
			if err != nil {
				t.Fatal(err)
			}
			b := connect(t, c, clock, snap)
			if err = c.Handle(ctx, Event{Kind: "stop", Binding: b, Reason: "duration limit"}); err != nil {
				t.Fatal(err)
			}
			p.closeHook = func() { assertHangupStillOccupied(t, c, b) }
			err = c.Handle(ctx, Event{Kind: "failed", Binding: b, Reason: "Active Call control disconnected"})
			if (err != nil) != closeFails {
				t.Fatalf("cleanup result: %v", err)
			}
			rep, err := c.Report(ctx, b.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if closeFails {
				if rep.Phase != Failed || len(rep.Failures) == 0 {
					t.Fatalf("cleanup failure masked: %+v", rep)
				}
			} else if rep.Phase != Completed || len(rep.Failures) > 0 {
				t.Fatalf("successful cleanup failed: %+v", rep)
			}
			assertArtifactMatches(t, c, b.RunID)
		})
	}
}

func TestReceiverStopDoesNotMaskMediaFailure(t *testing.T) {
	ctx := context.Background()
	c, clock, _ := testController(t)
	snap, err := c.Prepare(ctx, sampleScenario())
	if err != nil {
		t.Fatal(err)
	}
	b := connect(t, c, clock, snap)
	if err = c.Handle(ctx, Event{Kind: "stop", Binding: b, Reason: "duration limit"}); err != nil {
		t.Fatal(err)
	}
	if err = c.Handle(ctx, Event{Kind: "failed", Binding: b, Reason: "media transport failed"}); err != nil {
		t.Fatal(err)
	}
	rep, err := c.Report(ctx, b.RunID)
	if err != nil || rep.Phase != Failed || rep.TerminationReason != "media transport failed" {
		t.Fatalf("media failure masked: %+v %v", rep, err)
	}
}
