package simulator

import (
	"context"
	"testing"
	"time"
)

func TestStopKeepsMediaOpenUntilGatewayConfirmsEnd(t *testing.T) {
	ctx := context.Background()
	c, clock, p := testController(t)
	snap, _ := c.Prepare(ctx, sampleScenario())
	b := connect(t, c, clock, snap)
	if err := c.Stop(ctx, b.RunID); err != nil {
		t.Fatal(err)
	}
	if p.hangups != 1 || p.closes != 0 {
		t.Fatalf("stop cut media before native hangup: hangups=%d closes=%d", p.hangups, p.closes)
	}
	if _, err := c.Report(ctx, b.RunID); err != ErrNotReady {
		t.Fatalf("stop published unconfirmed report: %v", err)
	}
	if err := c.Handle(ctx, Event{Kind: "ended", Binding: b}); err != nil {
		t.Fatal(err)
	}
	if p.closes != 1 {
		t.Fatalf("native end did not close voice exactly once: %d", p.closes)
	}
}

func TestStopStillClosesMediaAfterMissingConfirmationDeadline(t *testing.T) {
	ctx := context.Background()
	c, clock, p := testController(t)
	snap, _ := c.Prepare(ctx, sampleScenario())
	b := connect(t, c, clock, snap)
	if err := c.Stop(ctx, b.RunID); err != nil {
		t.Fatal(err)
	}
	clock.advance(c.opts.SetupTimeout + time.Millisecond)
	rep, err := c.Report(ctx, b.RunID)
	if err != nil || rep.Phase != Failed || p.closes != 1 || p.hangups != 1 {
		t.Fatalf("unconfirmed stop leaked media: %+v %v closes=%d hangups=%d", rep, err, p.closes, p.hangups)
	}
}

func TestTerminationReportsLocalRemoteAndUnknownEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, initiator, reason, kind, side, evidence string
		stop                                          bool
	}{
		{name: "caller-bye", initiator: "caller", reason: "ByCaller", kind: "ended", side: "caller", evidence: "native_gateway_hangup"},
		{name: "gateway-bye", initiator: "callee", reason: "ByCallee", kind: "ended", side: "receiver_gateway", evidence: "native_gateway_hangup"},
		{name: "system-hangup", initiator: "system", reason: "Autohangup", kind: "ended", side: "receiver_gateway", evidence: "native_gateway_hangup"},
		{name: "default-system", initiator: "system", reason: "BySystem", kind: "ended", side: "unknown", evidence: "native_gateway_hangup"},
		{name: "ambiguous-system", initiator: "system", reason: "ByCaller", kind: "ended", side: "unknown", evidence: "native_gateway_hangup"},
		{name: "disconnect", kind: "failed", reason: "Active Call control disconnected", side: "unknown", evidence: "control_disconnected"},
		{name: "local-stop", kind: "ended", side: "receiver_gateway", evidence: "local_hangup_request", stop: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			c, clock, _ := testController(t)
			c.opts.DataDir = t.TempDir()
			snap, _ := c.Prepare(ctx, sampleScenario())
			b := connect(t, c, clock, snap)
			if tc.stop {
				if err := c.Stop(ctx, b.RunID); err != nil {
					t.Fatal(err)
				}
			}
			event := Event{Kind: tc.kind, Binding: b, Reason: tc.reason}
			if tc.initiator != "" {
				event.Termination = &Termination{NativeInitiator: tc.initiator, NativeReason: tc.reason}
			}
			if err := c.Handle(ctx, event); err != nil {
				t.Fatal(err)
			}
			rep, err := c.Report(ctx, b.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Termination.Side != tc.side || rep.Termination.Evidence != tc.evidence || rep.Termination.GatewayEnded != (tc.kind == "ended") {
				t.Fatalf("wrong termination: %+v", rep.Termination)
			}
			restored, err := NewController(c.opts)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := restored.Report(ctx, b.RunID)
			if err != nil || saved.Termination != rep.Termination {
				t.Fatalf("termination lost on restore: %+v %v", saved.Termination, err)
			}
		})
	}
}

func TestActiveCallRetainsScopedNativeHangupFields(t *testing.T) {
	h := newActiveCallHarness(t)
	if h.webhook("native-end", "sip:"+activeCallTestRun+"@simulator.invalid").Code != 204 {
		t.Fatal("invite failed")
	}
	conn := activeCallReceive(t, h.attached)
	for _, v := range []map[string]any{
		{"event": "hangup", "trackId": "foreign", "initiator": "caller", "reason": "ByCaller"},
		{"event": "hangup", "trackId": "native-end", "initiator": "caller", "reason": "ByCaller", "refer": true},
		{"event": "hangup", "trackId": "native-end", "initiator": "callee", "reason": "ByCallee"},
	} {
		if err := conn.WriteJSON(v); err != nil {
			t.Fatal(err)
		}
	}
	e := activeCallReceive(t, h.controller.events)
	if e.Termination == nil || e.Termination.NativeInitiator != "callee" || e.Termination.NativeReason != "ByCallee" {
		t.Fatalf("native evidence missing or foreign: %+v", e)
	}
}

func TestReportRetainsEffectiveTurnLimitAndLocalStopBeforeDisconnect(t *testing.T) {
	ctx := context.Background()
	c, clock, _ := testController(t)
	c.opts.DataDir = t.TempDir()
	scenario := sampleScenario()
	scenario.MaxRemoteTurns = 2
	snap, err := c.Prepare(ctx, scenario)
	if err != nil {
		t.Fatal(err)
	}
	b := connect(t, c, clock, snap)
	for i := 0; i < 2; i++ {
		for _, kind := range []string{"remote_speech_started", "remote_speech_ended"} {
			if err = c.Observe(ctx, b, Observation{Kind: kind, AtMS: c.RelativeMS()}); err != nil {
				t.Fatal(err)
			}
		}
	}
	clock.advance(0)
	if err = c.Handle(ctx, Event{Kind: "failed", Binding: b, Reason: "Active Call control disconnected"}); err != nil {
		t.Fatal(err)
	}
	rep, err := c.Report(ctx, b.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.MaxRemoteTurns != 2 || rep.RemoteSpeechStarts != 2 || rep.MaxDurationMS != scenario.MaxDurationMS || rep.Termination.LocalReason != "remote turn limit" || rep.Termination.GatewayEnded || !rep.Termination.LocalHangupRequested {
		t.Fatalf("lost preceding stop evidence or limits: %+v", rep)
	}
}

func TestNativeTerminationDoesNotRetainArbitraryVendorText(t *testing.T) {
	ctx := context.Background()
	c, clock, _ := testController(t)
	snap, _ := c.Prepare(ctx, sampleScenario())
	b := connect(t, c, clock, snap)
	if err := c.Handle(ctx, Event{Kind: "ended", Binding: b, Termination: &Termination{NativeInitiator: "caller with secret", NativeReason: "raw provider phrase"}}); err != nil {
		t.Fatal(err)
	}
	rep, err := c.Report(ctx, b.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Termination.Side != "unknown" || rep.Termination.NativeInitiator != "" || rep.Termination.NativeReason != "other" {
		t.Fatalf("untrusted native fields retained: %+v", rep.Termination)
	}
}

func TestNativeEndAfterMediaFailureAddsAttributionWithoutErasingFailure(t *testing.T) {
	ctx := context.Background()
	c, clock, _ := testController(t)
	c.opts.DataDir = t.TempDir()
	snap, _ := c.Prepare(ctx, sampleScenario())
	b := connect(t, c, clock, snap)
	if err := c.Handle(ctx, Event{Kind: "failed", Binding: b, Reason: "media transport failed"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Handle(ctx, Event{Kind: "ended", Binding: b, Termination: &Termination{NativeInitiator: "caller", NativeReason: "ByCaller"}}); err != nil {
		t.Fatal(err)
	}
	rep, err := c.Report(ctx, b.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Phase != Failed || rep.Termination.Side != "caller" || !rep.Termination.GatewayEnded || len(rep.Failures) != 1 {
		t.Fatalf("late native attribution lost or erased failure: %+v", rep)
	}
}
