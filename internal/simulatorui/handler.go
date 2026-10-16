// Package simulatorui serves the isolated receiving simulator's workbench.
package simulatorui

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	simulatorpresets "github.com/chuanmingliu/golive/scenarios/simulator"
)

//go:embed assets/*
var assets embed.FS

// Settings is an explicit allowlist. Never encode config.Config on this surface.
type Settings struct {
	ASR          string  `json:"asr"`
	LLM          string  `json:"llm"`
	TTS          string  `json:"tts"`
	BackendModel string  `json:"backend_model"`
	Voice        string  `json:"voice"`
	Language     string  `json:"language"`
	Speed        float64 `json:"speed"`
	ClientRate   int     `json:"client_rate"`
	AllowBargeIn bool    `json:"allow_barge_in"`
	OnNewQuery   string  `json:"on_new_query"`
}
type Constraints struct {
	ReceivingOnly          bool `json:"receiving_only"`
	PrivateGatewayRequired bool `json:"private_gateway_required"`
	ProviderOverride       bool `json:"provider_override"`
	AudioRetention         bool `json:"audio_retention"`
	MaxConcurrentRuns      int  `json:"max_concurrent_runs"`
}
type ConfigResponse struct {
	Active      Settings    `json:"active"`
	Constraints Constraints `json:"constraints"`
}

// New captures only safe settings and delegates the existing management API.
// The readiness callback must obey its context; it never runs on GET config.
func New(cfg config.Config, token string, ready func(context.Context) error, control http.Handler) http.Handler {
	// The simulator receiver pins media to 16 kHz regardless of the startup profile.
	dto := ConfigResponse{Active: Settings{cfg.ASR, cfg.LLM, cfg.TTS, cfg.BackendModel, cfg.Voice, cfg.Language, cfg.Speed, 16000, cfg.Duplex.AllowBargeIn, cfg.Duplex.OnNewQuery}, Constraints: Constraints{true, true, false, false, 1}}
	static, _ := fs.Sub(assets, "assets")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/sim/v1/") {
			auth := r.Header.Get("Authorization")
			if token == "" || !strings.HasPrefix(auth, "Bearer ") || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, "Bearer ")), []byte(token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			switch r.URL.Path {
			case "/sim/v1/config", "/sim/v1/scenarios", "/sim/v1/readiness":
				if r.Method != http.MethodGet {
					w.Header().Set("Allow", "GET")
					http.Error(w, "method not allowed", 405)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/sim/v1/config":
					_ = json.NewEncoder(w).Encode(dto)
				case "/sim/v1/scenarios":
					presets, err := simulatorpresets.All()
					if err != nil {
						http.Error(w, "presets unavailable", 500)
						return
					}
					_ = json.NewEncoder(w).Encode(struct {
						Scenarios any `json:"scenarios"`
					}{presets})
				case "/sim/v1/readiness":
					ctx, cancel := context.WithTimeout(r.Context(), time.Second)
					defer cancel()
					ok := ready != nil && ready(ctx) == nil
					_ = json.NewEncoder(w).Encode(struct {
						GatewayReady bool `json:"gateway_ready"`
					}{ok})
				}
			default:
				control.ServeHTTP(w, r)
			}
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", 405)
			return
		}
		if r.URL.Path == "/" || r.URL.Path == "/sim" {
			http.Redirect(w, r, "/sim/", http.StatusTemporaryRedirect)
			return
		}
		switch r.URL.Path {
		case "/sim/":
			http.ServeFileFS(w, r, static, "index.html")
		case "/sim/app.js":
			http.ServeFileFS(w, r, static, "app.js")
		case "/sim/style.css":
			http.ServeFileFS(w, r, static, "style.css")
		default:
			http.NotFound(w, r)
		}
	})
}
