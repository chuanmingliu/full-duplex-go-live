package journal

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func coalescedDelivery(changes map[string]string) Line {
	l := latencyDelivery(map[string]string{"tool": "quote_total", "origin_turn": "item_3", "primary_origin_turn": "item_1", "origin_to_result_start_ms": "20"})
	l.Msg = "tools: delivering a coalesced task result"
	for k, v := range changes {
		l.kv[k] = v
	}
	return l
}

func coalescedFixture() (Line, Line, Line, Line) {
	primary, child := latencyQuotePair()
	child.kv["first_content_ms"], child.kv["first_readout_ms"] = "100", "1036"
	extra := latencyMetric("item_3", map[string]string{"first_content_present": "false", "first_content_ms": "0"})
	link := latencyDelivery(map[string]string{"tool": "quote_total", "origin_to_result_start_ms": "1465"})
	return primary, extra, child, link
}

func TestCoalescedLatencyPreservesOriginalAndAdditionalIntervals(t *testing.T) {
	primary, extra, child, link := coalescedFixture()
	baseline := latencyCall(t, primary, child, link).Latency
	for _, lines := range [][]Line{{primary, extra, child, link, coalescedDelivery(nil)}, {coalescedDelivery(nil), link, child, extra, primary}} {
		e := latencyCall(t, lines...).Latency
		if e.Joined != 2 || e.InvalidLinks != 0 || e.Missing != 0 || e.FirstContent.N != 2 || *e.FirstContent.P95 != 1945 {
			t.Fatalf("coalesced content: %+v", e)
		}
		if len(e.ResultRows) != 2 || len(e.QuoteRows) != 2 || !reflect.DeepEqual(e.ResultRows[0], baseline.ResultRows[0]) || !reflect.DeepEqual(e.QuoteRows[0], baseline.QuoteRows[0]) {
			t.Fatalf("primary evidence changed: %+v", e)
		}
		if e.ResultRows[1].TotalMS != 500 || *e.QuoteRows[1].TotalMS != 1436 || *e.QuoteReadout.P95 != 2881 {
			t.Fatalf("coalescing hid original delay: %+v", e)
		}
		for _, row := range []any{e.ResultRows[1], e.QuoteRows[1]} {
			data, _ := json.Marshal(row)
			if !strings.Contains(string(data), `"coalesced":true`) || !strings.Contains(string(data), `"primary_origin_turn":"item_1"`) {
				t.Fatalf("missing explicit coalesced provenance: %s", data)
			}
		}
		data, _ := json.Marshal(e.ResultRows[0])
		if strings.Contains(string(data), "coalesced") || strings.Contains(string(data), "primary_origin_turn") {
			t.Fatalf("legacy row changed: %s", data)
		}
	}
}

func TestCoalescedLatencyRejectsMetadataWithoutPoisoningPrimary(t *testing.T) {
	for name, changes := range map[string]map[string]string{
		"wrong primary": {"primary_origin_turn": "item_9"}, "empty primary": {"primary_origin_turn": ""},
		"wrong tool": {"tool": "other"}, "empty tool": {"tool": ""}, "wrong task": {"task": "task_9"},
		"wrong result": {"turn": "item_9"}, "same origin": {"origin_turn": "item_1"},
		"result origin": {"origin_turn": "item_2"}, "missing origin": {"origin_turn": "item_9"},
		"negative": {"origin_to_result_start_ms": "-1"}, "overflow": {"origin_to_result_start_ms": "9223372036854775808"},
		"sum overflow": {"origin_to_result_start_ms": "9223372036854775807"}, "boolean": {"origin_to_result_start_ms": "true"},
		"unicode": {"origin_to_result_start_ms": "١"}, "float": {"origin_to_result_start_ms": "1.0"},
		"huge": {"origin_to_result_start_ms": strings.Repeat("9", 5000)},
	} {
		t.Run(name, func(t *testing.T) {
			primary, extra, child, link := coalescedFixture()
			e := latencyCall(t, primary, extra, child, link, coalescedDelivery(changes)).Latency
			if e.InvalidLinks != 1 || e.Unverified == 0 || e.Joined != 1 || len(e.ResultRows) != 1 || len(e.QuoteRows) != 1 {
				t.Fatalf("invalid extra link affected primary or escaped strict accounting: %+v", e)
			}
		})
	}
}

func TestCoalescedLatencyRequiresUniquePrimaryAndOrigin(t *testing.T) {
	for _, name := range []string{"missing primary", "duplicate primary", "duplicate extra", "conflicting extra", "cancelled extra", "missing extra presence", "duplicate extra metric", "cancelled primary"} {
		t.Run(name, func(t *testing.T) {
			primary, extra, child, link := coalescedFixture()
			lines := []Line{primary, extra, child, link, coalescedDelivery(nil)}
			wantRows := 1
			switch name {
			case "missing primary":
				lines = []Line{primary, extra, child, coalescedDelivery(nil)}
				wantRows = 0
			case "duplicate primary":
				lines = append(lines, link)
				wantRows = 0
			case "duplicate extra":
				lines = append(lines, coalescedDelivery(nil))
			case "conflicting extra":
				lines = append(lines, coalescedDelivery(map[string]string{"task": "task_9"}))
			case "cancelled extra":
				extra.kv["truncated"] = "true"
			case "missing extra presence":
				delete(extra.kv, "first_content_present")
			case "duplicate extra metric":
				lines = append(lines, extra)
			case "cancelled primary":
				primary.kv["truncated"] = "true"
				wantRows = 0
			}
			e := latencyCall(t, lines...).Latency
			if e.InvalidLinks == 0 || e.Unverified == 0 || len(e.ResultRows) != wantRows {
				t.Fatalf("ambiguous link escaped strict accounting: %+v", e)
			}
		})
	}
}

func TestCoalescedLatencyAcceptsSignedOffsetAndPreservesDirectContent(t *testing.T) {
	primary, extra, child, link := coalescedFixture()
	extra.kv["first_content_present"], extra.kv["first_content_ms"] = "true", "10"
	e := latencyCall(t, primary, extra, child, link, coalescedDelivery(map[string]string{"origin_to_result_start_ms": "+20"})).Latency
	if e.InvalidLinks != 0 || e.Joined != 1 || e.FirstContent.N != 2 || *e.FirstContent.P50 != 1167.5 || len(e.ResultRows) != 2 || e.ResultRows[1].TotalMS != 500 {
		t.Fatalf("signed offset or direct content mishandled: %+v", e)
	}
}

func TestCoalescedLatencyReadoutOverflowRemainsUnknown(t *testing.T) {
	primary, extra, child, link := coalescedFixture()
	child.kv["first_readout_ms"] = "9223372036854775807"
	e := latencyCall(t, primary, extra, child, link, coalescedDelivery(nil)).Latency
	if e.QuoteReadout.N != 0 || e.QuoteUnknown != 2 {
		t.Fatalf("readout overflow escaped accounting: %+v", e)
	}
}

func TestCoalescedLatencyAmbiguityStaysUnverifiedWithFastDirectAnswers(t *testing.T) {
	primary, extra, child, link := coalescedFixture()
	primary.kv["first_content_present"], primary.kv["first_content_ms"] = "true", "100"
	extra.kv["first_content_present"], extra.kv["first_content_ms"] = "true", "100"
	link.kv["origin_to_result_start_ms"], child.kv["first_readout_ms"] = "20", "100"
	e := latencyCall(t, primary, extra, child, link, coalescedDelivery(nil), coalescedDelivery(nil)).Latency
	if e.Missing != 0 || e.Unverified != 2 || e.InvalidLinks != 2 || len(e.ResultRows) != 1 || *e.FirstContent.P95 != 480 {
		t.Fatalf("fast direct content concealed ambiguous relationship: %+v", e)
	}
}

func TestCoalescedLatencyMatchesExistingResultAndCannotClaimAnotherPrimaryOrigin(t *testing.T) {
	for _, otherPrimary := range []bool{false, true} {
		primary, extra, child, link := coalescedFixture()
		otherChild := latencyMetric("item_4", map[string]string{"response_to_speech": "false", "end_silence_ms": "0"})
		additional := coalescedDelivery(map[string]string{"turn": "item_4"})
		lines := []Line{primary, extra, child, otherChild, link}
		wantRows := 1
		if otherPrimary {
			additional = coalescedDelivery(nil)
			lines = append(lines, latencyDelivery(map[string]string{"origin_turn": "item_3", "turn": "item_4", "task": "task_2"}))
			wantRows = 2
		}
		e := latencyCall(t, append(lines, additional)...).Latency
		if e.InvalidLinks != 1 || len(e.ResultRows) != wantRows {
			t.Fatalf("mismatching result or previously claimed origin accepted: %+v", e)
		}
		for _, row := range e.ResultRows {
			if row.Coalesced {
				t.Fatalf("invalid coalesced row: %+v", row)
			}
		}
	}
}

func TestCoalescedLatencyInterruptedChildKeepsObservedContent(t *testing.T) {
	primary, extra, child, link := coalescedFixture()
	child.kv["truncated"] = "true"
	e := latencyCall(t, primary, extra, child, link, coalescedDelivery(nil)).Latency
	if e.Joined != 2 || len(e.ResultRows) != 2 || e.QuoteReadout.N != 2 || !e.ResultRows[0].Interrupted || !e.ResultRows[1].Interrupted {
		t.Fatalf("observed interrupted content lost: %+v", e)
	}
}
