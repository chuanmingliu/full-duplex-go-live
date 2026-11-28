package journal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// This is present-day reachability, not evidence that Talker generated a
// clause on a historical call. It never starts inference or calls the phone.
func (s *Server) handleTalkerStatus(w http.ResponseWriter, r *http.Request) {
	result := map[string]any{"enabled": s.cfg.TalkerURL != "", "up": false, "checked_at": time.Now().UTC()}
	if u := safeTalkerURL(s.cfg.TalkerPlayground); u != nil {
		result["playground_url"] = u.String()
	}
	base := safeTalkerURL(s.cfg.TalkerURL)
	if base == nil {
		writeJSON(w, http.StatusOK, result)
		return
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/v1/models"
	base.Fragment = ""
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err == nil {
		client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		defer client.CloseIdleConnections()
		started := time.Now()
		resp, err := client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			var listing struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&listing) == nil && len(listing.Data) > 0 {
				result["up"], result["model"], result["probe_ms"] = true, listing.Data[0].ID, time.Since(started).Milliseconds()
			}
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func safeTalkerURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil
	}
	return u
}
