package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/provider"
)

// Server exposes the live protocol over WebSocket, plus a health endpoint and
// the optional demo client.
type Server struct {
	cfg config.Config
	log *slog.Logger

	upgrader websocket.Upgrader

	mu       sync.Mutex
	sessions map[string]*Session

	// Factory lets tests inject a stub engine.
	Factory EngineFactory
}

// NewServer builds a server.
func NewServer(cfg config.Config, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		cfg:      cfg,
		log:      log,
		sessions: map[string]*Session{},
		upgrader: websocket.Upgrader{
			ReadBufferSize:  32 << 10,
			WriteBufferSize: 32 << 10,
			// golive is meant to run behind your own edge, and a voice session
			// carries no ambient credentials (no cookie is read here), so the
			// browser demo is allowed to connect from anywhere. Put an
			// authenticating proxy in front of it before exposing it.
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// Handler builds the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/live", s.handleLive)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/live/providers", s.handleConfig)
	mux.HandleFunc("/v1/live/config", s.handleConfig)

	if s.cfg.WebRoot != "" {
		if info, err := os.Stat(s.cfg.WebRoot); err == nil && info.IsDir() {
			mux.Handle("/", http.FileServer(http.Dir(filepath.Clean(s.cfg.WebRoot))))
			s.log.Info("serving demo client", "root", s.cfg.WebRoot)
		} else {
			s.log.Warn("web root not found; demo client disabled", "root", s.cfg.WebRoot)
		}
	}
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	active := len(s.sessions)
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"model":    s.cfg.Model,
		"sessions": active,
	})
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
			"instructions": s.cfg.Instructions,
			"greeting":     s.cfg.Greeting,
			"model":        s.cfg.Model,
			"language":     s.cfg.Language,
			"rate":         s.cfg.ClientRate,
		},
	})
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("websocket upgrade failed", "err", err)
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

	s.mu.Lock()
	s.sessions[id] = session
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, id)
		s.mu.Unlock()
	}()

	ctx := r.Context()
	if max := s.cfg.Duplex.SessionMaxSeconds; max > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(max)*time.Second)
		defer cancel()
	}
	session.Serve(ctx)
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
