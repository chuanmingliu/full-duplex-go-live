package journal

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// DreamingRecord is an operator-reviewed account of a memory's lifecycle.
// It deliberately has no connection to the private experience database.
type DreamingRecord struct {
	Version    int           `json:"version"`
	RecordedAt time.Time     `json:"recorded_at"`
	Runs       []DreamingRun `json:"runs"`
}

type DreamingRun struct {
	ID      string          `json:"id"`
	Title   string          `json:"title"`
	Summary string          `json:"summary"`
	Status  string          `json:"status"`
	Origin  string          `json:"origin"`
	Facts   []DreamingFact  `json:"facts"`
	Stages  []DreamingStage `json:"stages"`
}

type DreamingFact struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type DreamingStage struct {
	ID      string          `json:"id"`
	Title   string          `json:"title"`
	Status  string          `json:"status"`
	Summary string          `json:"summary"`
	Details []string        `json:"details"`
	Facts   []DreamingFact  `json:"facts"`
	Checks  []string        `json:"checks"`
	Blocks  []DreamingBlock `json:"blocks"`
	CallIDs []string        `json:"call_ids"`
}

type DreamingBlock struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}

func (d DreamingRecord) valid() bool {
	if d.Version != 1 || d.RecordedAt.IsZero() || len(d.Runs) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, run := range d.Runs {
		if !idPattern.MatchString(run.ID) || seen[run.ID] || run.Title == "" || len(run.Stages) == 0 || len(run.Stages) > 20 {
			return false
		}
		seen[run.ID] = true
		stages := map[string]bool{}
		for _, stage := range run.Stages {
			if !idPattern.MatchString(stage.ID) || stages[stage.ID] || stage.Title == "" {
				return false
			}
			stages[stage.ID] = true
			for _, id := range stage.CallIDs {
				if !idPattern.MatchString(id) {
					return false
				}
			}
		}
	}
	return true
}

func (s *Server) handleDreaming(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Use GET to read the dreaming record"})
		return
	}
	empty := map[string]any{"available": false, "runs": []DreamingRun{}}
	if s.cfg.NotesDir == "" {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	file, err := os.Open(filepath.Join(s.cfg.NotesDir, "dreaming.json"))
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Dreaming record cannot be read"})
		return
	}
	defer file.Close()
	const limit = 2 << 20
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	var record DreamingRecord
	if err != nil || len(data) > limit || json.Unmarshal(data, &record) != nil || !record.valid() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Dreaming record is invalid; check the reviewed notes file"})
		return
	}
	writeJSON(w, http.StatusOK, record)
}
