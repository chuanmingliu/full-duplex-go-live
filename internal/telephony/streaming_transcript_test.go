package telephony

import (
	"encoding/json"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/gorilla/websocket"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSimStreamingTranscriptCapture(t *testing.T) {
	s := startSim(t, "pcma")
	s.answer(t)
	for _, kind := range []string{"conversation.item.input_audio_transcription.delta", "response.audio_transcript.delta"} {
		s.agent.send <- map[string]any{"type": kind, "delta": "captured words", "item_id": "input_stream", "response_id": "response_1"}
	}
	s.agent.send <- &websocket.CloseError{Code: websocket.CloseNormalClosure, Text: live.CloseAssistantHangup}
	if r := s.finish(t, true); r.err != nil {
		t.Fatal(r.err)
	}
	b, err := os.ReadFile(filepath.Join(s.c.RecordDir, "sim", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e struct {
			Type string
			Data map[string]any
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		if e.Type == "transcript_delta" {
			n++
			if e.Data["text"] != "captured words" {
				t.Fatal(e)
			}
		}
	}
	if n != 2 {
		t.Fatalf("got %d transcript events", n)
	}
}
