package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/metrics"
	"github.com/chuanmingliu/golive/internal/provider"
)

// Server exposes the live protocol over WebSocket, plus health, metrics and
// the optional demo client.
type Server struct {
	cfg config.Config
	log *slog.Logger

	upgrader websocket.Upgrader

	mu       sync.Mutex
	sessions map[string]*Session
	ipCounts map[string]int
	held     int // reservations, including sessions not yet in the map
	draining atomic.Bool
	authGate *failGate

	// Factory lets tests inject a stub engine.
	Factory EngineFactory
	// Version is shown on /livez. Optional.
	Version string
}

// NewServer builds a server.
func NewServer(cfg config.Config, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		cfg:      cfg,
		log:      log,
		sessions: map[string]*Session{},
		ipCounts: map[string]int{},
		upgrader: websocket.Upgrader{
			ReadBufferSize:    32 << 10,
			WriteBufferSize:   32 << 10,
			HandshakeTimeout:  10 * time.Second,
			EnableCompression: false, // compressed frames are a memory DoS
		},
		authGate: newFailGate(cfg.AuthFailBurst, cfg.AuthFailWindowSeconds),
	}
	s.upgrader.CheckOrigin = s.checkOrigin
	return s
}

// Handler builds the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/live", s.handleLive)
	mux.HandleFunc("/livez", s.handleLivez)
	mux.HandleFunc("/healthz", s.handleLivez)
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.HandleFunc("/metrics", s.protectMetrics(s.handleMetrics))
	mux.HandleFunc("/v1/live/providers", s.protect(s.handleConfig))
	mux.HandleFunc("/v1/live/config", s.protect(s.handleConfig))

	if s.cfg.EnablePprof {
		mux.HandleFunc("/debug/pprof/", s.protect(pprof.Index))
		mux.HandleFunc("/debug/pprof/cmdline", s.protect(pprof.Cmdline))
		mux.HandleFunc("/debug/pprof/profile", s.protect(pprof.Profile))
		mux.HandleFunc("/debug/pprof/symbol", s.protect(pprof.Symbol))
		mux.HandleFunc("/debug/pprof/trace", s.protect(pprof.Trace))
		s.log.Info("pprof enabled at /debug/pprof")
	}

	if s.cfg.WebRoot != "" {
		if info, err := os.Stat(s.cfg.WebRoot); err == nil && info.IsDir() {
			mux.Handle("/", http.FileServer(http.Dir(filepath.Clean(s.cfg.WebRoot))))
			s.log.Info("serving demo client", "root", s.cfg.WebRoot)
		} else {
			s.log.Warn("web root not found; demo client disabled", "root", s.cfg.WebRoot)
		}
	}
	return s.withHTTP(mux)
}

func (s *Server) handleLivez(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	active := len(s.sessions)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"model":    s.cfg.Model,
		"sessions": active,
		"version":  s.Version,
	})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	active := len(s.sessions)
	s.mu.Unlock()
	body := map[string]any{
		"status":        "ok",
		"sessions":      active,
		"max_sessions":  s.cfg.MaxSessions,
		"draining":      s.draining.Load(),
		"auth_required": s.cfg.AuthRequired || s.cfg.AuthToken != "",
	}
	if s.draining.Load() {
		body["status"] = "draining"
		writeJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	if max := s.cfg.MaxSessions; max > 0 && active >= max {
		body["status"] = "full"
		writeJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.cfg.EnableMetrics {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	metrics.Default().WritePrometheus(w)
}

// handleConfig reports what a client may choose and what it gets if it chooses
// nothing. The defaults are here so the demo page can show the real server
// prompt and greeting as placeholders rather than inventing its own.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"asr":      provider.ASRNames(),
		"llm":      provider.LLMNames(),
		"tts":      provider.TTSNames(),
		"selected": map[string]string{"asr": s.cfg.ASR, "llm": s.cfg.LLM, "tts": s.cfg.TTS},
		"defaults": map[string]any{
			"instructions":             s.cfg.Instructions,
			"greeting":                 s.cfg.Greeting,
			"on_new_query":             s.cfg.Duplex.OnNewQuery,
			"user_backchannel_phrases": s.cfg.Duplex.UserBackchannelPhrases,
			"user_backchannel_hold_ms": s.cfg.Duplex.UserBackchannelHoldMS,
			"model":                    s.cfg.Model,
			"language":                 s.cfg.Language,
			"rate":                     s.cfg.ClientRate,
		},
		"allow_provider_override": s.cfg.AllowProviderOverride,
	})
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		metrics.Default().Rejected("draining")
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "draining"})
		return
	}
	if err := s.authorize(r); err != nil {
		s.rejectAuth(w, r)
		return
	}
	ip := s.clientIP(r)
	if !s.admit(ip) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "too_many_sessions"})
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.release(ip)
		metrics.Default().Rejected("upgrade")
		s.log.Warn("websocket upgrade failed", "err", err, "ip", ip, "request_id", requestIDFrom(r.Context()))
		return
	}

	id := "sess_" + randomID()
	session := NewSession(SessionOptions{
		Conn:    conn,
		Cfg:     s.cfg,
		Log:     s.log,
		ID:      id,
		Factory: s.Factory,
	})
	if rid := requestIDFrom(r.Context()); rid != "" {
		session.log = session.log.With("request_id", rid)
	}

	s.mu.Lock()
	s.sessions[id] = session
	s.mu.Unlock()
	metrics.Default().SessionOpen()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, id)
		s.mu.Unlock()
		s.release(ip)
		metrics.Default().SessionClose()
	}()

	ctx := r.Context()
	if max := s.cfg.Duplex.SessionMaxSeconds; max > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(max)*time.Second)
		defer cancel()
	}
	session.Serve(ctx)
}

// Drain stops accepting new sessions and asks the live ones to close. It
// returns when the map is empty or ctx is done.
func (s *Server) Drain(ctx context.Context) {
	s.draining.Store(true)
	s.mu.Lock()
	list := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		list = append(list, sess)
	}
	s.mu.Unlock()
	s.log.Info("draining sessions", "n", len(list))
	for _, sess := range list {
		sess.setCloseReason(live.CloseShutdown)
		sess.finish()
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		s.mu.Lock()
		n := len(s.sessions)
		s.mu.Unlock()
		if n == 0 {
			return
		}
		select {
		case <-ctx.Done():
			s.log.Warn("drain timed out", "remaining", n)
			return
		case <-tick.C:
		}
	}
}

func (s *Server) admit(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draining.Load() {
		metrics.Default().Rejected("draining")
		return false
	}
	if max := s.cfg.MaxSessions; max > 0 && s.held >= max {
		metrics.Default().Rejected("max_sessions")
		return false
	}
	if per := s.cfg.MaxSessionsPerIP; per > 0 && s.ipCounts[ip] >= per {
		metrics.Default().Rejected("max_sessions_per_ip")
		return false
	}
	s.held++
	s.ipCounts[ip]++
	return true
}

func (s *Server) release(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held > 0 {
		s.held--
	}
	if s.ipCounts[ip] <= 1 {
		delete(s.ipCounts, ip)
		return
	}
	s.ipCounts[ip]--
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func randomID() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(buf[:])
}
