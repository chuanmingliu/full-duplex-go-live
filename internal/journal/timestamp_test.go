package journal

import (
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Empty recognition must not lend its earlier start to the next words. The
// recognized turn's measured last sound and the displayed start are distinct.
func TestCallerTimestampsFollowTheRecognizedUtteranceAfterEmptySpeech(t *testing.T) {
	for _, emptyFinal := range []string{"asr: the turn closed on words already final", "asr: final", ""} {
		name := emptyFinal
		if name == "" {
			name = "recognizer ended without a final"
		}
		t.Run(name, func(t *testing.T) {
			line := func(ms int, msg string, kv map[string]string) Line {
				kv["session"] = "sess_latency"
				return Line{Time: latencyTestTime.Add(time.Duration(ms) * time.Millisecond), Level: "DEBUG", Msg: msg, kv: kv}
			}
			lines := []Line{
				line(1250, "listen: utterance opened", map[string]string{"start_ms": "1000"}),
				line(1800, "listen: utterance closed", map[string]string{"start_ms": "1000", "end_ms": "1800", "active_ms": "200"}),
			}
			if emptyFinal != "" {
				lines = append(lines, line(1800, emptyFinal, map[string]string{"text": ""}))
			}
			lines = append(lines,
				line(10250, "listen: utterance opened", map[string]string{"start_ms": "10000"}),
				line(11380, "listen: utterance closed", map[string]string{"start_ms": "10000", "end_ms": "11380", "active_ms": "1000"}),
				line(11380, "asr: the turn closed on words already final", map[string]string{"text": "Example request."}),
				line(11380, "think: delegating", map[string]string{"turn": "item_1", "transcript": "Example request."}),
				line(11380, "speak: segment played from the phrase cache", map[string]string{"turn": "item_1", "text": "Sure.", "audio_ms": "300"}),
			)
			metric := latencyMetric("item_1", nil)
			metric.Time = latencyTestTime.Add(12 * time.Second)
			lines = append(lines, metric)
			c := latencyCall(t, lines...)
			if len(c.Transcript) != 2 || len(c.Turns) != 1 {
				t.Fatalf("empty speech became a transcript or latency sample: lines=%+v turns=%+v", c.Transcript, c.Turns)
			}
			caller := c.Transcript[0]
			if caller.Who != "caller" || caller.Text != "Example request." || caller.StartS != 10 || caller.TS != 11 || caller.Turn != 1 {
				t.Errorf("caller timestamps %+v; want recognized start 10 s and measured last sound 11 s", caller)
			}
			turn := c.Turns[0]
			if turn.LastSoundS != 11 || turn.AudioOutS != 11.38 || turn.TotalMS != 380 {
				t.Errorf("recognized turn's timing changed: %+v", turn)
			}
			if got := TranscriptText(c); !strings.Contains(got, "[00:10.0] caller") || strings.Contains(got, "[00:01.0] caller") {
				t.Errorf("export used the earlier empty utterance's start:\n%s", got)
			}
		})
	}
}

func TestCachedOpenerAnchorsTheTurnBeforeContentAudio(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ready, out int
	}{{"at speech close", 0, 0}, {"delayed output", 120, 400}, {"ready before speech close", -100, 40}} {
		t.Run(tc.name, func(t *testing.T) {
			line := func(ms int, msg string, kv map[string]string) Line {
				kv["session"] = "sess_latency"
				return Line{Time: latencyTestTime.Add(time.Duration(ms) * time.Millisecond), Level: "DEBUG", Msg: msg, kv: kv}
			}
			metric := latencyMetric("item_1", map[string]string{"first_content_ms": "600", "tts_first_audio_ms": strconv.Itoa(tc.ready), "first_audio_out_ms": strconv.Itoa(tc.out)})
			metric.Time = latencyTestTime.Add(12 * time.Second)
			lines := []Line{
				line(10250, "listen: utterance opened", map[string]string{"start_ms": "10000"}),
				line(11000, "think: delegating", map[string]string{"turn": "item_1", "transcript": "Example request."}),
				line(11380, "asr: the turn closed on words already final", map[string]string{"text": "Example request."}),
				line(11380+tc.ready, "speak: opened the answer from the phrase cache at the pause", map[string]string{"turn": "item_1", "text": "Sure.", "audio_ms": "300"}),
				line(11980, "speak: first audio for segment", map[string]string{"turn": "item_1", "ms": "200"}),
				line(12000, "speak: segment synthesized", map[string]string{"turn": "item_1", "text": "Example answer."}),
				metric,
			}
			sort.SliceStable(lines, func(i, j int) bool { return lines[i].Time.Before(lines[j].Time) })
			c := latencyCall(t, lines...)
			if len(c.Transcript) != 2 || len(c.Turns) != 1 {
				t.Fatalf("lines=%+v turns=%+v", c.Transcript, c.Turns)
			}
			wantOut := float64(11380+tc.out) / 1000
			if got := c.Turns[0]; got.LastSoundS != 11 || got.AudioOutS != wantOut || got.TotalMS != 380+tc.out {
				t.Errorf("content audio moved the opener's timeline: %+v", got)
			}
			if caller := c.Transcript[0]; caller.Who != "caller" || caller.StartS != 10 || caller.TS != 11 {
				t.Errorf("caller timing %+v", caller)
			}
			if answer := c.Transcript[1]; answer.Who != "assistant" || answer.TS != wantOut || answer.Text != "Sure.Example answer." {
				t.Errorf("opener was omitted or moved to the content start: %+v", answer)
			}
			if c.Latency.FirstAudio.N != 1 || *c.Latency.FirstAudio.P95 != 380+tc.out || c.Latency.FirstContent.N != 1 || *c.Latency.FirstContent.P95 != 980 {
				t.Errorf("opener indexing changed strict latency evidence: %+v", c.Latency)
			}
			if len(c.Fillers) != 1 || c.Fillers[0].Outcome != "queued" || c.Fillers[0].TS != float64(11380+tc.ready)/1000 {
				t.Errorf("queue evidence was rewritten as playback: %+v", c.Fillers)
			}
		})
	}
}

func TestUnconfirmedQueuedOpenerDoesNotCreatePlayback(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes map[string]string
	}{
		{"no metric", nil},
		{"not output", map[string]string{"first_audio_present": "false"}},
		{"output presence absent", map[string]string{"first_audio_present": ""}},
		{"opener not used", map[string]string{"opened": "false"}},
		{"readiness offset absent", map[string]string{"tts_first_audio_ms": ""}},
		{"output offset absent", map[string]string{"first_audio_out_ms": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := []Line{{Time: latencyTestTime.Add(time.Second), Level: "DEBUG", Msg: "speak: opened the answer from the phrase cache at the pause", kv: map[string]string{"session": "sess_latency", "turn": "item_1", "text": "Queued only.", "audio_ms": "300"}}}
			if tc.changes != nil {
				changes := map[string]string{"tts_first_audio_ms": "0", "first_content_present": "false"}
				for k, v := range tc.changes {
					changes[k] = v
				}
				lines = append(lines, latencyMetric("item_1", changes))
			}
			c := latencyCall(t, lines...)
			if len(c.Transcript) != 0 {
				t.Errorf("queued text became a spoken line: %+v", c.Transcript)
			}
			for _, turn := range c.Turns {
				if turn.LastSoundS >= 0 || turn.AudioOutS >= 0 {
					t.Errorf("queue event fabricated a timeline: %+v", turn)
				}
			}
			if len(c.Fillers) != 1 || c.Fillers[0].Outcome != "queued" {
				t.Errorf("queue evidence lost: %+v", c.Fillers)
			}
		})
	}
}
