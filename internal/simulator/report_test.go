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

func TestReportRetentionRedactionAndMissingEvidence(t *testing.T) {
	for _, retain := range []bool{false, true} {
		ctx := context.Background()
		c, f, _ := testController(t)
		c.opts.DataDir = t.TempDir()
		c.opts.SourceCommit = "synthetic-source"
		c.opts.ProviderModel = "synthetic-model"
		c.opts.VoiceID = "synthetic-voice"
		s := sampleScenario()
		s.RetainTranscript = retain
		snap, err := c.Prepare(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		b := connect(t, c, f, snap)
		_ = c.Observe(ctx, b, Observation{Kind: "transcript", Role: "assistant", AtMS: c.RelativeMS(), Text: "synthetic callback at +1 (212) 555-0123"})
		_ = c.Observe(ctx, b, Observation{Kind: "transcript", Role: "customer", AtMS: c.RelativeMS(), Text: "secret credential"})
		_ = c.Observe(ctx, b, Observation{Kind: "customer_speech_started", AtMS: c.RelativeMS()})
		f.advance(10 * time.Millisecond)
		_ = c.Handle(ctx, Event{Kind: "failed", Binding: b, AtMS: c.RelativeMS(), Reason: "token=synthetic-secret"})
		rep, err := c.Report(ctx, b.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if rep.SourceCommit != "synthetic-source" || rep.ScenarioHash == "" || rep.PromptHash == "" {
			t.Fatal("missing provenance")
		}
		if len(rep.AssistantGaps) != 1 || rep.AssistantGaps[0].Valid {
			t.Fatal("ongoing failed customer turn omitted")
		}
		for _, check := range rep.Checks {
			if strings.HasPrefix(check.Name, "appointment") && check.Result != "indeterminate" {
				t.Fatal("unsupported semantic pass")
			}
		}
		dir := filepath.Join(c.opts.DataDir, b.RunID)
		report, err := os.ReadFile(filepath.Join(dir, "report.json"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(report), "synthetic-secret") || strings.Contains(string(report), "555-0123") {
			t.Fatal("report leaked data")
		}
		transcript, err := os.ReadFile(filepath.Join(dir, "transcript.json"))
		if !retain {
			if !os.IsNotExist(err) {
				t.Fatal("default retained transcript")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(transcript), "555-0123") || strings.Contains(string(transcript), "secret credential") {
				t.Fatal("transcript leaked data")
			}
			info, _ := os.Stat(filepath.Join(dir, "transcript.json"))
			if info.Mode().Perm() != 0600 {
				t.Fatal("transcript permissions")
			}
		}
	}
}
func TestReportBoundedFeedAndRestart(t *testing.T) {
	ctx := context.Background()
	c, _, p := testController(t)
	c.opts.DataDir = t.TempDir()
	c.opts.SourceCommit = "pinned-source"
	snap, err := c.PrepareIdempotent(ctx, "retry-key", sampleScenario())
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	for i := 0; i < 600; i++ {
		c.record(c.runs[snap.RunID], "synthetic_event", "")
	}
	c.mu.Unlock()
	events, err := c.Events(ctx, snap.RunID, 0)
	if err != nil || len(events.Events) != 512 || !events.Truncated || events.Next != 601 {
		t.Fatalf("unbounded feed: %+v %v", events, err)
	}
	opts := c.opts
	opts.SourceCommit = "new-source"
	restarted, err := NewController(opts)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.PrepareIdempotent(ctx, "retry-key", sampleScenario())
	if err != nil || replayed.RunID != snap.RunID || replayed.Phase != Aborted {
		t.Fatalf("restart replayed control: %+v %v", replayed, err)
	}
	if p.accepts != 0 || p.hangups != 0 {
		t.Fatal("restart sent effects")
	}
	rep, err := restarted.Report(ctx, snap.RunID)
	if err != nil || rep.SourceCommit != "pinned-source" || rep.PromptHash == "" {
		t.Fatalf("restart provenance lost: %+v %v", rep, err)
	}
	path := filepath.Join(opts.DataDir, snap.RunID, "report.json")
	if err = os.WriteFile(path, []byte(`{"broken":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Report(ctx, snap.RunID); err == nil {
		t.Fatal("truncated artifact accepted")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Report(ctx, snap.RunID); err == nil {
		t.Fatal("missing artifact accepted")
	}
	raw, _ := json.Marshal(sampleScenario())
	if !strings.Contains(string(raw), `"retain_audio":false`) {
		t.Fatal("retention default")
	}
}
func TestScenarioFixtures(t *testing.T) {
	entries, err := os.ReadDir("../../scenarios/simulator")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		f, err := os.Open(filepath.Join("../../scenarios/simulator", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		_, err = DecodeScenario(f)
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		count++
	}
	if count != 5 {
		t.Fatal("expected five synthetic fixtures")
	}
}
