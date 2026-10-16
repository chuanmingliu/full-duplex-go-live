package simulator

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Handler exposes only authenticated receiving-run management. The incoming route
// consumes a normalized trusted gateway callback, not native provider webhook JSON.
func (c *RunController) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.Header.Get("Authorization") != "Bearer "+auth || subtle.ConstantTimeCompare([]byte(auth), []byte(c.opts.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/sim/v1/")
		if path == r.URL.Path {
			http.NotFound(w, r)
			return
		}
		if path == "runs" && r.Method == http.MethodGet {
			runs, err := c.List(r.Context())
			respond(w, struct {
				Runs []Snapshot `json:"runs"`
			}{Runs: runs}, err)
			return
		}
		if r.Method == http.MethodPost && (path == "runs" || path == "incoming" || path == "validate") {
			raw, err := io.ReadAll(io.LimitReader(r.Body, MaxScenarioBytes+1))
			if err != nil {
				http.Error(w, "invalid body", 400)
				return
			}
			if len(raw) > MaxScenarioBytes {
				http.Error(w, "body exceeds 64 KiB", 413)
				return
			}
			if path == "validate" {
				s, err := DecodeScenario(strings.NewReader(string(raw)))
				if err != nil {
					respond(w, nil, err)
					return
				}
				respond(w, struct {
					Scenario     Scenario `json:"scenario"`
					ScenarioHash string   `json:"scenario_hash"`
					PromptHash   string   `json:"prompt_hash"`
				}{s, ScenarioHash(s), PromptHash(s)}, nil)
				return
			}
			if path == "runs" {
				key := r.Header.Get("Idempotency-Key")
				if key == "" {
					http.Error(w, "Idempotency-Key required", 400)
					return
				}
				s, err := DecodeScenario(strings.NewReader(string(raw)))
				if err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				snap, err := c.PrepareIdempotent(r.Context(), key, s)
				respond(w, snap, err)
				return
			}
			var in IncomingCall
			if !utf8.Valid(raw) || rejectDuplicateKeys(raw) != nil {
				http.Error(w, "invalid callback JSON", 400)
				return
			}
			d := json.NewDecoder(strings.NewReader(string(raw)))
			d.DisallowUnknownFields()
			if err = d.Decode(&in); err != nil {
				http.Error(w, "invalid callback JSON", 400)
				return
			}
			err = c.Incoming(r.Context(), in)
			respond(w, map[string]bool{"accepted": err == nil}, err)
			return
		}
		parts := strings.Split(path, "/")
		if len(parts) < 2 || parts[0] != "runs" || parts[1] == "" {
			http.NotFound(w, r)
			return
		}
		id := parts[1]
		if len(parts) == 2 && r.Method == http.MethodGet {
			snap, err := c.Get(r.Context(), id)
			respond(w, snap, err)
			return
		}
		if len(parts) == 3 {
			switch parts[2] {
			case "stop":
				if r.Method == http.MethodPost {
					err := c.Stop(r.Context(), id)
					if err != nil {
						respond(w, nil, err)
					} else {
						snap, _ := c.Get(r.Context(), id)
						respond(w, snap, nil)
					}
					return
				}
			case "events":
				if r.Method == http.MethodGet {
					after := uint64(0)
					if value := r.URL.Query().Get("after"); value != "" {
						n, err := strconv.ParseUint(value, 10, 64)
						if err != nil {
							http.Error(w, "invalid event cursor", 400)
							return
						}
						after = n
					}
					p, err := c.Events(r.Context(), id, after)
					respond(w, p, err)
					return
				}
			case "report":
				if r.Method == http.MethodGet {
					rep, err := c.Report(r.Context(), id)
					respond(w, rep, err)
					return
				}
			case "transcript":
				if r.Method == http.MethodGet {
					artifact, err := c.Transcript(r.Context(), id)
					respond(w, artifact, err)
					return
				}
			}
		}
		http.NotFound(w, r)
	})
}
func respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		code := http.StatusInternalServerError
		message := "operation failed"
		switch {
		case errors.Is(err, ErrNotFound):
			code = 404
			message = "run not found"
		case errors.Is(err, ErrConflict), errors.Is(err, ErrNotReady):
			code = 409
			message = err.Error()
		case errors.Is(err, ErrInvalidScenario), errors.Is(err, ErrInvalidTransition):
			code = 400
			message = err.Error()
		}
		http.Error(w, message, code)
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}
