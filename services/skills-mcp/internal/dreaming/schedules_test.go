package dreaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestCronValidationAndTimezone(t *testing.T) {
	task := CronTask{ID: "nightly", Name: "Nightly review", Enabled: true, Expression: "0 2 * * *", Timezone: "Asia/Shanghai"}
	schedule, err := task.schedule()
	requireOK(t, err)
	now := time.Date(2026, 11, 12, 17, 59, 0, 0, time.UTC)
	want := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)
	if got := schedule.Next(now); !got.Equal(want) {
		t.Fatal(got, want)
	}
	for _, expression := range []string{"bad", "* * * * * *", "61 * * * *", "0 0 30 2 *", "@hourly"} {
		task.Expression = expression
		if _, err = task.schedule(); err == nil {
			t.Fatal("accepted invalid cron", expression)
		}
	}
	task.Expression = "*/15 9-17 * * 1-5"
	task.Timezone = "America/New_York"
	if _, err = task.schedule(); err != nil {
		t.Fatal(err)
	}
	task.Timezone = "invalid/zone"
	if _, err = task.schedule(); err == nil {
		t.Fatal("accepted invalid timezone")
	}
}
func TestCronOnlyDispatchSurvivesBusyAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.sqlite")
	store, err := Open(path)
	requireOK(t, err)
	settings, err := store.Settings(ctx)
	requireOK(t, err)
	settings.AfterHangup = false
	settings.IdleSeconds = 0
	settings.Schedules = []CronTask{{ID: "nightly", Name: "Nightly", Enabled: true, Expression: "0 2 * * *", Timezone: "Asia/Shanghai"}}
	_, err = store.SaveSettings(ctx, settings)
	requireOK(t, err)
	closed := time.Now().UTC()
	var busy atomic.Bool
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := 0
		if busy.Load() {
			n = 1
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "sessions": n})
	}))
	defer engine.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/dreaming/source" {
			json.NewEncoder(w).Encode(map[string]any{"calls": []SourceCall{{ID: "scheduled-call", ClosedAt: closed, Mode: "real", Ready: true}}})
			return
		}
		raw, _ := json.Marshal(map[string]any{"id": "scheduled-call", "ended_at": closed, "transcript": []map[string]any{{"who": "caller", "text": "one question please"}}})
		json.NewEncoder(w).Encode(map[string]any{"archive_json": string(raw)})
	}))
	defer source.Close()
	auto, err := NewAutomatic(store, source.URL, []string{engine.URL})
	requireOK(t, err)
	auto.Collect(ctx)
	jobs, err := store.JobSummaries(ctx)
	requireOK(t, err)
	if len(jobs) != 0 {
		t.Fatal("cron-only queued before due")
	}
	calls, err := store.AutomaticCalls(ctx)
	requireOK(t, err)
	if calls[0].Status != "waiting_schedule" {
		t.Fatal(calls)
	}
	due := time.Now().Add(-time.Minute)
	_, err = store.db.Exec(`UPDATE cron_tasks SET next_run=?`, due.UnixMilli())
	requireOK(t, err)
	busy.Store(true)
	auto.Collect(ctx)
	tasks, err := store.CronTasks(ctx)
	requireOK(t, err)
	if tasks[0].PendingSince.IsZero() {
		t.Fatal("busy task lost its due occurrence")
	}
	requireOK(t, store.Close())
	store, err = Open(path)
	requireOK(t, err)
	defer store.Close()
	auto.Store = store
	busy.Store(false)
	auto.Collect(ctx)
	auto.Collect(ctx)
	jobs, err = store.JobSummaries(ctx)
	requireOK(t, err)
	if len(jobs) != 1 {
		t.Fatal("expected one scheduled review", jobs)
	}
	runs, err := store.CronRuns(ctx)
	requireOK(t, err)
	if len(runs) != 1 || len(runs[0].JobIDs) != 1 || !runs[0].ScheduledAt.Equal(time.UnixMilli(due.UnixMilli())) {
		t.Fatal(runs)
	}
	tasks, err = store.CronTasks(ctx)
	requireOK(t, err)
	if !tasks[0].PendingSince.IsZero() || !tasks[0].NextRun.After(time.Now()) {
		t.Fatal(tasks)
	}
	settings, err = store.Settings(ctx)
	requireOK(t, err)
	settings.Schedules = nil
	_, err = store.SaveSettings(ctx, settings)
	requireOK(t, err)
	tasks, err = store.CronTasks(ctx)
	requireOK(t, err)
	if len(tasks) != 0 {
		t.Fatal("removed schedule remains")
	}
	runs, err = store.CronRuns(ctx)
	requireOK(t, err)
	if len(runs) != 1 {
		t.Fatal("removing task erased its history")
	}
}

func TestCronDispatchReceiptRecoversAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.sqlite")
	store, err := Open(path)
	requireOK(t, err)
	settings, err := store.Settings(ctx)
	requireOK(t, err)
	settings.Schedules = []CronTask{{ID: "hourly", Name: "Hourly", Enabled: true, Expression: "0 * * * *", Timezone: "UTC"}}
	_, err = store.SaveSettings(ctx, settings)
	requireOK(t, err)
	_, err = store.db.Exec(`UPDATE cron_tasks SET next_run=?`, time.Now().Add(-time.Minute).UnixMilli())
	requireOK(t, err)
	tasks, err := store.DueSchedules(ctx, time.Now())
	requireOK(t, err)
	requireOK(t, store.LinkScheduledJob(ctx, tasks, "saved-job"))
	requireOK(t, store.Close())
	store, err = Open(path)
	requireOK(t, err)
	defer store.Close()
	tasks, err = store.DueSchedules(ctx, time.Now())
	requireOK(t, err)
	requireOK(t, store.FinishSchedules(ctx, tasks, 0))
	requireOK(t, store.FinishSchedules(ctx, tasks, 0))
	runs, err := store.CronRuns(ctx)
	requireOK(t, err)
	if len(runs) != 1 || runs[0].Queued != 1 || runs[0].JobIDs[0] != "saved-job" {
		t.Fatal(runs)
	}
}
