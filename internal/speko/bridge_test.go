package speko

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func bridgeClient(t *testing.T, server *httptest.Server, id, format string) *websocket.Conn {
	t.Helper()
	c := connect(t, server, "/v1/realtime?call="+id)
	if !bytes.Contains(receive(t, c), []byte("session.created")) {
		t.Fatal("missing created")
	}
	if err := c.WriteJSON(map[string]any{"type": "session.update", "session": map[string]string{"input_audio_format": format, "output_audio_format": format, "instructions": "Be brief.", "voice": "marin"}}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(receive(t, c), []byte("session.updated")) {
		t.Fatal("missing held updated")
	}
	return c
}

func opening(t *testing.T, server *httptest.Server, id string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"callId": id, "greeting": "Hello."})
	r, _ := http.NewRequest("POST", server.URL+"/v1/opening", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer local-secret")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestBridgeHeldCallAudioAndCleanup(t *testing.T) {
	var dialed atomic.Int32
	heard := make(chan []byte, 1)
	closed := make(chan struct{})
	upstream := router(t, func(c *websocket.Conn, r *http.Request) {
		dialed.Add(1)
		defer close(closed)
		if r.Header.Get("Authorization") != "Bearer speko-secret" || r.Header.Get("Idempotency-Key") == "" {
			t.Error("upstream auth")
		}
		var first struct {
			Type    string
			Session struct {
				Model        string
				Instructions string
				Input        []json.RawMessage
				Audio        struct {
					Format struct{ Type string }
					Output struct{ Voice string }
				}
				Delegation struct {
					Type      string
					Responses struct{ Model string }
				}
			}
		}
		if err := c.ReadJSON(&first); err != nil {
			t.Error(err)
			return
		}
		if first.Type != "session.start" || first.Session.Model != Model || first.Session.Audio.Format.Type != "audio/pcm" || first.Session.Audio.Output.Voice != "marin" || first.Session.Delegation.Type != "responses" || first.Session.Delegation.Responses.Model != "gpt-6-luna" || len(first.Session.Input) != 1 {
			t.Error("bad native setup")
		}
		_ = c.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"audio": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": upstreamRate}}}})
		var silence, greeting wireEvent
		if c.ReadJSON(&silence) != nil || silence.Type != "session.input_audio.append" {
			t.Error("input clock must start before greeting")
			return
		}
		if c.ReadJSON(&greeting) != nil || greeting.Type != "session.commentary.append" {
			t.Error("missing greeting cue")
			return
		}
		_ = c.WriteJSON(map[string]string{"type": "session.output_transcript.delta", "delta": "Hello."})
		// A 600 ms burst must be paced, not sent into the SIP queue at once.
		_ = c.WriteJSON(map[string]string{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(newPCMTransport("g711_alaw").encode(bytes.Repeat([]byte{0x33}, 4801)))})
		for {
			var e wireEvent
			if c.ReadJSON(&e) != nil {
				return
			}
			if e.Type == "session.input_audio.append" {
				b, _ := base64.StdEncoding.DecodeString(e.Audio)
				if len(b) == 960 && bytes.Equal(b[60:], newPCMTransport("g711_alaw").encode(bytes.Repeat([]byte{0x55}, 150))) {
					select {
					case heard <- b:
					default:
					}
				}
			}
		}
	})
	s, server := relayFor(t, upstream.URL, func(c *Config) { c.MaxSessions = 1 })
	c := bridgeClient(t, server, "call-1", "g711_alaw")
	if dialed.Load() != 0 {
		t.Fatal("Speko opened before SIP answer")
	}
	if opening(t, server, "wrong") != 409 {
		t.Fatal("unknown opening accepted")
	}
	if opening(t, server, "call-1") != 200 || opening(t, server, "call-1") != 409 {
		t.Fatal("release must happen once")
	}
	_ = c.WriteJSON(map[string]string{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x55}, 160))})
	var audio []byte
	var started time.Time
	var id string
	for {
		var e struct {
			Type, Delta string
			ResponseID  string `json:"response_id"`
			Response    struct{ ID, Status string }
		}
		if err := c.ReadJSON(&e); err != nil {
			t.Fatal(err)
		}
		switch e.Type {
		case "response.created":
			id = e.Response.ID
		case "response.audio.delta":
			if id == "" || e.ResponseID != id {
				t.Fatal("audio without response identity")
			}
			if started.IsZero() {
				started = time.Now()
			}
			b, _ := base64.StdEncoding.DecodeString(e.Delta)
			audio = append(audio, b...)
		case "response.done":
			if len(audio) != 4960 || !bytes.Equal(audio[:4801], bytes.Repeat([]byte{0x33}, 4801)) || !bytes.Equal(audio[4801:], bytes.Repeat([]byte{0xd5}, 159)) {
				t.Fatal("G.711 bytes or tail padding changed")
			}
			if time.Since(started) < 450*time.Millisecond {
				t.Fatal("audio was not paced")
			}
			select {
			case <-heard:
			case <-time.After(time.Second):
				t.Fatal("caller audio missing")
			}
			_ = c.Close()
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("upstream leaked")
			}
			waitEmpty(t, s)
			return
		}
	}
}

func TestBridgeHoldTimeoutAndShutdown(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		s, server := relayFor(t, "http://127.0.0.1:1", func(c *Config) { c.HoldTimeout = 100 * time.Millisecond })
		c := bridgeClient(t, server, "held", "g711_ulaw")
		if shutdown {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			if err := s.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			cancel()
		} else if !strings.Contains(string(receive(t, c)), "hold timeout") {
			t.Fatal("missing timeout error")
		}
		_ = c.Close()
		waitEmpty(t, s)
	}
}

func TestBridgeRejectsUnsupportedSetup(t *testing.T) {
	for _, session := range []string{
		`{"input_audio_format":"pcm16","output_audio_format":"pcm16"}`,
		`{"input_audio_format":"g711_alaw","output_audio_format":"g711_ulaw"}`,
		`{"input_audio_format":"g711_alaw","output_audio_format":"g711_alaw","tools":[{}]}`,
	} {
		if _, err := parseRealtimeStart(wireEvent{Type: "session.update", Session: json.RawMessage(session)}); err == nil {
			t.Fatal("unsupported session accepted")
		}
	}
}
