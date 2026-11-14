package journal

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Only these routes reach the fixed backend. Browser input cannot select a
// target host or expose the admin credential through a redirect.
func (s *Server) handleDreamProxy(w http.ResponseWriter, r *http.Request) {
	if s.cfg.DreamMemoryURL == "" || s.cfg.DreamAdminTokenFile == "" {
		writeJSON(w, 503, map[string]string{"error": "Live dreaming connection needs the Journal dream-memory and dream-admin-token-file settings"})
		return
	}
	path := "/admin/memory/live"
	if r.Method == http.MethodGet && r.PathValue("id") != "" {
		id := r.PathValue("id")
		if !idPattern.MatchString(id) {
			writeJSON(w, 400, map[string]string{"error": "invalid job ID"})
			return
		}
		path = "/admin/memory/jobs/" + id
	}
	var body []byte
	if r.Method != http.MethodGet {
		origin := r.Header.Get("Origin")
		if origin != "" {
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Host != r.Host || (parsed.Scheme != "https" && parsed.Scheme != "http") {
				writeJSON(w, 403, map[string]string{"error": "same-origin request required"})
				return
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeJSON(w, 403, map[string]string{"error": "same-origin JSON request required"})
			return
		}
		if r.Method == http.MethodPut {
			path = "/admin/memory/settings"
		} else {
			id := r.PathValue("id")
			if !idPattern.MatchString(id) {
				writeJSON(w, 400, map[string]string{"error": "invalid job ID"})
				return
			}
			path = "/admin/memory/dreams/" + id + "/retry"
		}
		var err error
		body, err = io.ReadAll(io.LimitReader(r.Body, 8193))
		if err != nil || len(body) > 8192 || !json.Valid(body) {
			writeJSON(w, 400, map[string]string{"error": "invalid JSON request"})
			return
		}
	}
	token, err := os.ReadFile(s.cfg.DreamAdminTokenFile)
	if err != nil || len(strings.TrimSpace(string(token))) < 16 {
		writeJSON(w, 503, map[string]string{"error": "Dreaming backend credential unavailable"})
		return
	}
	target, err := url.Parse(s.cfg.DreamMemoryURL)
	if err != nil || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || strings.Trim(target.Path, "/") != "" || (target.Scheme != "http" && target.Scheme != "https") {
		writeJSON(w, 503, map[string]string{"error": "Invalid dreaming backend origin"})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, strings.TrimRight(s.cfg.DreamMemoryURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "Dreaming request unavailable"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "Dreaming backend unavailable"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		status := 503
		if resp.StatusCode == 400 {
			var problem struct {
				Error string `json:"error"`
			}
			if json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&problem) == nil && problem.Error != "" {
				writeJSON(w, 400, problem)
				return
			}
		}
		if resp.StatusCode == 409 || resp.StatusCode == 400 {
			status = resp.StatusCode
		}
		writeJSON(w, status, map[string]string{"error": "Dreaming request failed; refresh settings and retry"})
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 || !json.Valid(data) {
		writeJSON(w, 503, map[string]string{"error": "Invalid dreaming backend response"})
		return
	}
	writeJSON(w, 200, json.RawMessage(data))
}
