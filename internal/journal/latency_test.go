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
		latencyStart("sess_latency", "call-latency"), o, latencyDelivery(nil), c})
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
