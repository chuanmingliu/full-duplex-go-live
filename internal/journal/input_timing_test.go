package journal

import "testing"

func inputTimingDelivery(changes map[string]string) Line {
	l := latencyDelivery(map[string]string{"tool": "quote_total", "origin_timing_version": "1", "origin_speech_end_ms": "1000", "origin_end_silence_ms": "380"})
	for k, v := range changes {
		l.kv[k] = v
	}
	return l
}

func TestInputTimingDoesNotCreateCallingPlayback(t *testing.T) {
	_, child := latencyQuotePair()
	child.kv["first_content_ms"], child.kv["first_readout_ms"] = "100", "1000"
	e := latencyCall(t, child, inputTimingDelivery(map[string]string{"origin_to_result_start_ms": "5000"})).Latency
	if e.FirstAudio.N != 0 || e.FirstContent.N != 0 || e.Joined != 0 || e.InvalidLinks != 0 || e.ToolResult.N != 1 || *e.QuoteReadout.P95 != 6380 {
		t.Fatalf("invented playback or lost measured quote interval: %+v", e)
	}
	if e.ResultRows[0].OriginEvidence != "caller_input_v1" || e.QuoteRows[0].OriginEvidence != "caller_input_v1" {
		t.Fatal("input-only provenance lost")
	}
}

func TestInputTimingKeepsCancelledOriginsAndMissingReadouts(t *testing.T) {
	origin, child := latencyQuotePair()
	origin.kv["truncated"] = "true"
	child.kv["first_readout_present"] = "false"
	e := latencyCall(t, origin, child, inputTimingDelivery(nil)).Latency
	if e.Cancelled != 1 || e.Joined != 0 || e.FirstContent.N != 0 || e.ToolResult.N != 1 || e.QuoteMissing != 1 || e.QuoteReadout.N != 0 {
		t.Fatalf("cancelled opener or missing readout became a response: %+v", e)
	}
}

func TestInputTimingRejectsBadMetadata(t *testing.T) {
	_, child := latencyQuotePair()
	for _, field := range []string{"origin_timing_version", "origin_speech_end_ms", "origin_end_silence_ms"} {
		for _, value := range []string{"", "-1", "true", "1.0", "١", "9223372036854775808"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				link := inputTimingDelivery(map[string]string{field: value})
				if value == "" {
					delete(link.kv, field)
				}
				e := latencyCall(t, child, link).Latency
				if e.InvalidLinks != 1 || e.ToolResult.N != 0 {
					t.Fatalf("bad input timing accepted: %+v", e)
				}
			})
		}
	}
	for _, changes := range []map[string]string{{"origin_timing_version": "2"}, {"origin_end_silence_ms": "9223372036854775807"}} {
		e := latencyCall(t, child, inputTimingDelivery(changes)).Latency
		if e.InvalidLinks != 1 {
			t.Fatalf("unknown version or overflow accepted: %+v", e)
		}
	}
}

func TestInputTimingRetainsPlaybackAndIdentityChecks(t *testing.T) {
	origin, child := latencyQuotePair()
	badOrigin := latencyMetric("item_1", map[string]string{"response_to_speech": "false"})
	for _, rows := range [][]Line{
		{origin, origin, child, inputTimingDelivery(nil)},
		{badOrigin, child, inputTimingDelivery(nil)},
		{origin, child, inputTimingDelivery(map[string]string{"origin_end_silence_ms": "400"})},
		{child, child, inputTimingDelivery(nil)},
		{child, inputTimingDelivery(nil), inputTimingDelivery(nil)},
		{inputTimingDelivery(nil)},
	} {
		e := latencyCall(t, rows...).Latency
		if e.InvalidLinks == 0 || e.ToolResult.N != 0 {
			t.Fatalf("invalid evidence accepted: %+v", e)
		}
	}
	// An actual output without a metric is still an integrity failure even
	// though caller-input provenance can establish a later tool interval.
	c := latencyCall(t, child, inputTimingDelivery(nil), transcriptSegment("item_1", 1, 1000, "synthetic"))
	e := NewServer(Config{}).withLatencyEvidence(c).Latency
	if e.Unverified == 0 || e.MissingOutputRecords != 1 {
		t.Fatal("input timing hid missing output metric")
	}
}

func TestInputTimingCoalescingStillRequiresVerifiedPrimary(t *testing.T) {
	_, child := latencyQuotePair()
	extra := coalescedDelivery(map[string]string{"origin_timing_version": "1", "origin_speech_end_ms": "1200", "origin_end_silence_ms": "100", "origin_to_result_start_ms": "200"})
	e := latencyCall(t, extra, child, inputTimingDelivery(nil)).Latency
	if e.InvalidLinks != 0 || e.ToolResult.N != 2 || e.FirstAudio.N != 0 || e.ResultRows[1].TotalMS != 300 {
		t.Fatalf("coalesced input origin lost: %+v", e)
	}
	e = latencyCall(t, extra, extra, child, inputTimingDelivery(nil)).Latency
	if e.InvalidLinks != 2 || e.ToolResult.N != 1 {
		t.Fatalf("ambiguous coalesced origin accepted: %+v", e)
	}
}

func TestInputTimingConversationDistributionMatchesSeparateQuotePolicy(t *testing.T) {
	origin, child := latencyQuotePair()
	child.kv["first_content_ms"], child.kv["first_readout_ms"] = "100", "1000"
	ordinary := latencyMetric("item_100", nil)
	e := latencyCall(t, ordinary, origin, child, inputTimingDelivery(map[string]string{"origin_to_result_start_ms": "5000"})).Latency
	if e.FirstContent.N != 2 || e.ConversationalFirstContent.N != 1 || *e.ConversationalFirstContent.P95 != 880 || e.QuoteOnlyResponses != 1 || *e.QuoteReadout.P95 != 6380 {
		t.Fatalf("quote-only content inflated conversation population: %+v", e)
	}
	// A real conversational answer keeps its own timestamp even when a later
	// quotation result uses the same caller origin.
	origin.kv["first_content_present"], origin.kv["first_content_ms"] = "true", "500"
	e = latencyCall(t, ordinary, origin, child, inputTimingDelivery(nil)).Latency
	if e.ConversationalFirstContent.N != 2 || e.QuoteOnlyResponses != 0 {
		t.Fatal("real conversational response was excluded")
	}
}

func TestInputTimingEvidenceUpgradeDoesNotDoubleCountMissingOutput(t *testing.T) {
	c := latencyCall(t, transcriptSegment("item_1", 1, 1000, "synthetic"))
	c.Latency.Version = 4
	c.Latency.MissingOutputRecords = 1
	c.Latency.Unverified = 1
	e := NewServer(Config{}).withLatencyEvidence(c).Latency
	if e.Version != 5 || e.Unverified != 1 || e.MissingOutputRecords != 1 {
		t.Fatalf("old overlay counted twice: %+v", e)
	}
}
