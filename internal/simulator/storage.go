package simulator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func (c *RunController) persistSnapshot(r *run) error {
	if c.opts.DataDir == "" {
		return nil
	}
	return writeJSONAtomic(filepath.Join(c.opts.DataDir, r.state.Binding.RunID), "state.json", persistedRun{
		Snapshot: c.snapshot(r), CreatedAt: r.createdAt,
		ScenarioID: r.scenario.ID, ExpectedOutcome: r.scenario.ExpectedOutcome,
		RetainTranscript: r.scenario.RetainTranscript, PrepareKeyHash: r.idempotencyKeyHash,
		ScenarioHash: r.scenarioHash, PromptHash: r.promptHash, Provenance: r.provenance,
		Warnings: r.warnings, Termination: r.termination,
		MaxRemoteTurns: r.scenario.MaxRemoteTurns, RemoteSpeechStarts: r.remoteTurns,
		MaxDurationMS: r.scenario.MaxDurationMS,
	})
}
func (c *RunController) persistReport(r *run) error {
	if c.opts.DataDir == "" || !r.terminalReady() {
		return nil
	}
	dir := filepath.Join(c.opts.DataDir, r.state.Binding.RunID)
	if err := writeJSONAtomic(dir, "report.json", buildReport(r, c.opts)); err != nil {
		return err
	}
	if r.scenario.RetainTranscript {
		return writeJSONAtomic(dir, "transcript.json", r.observer.transcript)
	}
	return nil
}
func (c *RunController) restore() error {
	entries, err := os.ReadDir(c.opts.DataDir)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(c.opts.DataDir, 0700)
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !safeID(entry.Name()) {
			continue
		}
		if len(c.runs) >= 128 {
			return errors.New("persisted run capacity reached")
		}
		raw, err := readBoundedFile(filepath.Join(c.opts.DataDir, entry.Name(), "state.json"), MaxScenarioBytes)
		if err != nil {
			return fmt.Errorf("read run metadata: %w", err)
		}
		if len(raw) > MaxScenarioBytes {
			return errors.New("run metadata exceeds limit")
		}
		var metadata persistedRun
		if err = json.Unmarshal(raw, &metadata); err != nil {
			return fmt.Errorf("invalid run metadata: %w", err)
		}
		snap := metadata.Snapshot
		if snap.RunID != entry.Name() || snap.Binding.RunID != snap.RunID {
			return errors.New("run metadata identity mismatch")
		}
		runctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := &run{state: State{Phase: snap.Phase, Binding: snap.Binding}, reason: snap.Reason, idempotencyKeyHash: metadata.PrepareKeyHash, scenarioHash: metadata.ScenarioHash, promptHash: metadata.PromptHash, provenance: metadata.Provenance, expiresAt: snap.ReservationExpiresAt, observer: newObserver(), ctx: runctx, cancel: cancel}
		r.warnings = append([]Warning(nil), metadata.Warnings...)
		r.termination = metadata.Termination
		r.scenario.MaxRemoteTurns = metadata.MaxRemoteTurns
		r.scenario.MaxDurationMS = metadata.MaxDurationMS
		r.remoteTurns = metadata.RemoteSpeechStarts
		r.createdAt = metadata.CreatedAt
		// Older metadata lacks creation time; expiry is a deterministic fallback.
		if r.createdAt.IsZero() {
			r.createdAt = snap.ReservationExpiresAt
		}
		r.scenario.ID = metadata.ScenarioID
		r.scenario.ExpectedOutcome = metadata.ExpectedOutcome
		r.scenario.RetainTranscript = metadata.RetainTranscript
		if !snap.Phase.Terminal() {
			r.state.Phase = Aborted
			r.reason = "process restart"
		}
		c.runs[snap.RunID] = r
		if metadata.PrepareKeyHash != "" {
			c.keys[metadata.PrepareKeyHash] = idempotency{hash: metadata.ScenarioHash, id: snap.RunID}
		}
		if r.state.Binding.CallID != "" {
			c.seenCalls[r.state.Binding.CallID] = snap.RunID
		}
		if r.state.Phase == Aborted {
			if err = c.persistSnapshot(r); err != nil {
				return err
			}
			if err = c.persistReport(r); err != nil {
				return err
			}
		}
	}
	return nil
}
func (c *RunController) Report(ctx context.Context, id string) (Report, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	return c.reportLocked(id)
}

func (c *RunController) reportLocked(id string) (Report, error) {
	r, ok := c.runs[id]
	if !ok {
		return Report{}, ErrNotFound
	}
	if !r.terminalReady() {
		return Report{}, ErrNotReady
	}
	if c.opts.DataDir != "" && r.scenario.SchemaVersion == 0 {
		raw, err := readBoundedFile(filepath.Join(c.opts.DataDir, id, "report.json"), 1<<20)
		if err != nil {
			return Report{}, err
		}
		if len(raw) > 1<<20 {
			return Report{}, errors.New("report artifact exceeds limit")
		}
		var rep Report
		err = json.Unmarshal(raw, &rep)
		if err == nil && (rep.RunID != id || rep.ScenarioHash != r.scenarioHash || rep.PromptHash != r.promptHash) {
			return Report{}, errors.New("report artifact identity mismatch")
		}
		return rep, err
	}
	return buildReport(r, c.opts), nil
}

type TranscriptArtifact struct {
	SchemaVersion int               `json:"schema_version"`
	RunID         string            `json:"run_id"`
	ScenarioHash  string            `json:"scenario_hash"`
	PromptHash    string            `json:"prompt_hash"`
	Truncated     bool              `json:"truncated"`
	Entries       []TranscriptEntry `json:"entries"`
}

// Transcript returns an independent copy only after the retained terminal report
// authorizes access. Restored paths always derive from the known run's safe ID.
func (c *RunController) Transcript(ctx context.Context, id string) (TranscriptArtifact, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TranscriptArtifact{}, err
	}
	rep, err := c.reportLocked(id)
	if err != nil {
		return TranscriptArtifact{}, err
	}
	if !rep.TranscriptRetained {
		return TranscriptArtifact{}, fmt.Errorf("%w: transcript retention disabled", ErrConflict)
	}
	r := c.runs[id]
	entries := r.observer.transcript
	if r.scenario.SchemaVersion == 0 {
		root, err := os.OpenRoot(filepath.Join(c.opts.DataDir, r.state.Binding.RunID))
		if err != nil {
			return TranscriptArtifact{}, err
		}
		defer root.Close()
		// Root.Open prevents a transcript symlink from escaping this run's directory.
		file, err := root.Open("transcript.json")
		if err != nil {
			return TranscriptArtifact{}, err
		}
		defer file.Close()
		raw, err := readBounded(file, 1<<20)
		if err != nil {
			return TranscriptArtifact{}, err
		}
		if err = json.Unmarshal(raw, &entries); err != nil {
			return TranscriptArtifact{}, err
		}
		if len(entries) > 256 {
			return TranscriptArtifact{}, errors.New("transcript entry limit exceeded")
		}
	}
	copyEntries := make([]TranscriptEntry, len(entries))
	for i, entry := range entries {
		if (entry.Role != "assistant" && entry.Role != "customer") || entry.AtMS < 0 {
			return TranscriptArtifact{}, errors.New("invalid transcript entry")
		}
		entry.Text = redact(entry.Text)
		copyEntries[i] = entry
	}
	return TranscriptArtifact{SchemaVersion: 1, RunID: id, ScenarioHash: rep.ScenarioHash, PromptHash: rep.PromptHash, Truncated: rep.TranscriptTruncated, Entries: copyEntries}, nil
}

type persistedRun struct {
	Snapshot
	CreatedAt          time.Time        `json:"created_at,omitempty"`
	ScenarioID         string           `json:"scenario_id,omitempty"`
	ExpectedOutcome    string           `json:"expected_outcome,omitempty"`
	RetainTranscript   bool             `json:"retain_transcript,omitempty"`
	PrepareKeyHash     string           `json:"prepare_key_hash,omitempty"`
	ScenarioHash       string           `json:"scenario_hash,omitempty"`
	PromptHash         string           `json:"prompt_hash,omitempty"`
	Provenance         reportProvenance `json:"provenance"`
	Warnings           []Warning        `json:"warnings,omitempty"`
	Termination        Termination      `json:"termination"`
	MaxRemoteTurns     int              `json:"max_remote_turns"`
	RemoteSpeechStarts int              `json:"remote_speech_start_count"`
	MaxDurationMS      int64            `json:"max_duration_ms"`
}

type reportProvenance struct {
	SourceCommit  string `json:"source_commit"`
	ProviderModel string `json:"provider_model"`
	VoiceID       string `json:"voice_id"`
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readBounded(f, limit)
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("artifact exceeds limit")
	}
	return raw, nil
}
