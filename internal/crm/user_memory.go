package crm

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A revision is addressed by the sequence number of its durable CRM event.
type memoryFact struct {
	Value     string `json:"value"`
	Source    string `json:"source"`
	Retracted bool   `json:"retracted,omitempty"`
}

type memoryRevision struct {
	Labeling *labelReceipt `json:"labeling,omitempty"`
	memoryFact
	Key string    `json:"key"`
	Seq int64     `json:"seq"`
	At  time.Time `json:"at"`
	By  string    `json:"by"`
}

type leadMemory struct {
	current map[string]memoryRevision
	history []memoryRevision
}

// Called during replay or with the service lock held after a successful append.
func (s *Service) applyMemory(ev Event) {
	if ev.Type == "label.archived" {
		seq, _ := strconv.ParseInt(ev.Ref, 10, 64)
		delete(s.labelJobs.pending, seq)
		return
	}
	if ev.Type != "memory.put" || ev.Memory == nil {
		return
	}
	m := s.memory[ev.Lead]
	if m == nil {
		m = &leadMemory{current: make(map[string]memoryRevision)}
		s.memory[ev.Lead] = m
	}
	r := memoryRevision{Labeling: ev.Labeling, memoryFact: *ev.Memory, Key: ev.Ref, Seq: ev.Seq, At: ev.At, By: ev.By}
	if r.Labeling != nil {
		s.labelResults = append(s.labelResults, r)
	}
	if r.Labeling != nil && len(r.Labeling.OriginalSession) > 0 {
		if s.labelJobs.pending == nil {
			s.labelJobs.pending = map[int64]memoryRevision{}
		}
		s.labelJobs.pending[r.Seq] = r
		select {
		case s.labelJobs.wake <- struct{}{}:
		default:
		}
	}
	m.current[ev.Ref] = r
	m.history = append(m.history, r)
}

func (m *leadMemory) active() []memoryRevision {
	facts := []memoryRevision{}
	if m != nil {
		for _, fact := range m.current {
			if !fact.Retracted {
				facts = append(facts, fact)
			}
		}
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].Seq > facts[j].Seq })
	return facts
}

func (s *Service) registerMemoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/label-results", s.authed(s.listLabelResults))
	mux.HandleFunc("GET /api/label-results/{revision}", s.authed(s.getLabelResult))
	mux.HandleFunc("PUT /api/leads/{id}/memory/{key}", s.authed(s.putMemory))
	mux.HandleFunc("GET /api/leads/{id}/memory", s.authed(s.getMemory))
	mux.HandleFunc("GET /api/leads/{id}/memory/trace", s.authed(s.getMemoryTrace))
}

func (s *Service) getMemory(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	s.mu.Lock()
	if lead := s.leads[id]; lead == nil || lead.Deleted {
		s.mu.Unlock()
		return fail(404, "unknown lead")
	}
	facts := s.memory[id].active()
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"lead": id, "facts": facts})
	return nil
}

func (s *Service) getMemoryTrace(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	s.mu.Lock()
	if lead := s.leads[id]; lead == nil || lead.Deleted {
		s.mu.Unlock()
		return fail(404, "unknown lead")
	}
	revisions := []memoryRevision{}
	if m := s.memory[id]; m != nil {
		revisions = append(revisions, m.history...)
	}
	archives := map[int64]map[string]string{}
	for _, rev := range revisions {
		if rev.Labeling == nil || len(rev.Labeling.OriginalSession) == 0 {
			continue
		}
		status := "stored"
		if _, pending := s.labelJobs.pending[rev.Seq]; pending {
			status = "pending"
		}
		archives[rev.Seq] = map[string]string{"status": status, "uri": archiveURI(rev)}
	}
	calls := []CallRecord{}
	for _, call := range s.calls {
		if call.Lead == id && len(call.MemorySeqs) > 0 {
			calls = append(calls, call)
		}
	}
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"lead": id, "revisions": revisions, "calls": calls, "archives": archives})
	return nil
}

func (s *Service) putMemory(w http.ResponseWriter, r *http.Request) error {
	var edit struct {
		memoryFact
		Previous *int64 `json:"previous_seq"`
	}
	if err := readJSON(r, &edit); err != nil {
		return err
	}
	id, key := r.PathValue("id"), r.PathValue("key")
	edit.Value, edit.Source = strings.TrimSpace(edit.Value), strings.TrimSpace(edit.Source)
	if !leadKey.MatchString(key) || edit.Previous == nil || edit.Source == "" || len(edit.Source) > 512 ||
		len(edit.Value) > 4096 || (edit.Value == "") != edit.Retracted {
		return fail(422, "provide a key, previous_seq, source and either a value or a retraction")
	}
	rev, err := s.saveMemory(id, key, who(r), *edit.Previous, edit.memoryFact)
	if err != nil {
		return err
	}
	writeJSON(w, 200, rev)
	return nil
}

func (s *Service) saveMemory(id, key, by string, previous int64, fact memoryFact) (memoryRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var old memoryRevision
	if lead := s.leads[id]; lead == nil || lead.Deleted {
		return old, fail(404, "unknown lead")
	}
	if m := s.memory[id]; m != nil {
		old = m.current[key]
	}
	if old.memoryFact == fact {
		return old, nil
	}
	if old.Seq != previous {
		return old, fail(409, "memory changed; read the latest revision before editing")
	}
	if fact.Retracted && old.Seq == 0 {
		return old, fail(404, "unknown memory key")
	}
	ev, err := s.events.Append(Event{Type: "memory.put", Lead: id, Ref: key, By: by, Memory: &fact})
	if err != nil {
		return old, err
	}
	s.applyMemory(ev)
	return s.memory[id].current[key], nil
}

func (s *Service) renderWithMemory(req CallRequest, vars map[string]any, now time.Time) (*Rendered, error) {
	defaults := s.defaults()
	// Resolve the template version once so the opt-in check and the render
	// agree if another operator publishes a new version concurrently.
	t, err := s.prompts.Get(req.Template, req.Version)
	if err != nil {
		return nil, err
	}
	var seqs []int64
	var phone string
	if _, override := vars["user_memory"]; !override && req.Lead != "" && slices.Contains(t.Names, "user_memory") {
		facts, ownerPhone := s.recallMemory(req.Lead)
		phone = ownerPhone
		if len(facts) > 0 {
			values := make(map[string]any, len(facts))
			for _, fact := range facts {
				if fact.Labeling != nil {
					values[fact.Key] = json.RawMessage(fact.Value)
				} else {
					values[fact.Key] = fact.Value
				}
				seqs = append(seqs, fact.Seq)
			}
			if defaults == nil {
				defaults = map[string]any{}
			}
			raw, _ := json.Marshal(values)
			defaults["user_memory"] = string(raw)
		}
	}
	out, err := s.prompts.renderTemplate(t, vars, now, defaults)
	if err == nil && slices.Contains(out.Defaulted, "user_memory") {
		out.MemorySeqs, out.memoryPhone = seqs, phone
	}
	return out, err
}

func (s *Service) recallMemory(id string) ([]memoryRevision, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lead := s.leads[id]
	if lead == nil || lead.Deleted {
		return nil, ""
	}
	facts := s.memory[id].active()
	facts = slices.DeleteFunc(facts, func(r memoryRevision) bool { return r.Labeling != nil && len(r.Labeling.Observations) == 0 })
	return facts[:min(8, len(facts))], lead.Values["phone"]
}
