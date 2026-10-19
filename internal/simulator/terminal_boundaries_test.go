package simulator

import (
	"context"
	"testing"
	"time"
)

func TestNormalEndSettlesPendingAudioBoundaries(t *testing.T) {
	for _, local := range []bool{false, true} {
		name := "remote"
		if local {
			name = "local"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			c, clock, _ := testController(t)
			c.opts.DataDir = t.TempDir()
			snap, err := c.Prepare(ctx, sampleScenario())
			if err != nil {
				t.Fatal(err)
			}
			b := connect(t, c, clock, snap)
			observe := func(kind string) {
				t.Helper()
				if err := c.Observe(ctx, b, Observation{Kind: kind, NonSilent: true, AtMS: c.RelativeMS()}); err != nil {
					t.Fatal(err)
				}
			}
			observe("customer_audio")
			clock.advance(20 * time.Millisecond)
			observe("customer_audio")
			clock.advance(40 * time.Millisecond)
			observe("assistant_audio")
			clock.advance(20 * time.Millisecond)
			observe("assistant_audio")
			if local {
				if err := c.Stop(ctx, b.RunID); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.Handle(ctx, Event{Kind: "ended", Binding: b}); err != nil {
				t.Fatal(err)
			}
			rep, err := c.Report(ctx, b.RunID)
			if err != nil || rep.Phase != Completed || len(rep.Failures) != 0 {
				t.Fatalf("normal end did not complete: %+v %v", rep, err)
			}
			if len(rep.AssistantGaps) != 1 || !rep.AssistantGaps[0].Valid || rep.AssistantGaps[0].ValueMS != 40 {
				t.Fatalf("pending customer end lost its observed response gap: %+v", rep.AssistantGaps)
			}
			for _, check := range rep.Checks {
				if check.Name == "clean_termination" && check.Result != "pass" {
					t.Fatalf("normal ending lost termination evidence: %+v", check)
				}
			}
			clock.advance(time.Second)
			assertArtifactMatches(t, c, b.RunID)
		})
	}
}

func TestNormalEndDoesNotInventMissingResponseOrSpeechEnd(t *testing.T) {
	for _, kind := range []string{"customer_audio", "customer_speech_started"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			c, clock, _ := testController(t)
			snap, err := c.Prepare(ctx, sampleScenario())
			if err != nil {
				t.Fatal(err)
			}
			b := connect(t, c, clock, snap)
			if err := c.Observe(ctx, b, Observation{Kind: kind, NonSilent: true, AtMS: c.RelativeMS()}); err != nil {
				t.Fatal(err)
			}
			if err := c.Handle(ctx, Event{Kind: "ended", Binding: b}); err != nil {
				t.Fatal(err)
			}
			rep, err := c.Report(ctx, b.RunID)
			if err != nil || len(rep.AssistantGaps) != 1 || rep.AssistantGaps[0].Valid {
				t.Fatalf("ending invented a response gap: %+v %v", rep.AssistantGaps, err)
			}
			want := "missing assistant response"
			if kind == "customer_speech_started" {
				want = "missing customer speech end"
			}
			if rep.AssistantGaps[0].Reason != want {
				t.Fatalf("wrong missing evidence: %+v", rep.AssistantGaps)
			}
		})
	}
}

func TestNormalEndPreservesRemoteBoundaryAndRealOverlap(t *testing.T) {
	for _, overlap := range []bool{false, true} {
		name := "remote_end"
		if overlap {
			name = "overlap"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			c, clock, _ := testController(t)
			snap, err := c.Prepare(ctx, sampleScenario())
			if err != nil {
				t.Fatal(err)
			}
			b := connect(t, c, clock, snap)
			observe := func(kind string) {
				t.Helper()
				if err := c.Observe(ctx, b, Observation{Kind: kind, NonSilent: true, AtMS: c.RelativeMS()}); err != nil {
					t.Fatal(err)
				}
			}
			observe("assistant_audio")
			clock.advance(40 * time.Millisecond)
			observe("customer_audio")
			clock.advance(20 * time.Millisecond)
			if overlap {
				observe("assistant_audio")
			}
			observe("customer_audio")
			if err := c.Handle(ctx, Event{Kind: "ended", Binding: b}); err != nil {
				t.Fatal(err)
			}
			rep, err := c.Report(ctx, b.RunID)
			if err != nil || len(rep.SimulatorGaps) != 1 {
				t.Fatalf("missing simulator gap: %+v %v", rep.SimulatorGaps, err)
			}
			gap := rep.SimulatorGaps[0]
			if overlap {
				if gap.Valid || gap.Reason != "overlapping speech" {
					t.Fatalf("ending erased real overlap: %+v", gap)
				}
			} else if !gap.Valid || gap.ValueMS != 40 {
				t.Fatalf("ending lost pending remote boundary: %+v", gap)
			}
		})
	}
}
