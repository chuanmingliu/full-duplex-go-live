package speko

import (
	"bytes"
	"encoding/base64"
	"github.com/gorilla/websocket"
	"net/http"
	"testing"
	"time"
)

func TestBridgeSurvivesInputRateWarning(t *testing.T) {
	upstream := router(t, func(c *websocket.Conn, r *http.Request) {
		var e wireEvent
		if c.ReadJSON(&e) != nil {
			return
		}
		_ = c.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"audio": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}}})
		_ = c.WriteJSON(map[string]any{"type": "error", "error": map[string]string{"code": "input_audio_rate_limit_exceeded", "message": "Excess audio was dropped; the session remains usable."}})
		_ = c.WriteJSON(map[string]string{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(newPCMTransport("g711_alaw").encode(bytes.Repeat([]byte{0x33}, 160)))})
		for c.ReadJSON(&e) == nil {
		}
	})
	s, server := relayFor(t, upstream.URL, nil)
	c := bridgeClient(t, server, "recoverable-rate", "g711_alaw")
	defer c.Close()
	opening(t, server, "recoverable-rate")
	for {
		var e wireEvent
		if err := c.ReadJSON(&e); err != nil {
			t.Fatal(err)
		}
		if e.Type == "error" {
			t.Fatalf("recoverable rate warning terminated the call: %s", e.Error)
		}
		if e.Type == "response.audio.delta" {
			break
		}
	}
	c.Close()
	waitEmpty(t, s)
}

func TestCallerJitterDoesNotAddDuplicateSilence(t *testing.T) {
	count := make(chan int, 1)
	upstream := router(t, func(c *websocket.Conn, r *http.Request) {
		var e wireEvent
		if c.ReadJSON(&e) != nil {
			return
		}
		_ = c.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"audio": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}}})
		n := 0
		tail := newPCMTransport("g711_alaw").encode([]byte{0x44})
		for c.ReadJSON(&e) == nil {
			if e.Type != "session.input_audio.append" {
				continue
			}
			n++
			pcm, _ := base64.StdEncoding.DecodeString(e.Audio)
			if len(pcm) >= 6 && bytes.Equal(pcm[len(pcm)-6:], tail) {
				count <- n
				return
			}
		}
	})
	s, server := relayFor(t, upstream.URL, nil)
	c := bridgeClient(t, server, "jitter", "g711_alaw")
	defer c.Close()
	opening(t, server, "jitter")
	for i := 0; i < 40; i++ {
		time.Sleep(25 * time.Millisecond)
		sample := byte(0x33)
		if i == 39 {
			sample = 0x44
		}
		if err := c.WriteJSON(map[string]string{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{sample}, 160))}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case n := <-count:
		if n > 44 {
			t.Fatalf("40 input frames caused %d upstream frames", n)
		}
	case <-time.After(time.Second):
		t.Fatal("input lost")
	}
	c.Close()
	waitEmpty(t, s)
}
