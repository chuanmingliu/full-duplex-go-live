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
func (s *Store) Run(ctx context.Context, reviewer Reviewer, report func(error)) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := s.Step(ctx, reviewer, time.Now()); err != nil && ctx.Err() == nil && report != nil {
			report(err)
		}
	}
}

func (s *Store) Step(ctx context.Context, reviewer Reviewer, now time.Time) error {
	var id string
	err := s.db.QueryRowContext(ctx, `UPDATE dreams SET status='running' WHERE id=(SELECT id FROM dreams WHERE status='pending' AND not_before<=? ORDER BY not_before,id LIMIT 1) RETURNING id`, now.UnixMilli()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	j, err := s.Job(ctx, id)
	if err != nil {
		return err
	}
	reviewCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	proposals, reviewErr := reviewer.Review(reviewCtx, j.Snapshot)
	cancel()
	reason := "provider review failed; check endpoint configuration and retry"
	if reviewErr == nil && len(proposals) > 8 {
		reviewErr = errors.New("too many lessons")
	}
	if reviewErr != nil && len(proposals) > 8 {
		reason = "validation failed: too many lessons"
	}
	if reviewErr == nil {
		for _, p := range proposals {
			if reviewErr = p.Validate(j.Snapshot); reviewErr != nil {
				reason = "validation failed: " + reviewErr.Error()
				break
			}
		}
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
	_, err = s.db.ExecContext(saveCtx, `UPDATE dreams SET status=?,error=? WHERE id=?`, status, reason, id)
	if err != nil {
		return err
	}
	return errors.New("dream review failed")
}
