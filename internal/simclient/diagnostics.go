package simclient

import (
	"fmt"

	"github.com/chuanmingliu/golive/internal/simulator"
)

// These fields come from a remote report. Preserve only numeric counts and
// known diagnostic enums, with the same privacy boundary as suite artifacts.
func sanitizeOperationalMetadata(report *simulator.Report) {
	fatal := report.HasFatalFailures()
	t := &report.Termination
	t.Side = diagnosticEnum(t.Side, "unknown", "caller", "receiver_gateway")
	t.Evidence = diagnosticEnum(t.Evidence, "not_observed", "native_gateway_hangup", "native_gateway_track_end", "local_hangup_request", "control_disconnected")
	t.NativeInitiator = diagnosticEnum(t.NativeInitiator, "caller", "callee", "system")
	t.NativeReason = diagnosticEnum(t.NativeReason, "ByCaller", "ByCallee", "ByRefer", "BySystem", "Autohangup", "InactivityTimeout", "NoAnswer", "NoBalance", "AnswerMachine", "ServerUnavailable", "Canceled", "Rejected", "Failed", "other")
	t.LocalReason = diagnosticEnum(t.LocalReason, "stopped", "duration limit", "remote turn limit", "inactivity limit", "scripted hangup", "customer_hangup")
	warnings := make([]simulator.Warning, 0, 4)
	projections := make(map[string]bool, 4)
	seen := make(map[string]bool, 4)
	for _, warning := range report.Warnings {
		if diagnosticEnum(warning.Code, "tts_open_failed", "tts_synthesis_failed", "tts_chunk_failed", "tts_empty_segment") == "" || warning.Count < 1 || warning.Count > 1000000000 || seen[warning.Code] {
			continue
		}
		seen[warning.Code] = true
		warnings = append(warnings, warning)
		projections[fmt.Sprintf("warning: %s (count=%d)", warning.Code, warning.Count)] = true
	}
	report.Warnings = warnings
	failures := make([]string, len(report.Failures))
	for i, failure := range report.Failures {
		failures[i] = "run failure"
		if projections[failure] {
			failures[i] = failure
		}
	}
	report.Failures = failures
	// Filtering unknown/duplicate metadata must never promote a malformed or
	// failed report to a successful protocol result.
	if fatal && !report.HasFatalFailures() {
		report.Failures = append(report.Failures, "run failure")
	}
}

func diagnosticEnum(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return ""
}
