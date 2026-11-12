package dreaming

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type reviewFunc func(context.Context, Snapshot) ([]Proposal, error)

func (f reviewFunc) Review(c context.Context, s Snapshot) ([]Proposal, error) { return f(c, s) }

func episode() Episode {
	return Episode{ID: "call1", Mode: "real", ClosedAt: time.Now().Add(-time.Minute).UTC(), Evidence: []Evidence{{ID: "turn1", Kind: "user", Text: "done; please repeat the price"}}}
}
func proposal() Proposal {
	return Proposal{Title: "Repeat the requested detail", Observation: "The user asked to repeat the price.", Lesson: "When asked to repeat, repeat only the requested detail from the current verified tool result; ask for clarification if unclear.", Evidence: []Reference{{"call1", "turn1"}}, Match: []string{"repeat.*price"}, Cases: []RoutingCase{{"repeat the price", true}, {"hello", false}}}
}
func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func requireOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestEvidenceReleaseIdempotencyAndSnapshot(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	e := episode()
	requireOK(t, s.Import(ctx, e))
	requireOK(t, s.Import(ctx, e))
	e.Evidence[0].Text = "changed"
	if !errors.Is(s.Import(ctx, e), ErrConflict) {
		t.Fatal("overwrote source")
	}
	req := DreamRequest{ID: "dream1", EpisodeIDs: []string{"call1"}}
	if _, err := s.Queue(ctx, req); !errors.Is(err, ErrIneligible) {
		t.Fatal("transcript released real call", err)
	}
	requireOK(t, s.Release(ctx, "call1"))
	f := Feedback{EpisodeID: "call1", ID: "owner1", Text: "You repeated the entire quote, not just its price."}
	requireOK(t, s.Feedback(ctx, f))
	requireOK(t, s.Feedback(ctx, f))
	f.Text = "overwrite"
	if !errors.Is(s.Feedback(ctx, f), ErrConflict) {
		t.Fatal("overwrote feedback")
	}
	f.ID = "turn1"
	if !errors.Is(s.Feedback(ctx, f), ErrConflict) {
		t.Fatal("source collision")
	}
	j, err := s.Queue(ctx, req)
	requireOK(t, err)
	if len(j.Snapshot.Episodes[0].Evidence) != 2 || j.Snapshot.Episodes[0].Evidence[0].Text != "done; please repeat the price" {
		t.Fatal(j)
	}
	requireOK(t, s.Feedback(ctx, Feedback{EpisodeID: "call1", ID: "owner2", Text: "additional feedback"}))
	j, err = s.Queue(ctx, req)
	requireOK(t, err)
	if len(j.Snapshot.Episodes[0].Evidence) != 2 {
		t.Fatal("queued evidence changed")
	}
	req.EpisodeIDs = []string{"missing"}
	if _, err = s.Queue(ctx, req); !errors.Is(err, ErrConflict) {
		t.Fatal("reused request ID", err)
	}
}

func TestWorkerScheduleFailureRetryAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.sqlite")
	s, err := Open(path)
	requireOK(t, err)
	e := episode()
	e.Mode = "simulation"
	requireOK(t, s.Import(ctx, e))
	future := time.Now().Add(time.Hour)
	_, err = s.Queue(ctx, DreamRequest{ID: "dream1", EpisodeIDs: []string{"call1"}, NotBefore: future})
	requireOK(t, err)
	calls := 0
	bad := reviewFunc(func(context.Context, Snapshot) ([]Proposal, error) {
		calls++
		p := proposal()
		p.Evidence[0].EvidenceID = "invented"
		return []Proposal{proposal(), p}, nil
	})
	requireOK(t, s.Step(ctx, bad, time.Now()))
	if calls != 0 {
		t.Fatal("ran before leisure window")
	}
	if s.Step(ctx, bad, future.Add(time.Second)) == nil {
		t.Fatal("accepted invented evidence")
	}
	lessons, err := s.Lessons(ctx)
	requireOK(t, err)
	if len(lessons) != 0 {
		t.Fatal("partial dream committed")
	}
	j, err := s.Job(ctx, "dream1")
	requireOK(t, err)
	if j.Status != "failed" {
		t.Fatal(j)
	}
	requireOK(t, s.Retry(ctx, "dream1"))
	// Simulate process loss after claiming a job. Reopening restores the queue.
	_, err = s.db.Exec(`UPDATE dreams SET status='running' WHERE id='dream1'`)
	requireOK(t, err)
	requireOK(t, s.Close())
	s, err = Open(path)
	requireOK(t, err)
	defer s.Close()
	good := reviewFunc(func(_ context.Context, snap Snapshot) ([]Proposal, error) {
		if snap.Episodes[0].Mode != "simulation" {
			t.Fatal(snap)
		}
		return []Proposal{proposal(), proposal()}, nil
	})
	requireOK(t, s.Step(ctx, good, future.Add(time.Second)))
	lessons, err = s.Lessons(ctx)
	requireOK(t, err)
	if len(lessons) != 1 {
		t.Fatal("duplicate lessons", lessons)
	}
	requireOK(t, s.Step(ctx, bad, future.Add(time.Second)))
	if calls != 1 {
		t.Fatal("reran completed job")
	}
	j, err = s.Job(ctx, "dream1")
	requireOK(t, err)
	if j.Status != "completed" {
		t.Fatal(j)
	}
}

func TestValidationAndCancellation(t *testing.T) {
	snap := Snapshot{Episodes: []Episode{episode()}}
	for _, mutate := range []func(*Proposal){
		func(p *Proposal) { p.Match = []string{"["} },
		func(p *Proposal) { p.Match = []string{".*"} },
		func(p *Proposal) { p.Cases[1].Match = true },
		func(p *Proposal) { p.Cases[0].Match = false },
		func(p *Proposal) { p.Lesson = strings.Repeat("x", 3001) },
	} {
		p := proposal()
		mutate(&p)
		if p.Validate(snap) == nil {
			t.Fatal("invalid proposal accepted", p)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := openTest(t)
	e := episode()
	e.Mode = "simulation"
	requireOK(t, s.Import(ctx, e))
	_, err := s.Queue(ctx, DreamRequest{ID: "cancel", EpisodeIDs: []string{"call1"}})
	requireOK(t, err)
	_ = s.Step(ctx, reviewFunc(func(c context.Context, _ Snapshot) ([]Proposal, error) { cancel(); return nil, c.Err() }), time.Now().Add(time.Second))
	j, err := s.Job(context.Background(), "cancel")
	requireOK(t, err)
	if j.Status != "pending" {
		t.Fatal("lost interrupted work", j)
	}
}

func TestExclusivePrivateDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.sqlite")
	s, err := Open(path)
	requireOK(t, err)
	if second, err := Open(path); err == nil {
		second.Close()
		t.Fatal("allowed concurrent service to reset active dreams")
	}
	info, err := os.Stat(path)
	requireOK(t, err)
	if info.Mode().Perm() != 0600 {
		t.Fatal("database is not private", info.Mode())
	}
	requireOK(t, s.Close())
	s, err = Open(path)
	requireOK(t, err)
	requireOK(t, s.Close())
}
