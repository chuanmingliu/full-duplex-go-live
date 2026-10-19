package simulator

import (
	"context"
	"testing"
	"time"
)

func TestMediaDisconnectWaitsForBoundedGatewayConfirmation(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		c, clock, _ := testController(t)
		ctx := context.Background()
		snap, err := c.Prepare(ctx, sampleScenario())
		if err != nil {
			t.Fatal(err)
		}
		b := connect(t, c, clock, snap)
		if err = c.MediaDisconnected(ctx, b); err != nil {
			t.Fatal(err)
		}
		clock.advance(600 * time.Millisecond)
		state, _ := c.Get(ctx, snap.RunID)
		if state.Phase != Connected {
			t.Fatalf("media disconnect raced valid gateway signaling: %s", state.Phase)
		}
		if confirmed {
			if err = c.Handle(ctx, Event{Kind: "ended", Binding: b, Termination: &Termination{NativeInitiator: "caller", NativeReason: "ByCaller"}}); err != nil {
				t.Fatal(err)
			}
		}
		clock.advance(c.opts.SetupTimeout)
		report, err := c.Report(ctx, snap.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if confirmed && (report.Phase != Completed || report.TerminationReason != "remote_hangup" || len(report.Failures) != 0) {
			t.Fatalf("confirmed call lost normal termination: %+v", report)
		}
		if !confirmed && (report.Phase != Failed || len(report.Failures) == 0) {
			t.Fatalf("missing confirmation did not fail by deadline: %+v", report)
		}
	}
}

func TestMediaDisconnectFreezesPendingCustomerWorkAndKeepsGaps(t *testing.T) {
	ctx := context.Background()
	c, clock, ports := testController(t)
	s := sampleScenario()
	s.Goal = "Complete the synthetic task."
	s.Actions = []Action{{ID: "pending", Trigger: "connected", Occurrence: 1, AfterMS: 1000, Kind: "speak", Text: "Synthetic action.", MaxHoldMS: 1000}}
	snap, err := c.Prepare(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	b := connect(t, c, clock, snap)
	observeCustomerHangup(t, c, b, Observation{Kind: "customer_audio", NonSilent: true})
	clock.advance(50 * time.Millisecond)
	observeCustomerHangup(t, c, b, Observation{Kind: "assistant_audio", NonSilent: true})
	if err := c.MediaDisconnected(ctx, b); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Second)
	// Also cover a scheduled callback that was already dispatched at close.
	c.performAction(b.RunID, s.Actions[0])
	_ = c.GoalReached(ctx, b)
	clock.advance(0)
	if ports.speaks != 0 || ports.hangups != 0 {
		t.Fatal("media teardown dispatched new customer work before native end")
	}
	if err := c.Handle(ctx, Event{Kind: "ended", Binding: b, Termination: &Termination{NativeInitiator: "caller", NativeReason: "ByCaller"}}); err != nil {
		t.Fatal(err)
	}
	report, err := c.Report(ctx, b.RunID)
	if err != nil || report.Phase != Completed || len(report.Failures) != 0 || len(report.AssistantGaps) != 1 || !report.AssistantGaps[0].Valid || report.AssistantGaps[0].ValueMS != 50 {
		t.Fatalf("media teardown lost completed report or audio boundaries: %+v %v", report, err)
	}
}
