package speko

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestBridgeDoesNotQueueIdleSilence(t *testing.T) {
	for _, samples := range []int{12000, 24000} {
		t.Run(fmt.Sprint(samples), func(t *testing.T) {
			upstream := router(t, func(c *websocket.Conn, r *http.Request) {
				var e wireEvent
				if c.ReadJSON(&e) != nil {
					return
				}
				_ = c.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"audio": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}}})
				// A provider burst includes idle silence followed by speech. Speech must
				// not wait for 1.5–3 seconds of digital silence or hit the queue limit.
				wire := append(bytes.Repeat([]byte{0xd5}, samples), bytes.Repeat([]byte{0x33}, 160)...)
				_ = c.WriteJSON(map[string]string{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(newPCMTransport("g711_alaw").encode(wire))})
				for c.ReadJSON(&e) == nil {
				}
			})
			s, server := relayFor(t, upstream.URL, nil)
			c := bridgeClient(t, server, "idle-silence", "g711_alaw")
			defer c.Close()
			start := time.Now()
			opening(t, server, "idle-silence")
			for {
				var e wireEvent
				if err := c.ReadJSON(&e); err != nil {
					t.Fatal(err)
				}
				if e.Type == "error" {
					t.Fatalf("bridge failed: %s", e.Error)
				}
				if e.Type != "response.audio.delta" {
					continue
				}
				b, _ := base64.StdEncoding.DecodeString(e.Delta)
				if !codecSilence(b, 0xd5) {
					if delay := time.Since(start); delay > 350*time.Millisecond {
						t.Fatalf("idle silence delayed speech by %s", delay)
					}
					break
				}
			}
			c.Close()
			waitEmpty(t, s)
		})
	}
}

func TestTrimIdleSilencePreservesSpeechAndFollowingPause(t *testing.T) {
	for _, zero := range []byte{0xd5, 0xff} {
		suffix := append([]byte{0x33, 0x44}, bytes.Repeat([]byte{zero}, 16000)...)
		input := append(bytes.Repeat([]byte{zero ^ 0x80}, 12000), suffix...)
		got, removed := trimIdleSilence(input, zero)
		if removed != 11200 || !bytes.Equal(got[800:], suffix) {
			t.Fatal("changed speech or silence following speech")
		}
		unchanged, removed := trimIdleSilence(suffix, zero)
		if removed != 0 || !bytes.Equal(unchanged, suffix) {
			t.Fatal("trimmed a voiced prefix")
		}
	}
}

func TestBridgeCancelsExcessQueuedSpeech(t *testing.T) {
	upstream := router(t, func(c *websocket.Conn, r *http.Request) {
		var e wireEvent
		if c.ReadJSON(&e) != nil {
			return
		}
		_ = c.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"audio": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}}})
		wire := bytes.Repeat([]byte{0x33}, 24000)
		_ = c.WriteJSON(map[string]string{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(newPCMTransport("g711_alaw").encode(wire))})
		for c.ReadJSON(&e) == nil {
		}
	})
	s, server := relayFor(t, upstream.URL, nil)
	c := bridgeClient(t, server, "excess-speech", "g711_alaw")
	defer c.Close()
	opening(t, server, "excess-speech")
	for {
		var e wireEvent
		if err := c.ReadJSON(&e); err != nil {
			t.Fatal(err)
		}
		if e.Type == "error" {
			t.Fatal("valid audio burst closed the call")
		}
		if e.Type == "output_audio_buffer.cleared" {
			break
		}
		if e.Type == "response.audio.delta" {
			t.Fatal("excess speech was silently accepted")
		}
	}
	c.Close()
	waitEmpty(t, s)
}
