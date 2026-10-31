package speko

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A valid provider burst is a failed reply, not a failed SIP call. Check that
// the old output is cleared, the input remains live, and new speech can resume.
func TestBridgePlayoutOverflowCancelsReplyWithoutClosingCall(t *testing.T) {
	stopped := make(chan struct{})
	resumed := make(chan struct{})
	upstream := router(t, func(c *websocket.Conn, r *http.Request) {
		var e wireEvent
		if c.ReadJSON(&e) != nil {
			return
		}
		_ = c.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"audio": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": upstreamRate}}}})
		overflowed, sent := false, false
		for c.ReadJSON(&e) == nil {
			switch e.Type {
			case "session.commentary.append":
				_ = c.WriteJSON(map[string]string{"type": "session.output_transcript.delta", "delta": "An interrupted reply"})
				_ = c.WriteJSON(map[string]string{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(newPCMTransport("g711_alaw").encode(bytes.Repeat([]byte{0x33}, bridgeQueueBytes+1600)))})
			case "session.instructions.append":
				if overflowed {
					t.Error("overflow repeatedly interrupted the same reply")
					return
				}
				overflowed = true
				close(stopped)
				// Delayed stale output must still be suppressed.
				_ = c.WriteJSON(map[string]string{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(newPCMTransport("g711_alaw").encode(bytes.Repeat([]byte{0x33}, 160)))})
			case "session.input_audio.append":
				if !overflowed || sent {
					continue
				}
				wire, _ := base64.StdEncoding.DecodeString(e.Audio)
				if bytes.Contains(newPCMTransport("g711_alaw").decode(wire), bytes.Repeat([]byte{0x44}, 80)) {
					_ = c.WriteJSON(map[string]string{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(wire)})
					sent = true
					close(resumed)
				}
			}
		}
	})
	s, server := relayFor(t, upstream.URL, nil)
	c := bridgeClient(t, server, "overflow", "g711_alaw")
	defer c.Close()
	opening(t, server, "overflow")
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	cancelled, cleared := false, false
	for !cleared {
		var event struct {
			Type     string `json:"type"`
			Boundary string `json:"bridge_boundary"`
			Response struct{ Status string }
		}
		if err := c.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "error" {
			t.Fatal("overflow closed the call")
		}
		if event.Type == "response.done" {
			cancelled = event.Response.Status == "cancelled" && event.Boundary == "playout_overflow"
		}
		cleared = event.Type == "output_audio_buffer.cleared"
	}
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if !cancelled || active != 1 {
		t.Fatal("reply loss was not explicit or session was closed")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("provider was not told to stop")
	}
	time.Sleep(audioIdle + 100*time.Millisecond)
	_ = c.WriteJSON(map[string]string{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, 160))})
	select {
	case <-resumed:
	case <-time.After(time.Second):
		t.Fatal("caller audio stopped flowing after overflow")
	}
	for {
		var event wireEvent
		if err := c.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "error" {
			t.Fatal("call closed after recovery")
		}
		if event.Type != "response.audio.delta" {
			continue
		}
		wire, _ := base64.StdEncoding.DecodeString(event.Delta)
		if !bytes.Contains(wire, bytes.Repeat([]byte{0x44}, 80)) {
			t.Fatal("stale audio escaped after overflow")
		}
		break
	}
}
