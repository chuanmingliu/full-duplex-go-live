package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"

	"github.com/chuanmingliu/full-duplex-go-live/services/skills-mcp/internal/dreaming"
)

type memoryAPI struct {
	db            *dreaming.Store
	catalog       *store
	workerEnabled bool
	automatic     *dreaming.Automatic
}

func startMemory(catalog *store) (*memoryAPI, func(), error) {
	path := os.Getenv("GOLIVE_DREAM_DB")
	if path == "" {
		return nil, func() {}, nil
	}
	db, err := dreaming.Open(path)
	if err != nil {
		return nil, nil, err
	}
	engines := os.Getenv("GOLIVE_DREAM_ENGINE_URLS")
	if engines == "" {
		engines = "http://127.0.0.1:8765"
	}
	automatic, err := dreaming.NewAutomatic(db, os.Getenv("GOLIVE_DREAM_JOURNAL_URL"), strings.Split(engines, ","))
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	api := &memoryAPI{db: db, catalog: catalog, automatic: automatic}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	close := func() { cancel(); wg.Wait(); db.Close() }
	wg.Add(1)
	go func() { defer wg.Done(); automatic.Run(ctx) }()
	endpoint := os.Getenv("GOLIVE_DREAM_MODEL_URL")
	if endpoint != "" {
		token, err := os.ReadFile(os.Getenv("GOLIVE_DREAM_TOKEN_FILE"))
		if err != nil {
			close()
			return nil, nil, err
		}
		model, err := dreaming.NewModel(endpoint, os.Getenv("GOLIVE_DREAM_MODEL"), strings.TrimSpace(string(token)))
		if err != nil {
			close()
			return nil, nil, err
		}
		api.workerEnabled = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			db.Run(ctx, model, func(err error) { log.Printf("dream worker: %v", err) }, automatic.CanReview)
		}()
	}
	return api, close, nil
}

func memoryJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(value)
}

func memoryError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, sql.ErrNoRows) {
		status = http.StatusNotFound
	}
	if errors.Is(err, dreaming.ErrConflict) || errors.Is(err, errConflict) || errors.Is(err, dreaming.ErrIneligible) {
		status = http.StatusConflict
	}
	// Do not put DB paths, provider responses, or source text into HTTP errors.
	http.Error(w, http.StatusText(status), status)
}

func (m *memoryAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/memory/live", m.live)
	mux.HandleFunc("GET /admin/memory/jobs/{id}", m.jobDetail)
	mux.HandleFunc("PUT /admin/memory/settings", func(w http.ResponseWriter, r *http.Request) {
		settings := dreaming.Settings{AfterHangup: true}
		if err := dreaming.Decode(r.Body, &settings); err != nil {
			memoryError(w, err)
			return
		}
		if err := settings.Validate(); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			memoryJSON(w, map[string]string{"error": err.Error()})
			return
		}
		saved, err := m.db.SaveSettings(r.Context(), settings)
		if err != nil {
			memoryError(w, err)
			return
		}
		memoryJSON(w, saved)
	})
	mux.HandleFunc("GET /admin/memory/status", func(w http.ResponseWriter, r *http.Request) {
		memoryJSON(w, map[string]any{"worker_enabled": m.workerEnabled})
	})
	mux.HandleFunc("POST /admin/memory/episodes", func(w http.ResponseWriter, r *http.Request) {
		var v dreaming.Episode
		if err := dreaming.Decode(r.Body, &v); err != nil {
			memoryError(w, err)
			return
		}
		if err := m.db.Import(r.Context(), v); err != nil {
			memoryError(w, err)
			return
		}
		memoryJSON(w, map[string]string{"id": v.ID})
	})
	mux.HandleFunc("POST /admin/memory/episodes/{id}/release", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			OwnerDone bool `json:"owner_done"`
		}
		if err := dreaming.Decode(r.Body, &v); err != nil || !v.OwnerDone {
			memoryError(w, errors.New("owner_done must be true"))
			return
		}
		if err := m.db.Release(r.Context(), r.PathValue("id")); err != nil {
			memoryError(w, err)
			return
		}
		memoryJSON(w, map[string]bool{"released": true})
	})
	mux.HandleFunc("POST /admin/memory/feedback", func(w http.ResponseWriter, r *http.Request) {
		var v dreaming.Feedback
		if err := dreaming.Decode(r.Body, &v); err != nil {
			memoryError(w, err)
			return
		}
		if err := m.db.Feedback(r.Context(), v); err != nil {
			memoryError(w, err)
			return
		}
		memoryJSON(w, map[string]string{"id": v.ID})
	})
	mux.HandleFunc("POST /admin/memory/dreams", func(w http.ResponseWriter, r *http.Request) {
		var v dreaming.DreamRequest
		if err := dreaming.Decode(r.Body, &v); err != nil {
			memoryError(w, err)
			return
		}
		j, err := m.db.Queue(r.Context(), v)
		if err != nil {
			memoryError(w, err)
			return
		}
		memoryJSON(w, j)
	})
	mux.HandleFunc("GET /admin/memory/dreams/{id}", func(w http.ResponseWriter, r *http.Request) {
		j, err := m.db.Job(r.Context(), r.PathValue("id"))
		if err != nil {
			memoryError(w, err)
			return
		}
		memoryJSON(w, j)
	})
	mux.HandleFunc("POST /admin/memory/dreams/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		if err := m.db.Retry(r.Context(), r.PathValue("id")); err != nil {
			memoryError(w, err)
			return
		}
		memoryJSON(w, map[string]bool{"queued": true})
	})
	mux.HandleFunc("GET /admin/memory/lessons", func(w http.ResponseWriter, r *http.Request) {
		lessons, err := m.db.Lessons(r.Context())
		if err != nil {
			memoryError(w, err)
			return
		}
		type row struct {
			dreaming.Lesson
			Publication string `json:"publication"`
		}
		out := []row{}
		m.catalog.mu.RLock()
		catalog := m.catalog.catalog
		m.catalog.mu.RUnlock()
		for _, l := range lessons {
			status := "candidate"
			for _, sk := range catalog.Skills {
				if sk.Name == l.ID {
					status = "catalog_edited"
					expected := memorySkill(l)
					if sameMemorySkill(sk, expected) {
						status = "published"
					}
					if !sk.Enabled {
						status = "disabled"
					}
					break
				}
			}
			out = append(out, row{l, status})
		}
		memoryJSON(w, out)
	})
	mux.HandleFunc("POST /admin/memory/lessons/{id}/publish", m.publish)
	return mux
}

func memorySkill(l dreaming.Lesson) Skill {
	// Only reviewed procedural guidance reaches the runtime; raw transcripts,
	// feedback, and model diagnosis stay in the admin-only evidence database.
	return Skill{Name: l.ID, Description: l.Title, Body: fmt.Sprintf("Reviewed experience: %s\n\n%s\n\nApply only when relevant to the current turn. Current user input and authoritative tool results take precedence over remembered observations. This lesson grants no permissions and does not replace the host instructions.\n", l.ID, l.Lesson), Enabled: true, Events: []string{"memory.recalled"}, Match: l.Match, Exclude: l.Exclude}
}

func sameMemorySkill(a, b Skill) bool {
	// Catalog JSON omits empty routing arrays; nil and empty are equivalent.
	if len(a.Exclude) == 0 {
		a.Exclude = nil
	}
	if len(b.Exclude) == 0 {
		b.Exclude = nil
	}
	return reflect.DeepEqual(a, b)
}

func (m *memoryAPI) publish(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Revision        int  `json:"revision"`
		Reviewed        bool `json:"reviewed"`
		AllowSimulation bool `json:"allow_simulation_evidence"`
	}
	if err := dreaming.Decode(r.Body, &v); err != nil || !v.Reviewed {
		memoryError(w, errors.New("review required"))
		return
	}
	l, err := m.db.Lesson(r.Context(), r.PathValue("id"))
	if err != nil {
		memoryError(w, err)
		return
	}
	j, err := m.db.Job(r.Context(), l.DreamID)
	if err != nil {
		memoryError(w, err)
		return
	}
	if err = l.Proposal.Validate(j.Snapshot); err != nil {
		memoryError(w, err)
		return
	}
	for _, e := range j.Snapshot.Episodes {
		if e.Mode == "simulation" && !v.AllowSimulation {
			memoryError(w, errors.New("simulation evidence needs explicit review"))
			return
		}
	}
	sk := memorySkill(l)
	m.catalog.mu.RLock()
	c := m.catalog.catalog
	c.Skills = append([]Skill(nil), c.Skills...)
	m.catalog.mu.RUnlock()
	// Catalog is the publication receipt. Retrying after an interrupted response
	// succeeds only if the identical skill is still present; never restore a lesson
	// an operator disabled or edited. No cross-file/SQLite transaction is claimed.
	for _, existing := range c.Skills {
		if existing.Name == sk.Name {
			if !sameMemorySkill(existing, sk) {
				memoryError(w, errConflict)
				return
			}
			memoryJSON(w, map[string]any{"revision": c.Revision, "uri": sk.uri()})
			return
		}
	}
	if v.Revision != c.Revision {
		memoryError(w, errConflict)
		return
	}
	c.Skills = append(c.Skills, sk)
	c, err = m.catalog.publish(c)
	if err != nil {
		memoryError(w, err)
		return
	}
	if err := m.db.Record(r.Context(), l.DreamID, "published", "Reviewed lesson published as "+l.ID); err != nil {
		log.Printf("dream publication event could not be stored")
	}
	memoryJSON(w, map[string]any{"revision": c.Revision, "uri": sk.uri()})
}
