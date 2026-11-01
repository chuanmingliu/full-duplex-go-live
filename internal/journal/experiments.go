package journal

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ExperimentSource is an operator-selected journal, never a browser-supplied URL.
// The upstream address stays private; browsers use the existing site's origin.
type ExperimentSource struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
	URL         string `json:"url,omitempty"`
}

var sourceID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func (s *Server) experimentSources() ([]ExperimentSource, error) {
	sources := []ExperimentSource{}
	if s.cfg.NotesDir == "" {
		return sources, nil
	}
	b, err := os.ReadFile(filepath.Join(s.cfg.NotesDir, "experiments.json"))
	if os.IsNotExist(err) {
		return sources, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &sources); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, src := range sources {
		u, err := url.Parse(src.URL)
		if err != nil || (src.Kind != "real" && src.Kind != "simulated") || !sourceID.MatchString(src.ID) || seen[src.ID] || u == nil || u.Scheme != "http" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("invalid experiment journal configuration")
		}
		seen[src.ID] = true
	}
	return sources, nil
}

func (s *Server) handleExperiments(w http.ResponseWriter, r *http.Request) {
	sources, err := s.experimentSources()
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "Experiment journals are not configured correctly"})
		return
	}
	for i := range sources {
		sources[i].URL = ""
	}
	writeJSON(w, 200, sources)
}

// Only read operations and the journal's own UI are available under this mount.
// Range requests stream through unchanged so long recordings remain seekable.
func (s *Server) experimentHandler(static http.Handler) http.Handler {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 12 * time.Second
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Read-only journal", 405)
			return
		}
		id, rest, found := strings.Cut(strings.TrimPrefix(r.URL.Path, "/experiments/"), "/")
		if !found || !sourceID.MatchString(id) {
			http.NotFound(w, r)
			return
		}
		sources, err := s.experimentSources()
		if err != nil {
			http.Error(w, "Journal configuration unavailable", 503)
			return
		}
		var src *ExperimentSource
		for i := range sources {
			if sources[i].ID == id {
				src = &sources[i]
				break
			}
		}
		if src == nil {
			http.NotFound(w, r)
			return
		}
		if rest == "api/source" {
			public := *src
			public.URL = ""
			writeJSON(w, 200, public)
			return
		}
		switch rest {
		case "", "index.html", "app.js", "app.css", "fillers.js", "fillers.css", "latency.js", "experiments.js", "experiments.css":
			w.Header().Set("Cache-Control", "no-cache")
			http.StripPrefix("/experiments/"+id, static).ServeHTTP(w, r)
			return
		}
		allowed := rest == "api/overview" || rest == "api/issues" || rest == "api/decisions" || rest == "api/benchmarks"
		if strings.HasPrefix(rest, "api/calls/") {
			call, part, _ := strings.Cut(strings.TrimPrefix(rest, "api/calls/"), "/")
			switch part {
			case "", "audio.wav", "transcript.txt", "peaks", "gate":
				allowed = idPattern.MatchString(call)
			}
		}
		if !allowed {
			http.NotFound(w, r)
			return
		}
		target, _ := url.Parse(src.URL)
		proxy := &httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(p *httputil.ProxyRequest) {
				p.SetURL(target)
				p.Out.URL.Path = "/" + rest
				p.Out.URL.RawPath = ""
				// Do not send Cloudflare identity, cookies or browser authorization to journals.
				p.Out.Header = make(http.Header)
				for _, key := range []string{"Range", "If-Range", "Accept"} {
					if v := p.In.Header.Get(key); v != "" {
						p.Out.Header.Set(key, v)
					}
				}
			},
			ModifyResponse: func(resp *http.Response) error {
				if resp.StatusCode >= 300 && resp.StatusCode < 400 {
					return errors.New("upstream journal redirect refused")
				}
				resp.Header.Del("Set-Cookie")
				resp.Header.Set("Cache-Control", "no-store")
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				http.Error(w, "This experiment journal is unavailable. Try another journal or retry later.", 502)
			},
		}
		proxy.ServeHTTP(w, r)
	})
}
