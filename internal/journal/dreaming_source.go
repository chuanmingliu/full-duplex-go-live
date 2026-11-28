package journal

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

type dreamSourceCall struct {
	ID       string    `json:"id"`
	ClosedAt time.Time `json:"closed_at"`
	Mode     string    `json:"mode"`
	Ready    bool      `json:"ready"`
}

// The collector reads the archive through this bounded, read-only API. The
// journal watcher owns archive creation, so collection never edits recordings.
func (s *Server) handleDreamSource(w http.ResponseWriter, r *http.Request) {
	since, err := time.Parse(time.RFC3339Nano, r.URL.Query().Get("since"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "valid since timestamp required"})
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	s.refresh()
	s.mu.Lock()
	calls := []dreamSourceCall{}
	for _, call := range s.calls {
		if call.EndedAt.IsZero() || call.EndedAt.Before(since) {
			continue
		}
		mode := "real"
		if s.simRuns[call.ID] != "" {
			mode = "simulation"
		}
		ready := false
		if s.archive != nil {
			e := s.archive.Entry(call.ID)
			ready = e != nil && (e.Audio || s.cfg.StoreDir == "")
		}
		calls = append(calls, dreamSourceCall{ID: call.ID, ClosedAt: call.EndedAt, Mode: mode, Ready: ready})
	}
	s.mu.Unlock()
	sort.Slice(calls, func(i, j int) bool { return calls[i].ID < calls[j].ID })
	if offset > len(calls) {
		offset = len(calls)
	}
	end := offset + 100
	if end > len(calls) {
		end = len(calls)
	}
	writeJSON(w, 200, map[string]any{"calls": calls[offset:end], "more": end < len(calls)})
}
func (s *Server) handleDreamEvidence(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) || s.archive == nil {
		writeJSON(w, 404, map[string]string{"error": "archive unavailable"})
		return
	}
	entry := s.archive.Entry(id)
	if entry == nil || (!entry.Audio && s.cfg.StoreDir != "") {
		writeJSON(w, 409, map[string]string{"error": "archive pending"})
		return
	}
	raw, err := os.ReadFile(filepath.Join(s.archive.Dir(), entry.Dir, archCall))
	if err != nil || len(raw) > 512<<10 || fmt.Sprintf("%x", sha256.Sum256(raw)) != entry.Files[archCall].SHA256 {
		writeJSON(w, 503, map[string]string{"error": "archive validation failed"})
		return
	}
	var call Call
	if json.Unmarshal(raw, &call) != nil || call.ID != id || call.EndedAt.IsZero() {
		writeJSON(w, 503, map[string]string{"error": "completed archive required"})
		return
	}
	refs := []string{}
	if entry.Audio {
		refs = append(refs, filepath.Join(s.archive.Dir(), entry.Dir, archAudio))
	}
	writeJSON(w, 200, map[string]any{"archive_json": string(raw), "audio_refs": refs, "feedback": s.feedback(id)})
}
