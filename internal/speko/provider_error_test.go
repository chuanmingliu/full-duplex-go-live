package speko

import (
	"github.com/gorilla/websocket"
	"net/http"
	"strings"
	"testing"
)

func TestProviderMessageRedactsBeforeTruncation(t *testing.T) {
	message := strings.Repeat("x", 1000) + "provider-secret\nlocal-secret"
	got := redactProviderMessage(message, "provider-secret", "local-secret")
	if strings.Contains(got, "secret") || strings.Contains(got, "\n") || len(got) > 1027 {
		t.Fatal("provider diagnostic was not safely bounded and redacted")
	}
}

func TestBridgeProviderErrorDoesNotLeakMessage(t *testing.T) {
	upstream := router(t, func(c *websocket.Conn, r *http.Request) {
		var event wireEvent
		if c.ReadJSON(&event) != nil {
			return
		}
		_ = c.WriteJSON(map[string]any{"type": "error", "error": map[string]string{"code": "invalid_request", "message": "speko-secret and local-secret"}})
	})
	s, server := relayFor(t, upstream.URL, nil)
	c := bridgeClient(t, server, "provider-error", "g711_alaw")
	defer c.Close()
	opening(t, server, "provider-error")
	got := string(receive(t, c))
	if !strings.Contains(got, "invalid_request") || strings.Contains(got, "speko-secret") || strings.Contains(got, "local-secret") {
		t.Fatal("provider error code lost or message leaked")
	}
	c.Close()
	waitEmpty(t, s)
}
