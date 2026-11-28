package journal

import "testing"

func continuationLink(child string, offset string) Line {
	l := latencyDelivery(map[string]string{"turn": child, "origin_to_result_start_ms": offset, "origin_timing_version": "1", "origin_speech_end_ms": "1000", "origin_end_silence_ms": "380"})
	l.Msg = "tools: continuing with function results"
	delete(l.kv, "task")
	return l
}

func TestContinuationLatencyIncludesAllModelRoundsAndPause(t *testing.T) {
	origin, child := latencyToolPair()
	child.kv["first_content_ms"] = "655"
	e := latencyCall(t, origin, continuationLink("item_2", "508"), child).Latency
	if e.Missing != 0 || e.InvalidLinks != 0 || e.ContinuationsJoined != 1 || *e.FirstContent.P95 != 1543 || e.ToolResult.N != 0 {
		t.Fatalf("%+v", e)
	}
}

func TestContinuationKeepsEarliestSpeechBeforeQuote(t *testing.T) {
	origin, child := latencyToolPair()
	child.kv["first_content_ms"] = "600"
	quote := latencyMetric("item_3", map[string]string{"response_to_speech": "false", "end_silence_ms": "0", "first_content_ms": "100", "quote_result": "true", "readout_expected": "true", "first_readout_present": "true", "first_readout_ms": "200"})
	e := latencyCall(t, origin, continuationLink("item_2", "500"), child, quote, latencyDelivery(map[string]string{"turn": "item_3", "origin_to_result_start_ms": "2000"})).Latency
	if e.InvalidLinks != 0 || e.QuoteOnlyResponses != 0 || e.ConversationalFirstContent.N != 1 || *e.FirstContent.P95 != 1480 || *e.QuoteReadout.P95 != 2580 {
		t.Fatalf("%+v", e)
	}
}

func TestContinuationChainWithoutOpenerAndInvalidLinks(t *testing.T) {
	origin, child := latencyToolPair()
	origin.kv["first_audio_present"] = "false"
	child.kv["first_audio_out_ms"], child.kv["first_content_ms"] = "100", "200"
	next := latencyMetric("item_3", map[string]string{"response_to_speech": "false", "end_silence_ms": "0", "first_audio_out_ms": "100", "first_content_ms": "200"})
	e := latencyCall(t, origin, continuationLink("item_3", "1000"), next, continuationLink("item_2", "500"), child).Latency
	if e.Missing != 0 || *e.FirstAudio.P95 != 980 || *e.FirstContent.P95 != 1080 || e.FirstContent.N != 1 {
		t.Fatalf("%+v", e)
	}
	for _, link := range []Line{continuationLink("item_2", "-1"), continuationLink("item_2", "9223372036854775807")} {
		if got := latencyCall(t, origin, link, child).Latency; got.InvalidLinks != 1 {
			t.Fatalf("%+v", got)
		}
	}
	link := continuationLink("item_2", "500")
	if got := latencyCall(t, origin, link, link, child).Latency; got.InvalidLinks != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestSilentContinuationStillValidatesLink(t *testing.T) {
	origin, child := latencyToolPair()
	child.kv["first_audio_present"], child.kv["first_content_present"] = "false", "false"
	link := continuationLink("item_2", "500")
	got := latencyCall(t, origin, link, child).Latency
	if got.InvalidLinks != 0 || len(got.ContinuationRows) != 0 || got.Missing != 1 {
		t.Fatalf("%+v", got)
	}
	link.kv["origin_to_result_start_ms"] = "-1"
	if got := latencyCall(t, origin, link, child).Latency; got.InvalidLinks != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestInputOnlyConfirmationCountedOnce(t *testing.T) {
	_, child := latencyToolPair()
	child.kv["first_content_ms"] = "824"
	child.kv["first_audio_out_ms"] = "824"
	link := continuationLink("item_2", "436")
	got := latencyCall(t, link, child).Latency
	if got.InputOnlyContinuations != 1 || got.ConversationalFirstContent.N != 1 || *got.FirstContent.P95 != 1640 {
		t.Fatalf("%+v", got)
	}
	next := latencyMetric("item_3", map[string]string{"response_to_speech": "false", "end_silence_ms": "0", "first_audio_out_ms": "100", "first_content_ms": "200"})
	got = latencyCall(t, continuationLink("item_3", "2000"), next, link, child).Latency
	if got.ConversationalFirstContent.N != 1 || *got.FirstContent.P95 != 1640 {
		t.Fatalf("%+v", got)
	}
	next.kv["first_content_ms"] = "2000"
	got = latencyCall(t, continuationLink("item_3", "100"), next, link, child).Latency
	if got.FirstAudio.N != 1 || *got.FirstAudio.P95 != 580 || *got.FirstContent.P95 != 1640 {
		t.Fatalf("audio and content must retain independent first boundaries: %+v", got)
	}
	got = latencyCall(t, link, link, child).Latency
	if got.InputOnlyContinuations != 0 || got.InvalidLinks != 2 {
		t.Fatalf("%+v", got)
	}
	wrong := continuationLink("item_3", "2000")
	wrong.kv["origin_speech_end_ms"] = "2000"
	got = latencyCall(t, wrong, next, link, child).Latency
	if got.InputOnlyContinuations != 0 || got.InvalidLinks != 1 {
		t.Fatalf("inconsistent caller clocks: %+v", got)
	}
}
