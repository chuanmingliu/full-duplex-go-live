package simulator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoverableWarningKeepsCallAndOtherResponseGapsValid(t *testing.T) {
	ctx := context.Background()
	c, clock, ports := testController(t)
	c.opts.DataDir = t.TempDir()
	snap, err := c.Prepare(ctx, sampleScenario())
	if err != nil {
		t.Fatal(err)
	}
	b := connect(t, c, clock, snap)
	observe := func(kind, id string) {
		t.Helper()
		if err := c.Observe(ctx, b, Observation{Kind: kind, ResponseID: id, AtMS: c.RelativeMS()}); err != nil {
			t.Fatal(err)
		}
		clock.advance(10 * time.Millisecond)
	}
	// Three complete measured boundaries; only response 2 loses a TTS segment.
	for _, id := range []string{"response-1", "response-2", "response-3"} {
		observe("customer_speech_started", id)
		observe("customer_speech_ended", id)
		observe("remote_speech_started", "")
		observe("remote_speech_ended", "")
		if id == "response-2" {
			if err := c.Warning(ctx, b, "tts_chunk_failed", id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if ports.hangups != 0 {
		t.Fatal("recoverable warning hung up the call")
	}
	current, err := c.Get(ctx, b.RunID)
	if err != nil || current.Phase != Connected {
		t.Fatalf("warning ended run: %+v %v", current, err)
	}
	if err := c.Handle(ctx, Event{Kind: "ended", Binding: b}); err != nil {
		t.Fatal(err)
	}
	rep, err := c.Report(ctx, b.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Phase != Completed || len(rep.AssistantGaps) != 3 || !rep.AssistantGaps[0].Valid || rep.AssistantGaps[1].Valid || !rep.AssistantGaps[2].Valid {
		t.Fatalf("wrong gaps after warning: %+v", rep.AssistantGaps)
	}
	if len(rep.Failures) != 1 || !strings.HasPrefix(rep.Failures[0], "warning:") || len(rep.Warnings) != 1 || rep.Warnings[0].Count != 1 {
		t.Fatalf("missing warning: %+v", rep)
	}
	for _, check := range rep.Checks {
		if check.Name == "clean_termination" && check.Result != "pass" {
			t.Fatalf("warning poisoned termination: %+v", check)
		}
	}
	restored, err := NewController(c.opts)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := restored.Report(ctx, b.RunID)
	if err != nil || len(saved.Warnings) != 1 || saved.Warnings[0].Count != 1 || !saved.AssistantGaps[0].Valid || saved.AssistantGaps[1].Valid {
		t.Fatalf("warning report lost after restart: %+v %v", saved, err)
	}
}

func TestWarningBeforeAudioInvalidatesOnlyMatchingResponseAndSurvivesAbort(t *testing.T) {
	ctx := context.Background()
	c, clock, _ := testController(t)
	c.opts.DataDir = t.TempDir()
	snap, _ := c.Prepare(ctx, sampleScenario())
	b := connect(t, c, clock, snap)
	for i := 0; i < 100; i++ {
		if err := c.Warning(ctx, b, "tts_chunk_failed", "response-1"); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []Observation{{Kind: "customer_speech_started", ResponseID: "response-1"}, {Kind: "customer_speech_ended", ResponseID: "response-1"}, {Kind: "remote_speech_started"}} {
		v.AtMS = c.RelativeMS()
		if err := c.Observe(ctx, b, v); err != nil {
			t.Fatal(err)
		}
		clock.advance(time.Millisecond)
	}
	if c.runs[b.RunID].observer.assistantGaps()[0].Valid {
		t.Fatal("audio arriving after failed segment became successful turn")
	}
	restarted, err := NewController(c.opts)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := restarted.Report(ctx, b.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Phase != Aborted || len(rep.Warnings) != 1 || rep.Warnings[0].Count != 100 || len(rep.Failures) != 1 {
		t.Fatalf("restart lost bounded warning count: %+v", rep)
	}
	if err := c.Warning(ctx, Binding{RunID: b.RunID, CallID: b.CallID, SessionID: "other"}, "tts_chunk_failed", "response-1"); err == nil {
		t.Fatal("foreign session warning admitted")
	}
	if err := c.Warning(ctx, b, "vendor secret text", "response-1"); err == nil {
		t.Fatal("untrusted warning text admitted")
	}
}

func TestOldReportWithoutTerminationOrWarningsRemainsReadable(t *testing.T) {
	ctx := context.Background()
	c, _, _ := testController(t)
	c.opts.DataDir = t.TempDir()
	snap, _ := c.Prepare(ctx, sampleScenario())
	if err := c.Stop(ctx, snap.RunID); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.opts.DataDir, snap.RunID, "report.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "termination")
	delete(fields, "warnings")
	raw, _ = json.Marshal(fields)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewController(c.opts)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := restarted.Report(ctx, snap.RunID)
	if err != nil || rep.RunID != snap.RunID {
		t.Fatalf("old artifact rejected: %+v %v", rep, err)
	}
}

func TestWarningAssociatesEveryResponseSharingAnAudioBoundary(t *testing.T) {
	ctx := context.Background()
	c, clock, _ := testController(t)
	snap, _ := c.Prepare(ctx, sampleScenario())
	b := connect(t, c, clock, snap)
	for _, id := range []string{"response-1", "response-2"} {
		if err := c.Observe(ctx, b, Observation{Kind: "customer_audio", NonSilent: true, ResponseID: id, AtMS: c.RelativeMS()}); err != nil {
			t.Fatal(err)
		}
		clock.advance(time.Millisecond)
	}
	if err := c.Warning(ctx, b, "tts_chunk_failed", "response-1"); err != nil {
		t.Fatal(err)
	}
	if !c.runs[b.RunID].observer.assistantTurns[0].Failed {
		t.Fatal("later response replaced dropped response identity in shared speech boundary")
	}
}

func TestRecoverableWarningDoesNotHideActualRunFailure(t *testing.T) {
	ctx := context.Background()
	c, clock, _ := testController(t)
	snap, _ := c.Prepare(ctx, sampleScenario())
	b := connect(t, c, clock, snap)
	for _, kind := range []string{"customer_speech_started", "customer_speech_ended", "remote_speech_started"} {
		if err := c.Observe(ctx, b, Observation{Kind: kind, ResponseID: "response-1", AtMS: c.RelativeMS()}); err != nil {
			t.Fatal(err)
		}
		clock.advance(time.Millisecond)
	}
	if err := c.Warning(ctx, b, "tts_open_failed", "response-2"); err != nil {
		t.Fatal(err)
	}
	if err := c.Handle(ctx, Event{Kind: "failed", Binding: b, Reason: "actual transport failure"}); err != nil {
		t.Fatal(err)
	}
	rep, err := c.Report(ctx, b.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Phase != Failed || len(rep.Failures) != 2 || len(rep.AssistantGaps) != 1 || rep.AssistantGaps[0].Valid {
		t.Fatalf("warning suppressed fatal evidence: %+v", rep)
	}
}

func TestReportHasFatalFailuresOnlyExemptsExactStructuredWarnings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report Report
		fatal  bool
	}{
		{name: "clean", report: Report{}, fatal: false},
		{name: "old-fatal", report: Report{Failures: []string{"transport failed"}}, fatal: true},
		{name: "old-warning-text-is-not-evidence", report: Report{Failures: []string{"warning: tts_chunk_failed (count=1)"}}, fatal: true},
		{name: "known-warning", report: Report{Failures: []string{"warning: tts_chunk_failed (count=2)"}, Warnings: []Warning{{Code: "tts_chunk_failed", Count: 2}}}, fatal: false},
		{name: "warning-and-fatal", report: Report{Failures: []string{"warning: tts_chunk_failed (count=2)", "transport failed"}, Warnings: []Warning{{Code: "tts_chunk_failed", Count: 2}}}, fatal: true},
		{name: "missing-projection", report: Report{Warnings: []Warning{{Code: "tts_chunk_failed", Count: 1}}}, fatal: true},
		{name: "mismatched-count", report: Report{Failures: []string{"warning: tts_chunk_failed (count=2)"}, Warnings: []Warning{{Code: "tts_chunk_failed", Count: 1}}}, fatal: true},
		{name: "unknown-code", report: Report{Failures: []string{"warning: unknown (count=1)"}, Warnings: []Warning{{Code: "unknown", Count: 1}}}, fatal: true},
		{name: "zero-count", report: Report{Failures: []string{"warning: tts_chunk_failed (count=0)"}, Warnings: []Warning{{Code: "tts_chunk_failed", Count: 0}}}, fatal: true},
		{name: "duplicate-code", report: Report{Failures: []string{"warning: tts_chunk_failed (count=1)"}, Warnings: []Warning{{Code: "tts_chunk_failed", Count: 1}, {Code: "tts_chunk_failed", Count: 1}}}, fatal: true},
		{name: "duplicate-projection", report: Report{Failures: []string{"warning: tts_chunk_failed (count=1)", "warning: tts_chunk_failed (count=1)"}, Warnings: []Warning{{Code: "tts_chunk_failed", Count: 1}}}, fatal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.report.HasFatalFailures(); got != tc.fatal {
				t.Fatalf("fatal=%v want=%v", got, tc.fatal)
			}
		})
	}
}
