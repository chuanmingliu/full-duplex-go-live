package speko

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
)

var callIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// bridgeCall is protected by Service.mu; release is delivered once only.
type bridgeCall struct {
	release chan string
	opened  bool
}

// Realtime implements the G.711 subset used by the SIP adapter. The call query
// is local correlation metadata and is never forwarded to Speko.
func (s *Service) Realtime(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	q := r.URL.Query()
	if len(q) != 1 || len(q["call"]) != 1 || !callIDPattern.MatchString(q.Get("call")) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "one_valid_call_id_required"})
		return
	}
	s.serve(w, r, true)
}

// Pool matches the adapter's dedicated-agent check without opening a session.
func (s *Service) Pool(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.statusLocked()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": status == "ready", "service": "golive", "backend": "speko", "status": status,
		"in_use": s.active, "capacity": s.cfg.MaxSessions,
		"realtime": map[string]any{"hold_for_opening": true, "dialect": "beta"},
	})
}

// Opening releases exactly the named answered call. Repeated releases are
// refused instead of causing a second greeting or a second upstream session.
func (s *Service) Opening(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	var body struct {
		CallID   string `json:"callId"`
		Greeting string `json:"greeting"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF || !callIDPattern.MatchString(body.CallID) || len(body.Greeting) > 4096 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_opening"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	call := s.calls[body.CallID]
	if s.closed || call == nil || call.opened {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "call_not_held"})
		return
	}
	call.opened = true
	call.release <- body.Greeting
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "kicked": []string{body.CallID}})
}
