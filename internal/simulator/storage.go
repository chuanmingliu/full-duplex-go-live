package simulator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func (c *RunController) persistSnapshot(r *run) error {
	if c.opts.DataDir == "" {
		return nil
	}
	return writeJSONAtomic(filepath.Join(c.opts.DataDir, r.state.Binding.RunID), "state.json", persistedRun{Snapshot: c.snapshot(r), PrepareKeyHash: r.idempotencyKeyHash, ScenarioHash: r.scenarioHash, PromptHash: r.promptHash, Provenance: r.provenance})
}
func (c *RunController) persistReport(r *run) error {
	if c.opts.DataDir == "" {
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
	r, ok := c.runs[id]
	if !ok {
		return Report{}, ErrNotFound
	}
	if !r.state.Phase.Terminal() {
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
		return rep, err
	}
	return buildReport(r, c.opts), ctx.Err()
}

type persistedRun struct {
	Snapshot
	PrepareKeyHash string           `json:"prepare_key_hash,omitempty"`
	ScenarioHash   string           `json:"scenario_hash,omitempty"`
	PromptHash     string           `json:"prompt_hash,omitempty"`
	Provenance     reportProvenance `json:"provenance"`
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
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("artifact exceeds limit")
	}
	return raw, nil
}
