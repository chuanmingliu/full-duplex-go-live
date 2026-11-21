package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/chuanmingliu/golive/internal/duplex"
)

// handleBusinessCallback accepts a tracking service's call-correlated event.
// Its separate credential cannot be substituted with a caller/session token.
func (s *Server) handleBusinessCallback(w http.ResponseWriter, r *http.Request) {
	if s.cfg.BusinessEventToken == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "business_callbacks_disabled"})
		return
	}
	if !tokenEqual(r.Header.Get("Authorization"), "Bearer "+s.cfg.BusinessEventToken) {
		s.rejectAuth(w, r)
		return
	}
	var event struct {
		Name    string `json:"name"`
		EventID string `json:"event_id"`
		Source  string `json:"source"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	call := r.PathValue("call")
	if dec.Decode(&event) != nil || dec.Decode(&struct{}{}) != io.EOF || event.Name != "addV" || strings.TrimSpace(event.EventID) == "" || len(event.EventID) > 128 || call == "" || cleanCallID(call) != call {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_business_event"})
		return
	}
	if event.Source == "" {
		event.Source = "tracking"
	}
	if event.Source != "tracking" && event.Source != "simulation" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_event_source"})
		return
	}
	s.mu.Lock()
	matches := []*Session{}
	for _, candidate := range s.sessions {
		if candidate.CallID() == call {
			matches = append(matches, candidate)
		}
	}
	s.mu.Unlock()
	if len(matches) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "active_call_not_found"})
		return
	}
	if len(matches) != 1 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "ambiguous_call"})
		return
	}
	session := matches[0]
	select {
	case <-session.stopRead:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "call_closing"})
		return
	default:
	}
	accepted := false
	session.withEngine(func(e *duplex.Engine) { accepted = e.ApplyBusinessEvent(event.Name) })
	if !accepted {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "call_not_ready"})
		return
	}
	// Keep payloads and business/customer identifiers out of the service log.
	s.log.Info("business: tracking event queued", "session", session.ID(), "call", call, "name", event.Name, "source", event.Source)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "call_id": call, "event_id": event.EventID, "source": event.Source})
}
