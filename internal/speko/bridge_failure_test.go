package speko

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestBridgeFailedUpgradeReleasesCall(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "speko-secret", 401) }))
	defer upstream.Close()
	s, server := relayFor(t, upstream.URL, nil)
	c := bridgeClient(t, server, "rejected", "g711_alaw")
	if opening(t, server, "rejected") != 200 {
		t.Fatal("release failed")
	}
	message := string(receive(t, c))
	if !strings.Contains(message, "bridge_failed") || strings.Contains(message, "speko-secret") {
		t.Fatal("error was missing or leaked a credential")
	}
	waitEmpty(t, s)
	if opening(t, server, "rejected") != 409 {
		t.Fatal("failed call remained in pool")
	}
}

func TestBridgeCancelDropsQueuedAudio(t *testing.T) {
	stopped := make(chan struct{})
	resume := make(chan struct{})
	upstream := router(t, func(c *websocket.Conn, r *http.Request) {
		var e wireEvent
		if c.ReadJSON(&e) != nil {
			return
		}
		_ = c.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"audio": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": upstreamRate}}}})
		for {
			if c.ReadJSON(&e) != nil {
				return
			}
			switch e.Type {
			case "session.commentary.append":
				_ = c.WriteJSON(map[string]string{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(newPCMTransport("g711_alaw").encode(bytes.Repeat([]byte{0x33}, 6400)))})
			case "session.instructions.append":
				close(stopped)
				for i := 0; i < 15; i++ {
					_ = c.WriteJSON(map[string]string{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(newPCMTransport("g711_alaw").encode(bytes.Repeat([]byte{0xd5}, 160)))})
					time.Sleep(20 * time.Millisecond)
				}
				select {
				case <-resume:
				case <-time.After(time.Second):
					return
				}
				_ = c.WriteJSON(map[string]string{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(newPCMTransport("g711_alaw").encode(bytes.Repeat([]byte{0x44}, 160)))})
			}
		}
	})
	s, server := relayFor(t, upstream.URL, nil)
	c := bridgeClient(t, server, "cancel", "g711_alaw")
	opening(t, server, "cancel")
	for {
		var e wireEvent
		if c.ReadJSON(&e) != nil {
			t.Fatal("no initial audio")
		}
		if e.Type == "response.audio.delta" {
			break
		}
	}
	send(t, c, `{"type":"response.cancel"}`)
	for {
		var e wireEvent
		if c.ReadJSON(&e) != nil {
			t.Fatal("no clear")
		}
		if e.Type == "output_audio_buffer.cleared" {
			break
		}
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("native stop instruction not sent")
	}
	time.Sleep(audioIdle + 60*time.Millisecond)
	close(resume)
	for {
		var e wireEvent
		if err := c.ReadJSON(&e); err != nil {
			t.Fatal(err)
		}
		if e.Type == "response.audio.delta" {
			b, _ := base64.StdEncoding.DecodeString(e.Delta)
			if codecSilence(b, 0xd5) {
				continue
			}
			if !bytes.Equal(b, bytes.Repeat([]byte{0x44}, 160)) {
				t.Fatal("stale queued audio escaped after clear")
			}
			break
		}
	}
	_ = c.Close()
	waitEmpty(t, s)
}

func TestBridgeShutdownCancelsPendingUpgrade(t *testing.T) {
	arrived := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(arrived); <-r.Context().Done() }))
	defer upstream.Close()
	s, server := relayFor(t, upstream.URL, nil)
	c := bridgeClient(t, server, "waiting", "g711_alaw")
	opening(t, server, "waiting")
	select {
	case <-arrived:
	case <-time.After(time.Second):
		t.Fatal("no upgrade")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatal("client not closed")
	}
	waitEmpty(t, s)
}

func TestBridgeClientDisconnectCancelsPendingUpgrade(t *testing.T) {
	arrived := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(arrived); <-r.Context().Done() }))
	defer upstream.Close()
	s, server := relayFor(t, upstream.URL, nil)
	c := bridgeClient(t, server, "disconnect", "g711_alaw")
	opening(t, server, "disconnect")
	select {
	case <-arrived:
	case <-time.After(time.Second):
		t.Fatal("no upgrade")
	}
	_ = c.Close()
	waitEmpty(t, s)
}
