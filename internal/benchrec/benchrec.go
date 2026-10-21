// Package benchrec is the one record every layer of the standing evaluation
// writes: journal/benchmarks/<date>-<build>-<layer>.json. The journal reads
// the directory and shows a scorecard per build; `make eval` fills it from
// the mocks so a commit carries its own numbers. A record is flat on purpose:
// a layer name, a build, where it ran, a map of metric → number, and a
// pointer to the run's own report for anyone who wants the rest.
package benchrec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Record is one layer's scorecard for one build.
type Record struct {
	// Layer: L1 turn decision (TurnBench), L2 duplex behaviour (golivefdb),
	// L3 latency (golivebench), L4 task and conversation (τ-voice, the
	// simulated customer).
	Layer string `json:"layer"`
	Build string `json:"build"`
	At    time.Time `json:"at"`
	// Source says what spoke: "mocks", "box" (real providers on the test
	// box), "phone" (a call), "sim" (the simulated customer).
	Source string `json:"source"`
	// Metrics are the layer's figures, by the name the layer's report uses
	// (R_R, L_R_ms, S_BC, p50_ms, p95_ms, …). Rates are 0–1.
	Metrics map[string]float64 `json:"metrics"`
	// Counts are the sample sizes behind the metrics (n_turns, n_yields).
	Counts map[string]int `json:"counts,omitempty"`
	// Thresholds are the bars this layer is held to, by metric name, with
	// the direction: ">=0.9" or "<=1200".
	Thresholds map[string]string `json:"thresholds,omitempty"`
	// Report is the run's own file, relative to the repository or absolute.
	Report string `json:"report,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Verdicts says which thresholds the record meets.
func (r Record) Verdicts() map[string]bool {
	out := map[string]bool{}
	for name, bar := range r.Thresholds {
		v, ok := r.Metrics[name]
		if !ok {
			continue
		}
		var limit float64
		switch {
		case strings.HasPrefix(bar, ">="):
			fmt.Sscanf(bar[2:], "%g", &limit)
			out[name] = v >= limit
		case strings.HasPrefix(bar, "<="):
			fmt.Sscanf(bar[2:], "%g", &limit)
			out[name] = v <= limit
		}
	}
	return out
}

// Write stores the record in dir as <date>-<build>-<layer>.json.
func Write(dir string, r Record) (string, error) {
	if r.At.IsZero() {
		r.At = time.Now()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s-%s.json", r.At.Format("2006-01-02T150405"), orUnknown(r.Build), strings.ToLower(r.Layer))
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(data, '\n'), 0o644)
}

// Read loads every record in dir, newest first.
func Read(dir string) ([]Record, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Record
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r Record
		if json.Unmarshal(data, &r) != nil || r.Layer == "" {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
