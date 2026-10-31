package speko

import (
	"bytes"
	"encoding/base64"
	"github.com/gorilla/websocket"
	"net/http"
	"testing"
)

// A real SIP peer sends caller packets during upstream setup. When the
// handshake completes those buffered packets arrive faster than wall time.
func TestBridgeDrainsStartupCallerAudio(t *testing.T) {
	upstream := router(t, func(c *websocket.Conn, r *http.Request) {
		var e wireEvent
		if c.ReadJSON(&e) != nil {
			return
		}
		_ = c.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"audio": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}}})
		frames := 0
		for {
			if c.ReadJSON(&e) != nil {
				return
			}
			if e.Type == "session.input_audio.append" {
				frames++
				if frames >= 130 {
					_ = c.WriteJSON(map[string]string{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(newPCMTransport("g711_alaw").encode(bytes.Repeat([]byte{0x33}, 160)))})
				}
			}
		}
	})
	s, server := relayFor(t, upstream.URL, nil)
	c := bridgeClient(t, server, "startup-burst", "g711_alaw")
	defer c.Close()
	if opening(t, server, "startup-burst") != 200 {
		t.Fatal("opening failed")
	}
	for i := 0; i < 140; i++ {
		if err := c.WriteJSON(map[string]string{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x55}, 160))}); err != nil {
			t.Fatal(err)
		}
	}
	for {
		var e wireEvent
		if err := c.ReadJSON(&e); err != nil {
			t.Fatal(err)
		}
		if e.Type == "error" {
			t.Fatalf("startup audio rejected: %s", e.Error)
		}
		if e.Type == "response.audio.delta" {
			break
		}
	}
	c.Close()
	waitEmpty(t, s)
}
