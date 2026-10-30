package journal

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func transcriptLog(ms int, msg string, fields map[string]string) Line {
	kv := map[string]string{"session": "sess_latency"}
	for k, v := range fields {
		kv[k] = v
	}
	return Line{Time: latencyTestTime.Add(time.Duration(ms) * time.Millisecond), Level: "DEBUG", Msg: msg, kv: kv}
}
func transcriptEnabled() Line {
	return transcriptLog(0, "transcript: assistant output enabled", map[string]string{"version": "1"})
}
func transcriptSegment(id string, seq, ms int, text string) Line {
	return transcriptLog(ms+100, "transcript: assistant segment started", map[string]string{"version": "1", "turn": id, "seq": fmt.Sprint(seq), "play_at": latencyTestTime.Add(time.Duration(ms) * time.Millisecond).Format(time.RFC3339Nano), "text": text})
}
func legacySegment(id, text string, ms int) Line {
	return transcriptLog(ms, "speak: segment synthesized", map[string]string{"turn": id, "text": text})
}
func assistantLines(c *Call) []Said {
	var out []Said
	for _, s := range c.Transcript {
		if s.Who == "assistant" {
			out = append(out, s)
		}
	}
	return out
}

func TestOutputTranscriptKeepsPreparedPrefixOnceAtOutputTime(t *testing.T) {
	prefix, body := strings.Repeat("前", 13), strings.Repeat("后", 67)
	c := latencyCall(t, transcriptEnabled(),
		transcriptLog(900, "speak: prepared quote segment played", map[string]string{"turn": "item_3", "chars": "13", "audio_ms": "600"}),
		transcriptSegment("item_3", 1, 1000, prefix),
		transcriptLog(1250, "speak: first audio for segment", map[string]string{"turn": "item_3", "ms": "250"}),
		legacySegment("item_3", body, 1300), transcriptSegment("item_3", 2, 1600, body))
	lines := assistantLines(c)
	if len(lines) != 1 || lines[0].Text != prefix+body || lines[0].TS != 1 {
		t.Fatalf("prepared prefix or output time lost: %+v", lines)
	}
	if strings.Count(TranscriptText(c), prefix) != 1 {
		t.Fatal("archive transcript missing or duplicating prepared prefix")
	}
}

func TestOutputTranscriptIgnoresUnplayedReadinessAndKeepsActualOrder(t *testing.T) {
	c := latencyCall(t, transcriptEnabled(),
		legacySegment("item_1", "never output", 50),
		transcriptLog(60, "speak: segment played from the phrase cache", map[string]string{"turn": "item_2", "text": "queued only"}),
		transcriptLog(70, "speak: prepared quote segment played", map[string]string{"turn": "item_3", "chars": "13"}),
		legacySegment("item_4", "rendered later", 80),
		transcriptSegment("item_4", 1, 100, "same"), transcriptSegment("item_4", 1, 100, "same"),
		transcriptSegment("item_4", 2, 200, "same"), legacySegment("item_4", "same", 300))
	lines := assistantLines(c)
	if len(lines) != 1 || lines[0].Text != "samesame" || lines[0].TS != .1 {
		t.Fatalf("queued or duplicate text entered transcript: %+v", lines)
	}
}

func TestOutputTranscriptTruncationAndLegacyFallback(t *testing.T) {
	for _, tc := range []struct {
		name  string
		heard string
		want  int
	}{{"partial", "heard", 1}, {"before output", "", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			c := latencyCall(t, transcriptEnabled(), transcriptSegment("item_1", 1, 100, "heard and unheard"), legacySegment("item_1", "unheard tail", 150), transcriptLog(160, "speak: turn truncated", map[string]string{"turn": "item_1", "heard_text": tc.heard, "played_ms": "60"}))
			lines := assistantLines(c)
			if len(lines) != tc.want || (tc.want == 1 && (lines[0].Text != tc.heard || !lines[0].Interrupted)) {
				t.Fatalf("truncation %+v", lines)
			}
		})
	}
	legacy := latencyCall(t, transcriptLog(100, "speak: first audio for segment", map[string]string{"turn": "item_1", "ms": "25"}), legacySegment("item_1", "legacy", 200))
	if lines := assistantLines(legacy); len(lines) != 1 || lines[0].Text != "legacy" || lines[0].TS != .1 {
		t.Fatalf("legacy fallback %+v", lines)
	}
}

func TestOutputTranscriptIncludesOpenerOnceAndUsesOutputForTurn(t *testing.T) {
	c := latencyCall(t, transcriptEnabled(),
		transcriptLog(50, "speak: opened the answer from the phrase cache at the pause", map[string]string{"turn": "item_1", "text": "opener"}),
		transcriptSegment("item_1", 1, 1000, "opener"), legacySegment("item_1", "body", 1200), transcriptSegment("item_1", 2, 1500, "body"),
		latencyMetric("item_1", map[string]string{"first_audio_out_ms": "500", "first_content_ms": "1000", "tts_first_audio_ms": "0", "opened": "true"}))
	lines := assistantLines(c)
	if len(lines) != 1 || lines[0].Text != "openerbody" || lines[0].TS != 1 {
		t.Fatalf("opener duplicated or timestamp reconstructed from readiness: %+v", lines)
	}
	if len(c.Turns) != 1 || c.Turns[0].AssistantText != "openerbody" || c.Turns[0].AudioOutS != 1 {
		t.Fatalf("turn output %+v", c.Turns)
	}
}

func TestOutputTranscriptRejectsMalformedEventsAndOtherSessions(t *testing.T) {
	wrong := transcriptSegment("item_1", 1, 100, "other call")
	wrong.kv["session"] = "another"
	bad := transcriptSegment("item_1", 1, 100, "bad time")
	bad.kv["play_at"] = "invalid"
	empty := transcriptSegment("", 1, 100, "missing turn")
	c := latencyCall(t, transcriptEnabled(), wrong, bad, empty, transcriptSegment("item_1", 1, 100, "valid"))
	if lines := assistantLines(c); len(lines) != 1 || lines[0].Text != "valid" {
		t.Fatalf("invalid transcript events accepted: %+v", lines)
	}
}
