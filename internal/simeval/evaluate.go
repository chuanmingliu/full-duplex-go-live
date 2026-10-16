// Package simeval independently judges the remote assistant's task behavior.
// It never sends requests without complete, matched, retained terminal evidence.
package simeval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/chuanmingliu/golive/internal/simclient"
	"github.com/chuanmingliu/golive/internal/simulator"
)

const PromptVersion = "remote-assistant-expectations-v1"

type Grade struct {
	ID            string `json:"id"`
	Result        string `json:"result"`
	Reason        string `json:"reason"`
	EvidenceTurns []int  `json:"evidence_turns"`
}
type Artifact struct {
	SchemaVersion  int     `json:"schema_version"`
	RunID          string  `json:"run_id"`
	ScenarioHash   string  `json:"scenario_hash"`
	PromptHash     string  `json:"prompt_hash"`
	TranscriptHash string  `json:"transcript_hash"`
	JudgeModel     string  `json:"judge_model"`
	PromptVersion  string  `json:"prompt_version"`
	Grades         []Grade `json:"grades"`
	Verdict        string  `json:"verdict"`
	Reason         string  `json:"reason"`
}

// LoadEvidence uses the receiver's scenario decoder and strict artifact decoding.
func LoadEvidence(scenarioPath, reportPath, transcriptPath string) (simulator.Scenario, simulator.Report, simulator.TranscriptArtifact, error) {
	var s simulator.Scenario
	var r simulator.Report
	var t simulator.TranscriptArtifact
	raw, err := simclient.ReadFile(scenarioPath)
	if err != nil {
		return s, r, t, err
	}
	if simclient.StrictJSON(raw, &s) != nil {
		return s, r, t, errors.New("invalid scenario artifact")
	}
	s, err = simulator.DecodeScenario(strings.NewReader(string(raw)))
	if err != nil {
		return s, r, t, errors.New("invalid scenario artifact")
	}
	raw, err = simclient.ReadFile(reportPath)
	if err != nil {
		return s, r, t, err
	}
	if simclient.StrictJSON(raw, &r) != nil {
		return s, r, t, errors.New("invalid report artifact")
	}
	if !explicitFields(raw, "transcript_truncated", "events_truncated") {
		return s, r, t, errors.New("report lacks explicit completeness evidence")
	}
	raw, err = simclient.ReadFile(transcriptPath)
	if err != nil {
		return s, r, t, err
	}
	if simclient.StrictJSON(raw, &t) != nil {
		return s, r, t, errors.New("invalid transcript artifact")
	}
	if !explicitFields(raw, "truncated") {
		return s, r, t, errors.New("transcript lacks explicit completeness evidence")
	}
	var envelope struct {
		Entries []json.RawMessage `json:"entries"`
	}
	_ = json.Unmarshal(raw, &envelope)
	for _, entry := range envelope.Entries {
		if !explicitFields(entry, "at_ms") {
			return s, r, t, errors.New("transcript lacks explicit timestamp evidence")
		}
	}
	return s, r, t, nil
}

// A zero Go value is not proof that a flag or timestamp was observed. Older
// envelopes without explicit fields remain indeterminate at this input boundary.
func explicitFields(raw []byte, names ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for _, name := range names {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	return true
}

func baseArtifact(s simulator.Scenario, r simulator.Report, t simulator.TranscriptArtifact) Artifact {
	raw, _ := json.Marshal(t)
	hash := sha256.Sum256(raw)
	a := Artifact{SchemaVersion: 1, ScenarioHash: simulator.ScenarioHash(s), PromptHash: simulator.PromptHash(s), TranscriptHash: hex.EncodeToString(hash[:]), PromptVersion: PromptVersion, Grades: []Grade{}, Verdict: "indeterminate", Reason: "insufficient_evidence"}
	if simclient.SafeID(r.RunID) {
		a.RunID = r.RunID
	}
	for _, e := range s.Expectations {
		a.Grades = append(a.Grades, Grade{ID: e.ID, Result: "indeterminate", Reason: "Insufficient validated evidence.", EvidenceTurns: []int{}})
	}
	return a
}
func validEvidence(s simulator.Scenario, r simulator.Report, t simulator.TranscriptArtifact) bool {
	outcome := s.ExpectedOutcome
	if outcome == "" {
		outcome = simulator.OutcomeConversation
	}
	raw, err := json.Marshal(t)
	if err != nil || len(raw) > simclient.MaxArtifactBytes {
		return false
	}
	if s.Validate() != nil || r.ExpectedOutcome != outcome || !s.RetainTranscript || len(s.Expectations) == 0 || !simclient.SafeID(r.RunID) || r.ScenarioID != s.ID || r.Phase != simulator.Completed || len(r.Failures) > 0 || !r.TranscriptRetained || r.TranscriptTruncated || r.EventsTruncated || t.SchemaVersion != 1 || t.RunID != r.RunID || t.Truncated || t.ScenarioHash != simulator.ScenarioHash(s) || r.ScenarioHash != t.ScenarioHash || t.PromptHash != simulator.PromptHash(s) || r.PromptHash != t.PromptHash || len(t.Entries) < 2 || len(t.Entries) > 256 {
		return false
	}
	assistant, customer := false, false
	last := int64(0)
	for _, e := range t.Entries {
		if e.AtMS < last || e.AtMS < 0 || len(e.Text) > 8192 || !utf8.ValidString(e.Text) || strings.TrimSpace(e.Text) == "" || strings.TrimSpace(e.Text) != e.Text {
			return false
		}
		for _, c := range e.Text {
			if unicode.IsControl(c) && c != '\n' && c != '\t' {
				return false
			}
		}
		switch e.Role {
		case "assistant":
			assistant = true
		case "customer":
			customer = true
		default:
			return false
		}
		last = e.AtMS
	}
	return assistant && customer
}

// Evaluate returns a separate, sanitized artifact even when judgment cannot run.
// Invalid, partial or missing evidence always leaves every expectation indeterminate.
func Evaluate(ctx context.Context, s simulator.Scenario, r simulator.Report, t simulator.TranscriptArtifact, c Config) (Artifact, error) {
	a := baseArtifact(s, r, t)
	if !validEvidence(s, r, t) {
		return a, errors.New("evaluation requires complete matching evidence and expectations")
	}
	if err := c.validate(); err != nil {
		a.Reason = "invalid_judge_configuration"
		return a, err
	}
	a.JudgeModel = c.Model
	grades, err := c.judge(ctx, s, t)
	if err != nil {
		a.Reason = "judge_unavailable_or_invalid"
		return a, errors.New("independent judge unavailable or returned invalid evidence")
	}
	if !validGrades(grades, s, t) {
		a.Reason = "invalid_judge_verdict"
		return a, errors.New("independent judge returned invalid verdict")
	}
	a.Grades = make([]Grade, 0, len(grades))
	a.Verdict = "pass"
	a.Reason = "judged_expectations"
	// Use scenario order and fixed reasons. Provider prose is untrusted and may echo
	// transcript secrets; it is never persisted or logged, even after redaction.
	for _, expect := range s.Expectations {
		for _, g := range grades {
			if g.ID == expect.ID {
				switch g.Result {
				case "pass":
					g.Reason = "Judge found supporting assistant evidence."
				case "fail":
					g.Reason = "Judge found contrary assistant evidence."
				case "indeterminate":
					g.Reason = "Judge could not establish this expectation."
				}
				a.Grades = append(a.Grades, g)
				if g.Result == "fail" {
					a.Verdict = "fail"
				} else if g.Result == "indeterminate" && a.Verdict != "fail" {
					a.Verdict = "indeterminate"
				}
			}
		}
	}
	return a, nil
}
func validGrades(grades []Grade, s simulator.Scenario, t simulator.TranscriptArtifact) bool {
	if len(grades) != len(s.Expectations) {
		return false
	}
	ids := map[string]bool{}
	for _, e := range s.Expectations {
		ids[e.ID] = true
	}
	seen := map[string]bool{}
	for _, g := range grades {
		if !ids[g.ID] || seen[g.ID] || len(g.Reason) > 1024 || !utf8.ValidString(g.Reason) || strings.TrimSpace(g.Reason) == "" || len(g.EvidenceTurns) > len(t.Entries) {
			return false
		}
		seen[g.ID] = true
		if g.Result != "pass" && g.Result != "fail" && g.Result != "indeterminate" {
			return false
		}
		assistant := false
		indices := map[int]bool{}
		for _, i := range g.EvidenceTurns {
			if i < 1 || i > len(t.Entries) || indices[i] {
				return false
			}
			indices[i] = true
			if t.Entries[i-1].Role == "assistant" {
				assistant = true
			}
		}
		if (g.Result == "pass" || g.Result == "fail") && !assistant {
			return false
		}
	}
	return true
}
