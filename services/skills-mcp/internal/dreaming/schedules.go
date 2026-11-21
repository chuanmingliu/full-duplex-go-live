package dreaming

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

// CronTask is a review schedule, using standard five-field cron expressions.
type CronTask struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Expression string `json:"expression"`
	Timezone   string `json:"timezone"`
}
type CronTaskState struct {
	CronTask
	NextRun      time.Time `json:"next_run"`
	PendingSince time.Time `json:"pending_since"`
}
type CronRun struct {
	ID           int64     `json:"id"`
	TaskID       string    `json:"task_id"`
	Name         string    `json:"name"`
	ScheduledAt  time.Time `json:"scheduled_at"`
	DispatchedAt time.Time `json:"dispatched_at"`
	JobIDs       []string  `json:"job_ids"`
	Queued       int       `json:"queued"`
	Failures     int       `json:"failures"`
}

func (t CronTask) schedule() (cron.Schedule, error) {
	if !identifier.MatchString(t.ID) || !validText(t.Name, 100) || len(t.Expression) > 100 || len(t.Timezone) > 100 || len(strings.Fields(t.Expression)) != 5 {
		return nil, errors.New("cron tasks require an ID, name, five-field expression and timezone")
	}
	location, err := time.LoadLocation(t.Timezone)
	if err != nil || t.Timezone == "" || t.Timezone == "Local" {
		return nil, errors.New("invalid cron timezone")
	}
	schedule, err := cron.ParseStandard("CRON_TZ=" + location.String() + " " + t.Expression)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression for %s", t.ID)
	}
	if schedule.Next(time.Now()).IsZero() {
		return nil, errors.New("cron expression has no upcoming occurrence")
	}
	return schedule, nil
}
func validateSchedules(tasks []CronTask) error {
	if len(tasks) > 16 {
		return errors.New("at most 16 cron tasks are supported")
	}
	seen := map[string]bool{}
	for _, task := range tasks {
		if seen[task.ID] {
			return errors.New("duplicate cron task ID")
		}
		seen[task.ID] = true
		if _, err := task.schedule(); err != nil {
			return err
		}
	}
	return nil
}
func syncSchedules(ctx context.Context, tx *sql.Tx, tasks []CronTask, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM cron_tasks`)
	if err != nil {
		return err
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
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	wanted := map[string]bool{}
	for _, task := range tasks {
		wanted[task.ID] = true
		schedule, err := task.schedule()
		if err != nil {
			return err
		}
		next := int64(0)
		if task.Enabled {
			next = schedule.Next(now).UnixMilli()
		}
		raw, _ := json.Marshal(task)
		_, err = tx.ExecContext(ctx, `INSERT INTO cron_tasks(id,definition,next_run,pending_since) VALUES(?,?,?,0)
   ON CONFLICT(id) DO UPDATE SET definition=excluded.definition,next_run=excluded.next_run,pending_since=0 WHERE cron_tasks.definition!=excluded.definition`, task.ID, string(raw), next)
		if err != nil {
			return err
		}
	}
	for _, id := range ids {
		if !wanted[id] {
			if _, err = tx.ExecContext(ctx, `DELETE FROM cron_tasks WHERE id=?`, id); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) CronTasks(ctx context.Context) ([]CronTaskState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT definition,next_run,pending_since FROM cron_tasks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []CronTaskState{}
	for rows.Next() {
		var task CronTaskState
		var raw string
		var next, pending int64
		if err = rows.Scan(&raw, &next, &pending); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &task.CronTask); err != nil {
			return nil, err
		}
		if next > 0 {
			task.NextRun = time.UnixMilli(next).UTC()
		}
		if pending > 0 {
			task.PendingSince = time.UnixMilli(pending).UTC()
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// Missed intervals coalesce into one pending occurrence. Its original due time
// survives busy calls, outages and restarts until successful dispatch.
func (s *Store) DueSchedules(ctx context.Context, now time.Time) ([]CronTaskState, error) {
	tasks, err := s.CronTasks(ctx)
	if err != nil {
		return nil, err
	}
	due := []CronTaskState{}
	for _, task := range tasks {
		if !task.Enabled {
			continue
		}
		if !task.NextRun.After(now) {
			schedule, err := task.schedule()
			if err != nil {
				return nil, err
			}
			next := schedule.Next(now)
			pending := task.PendingSince
			if pending.IsZero() {
				pending = task.NextRun
			}
			raw, _ := json.Marshal(task.CronTask)
			result, err := s.db.ExecContext(ctx, `UPDATE cron_tasks SET next_run=?,pending_since=? WHERE id=? AND definition=?`, next.UnixMilli(), pending.UnixMilli(), task.ID, string(raw))
			if err != nil {
				return nil, err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return nil, err
			}
			if count == 0 {
				continue
			}
			task.NextRun = next
			task.PendingSince = pending
		}
		if !task.PendingSince.IsZero() {
			due = append(due, task)
		}
	}
	return due, nil
}
func (s *Store) FinishSchedules(ctx context.Context, tasks []CronTaskState, failures int) error {
	if len(tasks) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, task := range tasks {
		definition, _ := json.Marshal(task.CronTask)
		result, err := tx.ExecContext(ctx, `UPDATE cron_tasks SET pending_since=0 WHERE id=? AND definition=? AND pending_since=?`, task.ID, string(definition), task.PendingSince.UnixMilli())
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			continue
		}
		rows, err := tx.QueryContext(ctx, `SELECT job_id FROM cron_dispatch_jobs WHERE task_id=? AND scheduled_at=? ORDER BY job_id`, task.ID, task.PendingSince.UnixMilli())
		if err != nil {
			return err
		}
		jobs := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			jobs = append(jobs, id)
		}
		rowErr := rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if rowErr != nil {
			return rowErr
		}
		rawJobs, _ := json.Marshal(jobs)
		_, err = tx.ExecContext(ctx, `INSERT INTO cron_runs(task_id,name,scheduled_at,dispatched_at,jobs,failures) VALUES(?,?,?,?,?,?)`, task.ID, task.Name, task.PendingSince.UnixMilli(), time.Now().UnixMilli(), string(rawJobs), failures)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) CronRuns(ctx context.Context) ([]CronRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,name,scheduled_at,dispatched_at,jobs,failures FROM cron_runs ORDER BY id DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []CronRun{}
	for rows.Next() {
		var run CronRun
		var scheduled, dispatched int64
		var raw string
		if err = rows.Scan(&run.ID, &run.TaskID, &run.Name, &scheduled, &dispatched, &raw, &run.Failures); err != nil {
			return nil, err
		}
		run.ScheduledAt = time.UnixMilli(scheduled).UTC()
		run.DispatchedAt = time.UnixMilli(dispatched).UTC()
		if err = json.Unmarshal([]byte(raw), &run.JobIDs); err != nil {
			return nil, err
		}
		run.Queued = len(run.JobIDs)
		if len(run.JobIDs) > 100 {
			run.JobIDs = run.JobIDs[:100]
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// Link before acknowledging capture, so a crash resumes the same dispatch.
func (s *Store) LinkScheduledJob(ctx context.Context, tasks []CronTaskState, id string) error {
	for _, task := range tasks {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO cron_dispatch_jobs(task_id,scheduled_at,job_id) VALUES(?,?,?) ON CONFLICT DO NOTHING`, task.ID, task.PendingSince.UnixMilli(), id); err != nil {
			return err
		}
	}
	return nil
}
