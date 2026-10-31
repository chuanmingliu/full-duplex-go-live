package speko

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/gorilla/websocket"
)

func TestGoodbyeRequiresReciprocalCompletePhrasesAndDrainedPlayback(t *testing.T) {
	now := time.Now()
	var g goodbyeGate
	g.caller("嗯，拜", now)
	g.caller("拜", now.Add(100*time.Millisecond))
	if g.ready(now.Add(3*time.Second), now.Add(time.Second), true) {
		t.Fatal("caller farewell alone closed the call")
	}
	g.assistant("嗯，", now.Add(300*time.Millisecond))
	g.assistant("拜拜。", now.Add(500*time.Millisecond))
	last := now.Add(time.Second)
	if g.ready(now.Add(1500*time.Millisecond), last, true) || g.ready(now.Add(3*time.Second), last, false) {
		t.Fatal("farewell playback was cut short")
	}
	if !g.ready(now.Add(3*time.Second), last, true) {
		t.Fatal("completed reciprocal farewell did not close")
	}
	if g.ready(now.Add(3*time.Second), now, true) {
		t.Fatal("transcript without subsequent audio closed the call")
	}
	g.caller("等一下", now.Add(3100*time.Millisecond))
	if g.ready(now.Add(5*time.Second), last, true) {
		t.Fatal("caller continuation did not cancel hangup")
	}
}

func TestGoodbyeRejectsQuotesNegationAndAdditionalSpeech(t *testing.T) {
	for _, text := range []string{"不要挂断，再见是什么意思", "他说拜拜", "\"bye\"", "'bye'", "“拜拜”", "bye?", "拜拜，还有个问题", "bye, don't hang up", "好的", "嗯", ""} {
		if farewell(text) {
			t.Errorf("accepted ambiguous phrase %q", text)
		}
	}
	now := time.Now()
	g := goodbyeGate{}
	g.caller("bye", now)
	g.assistant("goodbye", now.Add(time.Second))
	g.audio(bytes.Repeat([]byte{0x33}, 160), newPCMTransport("g711_alaw").codec, now.Add(2*time.Second))
	if g.ready(now.Add(4*time.Second), now.Add(2*time.Second), true) {
		t.Fatal("fresh microphone speech did not cancel hangup")
	}
}

func TestBridgeGoodbyeClosesNormallyAfterAudio(t *testing.T) {
	upstream := router(t, func(c *websocket.Conn, r *http.Request) {
		var e wireEvent
		if c.ReadJSON(&e) != nil {
			return
		}
		_ = c.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"audio": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}}})
		if c.ReadJSON(&e) != nil {
			return
		}
		for _, e := range []map[string]string{
			{"type": "session.input_transcript.delta", "delta": "嗯，拜"},
			{"type": "session.input_transcript.delta", "delta": "拜"},
			{"type": "session.output_transcript.delta", "delta": "嗯，拜拜。"},
			{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(newPCMTransport("g711_alaw").encode(bytes.Repeat([]byte{0x33}, 1600)))},
		} {
			_ = c.WriteJSON(e)
		}
		for c.ReadJSON(&e) == nil {
		}
	})
	s, server := relayFor(t, upstream.URL, func(cfg *Config) { cfg.AutoHangup = true })
	c := bridgeClient(t, server, "goodbye", "g711_alaw")
	defer c.Close()
	opening(t, server, "goodbye")
	_ = c.SetReadDeadline(time.Now().Add(4 * time.Second))
	frames := 0
	for {
		var e wireEvent
		err := c.ReadJSON(&e)
		if err != nil {
			var closed *websocket.CloseError
			if !errors.As(err, &closed) || closed.Code != websocket.CloseNormalClosure || closed.Text != live.CloseAssistantHangup {
				t.Fatalf("wrong termination: %v", err)
			}
			break
		}
		if e.Type == "response.audio.delta" {
			frames++
		}
	}
	if frames != 10 {
		t.Fatalf("farewell truncated: %d frames", frames)
	}
	waitEmpty(t, s)
}
