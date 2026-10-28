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
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/duplex"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/metrics"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/store"
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

	// store keeps sessions started with store:true. Nil when store_dir is
	// unset, in which case nothing is kept and nothing can be forked.
	store     *store.Store
	lastSweep atomic.Int64

	// openings are opening lines being synthesized ahead of the calls they
	// are for. See handleOpening.
	openMu   sync.Mutex
	openings map[string]*openingState
	// tel posts turn boundaries to a latency console, when one is configured.
	tel *telPoster

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
		openings: map[string]*openingState{},
		upgrader: websocket.Upgrader{
			ReadBufferSize:    32 << 10,
			WriteBufferSize:   32 << 10,
			HandshakeTimeout:  10 * time.Second,
			EnableCompression: false, // compressed frames are a memory DoS
		},
		authGate: newFailGate(cfg.AuthFailBurst, cfg.AuthFailWindowSeconds),
	}
	s.upgrader.CheckOrigin = s.checkOrigin
	if cfg.TelemetryURL != "" {
		s.tel = newTelPoster(cfg.TelemetryURL, cfg.TelemetryFormat, log)
		log.Info("turn telemetry enabled", "url", cfg.TelemetryURL, "format", cfg.TelemetryFormat)
	}
	if cfg.StoreDir != "" {
		st, err := store.Open(cfg.StoreDir, time.Duration(cfg.StoreTTLHours)*time.Hour)
		if err != nil {
			// A server that cannot record still answers the phone.
			log.Warn("session storage is disabled", "store_dir", cfg.StoreDir, "err", err)
		} else {
			s.store = st
			s.sweepStore()
			log.Info("session storage enabled", "store_dir", cfg.StoreDir, "ttl_hours", cfg.StoreTTLHours)
		}
	}
	// A batch warm of the phrase cache yields to live calls: the
	// synthesizer's allowance is the caller's first.
	duplex.CallsLive = func() int {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.sessions)
	}
	return s
}

// sweepStore removes expired stored sessions, at most once an hour. It runs
// when a session opens, which is the only time the store grows.
func (s *Server) sweepStore() {
	if s.store == nil {
		return
	}
	now := time.Now().Unix()
	last := s.lastSweep.Load()
	if last != 0 && now-last < 3600 {
		return
	}
	if !s.lastSweep.CompareAndSwap(last, now) {
		return
	}
	go func() {
		if n := s.store.Sweep(); n > 0 {
			s.log.Info("expired stored sessions removed", "n", n)
		}
	}()
}

// Handler builds the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// /v1/live/sessions is where gpt-live-1 lives; /v1/live is where golive
	// first put it, and stays.
	mux.HandleFunc("/v1/live", s.handleLive)
	mux.HandleFunc("/v1/live/sessions", s.handleLive)
	mux.HandleFunc("/v1/live/sessions/{id}/attach", s.handleAttach)
	mux.HandleFunc("/v1/live/sessions/{id}/fork", s.handleFork)
	mux.HandleFunc("/v1/live/sessions/{id}/content", s.handleContent)
	if s.cfg.Realtime.Enabled {
		// The same service in the OpenAI Realtime protocol, for clients that
		// cannot speak gpt-live-1 — and the two endpoints a phone call needs
		// around it.
		mux.HandleFunc("/v1/realtime", s.handleRealtime)
		mux.HandleFunc("/v1/opening", s.protect(s.handleOpening))
		mux.HandleFunc("/v1/pool", s.protect(s.handlePool))
		mux.HandleFunc("/v1/phrases", s.handlePhrases)
	}
	// Whatever else is asked for under /v1/ is said out loud. Putting golive
	// where another agent used to be means inheriting that agent's callers,
	// and the quickest way to learn what they expect is to see what they ask
	// for and do not get.
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		s.log.Info("request for an endpoint this server does not have",
			"method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery != "")
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not_found", "path": r.URL.Path})
	})
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
	s.serveSession(w, r, nil, nil)
}

// handleFork starts a new session from a stored one: same model, same
// instructions, the conversation so far as its history. The client still sends
// session.start, and may choose store, its Responses delegation settings and
// its own audio format there.
func (s *Server) handleFork(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(r); err != nil {
		s.rejectAuth(w, r)
		return
	}
	id := r.PathValue("id")
	meta, ok := s.sourceForFork(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "session_not_found",
			"hint":  "only a session started with store:true, on a server with store_dir set, can be forked",
		})
		return
	}
	s.serveSession(w, r, &meta, nil)
}

// sourceForFork finds the record to fork from: a session still running, or one
// that has ended and been kept.
func (s *Server) sourceForFork(id string) (store.Meta, bool) {
	if s.store == nil || !store.ValidID(id) {
		return store.Meta{}, false
	}
	s.mu.Lock()
	sess := s.sessions[id]
	s.mu.Unlock()
	if sess != nil {
		if meta, ok := sess.snapshot(); ok {
			return meta, true
		}
	}
	meta, err := s.store.Meta(id)
	if err != nil {
		return store.Meta{}, false
	}
	return meta, true
}

// handleAttach joins a sideband to a running session.
func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(r); err != nil {
		s.rejectAuth(w, r)
		return
	}
	id := r.PathValue("id")
	s.mu.Lock()
	sess := s.sessions[id]
	s.mu.Unlock()
	if sess == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session_not_found"})
		return
	}
	if s.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "draining"})
		return
	}
	if sess.Sidebands() >= MaxSidebands {
		metrics.Default().Rejected("sidebands")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": "too_many_sidebands",
			"hint":  "a session accepts a small, fixed number of sidebands",
		})
		return
	}
	if !websocket.IsWebSocketUpgrade(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]any{
			"error": "upgrade_required",
			"hint":  "attach is a WebSocket endpoint",
		})
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("sideband upgrade failed", "err", err, "session", id)
		return
	}
	sess.Attach(r.Context(), conn)
}

// handleContent serves a stored session's recording: one stereo WAV, the
// caller on the left channel and the assistant on the right.
func (s *Server) handleContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.authorize(r); err != nil {
		s.rejectAuth(w, r)
		return
	}
	id := r.PathValue("id")
	if s.store == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session_not_found"})
		return
	}
	size, err := s.store.ContentSize(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session_not_found"})
		return
	}
	if _, err := s.store.Meta(id); err != nil {
		// Recorded but not yet written up: the session is still running and
		// has never been snapshotted. Its audio is not a finished file.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "session_in_progress",
			"hint":  "the recording is available once the session has closed",
		})
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="`+id+`.wav"`)
	if r.Method == http.MethodHead {
		return
	}
	if err := s.store.WriteContent(id, w); err != nil {
		s.log.Warn("serving a stored recording failed", "session", id, "err", err)
	}
}

// serveSession upgrades a request and runs a session on it. seed is the stored
// session it forks from, or nil for a new one.
func (s *Server) serveSession(w http.ResponseWriter, r *http.Request, seed *store.Meta, rt *RealtimeOptions) {
	if s.draining.Load() {
		metrics.Default().Rejected("draining")
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "draining"})
		return
	}
	if err := s.authorize(r); err != nil {
		s.rejectAuth(w, r)
		return
	}
	if !websocket.IsWebSocketUpgrade(r) {
		// gpt-live-1 also takes a POST here carrying a WebRTC offer. golive
		// speaks WebSocket only, and says so rather than answering 400 to a
		// client that did nothing wrong.
		status, hint := http.StatusUpgradeRequired, "this is a WebSocket endpoint"
		if r.Method == http.MethodPost {
			status = http.StatusNotImplemented
			hint = "WebRTC sessions are not supported by this server; connect with a WebSocket and " +
				"send session.start"
		}
		writeJSON(w, status, map[string]any{"error": "websocket_required", "hint": hint})
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
	s.sweepStore()
	session := NewSession(SessionOptions{
		Conn:      conn,
		Cfg:       s.cfg,
		Log:       s.log,
		ID:        id,
		Factory:   s.Factory,
		Store:     s.store,
		Seed:      seed,
		Realtime:  rt,
		Telemetry: s.tel,
	})
	if rt != nil {
		// A Realtime server speaks first.
		session.rt.mu.Lock()
		session.queueRealtime(map[string]any{
			"type": "session.created",
			"session": realtimeSession(live.SessionConfig{
				ID:    id,
				Model: s.cfg.Model,
				Audio: &live.AudioConfig{Format: &live.AudioFormat{Type: "audio/pcm", Rate: rt.Rate}},
			}),
		})
		session.rt.mu.Unlock()
	}
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
