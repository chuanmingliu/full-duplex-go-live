package speko

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const startFrame = `{"type":"session.start","event_id":"start","session":{"instructions":"Be brief","golive":{},"future_field":{"keep":true}}}`

func router(t *testing.T, handle func(*websocket.Conn, *http.Request)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/live" || r.URL.RawQuery != "" {
			t.Error("unexpected upstream URL")
		}
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		handle(conn, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func relayFor(t *testing.T, upstream string, change func(*Config)) (*Service, *httptest.Server) {
	t.Helper()
	cfg := Config{URL: "ws" + strings.TrimPrefix(upstream, "http") + "/v1/live", APIKey: "speko-secret", AuthToken: "local-secret"}
	if change != nil {
		change(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		server.Close()
	})
	return s, server
}

func connect(t *testing.T, server *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	headers := http.Header{"Authorization": {"Bearer local-secret"}, "Idempotency-Key": {"caller-controlled"}, "X-Private": {"never-forward"}}
	c, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+path, headers)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	return c
}

func send(t *testing.T, c *websocket.Conn, data string) {
	t.Helper()
	if err := c.WriteMessage(websocket.TextMessage, []byte(data)); err != nil {
		t.Fatal(err)
	}
}

func receive(t *testing.T, c *websocket.Conn) []byte {
	t.Helper()
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func waitEmpty(t *testing.T, s *Service) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := s.active
		s.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("session capacity was not released")
}

func TestNativeSessionRoundTrip(t *testing.T) {
	keys := make(chan string, 2)
	upstream := router(t, func(c *websocket.Conn, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer speko-secret" {
			t.Error("wrong upstream credential")
		}
		if r.Header.Get("X-Private") != "" {
			t.Error("client header leaked")
		}
		key := r.Header.Get("Idempotency-Key")
		if key == "" || key == "caller-controlled" {
			t.Error("missing fresh server-owned idempotency key")
		}
		keys <- key
		_, data, err := c.ReadMessage()
		if err != nil {
			t.Error(err)
			return
		}
		var first struct {
			Type    string                     `json:"type"`
			EventID string                     `json:"event_id"`
			Session map[string]json.RawMessage `json:"session"`
		}
		if err := json.Unmarshal(data, &first); err != nil {
			t.Error(err)
			return
		}
		if first.Type != "session.start" || first.EventID != "start" || string(first.Session["model"]) != `"gpt-live-1"` || string(first.Session["future_field"]) != `{"keep":true}` {
			t.Errorf("session not preserved: %s", data)
		}
		if _, ok := first.Session["golive"]; ok {
			t.Error("local engine block forwarded")
		}
		// Send before receiving any audio: both directions must run concurrently.
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.started","session":{"id":"vendor-id"}}`))
		for {
			kind, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if string(data) == `{"type":"session.close"}` {
				_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.closed","reason":"close_requested"}`))
				return
			}
			if err := c.WriteMessage(kind, data); err != nil {
				return
			}
		}
	})
	s, server := relayFor(t, upstream.URL, nil)
	for _, path := range []string{"/v1/live", "/v1/live/sessions"} {
		c := connect(t, server, path)
		send(t, c, startFrame)
		if got := string(receive(t, c)); !strings.Contains(got, "vendor-id") {
			t.Fatal(got)
		}
		for _, frame := range []string{
			`{"type":"session.input_audio.append","audio":"AAEAAg=="}`,
			`{"type":"session.output_audio.delta","delta":"AQI="}`,
			`{"type":"session.delegation.created","delegation":{"id":"d1"}}`,
			`{"type":"response.event","event":{"type":"response.output_text.delta","delta":"hello"}}`,
			`{"type":"session.usage.updated","usage":{"audio_tokens":7}}`,
			`{"type":"future.vendor.event","opaque":[1,2,3]}`,
		} {
			send(t, c, frame)
			if got := string(receive(t, c)); got != frame {
				t.Fatalf("frame changed: %s", got)
			}
		}
		if err := c.WriteMessage(websocket.BinaryMessage, []byte{0, 1, 2}); err != nil {
			t.Fatal(err)
		}
		kind, binary, err := c.ReadMessage()
		if err != nil || kind != websocket.BinaryMessage || string(binary) != string([]byte{0, 1, 2}) {
			t.Fatal("binary frame changed", err)
		}
		send(t, c, `{"type":"session.close"}`)
		if got := string(receive(t, c)); !strings.Contains(got, "session.closed") {
			t.Fatal(got)
		}
		_, _, err = c.ReadMessage()
		if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
			t.Fatalf("close: %v", err)
		}
		waitEmpty(t, s)
	}
	if <-keys == <-keys {
		t.Fatal("idempotency keys reused")
	}
}

func TestAdmissionDoesNotDial(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer upstream.Close()
	for _, tc := range []struct {
		name, token, key, origin, path string
		status                         int
	}{
		{"missing local token", "", "", "", "/v1/live", 503},
		{"wrong bearer", "configured", "key", "", "/v1/live", 401},
		{"missing Speko key", "local-secret", "", "", "/v1/live", 503},
		{"foreign origin", "local-secret", "key", "https://evil.example", "/v1/live", 403},
		{"query", "local-secret", "key", "", "/v1/live?model=other", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, server := relayFor(t, upstream.URL, func(c *Config) { c.AuthToken, c.APIKey = tc.token, tc.key })
			headers := http.Header{"Authorization": {"Bearer local-secret"}}
			if tc.origin != "" {
				headers.Set("Origin", tc.origin)
			}
			c, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+tc.path, headers)
			if c != nil {
				_ = c.Close()
				t.Fatal("unexpected upgrade")
			}
			if err == nil || resp == nil {
				t.Fatal("expected HTTP refusal")
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.status)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request dialed upstream")
	}
}

func TestInvalidStartDoesNotDial(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer upstream.Close()
	s, server := relayFor(t, upstream.URL, nil)
	for _, frame := range []string{
		`null`, `{`, `{"type":"session.update","session":{}}`,
		`{"type":"session.start","session":null}`,
		`{"type":"session.start","session":{"model":"other"}}`,
		`{"type":"session.start","session":{"model":null}}`,
		`{"type":"session.start","session":{"golive":{"asr":"mock"}}}`,
	} {
		c := connect(t, server, "/v1/live")
		send(t, c, frame)
		if got := string(receive(t, c)); !strings.Contains(got, "invalid_session_start") {
			t.Fatal(got)
		}
		_ = c.Close()
		waitEmpty(t, s)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid start opened upstream")
	}
}

func TestUpstreamErrorsAreSanitized(t *testing.T) {
	for status, code := range map[int]string{401: "speko_auth_failed", 403: "speko_auth_failed", 429: "speko_rate_limited", 500: "speko_unavailable"} {
		t.Run(code+http.StatusText(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "speko-secret private upstream body")
			}))
			defer upstream.Close()
			s, server := relayFor(t, upstream.URL, nil)
			c := connect(t, server, "/v1/live")
			send(t, c, startFrame)
			got := string(receive(t, c))
			if !strings.Contains(got, code) || strings.Contains(got, "secret") || strings.Contains(got, "private") {
				t.Fatal(got)
			}
			waitEmpty(t, s)
		})
	}
}

func TestDisconnectAndShutdownCloseUpstream(t *testing.T) {
	for _, mode := range []string{"client", "shutdown", "timeout", "router"} {
		t.Run(mode, func(t *testing.T) {
			closed := make(chan struct{})
			upstream := router(t, func(c *websocket.Conn, r *http.Request) {
				defer close(closed)
				if _, _, err := c.ReadMessage(); err != nil {
					return
				}
				_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.started"}`))
				if mode == "router" {
					return
				}
				_, _, _ = c.ReadMessage()
			})
			s, server := relayFor(t, upstream.URL, func(c *Config) {
				if mode == "timeout" {
					c.SessionTimeout = 150 * time.Millisecond
				}
			})
			c := connect(t, server, "/v1/live")
			send(t, c, startFrame)
			receive(t, c)
			switch mode {
			case "client":
				_ = c.Close()
			case "shutdown":
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := s.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("upstream leaked")
			}
			waitEmpty(t, s)
			if mode != "client" {
				if _, _, err := c.ReadMessage(); err == nil {
					t.Fatal("client remained open")
				}
			}
		})
	}
}

func TestCapacityAndSetupDeadline(t *testing.T) {
	s, server := relayFor(t, "http://127.0.0.1:1", func(c *Config) { c.MaxSessions = 1; c.SetupTimeout = 150 * time.Millisecond })
	c := connect(t, server, "/v1/live") // No start frame: must expire without dialing.
	resp, err := http.Get(server.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("full readiness: %d", resp.StatusCode)
	}
	_, rejected, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/live", http.Header{"Authorization": {"Bearer local-secret"}})
	if err == nil || rejected == nil {
		t.Fatal("capacity limit ignored")
	}
	_ = rejected.Body.Close()
	if rejected.StatusCode != 503 {
		t.Fatal(rejected.StatusCode)
	}
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatal("setup deadline ignored")
	}
	waitEmpty(t, s)
	resp, err = http.Get(server.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
}

func TestMessageLimit(t *testing.T) {
	s, server := relayFor(t, "http://127.0.0.1:1", func(c *Config) { c.MaxMessageBytes = 32 })
	c := connect(t, server, "/v1/live")
	send(t, c, strings.Repeat("x", 100))
	if _, _, err := c.ReadMessage(); !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
		t.Fatal(err)
	}
	waitEmpty(t, s)
}

func TestShutdownCancelsPendingUpstreamUpgrade(t *testing.T) {
	arrived := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-r.Context().Done() // Never reply with HTTP 101.
	}))
	defer upstream.Close()
	s, server := relayFor(t, upstream.URL, nil)
	c := connect(t, server, "/v1/live")
	send(t, c, startFrame)
	select {
	case <-arrived:
	case <-time.After(time.Second):
		t.Fatal("no upstream upgrade")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("pending upgrade blocked shutdown: %v", err)
	}
	waitEmpty(t, s)
}

func TestConfigAndHealthWithoutCredentials(t *testing.T) {
	for _, url := range []string{"ws://public.example/v1/live", "wss://key@example.com/v1/live", "wss://example.com/v1/live?token=private", "https://example.com/v1/live", "wss://example.com/v1/realtime", "wss://example.com/v1/live#secret"} {
		if _, err := New(Config{URL: url}); err == nil {
			t.Errorf("accepted unsafe URL %s", url)
		}
	}
	if _, err := New(Config{APIKey: "key\r\nInjected: header"}); err == nil {
		t.Fatal("accepted invalid key")
	}
	if _, err := New(Config{MaxSessions: -1}); err == nil {
		t.Fatal("accepted negative limit")
	}
	s, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(context.Background())
	for path, status := range map[string]int{"/healthz": 200, "/readyz": 503} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != status {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	data, _ := json.Marshal(Config{APIKey: "private-key", AuthToken: "private-token"})
	if strings.Contains(string(data), "private") {
		t.Fatal("config serialized credentials")
	}
}
