package metrics

import (
	"bytes"
	"strings"
	"testing"
)

func TestHistogramAndCountersRender(t *testing.T) {
	r := New()
	r.SessionOpen()
	r.SessionOpen()
	r.SessionClose()
	r.Rejected("unauthorized")
	r.Rejected("unauthorized")
	r.Error("asr_unavailable")
	r.AudioDropped()
	r.Retry("llm")
	r.ProviderError("asr")
	r.BargeIn()
	r.SetBuild("1.0", "abc")
	r.ObserveTurn(350)
	r.ObserveTurn(1200)
	r.ObserveTurn(-40) // speculative; clamped

	if r.ActiveSessions() != 1 {
		t.Fatalf("active = %d, want 1", r.ActiveSessions())
	}

	var buf bytes.Buffer
	r.WritePrometheus(&buf)
	out := buf.String()
	for _, want := range []string{
		"golive_sessions_active 1",
		"golive_sessions_started_total 2",
		`golive_sessions_rejected_total{reason="unauthorized"} 2`,
		`golive_errors_total{code="asr_unavailable"} 1`,
		"golive_turn_first_audio_ms_count 3",
		`golive_turn_first_audio_ms_bucket{le="+Inf"} 3`,
		`golive_provider_retries_total{kind="llm"} 1`,
		`golive_provider_errors_total{kind="asr"} 1`,
		"golive_barge_ins_total 1",
		`golive_build_info{version="1.0",commit="abc"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prometheus output missing %q\n%s", want, out)
		}
	}
}
