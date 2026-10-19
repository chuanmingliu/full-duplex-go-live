package simulator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Check struct {
	Name     string `json:"name"`
	Result   string `json:"result"`
	Evidence string `json:"evidence"`
}
type ActionOutcome struct {
	ID     string `json:"id"`
	Result string `json:"result"`
	AtMS   int64  `json:"at_ms"`
	Reason string `json:"reason,omitempty"`
}
type Report struct {
	ScenarioID             string          `json:"scenario_id,omitempty"`
	ExpectedOutcome        string          `json:"expected_outcome,omitempty"`
	TerminationReason      string          `json:"termination_reason,omitempty"`
	Termination            Termination     `json:"termination"`
	MaxRemoteTurns         int             `json:"max_remote_turns"`
	RemoteTurns            int             `json:"remote_turn_count"`
	RemoteSpeechStarts     int             `json:"remote_speech_start_count"`
	MaxDurationMS          int64           `json:"max_duration_ms"`
	TranscriptTruncated    bool            `json:"transcript_truncated"`
	OverlapDurationMS      int64           `json:"overlap_duration_ms"`
	AssistantEndDebounceMS int64           `json:"assistant_end_debounce_ms"`
	CustomerEndDebounceMS  int64           `json:"customer_end_debounce_ms"`
	RunID                  string          `json:"run_id"`
	Phase                  Phase           `json:"phase"`
	SourceCommit           string          `json:"source_commit"`
	ScenarioHash           string          `json:"scenario_hash"`
	PromptHash             string          `json:"prompt_hash"`
	ProviderModel          string          `json:"provider_model"`
	VoiceID                string          `json:"voice_id"`
	Evidence               string          `json:"evidence"`
	AssistantGaps          []Gap           `json:"assistant_response_gaps"`
	SimulatorGaps          []Gap           `json:"simulator_response_gaps"`
	AssistantSamples       int             `json:"assistant_sample_count"`
	SimulatorSamples       int             `json:"simulator_sample_count"`
	InvalidObservations    int             `json:"invalid_observations"`
	Checks                 []Check         `json:"checks"`
	Actions                []ActionOutcome `json:"action_outcomes"`
	Failures               []string        `json:"failures"`
	Warnings               []Warning       `json:"warnings,omitempty"`
	EventsTruncated        bool            `json:"events_truncated"`
	TranscriptRetained     bool            `json:"transcript_retained"`
	AudioRetained          bool            `json:"audio_retained"`
}

// NativeCancelBeforeAnswerReason is emitted only for the normalized native
// caller CANCEL callback. A generic ended/stop callback is insufficient evidence.
const NativeCancelBeforeAnswerReason = "caller cancelled before answer"

var sensitiveNumbers = regexp.MustCompile(`\+?[0-9][0-9 ().-]{5,}[0-9]`)

func redact(s string) string {
	if len(s) > 1024 {
		s = s[:1024]
	}
	s = sensitiveNumbers.ReplaceAllString(s, "[redacted-number]")
	lower := strings.ToLower(s)
	for _, k := range []string{"bearer ", "api_key", "api-key", "token=", "authorization", "password", "secret"} {
		if strings.Contains(lower, k) {
			return "[redacted-sensitive-value]"
		}
	}
	return s
}
func check(name string, pass bool, evidence string) Check {
	result := "fail"
	if pass {
		result = "pass"
	}
	return Check{Name: name, Result: result, Evidence: evidence}
}
func buildReport(r *run, opts Options) Report {
	o := r.observer
	rep := Report{RunID: r.state.Binding.RunID, Phase: r.state.Phase, SourceCommit: r.provenance.SourceCommit, ScenarioHash: r.scenarioHash, PromptHash: r.promptHash, ProviderModel: r.provenance.ProviderModel, VoiceID: r.provenance.VoiceID, Evidence: AudioBoundaryEvidence, AssistantGaps: o.assistantGaps(), SimulatorGaps: o.customerGaps(), InvalidObservations: o.invalid, Actions: append([]ActionOutcome{}, r.actions...), Failures: append([]string{}, r.failures...), EventsTruncated: r.eventsTruncated, TranscriptRetained: r.scenario.RetainTranscript}
	rep.Termination = defaultTermination(r.termination)
	rep.MaxRemoteTurns = r.scenario.MaxRemoteTurns
	rep.RemoteTurns = r.remoteTurns
	rep.RemoteSpeechStarts = r.remoteSpeechStarts
	rep.MaxDurationMS = r.scenario.MaxDurationMS
	rep.Warnings = append([]Warning(nil), r.warnings...)
	rep.Failures = append(rep.Failures, warningFailures(r.warnings)...)
	rep.ScenarioID = r.scenario.ID
	rep.ExpectedOutcome = r.scenario.outcome()
	rep.TerminationReason = r.terminationReason()
	if rep.TerminationReason == "" {
		rep.TerminationReason = string(r.state.Phase)
	}
	rep.TranscriptTruncated = o.transcriptTruncated
	for _, action := range r.scenario.Actions {
		found := false
		for _, outcome := range rep.Actions {
			if outcome.ID == action.ID {
				found = true
				break
			}
		}
		if !found {
			rep.Actions = append(rep.Actions, ActionOutcome{ID: action.ID, Result: "not_triggered", Reason: "trigger or deadline not reached before termination"})
		}
	}
	rep.OverlapDurationMS = o.overlapMS
	rep.CustomerEndDebounceMS = 200
	rep.AssistantEndDebounceMS = 200
	if r.state.Phase == Failed || r.state.Phase == Aborted || len(r.failures) > 0 {
		for i := range rep.AssistantGaps {
			rep.AssistantGaps[i].Valid = false
			rep.AssistantGaps[i].Reason = "failed call or turn"
		}
		for i := range rep.SimulatorGaps {
			rep.SimulatorGaps[i].Valid = false
			rep.SimulatorGaps[i].Reason = "failed call or turn"
		}
	}
	rep.AssistantSamples = len(rep.AssistantGaps)
	rep.SimulatorSamples = len(rep.SimulatorGaps)
	if r.scenario.outcome() == OutcomeCancelBeforeAnswer {
		rep.Checks = append(rep.Checks,
			check("native_cancel_before_answer", r.nativeCancelledBeforeAnswer && o.ended == 1 && r.state.Phase == Completed, "native caller CANCEL confirmation before answered confirmation"),
			check("no_answer_or_greeting", !r.state.AnswerSeen && o.answered == 0 && o.greetings == 0, "gateway answered confirmation and successful voice opening count"),
			check("no_speech", !o.speechObserved && len(o.transcript) == 0, "received and delivered nonsilent audio boundaries"),
			check("no_run_failures", len(r.failures) == 0, "receiver control and observation failures"))
	} else {
		rep.Checks = append(rep.Checks, check("answered_once", o.answered == 1, "gateway answered confirmation"))
		rep.Checks = append(rep.Checks, check("greeting_once", o.greetings == 1, "successful voice opening request; audio delivery measured separately"))
	}
	limitEnd := r.terminalAt
	if r.receiverStop != nil {
		// Waiting for control teardown must not count as active call time.
		limitEnd = r.receiverStop.requestedAt
	}
	limitOK := r.connectedAt.IsZero() || limitEnd.IsZero() || limitEnd.Sub(r.connectedAt).Milliseconds() <= r.effectiveDurationLimitMS()+1000
	rep.Checks = append(rep.Checks, check("call_limits", limitOK && r.remoteTurns <= r.scenario.MaxRemoteTurns, "receiver controller deadlines and assistant turns separated by customer speech"))
	clean := Check{Name: "clean_termination", Result: "indeterminate", Evidence: "missing gateway ended confirmation"}
	if o.ended == 1 {
		clean = check("clean_termination", len(r.failures) == 0, "gateway ended confirmation")
	} else if r.state.Phase == Completed && r.receiverStop != nil && r.receiverStop.completed {
		clean = check("clean_termination", len(r.failures) == 0, "receiver stop controls completed; native gateway end not observed")
	}
	rep.Checks = append(rep.Checks, clean)
	interrupted := false
	for _, a := range r.scenario.Actions {
		if a.RequireRemoteSpeaking && a.Kind == "speak" {
			interrupted = true
		}
	}
	if interrupted {
		c := Check{Name: "interruption_overlap", Result: "indeterminate", Evidence: "missing audio overlap evidence"}
		if o.overlaps > 0 {
			c = check("interruption_overlap", true, AudioBoundaryEvidence)
		}
		rep.Checks = append(rep.Checks, c)
	}
	if r.scenario.outcome() == OutcomeConversation {
		for _, name := range []string{"identity_explanation", "clarification_response", "appointment_facts", "graceful_ending"} {
			rep.Checks = append(rep.Checks, Check{Name: name, Result: "indeterminate", Evidence: "semantic evaluator absent"})
		}
	}
	for _, expectation := range r.scenario.Expectations {
		rep.Checks = append(rep.Checks, Check{Name: "expectation_" + expectation.ID, Result: "indeterminate", Evidence: "semantic evaluator absent"})
	}
	if r.scenario.SchemaVersion == 0 || r.state.Phase == Aborted {
		for i := range rep.Checks {
			rep.Checks[i].Result = "indeterminate"
			rep.Checks[i].Evidence = "run evidence unavailable after process restart or abort"
		}
	}
	return rep
}
func writeJSONAtomic(dir, name string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return os.ErrInvalid
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".artifact-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}
