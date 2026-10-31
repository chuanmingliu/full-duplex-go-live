package speko

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Service is an authenticated, bounded relay. Each client owns exactly one
// upstream socket. Sessions are never retried or replayed after disconnects.
type Service struct {
	cfg      Config
	dialer   websocket.Dialer
	upgrader websocket.Upgrader
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	active   int
	closed   bool
	wg       sync.WaitGroup
	calls    map[string]*bridgeCall
}

func New(cfg Config) (*Service, error) {
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{cfg: cfg, ctx: ctx, cancel: cancel, calls: make(map[string]*bridgeCall)}
	s.dialer = *websocket.DefaultDialer
	s.dialer.HandshakeTimeout = cfg.SetupTimeout
	s.dialer.EnableCompression = false
	s.upgrader = websocket.Upgrader{HandshakeTimeout: cfg.SetupTimeout, CheckOrigin: s.checkOrigin}
	return s, nil
}

// Handler exposes the standalone service. Embedders may mount ServeHTTP and
// Ready directly under their own route names, and must call Shutdown.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", s.Ready)
	mux.Handle("/v1/live", s)
	mux.Handle("/v1/live/sessions", s)
	mux.HandleFunc("/v1/realtime", s.Realtime)
	mux.HandleFunc("GET /v1/pool", s.Pool)
	mux.HandleFunc("POST /v1/opening", s.Opening)
	return mux
}

// Ready checks local configuration and capacity, not upstream model access or
// account balance. Probing it never opens a billable Speko voice session.
func (s *Service) Ready(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	status := s.statusLocked()
	active := s.active
	s.mu.Unlock()
	code := http.StatusOK
	if status != "ready" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{"status": status, "model": Model, "sessions": active})
}

func (s *Service) statusLocked() string {
	switch {
	case s.closed:
		return "draining"
	case s.cfg.AuthToken == "":
		return "missing_golive_auth_token"
	case s.cfg.APIKey == "":
		return "missing_speko_api_key"
	case s.active >= s.cfg.MaxSessions:
		return "full"
	default:
		return "ready"
	}
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.serve(w, r, false)
}

func (s *Service) authorized(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.AuthToken == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "missing_golive_auth_token"})
		return false
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	var token string
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		token = parts[1]
	}
	got, want := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(s.cfg.AuthToken))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return false
	}
	return true
}

func (s *Service) serve(w http.ResponseWriter, r *http.Request, realtime bool) {
	if !s.authorized(w, r) {
		return
	}
	// No caller-controlled upstream URL, query, credential, or idempotency key.
	if !realtime && (r.URL.RawQuery != "" || r.URL.ForceQuery) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query_not_supported"})
		return
	}
	if r.Method != http.MethodGet || !websocket.IsWebSocketUpgrade(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "websocket_required"})
		return
	}
	if !s.checkOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin_not_allowed"})
		return
	}
	s.mu.Lock()
	status := s.statusLocked()
	if status != "ready" {
		s.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": status})
		return
	}
	s.active++
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
		s.wg.Done()
	}()
	client, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer client.Close()
	client.SetReadLimit(s.cfg.MaxMessageBytes)
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.SessionTimeout)
	defer cancel()
	stopShutdown := context.AfterFunc(s.ctx, cancel)
	defer stopShutdown()
	stopClient := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stopClient()
	if realtime {
		_ = s.bridge(ctx, client, r.URL.Query().Get("call"))
		return
	}
	_ = client.SetReadDeadline(time.Now().Add(s.cfg.SetupTimeout))
	kind, first, err := client.ReadMessage()
	if err != nil {
		return
	}
	start, err := prepareStart(kind, first)
	if err != nil {
		s.sendError(client, "invalid_session_start", err.Error())
		return
	}
	_ = client.SetReadDeadline(time.Time{})
	headers := http.Header{
		"Authorization":   {"Bearer " + s.cfg.APIKey},
		"Idempotency-Key": {rand.Text()},
	}
	upstream, response, err := s.dial(ctx, headers)
	if err != nil {
		code := "speko_unavailable"
		if response != nil {
			if response.Body != nil {
				_ = response.Body.Close()
			}
			switch response.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				code = "speko_auth_failed"
			case http.StatusTooManyRequests:
				code = "speko_rate_limited"
			}
		}
		// Do not expose provider response bodies, dial errors, or credentials.
		s.sendError(client, code, "Speko could not open the GPT-Live session")
		return
	}
	defer upstream.Close()
	stopUpstream := context.AfterFunc(ctx, func() { _ = upstream.Close() })
	defer stopUpstream()
	upstream.SetReadLimit(s.cfg.MaxMessageBytes)
	if err := s.write(upstream, websocket.TextMessage, start); err != nil {
		s.sendError(client, "speko_unavailable", "Speko could not start the GPT-Live session")
		return
	}
	done := make(chan error, 2)
	go func() { done <- s.copyFrames(upstream, client, false) }()
	go func() { done <- s.copyFrames(client, upstream, true) }()
	err = <-done
	code := websocket.CloseInternalServerErr
	if err == nil || websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		code = websocket.CloseNormalClosure
	} else if errors.Is(err, websocket.ErrReadLimit) {
		code = websocket.CloseMessageTooBig
	}
	closeFrame := websocket.FormatCloseMessage(code, "session ended")
	_ = client.WriteControl(websocket.CloseMessage, closeFrame, time.Now().Add(time.Second))
	_ = upstream.WriteControl(websocket.CloseMessage, closeFrame, time.Now().Add(time.Second))
	_ = client.Close()
	_ = upstream.Close()
	<-done // Both pumps must exit before releasing capacity or completing shutdown.
}

// Gorilla bounds the HTTP upgrade by a deadline, but cancellation after TCP
// connect alone does not interrupt ReadResponse. Close that connection on
// session cancellation as well, including while an upstream upgrade is stuck.
func (s *Service) dial(ctx context.Context, headers http.Header) (*websocket.Conn, *http.Response, error) {
	dialer := s.dialer
	var stop func() bool
	dialer.NetDialContext = func(dialCtx context.Context, network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(dialCtx, network, addr)
		if err == nil {
			stop = context.AfterFunc(ctx, func() { _ = conn.Close() })
		}
		return conn, err
	}
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	return dialer.DialContext(ctx, s.cfg.URL, headers)
}

func (s *Service) copyFrames(dst, src *websocket.Conn, fromRouter bool) error {
	for {
		kind, data, err := src.ReadMessage()
		if err != nil {
			return err
		}
		var event struct {
			Type string `json:"type"`
		}
		if kind == websocket.TextMessage {
			_ = json.Unmarshal(data, &event)
		}
		if !fromRouter && event.Type == "session.start" {
			return errors.New("a connection may start only one session")
		}
		if err := s.write(dst, kind, data); err != nil {
			return err
		}
		if fromRouter && event.Type == "session.closed" {
			return nil
		}
	}
}

func (s *Service) write(conn *websocket.Conn, kind int, data []byte) error {
	_ = conn.SetWriteDeadline(time.Now().Add(s.cfg.WriteTimeout))
	return conn.WriteMessage(kind, data)
}

func (s *Service) sendError(conn *websocket.Conn, code, message string) {
	data, _ := json.Marshal(map[string]any{
		"type": "error", "error": map[string]string{"type": "server_error", "code": code, "message": message},
	})
	_ = s.write(conn, websocket.TextMessage, data)
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "session rejected"), time.Now().Add(time.Second))
}

func (s *Service) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if len(s.cfg.AllowedOrigins) > 0 {
		for _, allowed := range s.cfg.AllowedOrigins {
			if origin == allowed {
				return true
			}
		}
		return false
	}
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host == r.Host
}

// Shutdown also closes hijacked WebSockets, which http.Server.Shutdown does not.
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
