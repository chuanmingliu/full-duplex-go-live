package dreaming

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type Settings struct {
	Revision          int        `json:"revision"`
	Enabled           bool       `json:"enabled"`
	AfterHangup       bool       `json:"after_hangup"`
	Schedules         []CronTask `json:"schedules"`
	IdleSeconds       int        `json:"idle_seconds"`
	IncludeSimulation bool       `json:"include_simulation"`
	Since             time.Time  `json:"since"`
}

type Event struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	Stage  string    `json:"stage"`
	Detail string    `json:"detail"`
}

type JobSummary struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	NotBefore   time.Time `json:"not_before"`
	Error       string    `json:"error,omitempty"`
	CallIDs     []string  `json:"call_ids"`
	Events      []Event   `json:"events"`
	Lessons     []Lesson  `json:"lessons,omitempty"`
	LessonCount int       `json:"lesson_count"`
}

func (s *Store) initLifecycle() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS dream_settings(id INTEGER PRIMARY KEY CHECK(id=1),body TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS dream_events(id INTEGER PRIMARY KEY AUTOINCREMENT, dream_id TEXT NOT NULL REFERENCES dreams(id), at TEXT NOT NULL, stage TEXT NOT NULL, detail TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS automatic_calls(id TEXT PRIMARY KEY, closed_at TEXT NOT NULL, status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', dream_id TEXT NOT NULL DEFAULT '');
 CREATE INDEX IF NOT EXISTS dream_events_job ON dream_events(dream_id,id);
 CREATE INDEX IF NOT EXISTS lessons_job ON lessons(dream_id);
 CREATE TABLE IF NOT EXISTS cron_tasks(id TEXT PRIMARY KEY,definition TEXT NOT NULL,next_run INTEGER NOT NULL,pending_since INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS cron_dispatch_jobs(task_id TEXT NOT NULL,scheduled_at INTEGER NOT NULL,job_id TEXT NOT NULL,PRIMARY KEY(task_id,scheduled_at,job_id));
 CREATE TABLE IF NOT EXISTS cron_runs(id INTEGER PRIMARY KEY AUTOINCREMENT,task_id TEXT NOT NULL,name TEXT NOT NULL,scheduled_at INTEGER NOT NULL,dispatched_at INTEGER NOT NULL,jobs TEXT NOT NULL,failures INTEGER NOT NULL);`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO dream_events(dream_id,at,stage,detail) SELECT id,?,'recovered','Interrupted review returned to the queue after restart' FROM dreams WHERE status='running'; UPDATE dreams SET status='pending' WHERE status='running';`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	body, _ := json.Marshal(Settings{Revision: 1, Enabled: true, AfterHangup: true, Schedules: []CronTask{}, IdleSeconds: 30, IncludeSimulation: true, Since: time.Now().UTC()})
	_, err = s.db.Exec(`INSERT INTO dream_settings(id,body) VALUES(1,?) ON CONFLICT(id) DO NOTHING`, string(body))
	return err
}
func (s *Store) Settings(ctx context.Context) (Settings, error) {
	settings := Settings{AfterHangup: true, Schedules: []CronTask{}}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT body FROM dream_settings WHERE id=1`).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &settings)
	}
	return settings, err
}
func (s *Store) SaveSettings(ctx context.Context, settings Settings) (Settings, error) {
	if err := settings.Validate(); err != nil {
		return Settings{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Settings{}, err
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT body FROM dream_settings WHERE id=1`).Scan(&raw); err != nil {
		return Settings{}, err
	}
	current := Settings{AfterHangup: true}
	if err = json.Unmarshal([]byte(raw), &current); err != nil {
		return Settings{}, err
	}
	if current.Revision != settings.Revision {
		return Settings{}, ErrConflict
	}
	settings.Since = current.Since
	settings.Revision++
	body, _ := json.Marshal(settings)
	if _, err = tx.ExecContext(ctx, `UPDATE dream_settings SET body=? WHERE id=1`, string(body)); err != nil {
		return Settings{}, err
	}
	if err = syncSchedules(ctx, tx, settings.Schedules, time.Now()); err != nil {
		return Settings{}, err
	}
	if err = tx.Commit(); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

type eventWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func recordEvent(ctx context.Context, writer eventWriter, id, stage, detail string) error {
	_, err := writer.ExecContext(ctx, `INSERT INTO dream_events(dream_id,at,stage,detail) VALUES(?,?,?,?)`, id, time.Now().UTC().Format(time.RFC3339Nano), stage, detail)
	return err
}

func (s *Store) Record(ctx context.Context, id, stage, detail string) error {
	return recordEvent(ctx, s.db, id, stage, detail)
}

type progressKey struct{}

func progress(ctx context.Context, stage, detail string) error {
	if f, ok := ctx.Value(progressKey{}).(func(string, string) error); ok {
		return f(stage, detail)
	}
	return nil
}
func (s *Store) JobSummaries(ctx context.Context) ([]JobSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM dreams ORDER BY not_before DESC,id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}

	out := []JobSummary{}
	for _, id := range ids {
		summary, err := s.jobSummary(ctx, id, false)
		if err != nil {
			return nil, err
		}
		out = append(out, summary)
	}
	return out, nil
}

func (s *Store) jobSummary(ctx context.Context, id string, detail bool) (JobSummary, error) {

	summary := JobSummary{ID: id, Events: []Event{}, Lessons: []Lesson{}}
	var raw string
	var ms int64
	err := s.db.QueryRowContext(ctx, `SELECT status,not_before,error,request FROM dreams WHERE id=?`, id).Scan(&summary.Status, &ms, &summary.Error, &raw)
	if err != nil {
		return JobSummary{}, err
	}
	var request DreamRequest
	if err = json.Unmarshal([]byte(raw), &request); err != nil {
		return JobSummary{}, err
	}
	summary.CallIDs = request.EpisodeIDs
	summary.NotBefore = time.UnixMilli(ms).UTC()
	eventLimit := 1
	if detail {
		eventLimit = 100
	}
	events, err := s.db.QueryContext(ctx, `SELECT id,at,stage,detail FROM (SELECT id,at,stage,detail FROM dream_events WHERE dream_id=? ORDER BY id DESC LIMIT ?) ORDER BY id`, id, eventLimit)
	if err != nil {
		return JobSummary{}, err
	}
	for events.Next() {
		var e Event
		var at string
		if err = events.Scan(&e.ID, &at, &e.Stage, &e.Detail); err != nil {
			break
		}
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		summary.Events = append(summary.Events, e)
	}
	rowErr := events.Err()
	events.Close()
	if err != nil {
		return JobSummary{}, err
	}
	if rowErr != nil {
		return JobSummary{}, rowErr
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM lessons WHERE dream_id=?`, id).Scan(&summary.LessonCount); err != nil {
		return JobSummary{}, err
	}
	if !detail {
		return summary, nil
	}
	lessons, err := s.db.QueryContext(ctx, `SELECT id,body FROM lessons WHERE dream_id=? ORDER BY id`, id)
	if err != nil {
		return JobSummary{}, err
	}
	for lessons.Next() {
		l := Lesson{DreamID: id}
		var raw string
		if err = lessons.Scan(&l.ID, &raw); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &l.Proposal); err != nil {
			break
		}
		summary.Lessons = append(summary.Lessons, l)
	}
	rowErr = lessons.Err()
	lessons.Close()
	if err != nil {
		return JobSummary{}, err
	}
	if rowErr != nil {
		return JobSummary{}, rowErr
	}
	return summary, nil
}

func (s *Store) JobDetail(ctx context.Context, id string) (JobSummary, error) {
	return s.jobSummary(ctx, id, true)
}

func (settings Settings) Validate() error {
	if settings.IdleSeconds < 0 || settings.IdleSeconds > 3600 {
		return errors.New("Idle delay must be between 0 and 3600 seconds")
	}
	return validateSchedules(settings.Schedules)
}
