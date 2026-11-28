package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
)

func TestRealtimeProtectedOpeningSurvivesCallerSpeech(t *testing.T) {
	ts, _, _ := gatewayServer(t, func(cfg *config.Config) {
		cfg.Realtime.HoldForOpening = true
		cfg.Duplex.ProtectOpening = true
	})
	c := dialRealtime(t, ts, "/v1/realtime")
	sendRaw(t, c.conn, activeCallSessionUpdate)
	c.first("session.updated", 2*time.Second)
	const greeting = "您好，这是来电的固定开场，我们先完整说明来电目的，然后再根据您的回答继续交流。"
	postJSON(t, ts.URL+"/v1/opening", `{"greeting":"`+greeting+`","warm":false}`)
	await(t, "opening playback", 3*time.Second, func() bool { return c.audioMS(16000) > 150 })
	c.turn(16000, 700)
	if c.count("input_audio_buffer.speech_started") != 0 || c.count("response.created") != 1 {
		t.Fatal("gateway was told to replace the protected opening")
	}
	done := c.first("response.done", 10*time.Second)
	var response struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(done["response"], &response); err != nil || response.Status != "completed" {
		t.Fatalf("opening completion: %s, %v", response.Status, err)
	}
	if !strings.HasPrefix(c.text("response.audio_transcript.delta"), greeting) {
		t.Fatal("opening transcript was incomplete")
	}
	await(t, "answer to retained caller speech", 5*time.Second, func() bool { return c.count("response.created") >= 2 })
	if c.index("response.created", 1) < c.index("response.done", 0) {
		t.Fatal("caller response preceded opening completion")
	}
}
