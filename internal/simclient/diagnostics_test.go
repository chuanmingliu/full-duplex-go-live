package simclient

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/simulator"
)

func TestRecoveredWarningPreservesProtocolVerdictAndDiagnostics(t *testing.T) {
	s := scenario(t)
	report := validReport(s, "owned")
	report.Warnings = []simulator.Warning{{Code: "tts_chunk_failed", Count: 2}}
	report.Failures = []string{"warning: tts_chunk_failed (count=2)"}
	report.Termination = simulator.Termination{Side: "receiver_gateway", Evidence: "native_gateway_hangup", GatewayEnded: true, LocalHangupRequested: true, NativeInitiator: "callee", NativeReason: "ByCallee", LocalReason: "duration limit"}
	for name, r := range map[string]simulator.Report{"raw": report, "sanitized": sanitizeReport(s, report)} {
		if got := ProtocolVerdict(s, r); got != "pass" {
			t.Errorf("%s: recovered protocol result=%s, want pass", name, got)
		}
		if len(r.Failures) != 1 || r.Failures[0] != "warning: tts_chunk_failed (count=2)" || r.Termination != report.Termination {
			t.Errorf("%s: warning severity or termination lost", name)
		}
	}
	report.Failures = append(report.Failures, "control disconnected")
	if ProtocolVerdict(s, report) != "fail" {
		t.Fatal("recoverable warning hid a real control failure")
	}
}

func TestSanitizedDiagnosticsExcludeArbitraryReportText(t *testing.T) {
	s := scenario(t)
	report := validReport(s, "owned")
	report.TerminationReason = "PRIVATE_TERMINATION"
	report.Termination = simulator.Termination{Side: "PRIVATE_SIDE", Evidence: "PRIVATE_EVIDENCE", NativeInitiator: "PRIVATE_INITIATOR", NativeReason: "PRIVATE_REASON", LocalReason: "PRIVATE_LOCAL"}
	report.Warnings = []simulator.Warning{{Code: "PRIVATE_WARNING", Count: 1}, {Code: "tts_chunk_failed", Count: 3}}
	report.Failures = []string{"warning: PRIVATE_WARNING (count=1)", "warning: tts_chunk_failed (count=3)"}
	safe := sanitizeReport(s, report)
	raw, _ := json.Marshal(safe)
	if strings.Contains(string(raw), "PRIVATE_") {
		t.Fatalf("arbitrary diagnostic text escaped: %s", raw)
	}
	if len(safe.Warnings) != 1 || safe.Warnings[0].Code != "tts_chunk_failed" || safe.Failures[0] != "run failure" || safe.Failures[1] != "warning: tts_chunk_failed (count=3)" {
		t.Fatal("sanitization lost warning classification")
	}
	if ProtocolVerdict(s, safe) != "fail" {
		t.Fatal("unknown failure became a warning during sanitization")
	}
}

func TestSanitizedReportRetainsNormalHangupResults(t *testing.T) {
	s := scenario(t)
	for _, reason := range []string{"remote_hangup", "customer_hangup"} {
		for _, evidence := range []string{"native_gateway_hangup", "native_gateway_track_end"} {
			report := validReport(s, "owned")
			report.TerminationReason = reason
			report.Termination = simulator.Termination{Side: "unknown", Evidence: evidence, GatewayEnded: true}
			if reason == "customer_hangup" {
				report.Termination.LocalReason = reason
				report.Termination.LocalHangupRequested = true
			}
			safe := sanitizeReport(s, report)
			if safe.TerminationReason != reason || safe.Termination != report.Termination || ProtocolVerdict(s, safe) != "pass" {
				t.Fatalf("normal hangup metadata lost: %+v", safe)
			}
		}
	}
}

func TestSanitizedReportRetainsReceiverLimitResults(t *testing.T) {
	s := scenario(t)
	for reason, localReason := range map[string]string{
		"duration_limit":    "duration limit",
		"remote_turn_limit": "remote turn limit",
		"inactivity":        "inactivity limit",
	} {
		for _, local := range []string{localReason, reason} {
			t.Run(reason+"/"+local, func(t *testing.T) {
				report := validReport(s, "owned")
				report.TerminationReason = reason
				report.Termination = simulator.Termination{Side: "receiver_gateway", Evidence: "local_hangup_request", LocalHangupRequested: true, LocalReason: local}
				safe := sanitizeReport(s, report)
				if safe.TerminationReason != reason || safe.Termination != report.Termination || ProtocolVerdict(s, safe) != "pass" {
					t.Fatalf("receiver limit metadata or verdict lost: %+v", safe)
				}
			})
		}
	}
}
