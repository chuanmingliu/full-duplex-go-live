package speko

import (
	"bytes"
	"encoding/base64"
	"github.com/gorilla/websocket"
	"net/http"
	"testing"
	"time"
)

func TestPreconnectHoldsGreetingAndInputUntilAnswer(t *testing.T) {
	ready := make(chan struct{})
	input := make(chan string, 1)
	upstream := router(t, func(c *websocket.Conn, r *http.Request) {
		var e wireEvent
		if c.ReadJSON(&e) != nil {
			return
		}
		if bytes.Contains(e.Session, []byte("Greet the caller")) {
			t.Error("greeting released before answer")
		}
		_ = c.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"audio": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}}})
		close(ready)
		if c.ReadJSON(&e) == nil {
			input <- e.Type
		}
		for c.ReadJSON(&e) == nil {
		}
	})
	s, server := relayFor(t, upstream.URL, func(cfg *Config) { cfg.Preconnect = true })
	c := bridgeClient(t, server, "preconnect", "g711_alaw")
	defer c.Close()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("connection was not prepared while ringing")
	}
	_ = c.WriteJSON(map[string]string{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, 160))})
	select {
	case <-input:
		t.Fatal("pre-answer audio reached provider")
	case <-time.After(100 * time.Millisecond):
	}
	opening(t, server, "preconnect")
	select {
	case got := <-input:
		if got != "session.input_audio.append" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("no post-answer input")
	}
	c.Close()
	waitEmpty(t, s)
}

func TestSustainedCallerSpeechClearsPlayback(t *testing.T) {
	upstream := router(t, func(c *websocket.Conn, r *http.Request) {
		var e wireEvent
		if c.ReadJSON(&e) != nil {
			return
		}
		_ = c.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"audio": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}}})
		_ = c.WriteJSON(map[string]string{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(newPCMTransport("g711_alaw").encode(bytes.Repeat([]byte{0x33}, 8000)))})
		for c.ReadJSON(&e) == nil {
			if e.Type == "session.instructions.append" {
				// The provider can still send stale speech after the stop instruction.
				_ = c.WriteJSON(map[string]string{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(newPCMTransport("g711_alaw").encode(bytes.Repeat([]byte{0x33}, 160)))})
				time.Sleep(400 * time.Millisecond)
				_ = c.WriteJSON(map[string]string{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(newPCMTransport("g711_alaw").encode(bytes.Repeat([]byte{0x22}, 160)))})
			}
		}
	})
	s, server := relayFor(t, upstream.URL, func(cfg *Config) { cfg.LocalBargeIn = true })
	c := bridgeClient(t, server, "barge-in", "g711_alaw")
	defer c.Close()
	opening(t, server, "barge-in")
	for {
		var e wireEvent
		if err := c.ReadJSON(&e); err != nil {
			t.Fatal(err)
		}
		if e.Type == "response.audio.delta" {
			break
		}
	}
	for i := 0; i < 6; i++ {
		_ = c.WriteJSON(map[string]string{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, 160))})
		time.Sleep(20 * time.Millisecond)
	}
	_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		var e wireEvent
		if err := c.ReadJSON(&e); err != nil {
			t.Fatalf("sustained caller speech did not clear playback: %v", err)
		}
		if e.Type == "output_audio_buffer.cleared" {
			break
		}
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	for {
		var e wireEvent
		if err := c.ReadJSON(&e); err != nil {
			t.Fatalf("playback did not recover: %v", err)
		}
		if e.Type != "response.audio.delta" {
			continue
		}
		b, _ := base64.StdEncoding.DecodeString(e.Delta)
		if bytes.Contains(b, []byte{0x33}) {
			t.Fatal("stale audio escaped after clear")
		}
		if bytes.Contains(b, []byte{0x22}) {
			break
		}
	}
	c.Close()
	waitEmpty(t, s)
}

func TestPreconnectCancelledCallClosesProvider(t *testing.T) {
	ready, closed := make(chan struct{}), make(chan struct{})
	upstream := router(t, func(c *websocket.Conn, r *http.Request) {
		defer close(closed)
		var e wireEvent
		if c.ReadJSON(&e) != nil {
			return
		}
		_ = c.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"audio": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}}})
		close(ready)
		for c.ReadJSON(&e) == nil {
		}
	})
	s, server := relayFor(t, upstream.URL, func(cfg *Config) { cfg.Preconnect = true })
	c := bridgeClient(t, server, "cancel-before-answer", "g711_alaw")
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("provider was not prepared")
	}
	c.Close()
	waitEmpty(t, s)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("provider leaked after unanswered call")
	}
}
