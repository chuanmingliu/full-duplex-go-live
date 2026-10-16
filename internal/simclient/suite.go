package simclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/simulator"
)

type Manifest struct {
	SchemaVersion int      `json:"schema_version"`
	ID            string   `json:"id"`
	Repeat        int      `json:"repeat"`
	Scenarios     []string `json:"scenarios"`
}
type Suite struct {
	Manifest  Manifest
	Scenarios []simulator.Scenario
}

// LoadSuite validates every input before a caller can create any reservation.
func LoadSuite(path string) (Suite, error) {
	var suite Suite
	raw, err := ReadFile(path)
	if err != nil {
		return suite, err
	}
	if err = StrictJSON(raw, &suite.Manifest); err != nil {
		return suite, errors.New("invalid suite manifest")
	}
	// JSON field names are exact, including case.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for k := range fields {
		if k != "schema_version" && k != "id" && k != "repeat" && k != "scenarios" {
			return suite, errors.New("unknown manifest field")
		}
	}
	m := suite.Manifest
	if m.SchemaVersion != 1 || !SafeID(m.ID) || m.Repeat < 1 || m.Repeat > 10 || len(m.Scenarios) < 1 || len(m.Scenarios) > 32 || m.Repeat*len(m.Scenarios) > 100 {
		return suite, errors.New("invalid suite bounds")
	}
	seen := map[string]bool{}
	for _, name := range m.Scenarios {
		if name == "" || filepath.IsAbs(name) {
			return suite, errors.New("scenario paths must be relative")
		}
		raw, err := ReadFile(filepath.Join(filepath.Dir(path), name))
		if err != nil {
			return suite, errors.New("cannot read suite scenario")
		}
		var decoded simulator.Scenario
		if StrictJSON(raw, &decoded) != nil {
			return suite, errors.New("invalid suite scenario JSON")
		}
		scenario, err := simulator.DecodeScenario(strings.NewReader(string(raw)))
		if err != nil || seen[scenario.ID] {
			return suite, errors.New("invalid or duplicate suite scenario")
		}
		seen[scenario.ID] = true
		suite.Scenarios = append(suite.Scenarios, scenario)
	}
	return suite, nil
}

type Ready struct {
	Type         string `json:"type"`
	RunID        string `json:"run_id"`
	ScenarioID   string `json:"scenario_id"`
	Trial        int    `json:"trial"`
	CalledSIPURI string `json:"called_sip_uri"`
}
type Counts struct {
	Pass          int `json:"pass"`
	Fail          int `json:"fail"`
	Indeterminate int `json:"indeterminate"`
	NotRequested  int `json:"not_requested,omitempty"`
}
type RunResult struct {
	RunID           string `json:"run_id"`
	ScenarioID      string `json:"scenario_id"`
	ScenarioHash    string `json:"scenario_hash"`
	PromptHash      string `json:"prompt_hash"`
	Trial           int    `json:"trial"`
	ProtocolVerdict string `json:"protocol_verdict"`
	SemanticVerdict string `json:"semantic_verdict"`
	Verdict         string `json:"verdict"`
}
type Summary struct {
	SchemaVersion  int         `json:"schema_version"`
	SuiteID        string      `json:"suite_id"`
	InvocationID   string      `json:"invocation_id"`
	Repeat         int         `json:"repeat"`
	RequestedRuns  int         `json:"requested_runs"`
	Complete       bool        `json:"complete"`
	Counts         Counts      `json:"counts"`
	ProtocolCounts Counts      `json:"protocol_counts"`
	SemanticCounts Counts      `json:"semantic_counts"`
	Runs           []RunResult `json:"runs"`
}
type Runner struct {
	Client       *Client
	Ready        io.Writer
	Output       string
	PollInterval time.Duration
	RunWait      time.Duration
}

func (r *Runner) Run(ctx context.Context, suite Suite) (summary Summary, err error) {
	if r.Client == nil || r.Ready == nil || r.Output == "" {
		return summary, errors.New("invalid suite runner")
	}
	// Revalidate caller-created suites before network action as well.
	if suite.Manifest.SchemaVersion != 1 || !SafeID(suite.Manifest.ID) || suite.Manifest.Repeat < 1 || suite.Manifest.Repeat > 10 || len(suite.Scenarios) < 1 || len(suite.Scenarios) > 32 || len(suite.Scenarios)*suite.Manifest.Repeat > 100 {
		return summary, errors.New("invalid suite")
	}
	for _, s := range suite.Scenarios {
		if s.Validate() != nil {
			return summary, errors.New("invalid suite scenario")
		}
	}
	entropy := make([]byte, 16)
	if _, err = rand.Read(entropy); err != nil {
		return summary, errors.New("cannot create invocation identity")
	}
	summary = Summary{SchemaVersion: 1, SuiteID: suite.Manifest.ID, InvocationID: hex.EncodeToString(entropy), Repeat: suite.Manifest.Repeat, RequestedRuns: len(suite.Scenarios) * suite.Manifest.Repeat, Runs: []RunResult{}}
	persist := func() error { return WritePrivate(filepath.Join(r.Output, "summary.json"), summary) }
	if err = persist(); err != nil {
		return summary, err
	}
	defer func() {
		if saveErr := persist(); err == nil {
			err = saveErr
		}
	}()
	for _, s := range suite.Scenarios {
		for trial := 1; trial <= suite.Manifest.Repeat; trial++ {
			if err = ctx.Err(); err != nil {
				return summary, errors.New("suite cancelled")
			}
			// The key is unique per invocation and attempt. A failed POST is never retried.
			key := summary.InvocationID + "-" + hex.EncodeToString([]byte{byte(len(summary.Runs) + 1)})
			var result RunResult
			result, err = r.runOne(ctx, s, trial, key)
			if result.RunID != "" {
				summary.Runs = append(summary.Runs, result)
				addCount(&summary.Counts, result.Verdict)
				addCount(&summary.ProtocolCounts, result.ProtocolVerdict)
				addCount(&summary.SemanticCounts, result.SemanticVerdict)
			}
			if err != nil {
				return summary, err
			}
			if err = persist(); err != nil {
				return summary, err
			}
		}
	}
	summary.Complete = true
	return summary, nil
}
func addCount(c *Counts, v string) {
	switch v {
	case "pass":
		c.Pass++
	case "fail":
		c.Fail++
	case "not_requested":
		c.NotRequested++
	default:
		c.Indeterminate++
	}
}
func (r *Runner) runOne(ctx context.Context, s simulator.Scenario, trial int, key string) (result RunResult, err error) {
	result = RunResult{ScenarioID: s.ID, ScenarioHash: simulator.ScenarioHash(s), PromptHash: simulator.PromptHash(s), Trial: trial, ProtocolVerdict: "indeterminate", SemanticVerdict: "indeterminate", Verdict: "indeterminate"}
	wait := r.RunWait
	if wait <= 0 || wait > 8*time.Minute {
		wait = 8 * time.Minute
	}
	runctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	snap, err := r.Client.Prepare(runctx, key, s)
	if err != nil {
		return result, err
	}
	if !SafeID(snap.RunID) {
		return result, errors.New("invalid reservation identity; preparation ambiguous")
	}
	if snap.Binding.RunID != snap.RunID {
		return result, errors.New("ambiguous reservation binding")
	}
	result.RunID = snap.RunID
	owned := true
	defer func() {
		if owned {
			cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = r.Client.Stop(cleanup, result.RunID)
		}
	}()
	if snap.Phase != simulator.Waiting || snap.Binding.RunID != snap.RunID {
		return result, errors.New("invalid reservation state")
	}
	sip, err := calledURI(snap)
	if err != nil {
		return result, err
	}
	if err = runctx.Err(); err != nil {
		return result, errors.New("suite cancelled")
	}
	if err = json.NewEncoder(r.Ready).Encode(Ready{Type: "ready", RunID: snap.RunID, ScenarioID: s.ID, Trial: trial, CalledSIPURI: sip}); err != nil {
		return result, errors.New("cannot emit ready record")
	}
	if flusher, ok := r.Ready.(interface{ Flush() error }); ok {
		if flusher.Flush() != nil {
			return result, errors.New("cannot flush ready record")
		}
	}
	if flusher, ok := r.Ready.(interface{ Flush() }); ok {
		flusher.Flush()
	}
	interval := r.PollInterval
	if interval <= 0 || interval > 5*time.Second {
		interval = 500 * time.Millisecond
	}
	for {
		snap, err = r.Client.Get(runctx, result.RunID)
		if err != nil {
			return result, err
		}
		if snap.RunID != result.RunID || snap.Binding.RunID != result.RunID {
			return result, errors.New("control response identity mismatch")
		}
		if snap.Phase.Terminal() {
			owned = false
			break
		}
		switch snap.Phase {
		case simulator.Waiting, simulator.Ringing, simulator.Answering, simulator.Connected, simulator.Ending:
		default:
			return result, errors.New("invalid run phase")
		}
		timer := time.NewTimer(interval)
		select {
		case <-runctx.Done():
			timer.Stop()
			return result, errors.New("run wait cancelled or timed out")
		case <-timer.C:
		}
	}
	report, err := r.Client.Report(runctx, result.RunID)
	if err != nil {
		return result, err
	}
	if report.RunID != result.RunID || report.ScenarioID != s.ID || report.ScenarioHash != result.ScenarioHash || report.PromptHash != result.PromptHash || report.Phase != snap.Phase || !report.Phase.Terminal() || report.ExpectedOutcome != expectedOutcome(s) {
		return result, errors.New("report identity or terminal state mismatch")
	}
	result.ProtocolVerdict = ProtocolVerdict(s, report)
	if len(s.Expectations) == 0 {
		result.SemanticVerdict = "not_requested"
	}
	result.Verdict = result.ProtocolVerdict
	if result.Verdict == "pass" && len(s.Expectations) > 0 {
		result.Verdict = "indeterminate"
	}
	if err = WritePrivate(filepath.Join(r.Output, result.RunID+"-report.json"), sanitizeReport(s, report)); err != nil {
		return result, err
	}
	return result, nil
}
func calledURI(s simulator.Snapshot) (string, error) {
	uri := strings.ReplaceAll(s.DialTargetReference, "<run_id>", s.RunID)
	if !strings.HasPrefix(uri, "sip:"+s.RunID+"@") && !strings.HasPrefix(uri, "sips:"+s.RunID+"@") {
		return "", errors.New("reservation lacks exact called SIP URI")
	}
	if len(uri) > 512 || strings.ContainsAny(uri, "\r\n\t <>?#;") || strings.Count(uri, "@") != 1 {
		return "", errors.New("invalid called SIP URI")
	}
	host := uri[strings.Index(uri, "@")+1:]
	if host == "" {
		return "", errors.New("invalid called SIP URI")
	}
	return uri, nil
}

// ProtocolVerdict explicitly selects deterministic lifecycle/action evidence. The
// receiver's legacy semantic placeholders never influence this protocol verdict.
func ProtocolVerdict(s simulator.Scenario, report simulator.Report) string {
	if report.Phase != simulator.Completed || len(report.Failures) > 0 {
		return "fail"
	}
	required := []string{"answered_once", "greeting_once", "call_limits", "clean_termination"}
	if s.ExpectedOutcome == simulator.OutcomeCancelBeforeAnswer {
		required = []string{"native_cancel_before_answer", "no_answer_or_greeting", "no_speech", "no_run_failures", "call_limits", "clean_termination"}
	}
	for _, a := range s.Actions {
		if a.RequireRemoteSpeaking && a.Kind == "speak" {
			required = append(required, "interruption_overlap")
			break
		}
	}
	result := "pass"
	for _, name := range required {
		found := false
		for _, c := range report.Checks {
			if c.Name == name {
				if found {
					return "indeterminate"
				}
				found = true
				switch c.Result {
				case "fail":
					return "fail"
				case "pass":
				default:
					result = "indeterminate"
				}
			}
		}
		if !found {
			result = "indeterminate"
		}
	}
	for _, a := range s.Actions {
		completed := false
		for _, outcome := range report.Actions {
			if outcome.ID == a.ID {
				switch outcome.Result {
				case "failed", "timed_out":
					return "fail"
				case "completed":
					completed = true
				}
			}
		}
		if !completed {
			result = "indeterminate"
		}
	}
	if report.EventsTruncated {
		return "indeterminate"
	}
	return result
}
func expectedOutcome(s simulator.Scenario) string {
	if s.ExpectedOutcome == "" {
		return simulator.OutcomeConversation
	}
	return s.ExpectedOutcome
}
func sanitizeReport(s simulator.Scenario, report simulator.Report) simulator.Report {
	report.SourceCommit = ""
	report.ProviderModel = ""
	report.VoiceID = ""
	report.Evidence = ""
	report.TerminationReason = "" // free-form call material is excluded from suite artifacts
	allowed := map[string]bool{}
	for _, name := range []string{"answered_once", "greeting_once", "call_limits", "clean_termination", "interruption_overlap", "native_cancel_before_answer", "no_answer_or_greeting", "no_speech", "no_run_failures"} {
		allowed[name] = true
	}
	for _, e := range s.Expectations {
		allowed["expectation_"+e.ID] = true
	}
	checks := []simulator.Check{}
	for _, c := range report.Checks {
		if allowed[c.Name] {
			c.Evidence = ""
			if c.Result != "pass" && c.Result != "fail" && c.Result != "indeterminate" {
				c.Result = "indeterminate"
			}
			checks = append(checks, c)
		}
	}
	report.Checks = checks
	knownActions := map[string]bool{}
	for _, action := range s.Actions {
		knownActions[action.ID] = true
	}
	actions := []simulator.ActionOutcome{}
	for _, a := range report.Actions {
		if knownActions[a.ID] {
			a.Reason = ""
			switch a.Result {
			case "started", "completed", "cancelled", "timed_out", "failed", "skipped", "not_triggered":
			default:
				a.Result = "failed"
			}
			actions = append(actions, a)
		}
	}
	report.Actions = actions
	for i := range report.Actions {
		report.Actions[i].Reason = ""
	}
	for i := range report.Failures {
		report.Failures[i] = "run failure"
	}
	for i := range report.AssistantGaps {
		report.AssistantGaps[i].Reason = ""
		report.AssistantGaps[i].Evidence = ""
	}
	for i := range report.SimulatorGaps {
		report.SimulatorGaps[i].Reason = ""
		report.SimulatorGaps[i].Evidence = ""
	}
	return report
}
