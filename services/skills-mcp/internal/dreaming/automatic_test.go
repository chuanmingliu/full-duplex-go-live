package dreaming

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAutomaticCaptureIdleReviewAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.sqlite")
	store, err := Open(path)
	requireOK(t, err)
	settings, err := store.Settings(ctx)
	requireOK(t, err)
	if !settings.Enabled || settings.IdleSeconds != 30 {
		t.Fatal(settings)
	}
	settings.IdleSeconds = 0
	settings, err = store.SaveSettings(ctx, settings)
	requireOK(t, err)
	if _, err = store.SaveSettings(ctx, Settings{Revision: 1, IdleSeconds: 0}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale write accepted", err)
	}
	var sessions atomic.Int32
	sessions.Store(1)
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "sessions": sessions.Load()})
	}))
	defer engine.Close()
	closed := time.Now().UTC()
	var ready atomic.Bool
	var sourceReads atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceReads.Add(1)
		switch r.URL.Path {
		case "/api/dreaming/source":
			json.NewEncoder(w).Encode(map[string]any{"calls": []SourceCall{{ID: "call1", ClosedAt: closed, Mode: "real", Ready: ready.Load()}, {ID: "old-call", ClosedAt: settings.Since.Add(-time.Hour), Mode: "real", Ready: true}}})
		case "/api/dreaming/source/call1":
			raw, _ := json.Marshal(map[string]any{"id": "call1", "ended_at": closed, "transcript": []map[string]any{{"who": "caller", "text": "Please repeat the price"}}})
			json.NewEncoder(w).Encode(map[string]any{"archive_json": string(raw), "feedback": map[string]any{"note": "Repeat only the requested item", "rating": 3}, "audio_refs": []string{"local/audio.wav"}})
		default:
			t.Error("unexpected source read", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer source.Close()
	automatic, err := NewAutomatic(store, source.URL, []string{engine.URL})
	requireOK(t, err)
	automatic.Collect(ctx)
	if sourceReads.Load() != 0 || automatic.CanReview(ctx) {
		t.Fatal("ran while a call was active")
	}
	sessions.Store(0)
	automatic.Collect(ctx)
	calls, err := store.AutomaticCalls(ctx)
	requireOK(t, err)
	if len(calls) != 1 || calls[0].Status != "waiting_archive" {
		t.Fatal(calls)
	}
	// A prior partial/manual import must survive later archive reserialization.
	savedRaw, _ := json.MarshalIndent(map[string]any{"id": "call1", "ended_at": closed, "transcript": []map[string]any{{"who": "caller", "text": "Please repeat the price"}}}, "", "  ")
	savedEpisode, err := ProjectArchive(savedRaw, "real", []string{"local/audio.wav"})
	requireOK(t, err)
	requireOK(t, store.Import(ctx, savedEpisode))
	ready.Store(true)
	automatic.Collect(ctx)
	automatic.Collect(ctx)
	jobs, err := store.JobSummaries(ctx)
	requireOK(t, err)
	if len(jobs) != 1 || jobs[0].Status != "pending" {
		t.Fatal(jobs)
	}
	job, err := store.Job(ctx, jobs[0].ID)
	requireOK(t, err)
	if len(job.Snapshot.Episodes[0].Evidence) != 2 || job.Snapshot.Episodes[0].ArchiveJSON != string(savedRaw) {
		t.Fatal("evidence or feedback missing")
	}
	if !automatic.CanReview(ctx) {
		t.Fatal("idle worker did not become ready")
	}
	var reviewed atomic.Int32
	reviewer := reviewFunc(func(ctx context.Context, snapshot Snapshot) ([]Proposal, error) {
		reviewed.Add(1)
		p := proposal()
		p.Evidence[0].EvidenceID = "transcript-0"
		return []Proposal{p}, nil
	})
	requireOK(t, store.stepWhileIdle(ctx, reviewer, automatic.CanReview))
	jobs, err = store.JobSummaries(ctx)
	requireOK(t, err)
	if jobs[0].Status != "completed" || jobs[0].LessonCount != 1 || reviewed.Load() != 1 {
		t.Fatal(jobs)
	}
	if jobs[0].Events[len(jobs[0].Events)-1].Stage != "completed" {
		t.Fatal("missing durable completion event")
	}
	requireOK(t, store.Close())
	store, err = Open(path)
	requireOK(t, err)
	defer store.Close()
	automatic.Store = store
	automatic.Collect(ctx)
	requireOK(t, store.stepWhileIdle(ctx, reviewer, automatic.CanReview))
	if reviewed.Load() != 1 {
		t.Fatal("restart reviewed the same hangup twice")
	}
	persisted, err := store.Settings(ctx)
	requireOK(t, err)
	if persisted.Revision != settings.Revision || !persisted.Since.Equal(settings.Since) {
		t.Fatal("settings lost")
	}
}

func TestReviewDefersWhenAgentBecomesBusy(t *testing.T) {
	ctx := context.Background()
	store := openTest(t)
	requireOK(t, store.Import(ctx, episode()))
	requireOK(t, store.Release(ctx, "call1"))
	_, err := store.Queue(ctx, DreamRequest{ID: "idle-review", EpisodeIDs: []string{"call1"}})
	requireOK(t, err)
	var allow atomic.Bool
	allow.Store(true)
	started := make(chan struct{})
	completed := make(chan error, 1)
	go func() {
		completed <- store.stepWhileIdle(ctx, reviewFunc(func(ctx context.Context, _ Snapshot) ([]Proposal, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}), func(context.Context) bool { return allow.Load() })
	}()
	<-started
	allow.Store(false)
	select {
	case err = <-completed:
		if err == nil {
			t.Fatal("interrupted review succeeded")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("busy review was not cancelled")
	}
	job, err := store.Job(ctx, "idle-review")
	requireOK(t, err)
	if job.Status != "pending" {
		t.Fatal(job.Status)
	}
	lessons, err := store.Lessons(ctx)
	requireOK(t, err)
	if len(lessons) != 0 {
		t.Fatal("interrupted draft committed")
	}
	allow.Store(true)
	requireOK(t, store.stepWhileIdle(ctx, reviewFunc(func(context.Context, Snapshot) ([]Proposal, error) { return []Proposal{}, nil }), func(context.Context) bool { return allow.Load() }))
	jobs, err := store.JobSummaries(ctx)
	requireOK(t, err)
	detail, err := store.JobDetail(ctx, "idle-review")
	requireOK(t, err)
	stages := []string{}
	for _, event := range detail.Events {
		stages = append(stages, event.Stage)
	}
	if !strings.Contains(strings.Join(stages, ","), "deferred") || jobs[0].Status != "completed" {
		t.Fatal(stages)
	}
}

func TestActivityUnknownAndIdleDelay(t *testing.T) {
	store := openTest(t)
	ctx := context.Background()
	var mode atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 1:
			json.NewEncoder(w).Encode(map[string]any{"status": "ok", "sessions": 1})
		case 2:
			http.Error(w, "unavailable", 503)
		case 3:
			w.Write([]byte(`{"status":"ok"}`))
		default:
			w.Write([]byte(`{"status":"ok","sessions":0}`))
		}
	}))
	defer engine.Close()
	automatic, err := NewAutomatic(store, "", []string{engine.URL})
	requireOK(t, err)
	if automatic.CanReview(ctx) {
		t.Fatal("idle delay was skipped")
	}
	automatic.Activity.mu.Lock()
	automatic.Activity.state.IdleSince = time.Now().Add(-time.Minute)
	automatic.Activity.mu.Unlock()
	if !automatic.CanReview(ctx) {
		t.Fatal("idle delay did not expire")
	}
	for _, m := range []int32{1, 2, 3} {
		mode.Store(m)
		if automatic.CanReview(ctx) {
			t.Fatalf("unsafe readiness mode %d", m)
		}
		if !automatic.Activity.State().IdleSince.IsZero() {
			t.Fatal("idle timer not reset")
		}
	}
}
