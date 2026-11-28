package crm

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type labelSummary struct {
	Revision   int64     `json:"revision"`
	At         time.Time `json:"at"`
	By         string    `json:"by"`
	Lead       string    `json:"lead_id"`
	Name       string    `json:"name"`
	Transcript string    `json:"transcript_id"`
	Call       string    `json:"call_id,omitempty"`
	Vehicle    string    `json:"vehicle_id,omitempty"`
	Count      int       `json:"observations"`
	Archive    string    `json:"archive"`
	Current    bool      `json:"current"`
}

// labelResults is in durable event sequence order. Older pages use an exclusive
// cursor so newly completed calls cannot shift results across page boundaries.
func (s *Service) listLabelResults(w http.ResponseWriter, r *http.Request) error {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("id")))
	before := int64(0)
	var err error
	if raw := r.URL.Query().Get("before"); raw != "" {
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before < 1 {
			return fail(400, "invalid revision cursor")
		}
	}
	const pageSize = 25
	rows := make([]labelSummary, 0, pageSize)
	var next int64
	s.mu.Lock()
	end := len(s.labelResults)
	if before > 0 {
		end = sort.Search(end, func(i int) bool { return s.labelResults[i].Seq >= before })
	}
	for i := end - 1; i >= 0; i-- {
		rev := s.labelResults[i]
		l := rev.Labeling
		lead := s.leads[l.User.ID]
		if lead == nil || lead.Deleted {
			continue
		}
		if q != "" && q != strconv.FormatInt(rev.Seq, 10) &&
			!strings.Contains(strings.ToLower(l.User.ID), q) && !strings.Contains(strings.ToLower(l.CallID), q) &&
			!strings.Contains(strings.ToLower(l.TranscriptID), q) && !strings.Contains(strings.ToLower(l.VehicleID), q) {
			continue
		}
		if len(rows) == pageSize {
			next = rows[len(rows)-1].Revision
			break
		}
		archive := "unavailable"
		if len(l.OriginalSession) > 0 {
			archive = "stored"
			if _, ok := s.labelJobs.pending[rev.Seq]; ok {
				archive = "pending"
			}
		}
		rows = append(rows, labelSummary{rev.Seq, rev.At, rev.By, l.User.ID, l.User.Name, l.TranscriptID, l.CallID, l.VehicleID, len(l.Observations), archive, s.memory[l.User.ID].current[rev.Key].Seq == rev.Seq})
	}
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"results": rows, "next_before": next})
	return nil
}

func (s *Service) getLabelResult(w http.ResponseWriter, r *http.Request) error {
	seq, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil || seq < 1 {
		return fail(400, "invalid label revision")
	}
	s.mu.Lock()
	i := sort.Search(len(s.labelResults), func(i int) bool { return s.labelResults[i].Seq >= seq })
	if i == len(s.labelResults) || s.labelResults[i].Seq != seq {
		s.mu.Unlock()
		return fail(404, "label result not found")
	}
	rev := s.labelResults[i]
	lead := s.leads[rev.Labeling.User.ID]
	s.mu.Unlock()
	if lead == nil || lead.Deleted {
		return fail(404, "label result not found")
	}
	writeJSON(w, 200, map[string]any{"fields": json.RawMessage(rev.Value), "labeling": rev.Labeling})
	return nil
}
