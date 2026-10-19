package simulator

import (
	"context"
	"testing"
	"time"
)

func TestRemoteTurnLimitIgnoresAssistantSpeechSegments(t *testing.T) {
	ctx := context.Background()
	c, clock, ports := testController(t)
	c.opts.DataDir = t.TempDir()
	scenario := sampleScenario()
	scenario.MaxRemoteTurns = 40
	scenario.MaxDurationMS = 300000
	snap, err := c.Prepare(ctx, scenario)
	if err != nil {
		t.Fatal(err)
	}
	binding := connect(t, c, clock, snap)
	observe := func(v Observation) {
		t.Helper()
		v.AtMS = c.RelativeMS()
		if err := c.Observe(ctx, binding, v); err != nil {
			t.Fatal(err)
		}
	}
	for turn := 0; turn < 5; turn++ {
		observe(Observation{Kind: "customer_audio", NonSilent: true})
		clock.advance(100 * time.Millisecond)
		observe(Observation{Kind: "customer_audio", End: true})
		clock.advance(20 * time.Millisecond)
		for segment := 0; segment < 9; segment++ {
			observe(Observation{Kind: "assistant_audio", NonSilent: true})
			// Inferred speech ends at each pause, but only customer speech can
			// separate assistant turns for the scenario's turn budget.
			clock.advance(3 * time.Second)
			current, err := c.Get(ctx, snap.RunID)
			if err != nil || current.Phase != Connected {
				t.Fatalf("speech segment exhausted turn budget: %+v %v", current, err)
			}
		}
	}
	if ports.hangups != 0 {
		t.Fatal("speech segments requested a premature hangup")
	}
	if got := c.runs[snap.RunID].remoteTurns; got != 5 {
		t.Fatalf("remote turns = %d, want 5", got)
	}
	if err := c.Handle(ctx, Event{Kind: "ended", Binding: binding, AtMS: c.RelativeMS(), Termination: &Termination{NativeInitiator: "caller", NativeReason: "ByCaller"}}); err != nil {
		t.Fatal(err)
	}
	report, err := c.Report(ctx, snap.RunID)
	if err != nil || report.Phase != Completed || report.RemoteTurns != 5 || report.RemoteSpeechStarts != 45 || len(report.AssistantGaps) != 5 {
		t.Fatalf("incomplete report: %+v %v", report, err)
	}
	for _, gap := range report.AssistantGaps {
		if !gap.Valid || gap.ValueMS != 20 {
			t.Fatalf("lost response gap after clean hangup: %+v", gap)
		}
	}
	for _, check := range report.Checks {
		if (check.Name == "call_limits" || check.Name == "clean_termination") && check.Result != "pass" {
			t.Fatalf("clean call failed a protocol check: %+v", check)
		}
	}
	restored, err := NewController(c.opts)
	if err != nil {
		t.Fatal(err)
	}
	restoredReport, err := restored.Report(ctx, snap.RunID)
	if err != nil || restoredReport.RemoteTurns != report.RemoteTurns || restoredReport.RemoteSpeechStarts != report.RemoteSpeechStarts || len(restoredReport.AssistantGaps) != len(report.AssistantGaps) {
		t.Fatalf("turn counts or gaps were lost after restart: %+v %v", restoredReport, err)
	}
	if restored.runs[snap.RunID].remoteTurns != 5 || restored.runs[snap.RunID].remoteSpeechStarts != 45 {
		t.Fatal("persisted state did not distinguish turns from speech segments")
	}
}

func TestRemoteTurnLimitRequiresInterveningCustomerSpeech(t *testing.T) {
	ctx := context.Background()
	c, clock, ports := testController(t)
	scenario := sampleScenario()
	scenario.MaxRemoteTurns = 2
	snap, err := c.Prepare(ctx, scenario)
	if err != nil {
		t.Fatal(err)
	}
	binding := connect(t, c, clock, snap)
	observe := func(v Observation) {
		t.Helper()
		v.AtMS = c.RelativeMS()
		if err := c.Observe(ctx, binding, v); err != nil {
			t.Fatal(err)
		}
	}
	for segment := 0; segment < 3; segment++ {
		observe(Observation{Kind: "remote_speech_started"})
		observe(Observation{Kind: "remote_speech_ended"})
		// Silence and unmatched end markers are not customer utterances.
		observe(Observation{Kind: "customer_audio"})
		observe(Observation{Kind: "customer_audio", End: true})
		clock.advance(0)
		if ports.hangups != 0 {
			t.Fatal("assistant pause counted as a new turn")
		}
	}
	observe(Observation{ID: "customer-start", Kind: "customer_speech_started"})
	observe(Observation{Kind: "customer_speech_ended"})
	observe(Observation{Kind: "remote_speech_started"})
	clock.advance(0)
	current, err := c.Get(ctx, snap.RunID)
	if err != nil || current.Phase != Ending || ports.hangups != 1 || c.runs[snap.RunID].remoteTurns != 2 {
		t.Fatalf("assistant turn did not enforce limit: %+v %v", current, err)
	}
}
