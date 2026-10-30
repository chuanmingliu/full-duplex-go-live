package journal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

var latencyTestTime = time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)

func latencyMetric(id string, changes map[string]string) Line {
	kv := map[string]string{"session": "sess_latency", "turn": id,
		"response_to_speech": "true", "first_audio_present": "true", "first_content_present": "true",
		"opened": "true", "first_audio_out_ms": "0", "first_content_ms": "500", "opener_ms": "340",
		"end_silence_ms": "380", "asr_final_ms": "0", "llm_first_token_ms": "0", "turn_start_ms": "0", "truncated": "false"}
	for k, v := range changes {
		kv[k] = v
	}
	return Line{Time: latencyTestTime.Add(time.Second), Level: "INFO", Msg: "turn: response latency", kv: kv}
}

func latencyDelivery(changes map[string]string) Line {
	kv := map[string]string{"session": "sess_latency", "task": "task_1", "origin_turn": "item_1", "turn": "item_2", "origin_to_result_start_ms": "450"}
	for k, v := range changes {
		kv[k] = v
	}
	return Line{Time: latencyTestTime.Add(time.Second), Level: "INFO", Msg: "tools: delivering a task's result", kv: kv}
}

func latencyStart(session, call string) Line {
	return Line{Time: latencyTestTime, Level: "INFO", Msg: "session started", kv: map[string]string{"session": session, "call": call, "realtime": "true"}}
}

func latencyCall(t *testing.T, lines ...Line) *Call {
	t.Helper()
	ix := NewIndexer()
	ix.Feed(latencyStart("sess_latency", "call-latency"))
	for _, l := range lines {
		ix.Feed(l)
	}
	for _, c := range ix.Calls() {
		if c.ID == "call-latency" {
			return c
		}
	}
	t.Fatal("missing synthetic call")
	return nil
}

func latencyToolPair() (Line, Line) {
	return latencyMetric("item_1", map[string]string{"first_content_present": "false", "first_content_ms": "0"}),
		latencyMetric("item_2", map[string]string{"response_to_speech": "false", "opened": "false", "opener_ms": "0", "end_silence_ms": "0", "first_content_ms": "0"})
}

func TestLatencyExplicitCallerZeroSurvivesAndResultDoesNotBecomeCaller(t *testing.T) {
	caller := latencyMetric("item_1", map[string]string{"first_content_ms": "0", "end_silence_ms": "0"})
	result := latencyMetric("item_2", map[string]string{"response_to_speech": "false", "llm_first_token_ms": "800"})
	c := latencyCall(t, caller, result)
	if len(c.Turns) != 1 || c.Turns[0].ID != "item_1" || c.Summary.Turns != 1 || c.Turns[0].TotalMS != 0 {
		t.Fatalf("caller summary %+v turns %+v", c.Summary, c.Turns)
	}
	e := c.Latency
	if e.FirstAudio.N != 1 || e.FirstContent.N != 1 || e.FirstContent.P95 == nil || *e.FirstContent.P95 != 0 {
		t.Fatalf("evidence %+v", e)
	}
	raw, _ := json.Marshal(e)
	if !strings.Contains(string(raw), `"p95_ms":0`) {
		t.Fatalf("zero omitted: %s", raw)
	}
	empty := latencyCall(t, result).Latency
	raw, _ = json.Marshal(empty)
	if empty.FirstContent.P95 != nil || !strings.Contains(string(raw), `"p95_ms":null`) {
		t.Fatalf("absence became zero: %s", raw)
	}
}

func TestLatencyToolResultAddsOffsetAndCallerPauseExactlyOnce(t *testing.T) {
	origin, child := latencyToolPair()
	origin.kv["end_silence_ms"], origin.kv["first_audio_out_ms"] = "600", "42"
	c := latencyCall(t, child, latencyDelivery(nil), origin)
	e := c.Latency
	if e.Joined != 1 || e.Missing != 0 || e.Unverified != 0 || e.FirstAudio.N != 1 || *e.FirstAudio.P95 != 642 || e.FirstContent.N != 1 || *e.FirstContent.P95 != 1050 {
		t.Fatalf("joined %+v", e)
	}
	if c.Summary.Turns != 1 || c.Summary.P95 != 642 {
		t.Fatalf("filler summary was replaced by result: %+v", c.Summary)
	}
	if e.ToolResult.N != 1 || *e.ToolResult.P95 != 1050 || len(e.ResultRows) != 1 || e.ResultRows[0].PauseMS != 600 {
		t.Fatalf("result offset or pause changed: %+v", e)
	}
}

func TestLatencyResultSpeechRemainsInTranscriptWithoutBecomingAGreeting(t *testing.T) {
	origin, child := latencyToolPair()
	audio := Line{Time: latencyTestTime.Add(time.Second), Level: "DEBUG", Msg: "speak: first audio for segment", kv: map[string]string{"session": "sess_latency", "turn": "item_2", "ms": "0"}}
	words := Line{Time: latencyTestTime.Add(2 * time.Second), Level: "DEBUG", Msg: "speak: segment synthesized", kv: map[string]string{"session": "sess_latency", "turn": "item_2", "text": "示例结果。"}}
	c := latencyCall(t, origin, latencyDelivery(nil), audio, words, child)
	if len(c.Transcript) != 1 || c.Transcript[0].Text != "示例结果。" || c.Transcript[0].Kind != "answer" || c.Transcript[0].Turn != 0 {
		t.Fatalf("result speech %+v", c.Transcript)
	}
	if c.Summary.Turns != 1 || c.Latency.FirstContent.N != 1 {
		t.Fatal("result speech became an extra caller sample")
	}
}

func TestLatencyMissingAndAmbiguousLinksCannotFillContent(t *testing.T) {
	cases := map[string]func() []Line{
		"missing link":  func() []Line { o, c := latencyToolPair(); return []Line{o, c} },
		"missing child": func() []Line { o, _ := latencyToolPair(); return []Line{o, latencyDelivery(nil)} },
		"legacy link": func() []Line {
			o, c := latencyToolPair()
			return []Line{o, latencyDelivery(map[string]string{"origin_to_result_start_ms": ""}), c}
		},
		"duplicate link": func() []Line {
			o, c := latencyToolPair()
			return []Line{o, latencyDelivery(nil), latencyDelivery(nil), c}
		},
		"conflicting offset": func() []Line {
			o, c := latencyToolPair()
			return []Line{o, latencyDelivery(nil), latencyDelivery(map[string]string{"origin_to_result_start_ms": "451"}), c}
		},
		"conflicting task": func() []Line {
			o, c := latencyToolPair()
			return []Line{o, latencyDelivery(nil), latencyDelivery(map[string]string{"task": "task_2"}), c}
		},
		"shared result": func() []Line {
			o, c := latencyToolPair()
			return []Line{o, latencyDelivery(nil), latencyDelivery(map[string]string{"origin_turn": "item_3", "task": "task_2"}), c}
		},
		"duplicate child": func() []Line { o, c := latencyToolPair(); return []Line{o, latencyDelivery(nil), c, c} },
		"negative offset": func() []Line {
			o, c := latencyToolPair()
			return []Line{o, latencyDelivery(map[string]string{"origin_to_result_start_ms": "-1"}), c}
		},
		"other session child": func() []Line {
			o, c := latencyToolPair()
			c.kv["session"] = "other"
			return []Line{o, latencyDelivery(nil), c}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			e := latencyCall(t, setup()...).Latency
			if e.Joined != 0 || e.FirstContent.N != 0 || e.Missing != 1 {
				t.Fatalf("invalid join %+v", e)
			}
			if name != "missing link" && e.Unverified == 0 {
				t.Fatalf("invalid link was not marked: %+v", e)
			}
			if e.ToolResult.N != 0 || len(e.ResultRows) != 0 {
				t.Fatalf("invalid link became result evidence: %+v", e)
			}
		})
	}
}

func TestLatencyInterruptedResultRequiresActualContentAndCancelledOriginStaysCancelled(t *testing.T) {
	o, c := latencyToolPair()
	c.kv["truncated"] = "true"
	if e := latencyCall(t, o, latencyDelivery(nil), c).Latency; e.Joined != 1 || e.FirstContent.N != 1 {
		t.Fatalf("heard result excluded %+v", e)
	}
	c.kv["first_content_present"] = "false"
	if e := latencyCall(t, o, latencyDelivery(nil), c).Latency; e.Joined != 0 || e.Missing != 1 || e.Unverified == 0 {
		t.Fatalf("unplayed result counted %+v", e)
	}
	c.kv["first_content_present"] = "true"
	o.kv["truncated"] = "true"
	if e := latencyCall(t, o, latencyDelivery(nil), c).Latency; e.Joined != 0 || e.Cancelled != 1 || e.FirstContent.N != 0 {
		t.Fatalf("cancelled origin resurrected %+v", e)
	}
}

func TestLatencyLegacyAndMissingIdentityCannotEstablishEvidence(t *testing.T) {
	for _, key := range []string{"response_to_speech", "first_audio_present", "first_content_present", "opened", "turn"} {
		t.Run(key, func(t *testing.T) {
			l := latencyMetric("item_1", map[string]string{"llm_first_token_ms": "300"})
			delete(l.kv, key)
			e := latencyCall(t, l).Latency
			if e.Unverified != 1 || e.FirstAudio.N != 0 || e.FirstContent.N != 0 {
				t.Fatalf("legacy evidence %+v", e)
			}
		})
	}
}

func TestLatencyDistributionPreservesHalfMillisecondMedianAndInclusiveTarget(t *testing.T) {
	d := latencyDistribution([]int{TargetMS - 1, TargetMS})
	if d.P50 == nil || *d.P50 != float64(TargetMS)-.5 || *d.P95 != TargetMS || d.Over != 1 {
		t.Fatalf("distribution %+v", d)
	}
}

func TestLatencyDirectContentIsNeverReplacedAndBadLinksRemainUnverified(t *testing.T) {
	_, child := latencyToolPair()
	good := latencyCall(t, latencyMetric("item_1", nil), latencyDelivery(nil), child).Latency
	if good.Joined != 0 || *good.FirstContent.P95 != 880 || good.Unverified != 0 {
		t.Fatalf("direct response changed %+v", good)
	}
	child.kv["first_content_present"] = "false"
	bad := latencyCall(t, latencyMetric("item_1", nil), latencyDelivery(nil), child).Latency
	if bad.InvalidLinks != 1 || bad.Unverified != 1 || *bad.FirstContent.P95 != 880 {
		t.Fatalf("invalid link was hidden %+v", bad)
	}
}

func TestLatencySlowToolResultRemainsVisibleAfterFastDirectContent(t *testing.T) {
	_, child := latencyToolPair()
	child.kv["first_audio_out_ms"], child.kv["first_content_ms"] = "153", "153"
	origin := latencyMetric("item_1", map[string]string{"first_content_ms": "702"})
	c := latencyCall(t, origin, latencyDelivery(map[string]string{"origin_to_result_start_ms": "3015"}), child)
	e := c.Latency
	if e.FirstContent.N != 1 || *e.FirstContent.P95 != 1082 || e.Joined != 0 || c.Summary.Turns != 1 {
		t.Fatalf("first content or caller count changed: %+v", e)
	}
	if e.ToolResult.N != 1 || *e.ToolResult.P95 != 3548 || e.ToolResult.Over != 1 || len(e.ResultRows) != 1 {
		t.Fatalf("slow result hidden: %+v", e)
	}
	r := e.ResultRows[0]
	if r.OriginTurn != "item_1" || r.ResultTurn != "item_2" || r.PauseMS != 380 || r.OffsetMS != 3015 || r.ChildContentMS != 153 || r.TotalMS != 3548 {
		t.Fatalf("result decomposition %+v", r)
	}
}

func latencyQuotePair() (Line, Line) {
	o, c := latencyToolPair()
	c.kv["quote_result"], c.kv["readout_expected"], c.kv["first_readout_present"], c.kv["first_readout_ms"] = "true", "true", "true", "0"
	return o, c
}

func TestLatencyQuotationReadoutRequiresOutputNotCachedPosthook(t *testing.T) {
	o, c := latencyQuotePair()
	c.kv["first_content_ms"], c.kv["first_readout_ms"] = "1", "1265"
	e := latencyCall(t, o, latencyDelivery(map[string]string{"tool": "quote_total", "origin_to_result_start_ms": "542"}), c).Latency
	if e.FirstContent.N != 1 || *e.FirstContent.P95 != 923 || *e.ToolResult.P95 != 923 || e.Joined != 1 {
		t.Fatalf("ordinary content changed: %+v", e)
	}
	if e.QuoteReadout.N != 1 || *e.QuoteReadout.P95 != 2187 || e.QuoteReadout.Over != 1 || len(e.QuoteRows) != 1 || *e.QuoteRows[0].ChildReadoutMS != 1265 {
		t.Fatalf("posthook hid quotation readout: %+v", e)
	}
}

func TestLatencyQuotationReadoutZeroPresenceAndMissingStayDistinct(t *testing.T) {
	o, c := latencyQuotePair()
	c.kv["truncated"] = "true"
	link := latencyDelivery(map[string]string{"tool": "quote_total"})
	e := latencyCall(t, o, link, c).Latency
	if e.QuoteReadout.N != 1 || *e.QuoteReadout.P95 != 830 || *e.QuoteRows[0].ChildReadoutMS != 0 {
		t.Fatalf("played zero excluded: %+v", e)
	}
	raw, _ := json.Marshal(e.QuoteRows[0])
	if !strings.Contains(string(raw), `"child_first_readout_ms":0`) {
		t.Fatal("played zero omitted")
	}
	c.kv["first_readout_present"] = "false"
	e = latencyCall(t, o, link, c).Latency
	if e.QuoteReadout.N != 0 || e.QuoteMissing != 1 || e.FirstContent.N != 1 || e.QuoteRows[0].TotalMS != nil {
		t.Fatalf("unplayed readout became evidence: %+v", e)
	}
	raw, _ = json.Marshal(e.QuoteRows[0])
	if !strings.Contains(string(raw), `"total_ms":null`) {
		t.Fatal("missing readout became zero")
	}
}

func TestLatencyQuotationReadoutProvenanceIsFailClosed(t *testing.T) {
	for name, changes := range map[string]map[string]string{
		"legacy":                   {"quote_result": "", "readout_expected": "", "first_readout_present": ""},
		"no deterministic readout": {"readout_expected": "false"},
		"conflicting quote":        {"quote_result": "false"},
		"absent presence":          {"first_readout_present": ""},
		"malformed stamp":          {"first_readout_ms": "bad"},
		"before content":           {"first_content_ms": "1", "first_readout_ms": "0"},
		"negative stamp":           {"first_readout_ms": "-1"},
	} {
		t.Run(name, func(t *testing.T) {
			o, c := latencyQuotePair()
			for k, v := range changes {
				c.kv[k] = v
			}
			e := latencyCall(t, o, latencyDelivery(map[string]string{"tool": "quote_total"}), c).Latency
			if e.QuoteReadout.N != 0 || e.QuoteUnknown != 1 || e.FirstContent.N != 1 {
				t.Fatalf("unverified readout counted: %+v", e)
			}
		})
	}
	o, c := latencyQuotePair()
	c.kv["quote_result"], c.kv["readout_expected"], c.kv["first_readout_present"] = "false", "false", "false"
	e := latencyCall(t, o, latencyDelivery(map[string]string{"tool": "quote_total"}), c).Latency
	if len(e.QuoteRows) != 0 || e.QuoteUnknown != 0 || e.QuoteMissing != 0 {
		t.Fatalf("nonquote outcome treated as quote: %+v", e)
	}
}

func TestLatencyQuotationOutputStillRequiresUniqueOriginLink(t *testing.T) {
	o, c := latencyQuotePair()
	link := latencyDelivery(map[string]string{"tool": "quote_total"})
	for _, lines := range [][]Line{{o, c}, {o, link, link, c}, {o, link, c, c}} {
		e := latencyCall(t, lines...).Latency
		if e.QuoteReadout.N != 0 || e.QuoteUnknown+e.Unverified == 0 {
			t.Fatalf("missing or ambiguous origin counted: %+v", e)
		}
	}
}

func TestLatencyQuotationReadoutDoesNotBorrowAnotherSessionsOutput(t *testing.T) {
	o, c := latencyQuotePair()
	c.kv["first_readout_present"] = "false"
	otherO, otherC := latencyQuotePair()
	otherO.kv["session"], otherC.kv["session"] = "other", "other"
	otherLink := latencyDelivery(map[string]string{"session": "other", "tool": "quote_total"})
	e := latencyCall(t, otherO, otherLink, otherC, o, latencyDelivery(map[string]string{"tool": "quote_total"}), c).Latency
	if e.QuoteReadout.N != 0 || e.QuoteMissing != 1 || len(e.QuoteRows) != 1 {
		t.Fatalf("borrowed another session's readout: %+v", e)
	}
}

func latencyLog(lines []Line) string {
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "time=%s level=%s msg=%q", l.Time.Format(time.RFC3339Nano), l.Level, l.Msg)
		var keys []string
		for k := range l.kv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, " %s=%q", k, l.kv[k])
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func TestLatencyArchiveBackfillPreservesSourcesAndKeepsSessionsSeparate(t *testing.T) {
	dir := t.TempDir()
	o, c := latencyToolPair()
	otherO, otherC, otherLink := latencyToolPairForSession("other")
	log := latencyLog([]Line{latencyStart("other", "other-call"), otherO, otherLink, otherC,
		latencyStart("sess_latency", "call-latency"), o, latencyDelivery(map[string]string{"tool": "quote_total"}), c})
	path := filepath.Join(dir, "source.log")
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	old := &Call{ID: "call-latency", Session: "sess_latency", StartedAt: latencyTestTime, EndedAt: latencyTestTime.Add(time.Minute), Build: "preserved", Greeting: "synthetic greeting", Summary: Stats{Turns: 99}}
	s := NewServer(Config{ArchiveDir: filepath.Join(dir, "archive")})
	if _, err := s.archive.Put(old, Sources{LogPath: path, LogTo: int64(len(log))}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.archive.Path(old.ID, archCall))
	if err != nil {
		t.Fatal(err)
	}
	got := s.withLatencyEvidence(old)
	if got == old || got.Latency == nil || !got.Latency.Recomputed || got.Latency.Joined != 1 || got.Latency.FirstContent.N != 1 || *got.Latency.FirstContent.P95 != 830 || got.Build != old.Build || got.Greeting != old.Greeting {
		t.Fatalf("backfill %+v", got)
	}
	if old.Latency != nil || old.Summary.Turns != 99 {
		t.Fatal("backfill mutated the in-memory source")
	}
	old.Latency = &LatencyEvidence{Version: 2, FirstContent: latencyDistribution([]int{1}), QuoteReadout: latencyDistribution([]int{1})}
	upgraded := s.withLatencyEvidence(old)
	if upgraded.Latency.Version != 4 || upgraded.Latency.ToolResult.N != 1 || *upgraded.Latency.ToolResult.P95 != 830 || old.Latency.Version != 2 || upgraded.Latency.QuoteReadout.N != 0 || upgraded.Latency.QuoteUnknown != 1 {
		t.Fatalf("old evidence not upgraded independently: %+v", upgraded.Latency)
	}
	old.Latency = nil
	after, err := os.ReadFile(s.archive.Path(old.ID, archCall))
	if err != nil || string(before) != string(after) {
		t.Fatal("backfill changed the archived call")
	}
	wrong := *old
	wrong.Session = "other"
	if s.withLatencyEvidence(&wrong).Latency != nil {
		t.Fatal("borrowed another session's evidence")
	}
	missing := *old
	missing.ID = "not-archived"
	if s.withLatencyEvidence(&missing).Latency != nil {
		t.Fatal("missing log became measured evidence")
	}
}

func latencyToolPairForSession(session string) (Line, Line, Line) {
	o, c := latencyToolPair()
	l := latencyDelivery(map[string]string{"origin_to_result_start_ms": "5000"})
	o.kv["session"], c.kv["session"], l.kv["session"] = session, session, session
	return o, c, l
}
