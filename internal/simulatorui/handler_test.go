package simulatorui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/config"
)

func TestSafeConfigAuthenticationAndReadiness(t *testing.T) {
	const secret = "DO-NOT-SERIALIZE-SENTINEL"
	cfg := config.Default()
	cfg.AuthToken = secret
	cfg.MetricsToken = secret
	cfg.BackendBaseURL = "https://" + secret + ".invalid"
	cfg.Instructions = secret
	cfg.Greeting = secret
	cfg.StoreDir = "/" + secret
	cfg.WebRoot = "/" + secret
	cfg.TelemetryURL = "https://" + secret + ".invalid"
	cfg.TLSKeyFile = "/" + secret
	probes := 0
	fallback := false
	handler := New(cfg, "test-control-token", func(ctx context.Context) error {
		probes++
		if deadline, ok := ctx.Deadline(); !ok || deadline.IsZero() {
			t.Error("readiness requires deadline")
		}
		return errors.New(secret)
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fallback = true; w.WriteHeader(202) }))
	for _, auth := range []string{"", "test-control-token", "Bearer wrong", "bearer test-control-token", "Bearer test-control-token "} {
		req := httptest.NewRequest("GET", "/sim/v1/config", nil)
		req.Header.Set("Authorization", auth)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != 401 {
			t.Fatalf("auth %q got %d", auth, res.Code)
		}
	}
	for _, path := range []string{"config", "scenarios", "readiness", "runs"} {
		req := httptest.NewRequest("GET", "/sim/v1/"+path, nil)
		req.Header.Set("Authorization", "Bearer test-control-token")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != 200 && path != "runs" {
			t.Fatalf("%s: %d %s", path, res.Code, res.Body.String())
		}
		if strings.Contains(res.Body.String(), secret) {
			t.Fatalf("%s leaked secret: %s", path, res.Body.String())
		}
		if path == "config" {
			if probes != 0 {
				t.Fatal("config must not probe gateway")
			}
			var dto map[string]json.RawMessage
			if err := json.Unmarshal(res.Body.Bytes(), &dto); err != nil {
				t.Fatal(err)
			}
			if len(dto) != 2 || dto["active"] == nil || dto["constraints"] == nil {
				t.Fatalf("unexpected safe DTO: %s", res.Body.String())
			}
			if strings.Contains(res.Body.String(), "backend_base_url") || strings.Contains(res.Body.String(), "auth_token") {
				t.Fatal("unsafe fields")
			}
		}
		if path == "readiness" && res.Body.String() != "{\"gateway_ready\":false}\n" {
			t.Fatalf("readiness leaked internal error: %s", res.Body.String())
		}
	}
	if probes != 1 || !fallback {
		t.Fatal("routes not dispatched correctly")
	}
	req := httptest.NewRequest("GET", "/sim/v1/runs", nil)
	res := httptest.NewRecorder()
	fallback = false
	handler.ServeHTTP(res, req)
	if fallback || res.Code != 401 {
		t.Fatal("fallback must be authenticated")
	}
}

func TestEmbeddedAssetsAndSecurityHeaders(t *testing.T) {
	handler := New(config.Default(), "token", nil, http.NotFoundHandler())
	for _, path := range []string{"/sim/", "/sim/app.js", "/sim/style.css"} {
		req := httptest.NewRequest("GET", path, nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != 200 || res.Body.Len() == 0 {
			t.Fatalf("%s: %d", path, res.Code)
		}
		csp := res.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Fatalf("unsafe CSP %s", csp)
		}
		if res.Header().Get("Cache-Control") != "no-store" || res.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("missing security headers")
		}
		if path == "/sim/app.js" {
			for _, forbidden := range []string{"innerHTML", "localStorage", "sessionStorage", "getUserMedia"} {
				if strings.Contains(res.Body.String(), forbidden) {
					t.Fatalf("forbidden browser primitive %s", forbidden)
				}
			}
		}
	}
	for _, path := range []string{"/sim/../../go.mod", "/sim/missing", "/v1/config"} {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest("GET", path, nil))
		if res.Code != 404 {
			t.Fatalf("%s unexpectedly served: %d", path, res.Code)
		}
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest("GET", "/", nil))
	if res.Code != 307 || res.Header().Get("Location") != "/sim/" {
		t.Fatal("root should redirect")
	}
}

func TestMetadataMethodsAndEmptyToken(t *testing.T) {
	for _, path := range []string{"config", "scenarios", "readiness"} {
		handler := New(config.Default(), "token", nil, http.NotFoundHandler())
		req := httptest.NewRequest("POST", "/sim/v1/"+path, nil)
		req.Header.Set("Authorization", "Bearer token")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != 405 || res.Header().Get("Allow") != "GET" {
			t.Fatal("metadata must be read only")
		}
	}
	handler := New(config.Default(), "", nil, http.NotFoundHandler())
	req := httptest.NewRequest("GET", "/sim/v1/config", nil)
	req.Header.Set("Authorization", "Bearer ")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != 401 {
		t.Fatal("empty token accepted")
	}
}
