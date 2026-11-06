package journal

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func handoffTranscriptLines() []Line {
	line := func(ms int, msg string, kv map[string]string) Line {
		kv["session"] = "sess_latency"
		return Line{Time: latencyTestTime.Add(time.Duration(ms) * time.Millisecond), Level: "DEBUG", Msg: msg, kv: kv}
	}
	origin := latencyMetric("item_1", map[string]string{"first_content_present": "false", "tts_first_audio_ms": "0"})
	origin.Time = latencyTestTime.Add(2 * time.Second)
	result := latencyMetric("item_2", map[string]string{"response_to_speech": "false", "opened": "false", "opener_ms": "0", "end_silence_ms": "0", "first_audio_out_ms": "100", "first_content_ms": "100", "tts_first_audio_ms": "100"})
	result.Time = latencyTestTime.Add(3 * time.Second)
	return []Line{
		line(250, "listen: utterance opened", map[string]string{"start_ms": "0"}),
		line(1380, "asr: the turn closed on words already final", map[string]string{"text": "Example request."}),
		line(1380, "think: delegating", map[string]string{"turn": "item_1", "transcript": "Example request."}),
		line(1380, "speak: opened the answer from the phrase cache at the pause", map[string]string{"turn": "item_1", "text": "Opener.", "audio_ms": "400"}),
		line(1500, "speak: first audio for segment", map[string]string{"turn": "item_1", "ms": "120"}),
		line(1600, "speak: segment synthesized", map[string]string{"turn": "item_1", "text": "Discarded synthesized body."}),
		line(1700, "speak: segment played from the phrase cache", map[string]string{"turn": "item_1", "text": "Discarded cached body."}),
		line(1800, "tools: ready result replaced the unplayed calling body", map[string]string{"turn": "item_1", "task": "task_1"}),
		line(1900, "speak: turn complete", map[string]string{"turn": "item_1", "audio_ms": "400", "chars": "7"}),
		origin,
		line(2100, "speak: segment synthesized", map[string]string{"turn": "item_1", "text": "Late discarded synthesis."}),
		line(2200, "speak: segment played from the phrase cache", map[string]string{"turn": "item_1", "text": "Late discarded cache."}),
		line(2500, "tools: delivering a task's result", map[string]string{"task": "task_1", "origin_turn": "item_1", "turn": "item_2", "origin_to_result_start_ms": "1120"}),
		line(2500, "think: delegating", map[string]string{"turn": "item_2", "reason": "task_result"}),
		line(2600, "speak: first audio for segment", map[string]string{"turn": "item_2", "ms": "100"}),
		line(2800, "speak: segment synthesized", map[string]string{"turn": "item_2", "text": "Actual result."}),
		result,
		line(4000, "session: response latency summary", map[string]string{}),
	}
}

func TestReadyHandoffTranscriptExcludesPreloggedAndLateBody(t *testing.T) {
	c := latencyCall(t, handoffTranscriptLines()...)
	if len(c.Transcript) != 3 || c.Transcript[1].Text != "Opener." || c.Transcript[2].Text != "Actual result." {
		t.Fatalf("discarded body entered the transcript: %+v", c.Transcript)
	}
	if len(c.Turns) != 1 || c.Turns[0].AssistantText != "Opener." || c.Turns[0].Interrupted || c.Turns[0].HeardMS != 0 || c.Turns[0].TotalMS != 380 {
		t.Fatalf("clean opener completion was changed: %+v", c.Turns)
	}
	if c.Events.Interruptions != 0 || c.DurationS != 4 {
		t.Fatalf("handoff invented an interruption or duration: %+v", c)
	}
	for _, marker := range c.Markers {
		if marker.Kind == "interrupted" {
			t.Fatalf("handoff invented an interruption marker: %+v", marker)
		}
	}
	if c.Latency.FirstAudio.N != 1 || c.Latency.ToolResult.N != 1 || c.Latency.Joined != 1 {
		t.Fatalf("handoff changed result grouping: %+v", c.Latency)
	}
}

func TestArchivedTranscriptEndpointReindexesWithoutRewritingSources(t *testing.T) {
	dir := t.TempDir()
	log := latencyLog(append([]Line{latencyStart("sess_latency", "call-latency")}, handoffTranscriptLines()...))
	path := filepath.Join(dir, "source.log")
	if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	old := &Call{ID: "call-latency", Session: "sess_latency", StartedAt: latencyTestTime, EndedAt: latencyTestTime.Add(4 * time.Second), Latency: &LatencyEvidence{Version: 1}, Transcript: []Said{{TS: 1, Who: "assistant", Text: "Discarded archived body."}}}
	s := NewServer(Config{ArchiveDir: filepath.Join(dir, "archive")})
	if _, err := s.archive.Put(old, Sources{LogPath: path, LogTo: int64(len(log))}); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, name := range []string{archCall, archTranscript, archLog} {
		b, err := os.ReadFile(s.archive.Path(old.ID, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = string(b)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/calls/call-latency/transcript.txt", nil))
	if body := w.Body.String(); w.Code != http.StatusOK || strings.Contains(body, "Discarded") || strings.Contains(body, "Late discarded") || !strings.Contains(body, "Opener.") || !strings.Contains(body, "Actual result.") {
		t.Fatalf("endpoint served stale transcript: status=%d body=%s", w.Code, body)
	}
	for name, want := range before {
		got, err := os.ReadFile(s.archive.Path(old.ID, name))
		if err != nil || string(got) != want {
			t.Fatalf("reindex changed archive source %s: %v", name, err)
		}
	}
}
