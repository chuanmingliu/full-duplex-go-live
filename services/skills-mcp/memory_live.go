package main

import (
	"net/http"

	"github.com/chuanmingliu/full-duplex-go-live/services/skills-mcp/internal/dreaming"
)

func (m *memoryAPI) live(w http.ResponseWriter, r *http.Request) {
	settings, err := m.db.Settings(r.Context())
	if err != nil {
		memoryError(w, err)
		return
	}
	jobs, err := m.db.JobSummaries(r.Context())
	if err != nil {
		memoryError(w, err)
		return
	}
	calls, err := m.db.AutomaticCalls(r.Context())
	if err != nil {
		memoryError(w, err)
		return
	}
	var activity dreaming.ActivityState
	var collector dreaming.CollectorState
	if m.automatic != nil {
		activity = m.automatic.Activity.State()
		collector = m.automatic.State()
	}
	memoryJSON(w, map[string]any{"connected": true, "worker_enabled": m.workerEnabled, "settings": settings, "activity": activity, "collector": collector, "calls": calls, "jobs": jobs})
}

func (m *memoryAPI) jobDetail(w http.ResponseWriter, r *http.Request) {
	job, err := m.db.JobDetail(r.Context(), r.PathValue("id"))
	if err != nil {
		memoryError(w, err)
		return
	}
	publications := map[string]string{}
	m.catalog.mu.RLock()
	catalog := m.catalog.catalog
	m.catalog.mu.RUnlock()
	for _, lesson := range job.Lessons {
		publications[lesson.ID] = "candidate"
		for _, skill := range catalog.Skills {
			if skill.Name != lesson.ID {
				continue
			}
			publications[lesson.ID] = "catalog_edited"
			if sameMemorySkill(skill, memorySkill(lesson)) {
				publications[lesson.ID] = "published"
			}
			if !skill.Enabled {
				publications[lesson.ID] = "disabled"
			}
		}
	}
	memoryJSON(w, map[string]any{"job": job, "publications": publications})
}
