package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/metrics"

	_ "github.com/chuanmingliu/golive/internal/provider/mock"
)

func serve(t *testing.T, cfg config.Config) *httptest.Server {
	t.Helper()
	cfg.WebRoot = ""
	srv := NewServer(cfg, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestUnauthorizedUpgradeIsRejected(t *testing.T) {
	cfg := config.Default()
	cfg.AuthToken = "s3cret"
	cfg.Greeting = ""
	ts := serve(t, cfg)

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/live"
	_, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		t.Fatal("unauthenticated dial succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}

	header := http.Header{"Authorization": []string{"Bearer s3cret"}}
	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		t.Fatalf("authenticated dial: %v", err)
	}
	conn.Close()
}

func TestTokenQueryParamAuthenticates(t *testing.T) {
	cfg := config.Default()
	cfg.AuthToken = "s3cret"
	cfg.Greeting = ""
	ts := serve(t, cfg)

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/live?token=s3cret"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("query-token dial: %v", err)
	}
	conn.Close()
}

func TestMaxSessionsIsEnforced(t *testing.T) {
	cfg := config.Default()
	cfg.Greeting = ""
	cfg.MaxSessions = 1
	ts := serve(t, cfg)

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/live"
	first, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	defer first.Close()

	_, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		t.Fatal("second session was admitted")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %v, want 503", resp)
	}
}

func TestDisallowedOriginIsRejected(t *testing.T) {
	cfg := config.Default()
	cfg.Greeting = ""
	cfg.AllowedOrigins = []string{"https://app.example"}
	ts := serve(t, cfg)

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/live"
	_, resp, err := websocket.DefaultDialer.Dial(url, http.Header{
		"Origin": []string{"https://evil.example"},
	})
	if err == nil {
		t.Fatal("cross-origin dial succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}

func TestIdleTimeoutClosesTheSession(t *testing.T) {
	cfg := config.Default()
	cfg.Greeting = ""
	cfg.Duplex.IdleTimeoutSeconds = 1
	ts := serve(t, cfg)

	conn := dial(t, ts)
	r := startReader(conn)
	startSession(t, conn, cfg.ClientRate)
	await(t, "session.started", 2*time.Second, func() bool {
		return r.count(live.ServerSessionStarted) > 0
	})
	await(t, "idle close", 4*time.Second, func() bool {
		return r.count(live.ServerSessionClosed) > 0
	})
}

func TestLivezAndReadyzAndMetrics(t *testing.T) {
	metrics.ResetDefault()
	cfg := config.Default()
	cfg.EnableMetrics = true
	ts := serve(t, cfg)

	for _, path := range []string{"/livez", "/healthz", "/readyz"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s returned %d", path, resp.StatusCode)
		}
		resp.Body.Close()
	}

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "golive_sessions_active") {
		t.Fatalf("metrics body missing gauge:\n%s", body)
	}
}

func TestProviderOverrideCanBeDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Greeting = ""
	cfg.AllowProviderOverride = false
	ts := serve(t, cfg)
	conn := dial(t, ts)
	r := startReader(conn)

	send(t, conn, live.SessionStartEvent{
		Envelope: live.Envelope{Type: live.ClientSessionStart, EventID: "start"},
		Session: live.SessionConfig{
			Audio:  &live.AudioConfig{Format: &live.AudioFormat{Type: "audio/pcm", Rate: cfg.ClientRate}},
			Golive: &live.GoliveConfig{ASR: "mock"},
		},
	})
	await(t, "an error", 2*time.Second, func() bool { return r.count(live.ServerError) > 0 })
	ev, _ := r.lastError()
	if ev.Error.Code != "provider_override_disabled" {
		t.Errorf("code = %q, want provider_override_disabled", ev.Error.Code)
	}
}

func TestConfigEndpointRequiresAuthWhenConfigured(t *testing.T) {
	cfg := config.Default()
	cfg.AuthToken = "s3cret"
	ts := serve(t, cfg)

	resp, err := http.Get(ts.URL + "/v1/live/config")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated config returned %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/live/config", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("authenticated config returned %d", resp2.StatusCode)
	}
	var body struct {
		Allow bool `json:"allow_provider_override"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
}

func TestWrongLengthTokenDoesNotCrash(t *testing.T) {
	cfg := config.Default()
	cfg.AuthToken = "s3cret"
	cfg.Greeting = ""
	ts := serve(t, cfg)

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/live"
	_, resp, err := websocket.DefaultDialer.Dial(url, http.Header{
		"Authorization": []string{"Bearer x"},
	})
	if err == nil {
		t.Fatal("short token was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401 (not a 500 from ConstantTimeCompare panic)", resp)
	}
}

func TestAuthFailThrottle(t *testing.T) {
	cfg := config.Default()
	cfg.AuthToken = "s3cret"
	cfg.AuthFailBurst = 2
	cfg.AuthFailWindowSeconds = 60
	cfg.Greeting = ""
	ts := serve(t, cfg)
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/live"

	for i := 0; i < 2; i++ {
		_, resp, err := websocket.DefaultDialer.Dial(url, nil)
		if err == nil {
			t.Fatal("unauthenticated dial succeeded")
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %v, want 401", i+1, resp)
		}
	}
	_, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		t.Fatal("throttled dial succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %v, want 429", resp)
	}
}

func TestMetricsPublicBypassesSessionAuth(t *testing.T) {
	cfg := config.Default()
	cfg.AuthToken = "s3cret"
	cfg.MetricsPublic = true
	cfg.EnableMetrics = true
	ts := serve(t, cfg)

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public metrics returned %d", resp.StatusCode)
	}
}

func TestMetricsTokenIsAccepted(t *testing.T) {
	cfg := config.Default()
	cfg.AuthToken = "session-secret"
	cfg.MetricsToken = "scrape-secret"
	cfg.EnableMetrics = true
	ts := serve(t, cfg)

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated metrics returned %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer scrape-secret")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("metrics token returned %d", resp2.StatusCode)
	}
}

func TestLivezCarriesRequestID(t *testing.T) {
	ts := serve(t, config.Default())
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/livez", nil)
	req.Header.Set("X-Request-ID", "req-test-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("X-Request-ID"); got != "req-test-1" {
		t.Fatalf("X-Request-ID = %q", got)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff header")
	}
}

func TestRedactURLHidesToken(t *testing.T) {
	u, err := url.Parse("http://x/v1/live?token=s3cret&x=1")
	if err != nil {
		t.Fatal(err)
	}
	got := redactURL(u)
	if strings.Contains(got, "s3cret") {
		t.Fatalf("token leaked: %s", got)
	}
	if !strings.Contains(got, "token=redacted") {
		t.Fatalf("redaction missing: %s", got)
	}
}

func TestEventFloodClosesTheSession(t *testing.T) {
	cfg := config.Default()
	cfg.Greeting = ""
	cfg.Duplex.MaxEventsPerSecond = 3
	ts := serve(t, cfg)
	conn := dial(t, ts)
	r := startReader(conn)
	startSession(t, conn, cfg.ClientRate)
	await(t, "session.started", 2*time.Second, func() bool {
		return r.count(live.ServerSessionStarted) > 0
	})
	for i := 0; i < 30; i++ {
		ev := live.SessionUpdateEvent{
			Envelope: live.Envelope{Type: live.ClientSessionUpdate, EventID: "u"},
		}
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			// The server may have already closed us; that's the success path.
			break
		}
	}
	await(t, "flood close", 3*time.Second, func() bool {
		return r.count(live.ServerSessionClosed) > 0
	})
	if r.closeReason() != live.CloseFlooded {
		t.Fatalf("close reason = %q, want %s", r.closeReason(), live.CloseFlooded)
	}
}

func TestDrainClosesLiveSessions(t *testing.T) {
	cfg := config.Default()
	cfg.WebRoot = ""
	cfg.Greeting = ""
	srv := NewServer(cfg, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	conn := dial(t, ts)
	r := startReader(conn)
	startSession(t, conn, cfg.ClientRate)
	await(t, "session.started", 2*time.Second, func() bool {
		return r.count(live.ServerSessionStarted) > 0
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv.Drain(ctx)
	await(t, "drain close", 2*time.Second, func() bool {
		return r.count(live.ServerSessionClosed) > 0
	})
	if r.closeReason() != live.CloseShutdown {
		t.Fatalf("close reason = %q, want %s", r.closeReason(), live.CloseShutdown)
	}
}
