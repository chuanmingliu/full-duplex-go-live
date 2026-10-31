package telephony

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/gorilla/websocket"
)

func TestSimAgentCloseClassification(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		code         int
		ack, failed  bool
	}{
		{"assistant-goodbye", live.CloseAssistantHangup, websocket.CloseNormalClosure, true, false},
		{"requested", live.CloseRequested, websocket.CloseNormalClosure, true, false},
		{"remote", live.CloseRemoteHangup, websocket.CloseNormalClosure, true, false},
		{"unknown-normal", "unknown", websocket.CloseNormalClosure, true, true},
		{"shutdown", live.CloseShutdown, websocket.CloseGoingAway, true, true},
		{"wrong-code", live.CloseAssistantHangup, websocket.CloseInternalServerErr, true, true},
		{"bye-not-acknowledged", live.CloseAssistantHangup, websocket.CloseNormalClosure, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := startSim(t, "pcma")
			s.answer(t)
			s.agent.send <- &websocket.CloseError{Code: tc.code, Text: tc.reason}
			result := s.finish(t, tc.ack)
			if (result.err != nil) != tc.failed {
				t.Fatalf("normal goodbye classified incorrectly: failed=%v want=%v err=%v", result.err != nil, tc.failed, result.err)
			}
			data, err := os.ReadFile(filepath.Join(s.c.RecordDir, "sim", "summary.json"))
			if err != nil {
				t.Fatal(err)
			}
			var summary struct{ Failed bool }
			if err := json.Unmarshal(data, &summary); err != nil || summary.Failed != tc.failed {
				t.Fatalf("incorrect saved call status: %s, err=%v", data, err)
			}
		})
	}
}
