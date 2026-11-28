package crm

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const skillsPanelBase = "http://127.0.0.1:8794"

var skillsPanelClient = &http.Client{
	Timeout:       150 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (s *Service) skillsPanelToken() (string, error) {
	body, err := os.ReadFile(s.cfg.SkillsAdminTokenFile)
	token := strings.TrimSpace(string(body))
	if err != nil || len(token) < 16 {
		return "", fail(http.StatusServiceUnavailable, "Skills & tools is unavailable")
	}
	return token, nil
}

func (s *Service) registerSkillsPanel(mux *http.ServeMux, origin string) {
	mux.HandleFunc("GET /api/skills-tools/access", s.authed(func(w http.ResponseWriter, _ *http.Request) error {
		if _, err := s.skillsPanelToken(); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]bool{"enabled": true})
		return nil
	}))

	for path, upstream := range map[string]string{
		"/skills-tools/{$}": "/", "/skills-tools/panel.js": "/panel.js", "/skills-tools/panel.css": "/panel.css",
	} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			if err := serveSkillsPanel(w, r, origin+upstream, ""); err != nil {
				http.Error(w, "Skills & tools is unavailable", http.StatusBadGateway)
			}
		})
	}

	for _, method := range []string{"GET /admin/catalog", "PUT /admin/catalog", "POST /admin/preview"} {
		verb, path, _ := strings.Cut(method, " ")
		mux.HandleFunc(verb+" /skills-tools"+path, s.authed(func(w http.ResponseWriter, r *http.Request) error {
			token, err := s.skillsPanelToken()
			if err != nil {
				return err
			}
			if err := serveSkillsPanel(w, r, origin+path, token); err != nil {
				return fail(http.StatusBadGateway, "Skills & tools is unavailable")
			}
			return nil
		}))
	}
}

// Only the three panel assets and the editor's catalog and preview operations
// are registered. A fresh request keeps Leads credentials and cookies local.
func serveSkillsPanel(w http.ResponseWriter, r *http.Request, address, token string) error {
	var body io.Reader
	if r.Method == http.MethodPut || r.Method == http.MethodPost {
		limit := int64(2 << 20)
		if r.Method == http.MethodPut {
			limit = 64 << 20
		}
		body = http.MaxBytesReader(w, r.Body, limit)
	}
	out, err := http.NewRequestWithContext(r.Context(), r.Method, address, body)
	if err != nil {
		return err
	}
	if body != nil {
		out.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		out.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := skillsPanelClient.Do(out)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return errors.New("unexpected Skills MCP redirect")
	}
	for _, header := range []string{"Content-Type", "Content-Security-Policy", "X-Content-Type-Options"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
	return nil
}
