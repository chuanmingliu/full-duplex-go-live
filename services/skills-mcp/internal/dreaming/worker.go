package dreaming

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Reviewer interface {
	Review(context.Context, Snapshot) ([]Proposal, error)
}

func lessonID(job string, index int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", job, index)))
	return fmt.Sprintf("memory-%x", sum[:12])
}

// Run processes a single queue on this service instance. The explicit not_before
// deadline is an owner-selected leisure window, not an inference from MCP traffic.
func (s *Store) Run(ctx context.Context, reviewer Reviewer, report func(error), allow func(context.Context) bool) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if allow != nil && !allow(ctx) {
			continue
		}
		if err := s.stepWhileIdle(ctx, reviewer, allow); err != nil && ctx.Err() == nil && report != nil {
			report(err)
		}
	}
}

// A new call or unknown health cancels the review context and returns the job
// to pending. Cancellation is best effort at the remote provider boundary.
func (s *Store) stepWhileIdle(ctx context.Context, reviewer Reviewer, allow func(context.Context) bool) error {
	reviewCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	monitorDone := make(chan struct{})
	if allow != nil {
		go func() {
			defer close(monitorDone)
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-reviewCtx.Done():
					return
				case <-done:
					return
				case <-ticker.C:
					if !allow(reviewCtx) {
						cancel()
						return
					}
				}
			}
		}()
	}
	if allow == nil {
		close(monitorDone)
	}
	err := s.Step(reviewCtx, reviewer, time.Now())
	close(done)
	cancel()
	<-monitorDone
	return err
}

func (s *Store) Step(ctx context.Context, reviewer Reviewer, now time.Time) (result error) {
	var id string
	err := s.db.QueryRowContext(ctx, `UPDATE dreams SET status='running' WHERE id=(SELECT id FROM dreams WHERE status='pending' AND not_before<=? ORDER BY not_before,id LIMIT 1) RETURNING id`, now.UnixMilli()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		if result == nil {
			return
		}
		recovery, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = s.db.ExecContext(recovery, `UPDATE dreams SET status='pending',error='Review interrupted before completion' WHERE id=? AND status='running'`, id)
	}()
	j, err := s.Job(ctx, id)
	if err != nil {
		return err
	}
	if err = s.Record(ctx, id, "running", "Background review started"); err != nil {
		return err
	}
	ctx = context.WithValue(ctx, progressKey{}, func(stage, detail string) error { return s.Record(ctx, id, stage, detail) })
	reviewCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	proposals, reviewErr := reviewer.Review(reviewCtx, j.Snapshot)
	cancel()
	reason := "provider review failed; check endpoint configuration and retry"
	if reviewErr == nil {
		reviewErr = ctx.Err()
	}
	if reviewErr == nil {
		if reviewErr = validateProposals(proposals, j.Snapshot); reviewErr != nil {
			reason = "validation failed: " + reviewErr.Error()
		}
	}
	if reviewErr == nil {
		reviewErr = progress(ctx, "validated", "Evidence references and retrieval cases passed")
	}
	if reviewErr == nil {
		reviewErr = s.finish(ctx, j, proposals)
		reason = "could not commit dream result; retry after storage recovery"
	}
	if reviewErr == nil {
		return nil
	}
	// Persist only a category; provider error bodies can contain private source data.
	status := "failed"
	if ctx.Err() != nil {
		status, reason = "pending", ""
	}
	saveCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	stage := "failed"
	if status == "pending" {
		stage = "deferred"
		reason = "Review interrupted; waiting for an enabled, idle worker"
	}
	_ = s.Record(saveCtx, id, stage, reason)
	_, err = s.db.ExecContext(saveCtx, `UPDATE dreams SET status=?,error=? WHERE id=?`, status, reason, id)
	if err != nil {
		return err
	}
	return errors.New("dream review failed")
}
