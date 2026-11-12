package dreaming

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	lock *os.File
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("memory database is already open by another service")
	}
	opened := false
	defer func() {
		if !opened {
			lock.Close()
		}
	}()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	err = f.Chmod(0600)
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS episodes(id TEXT PRIMARY KEY, body TEXT NOT NULL, released INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS feedback(episode_id TEXT NOT NULL REFERENCES episodes(id), id TEXT NOT NULL, text TEXT NOT NULL, PRIMARY KEY(episode_id,id));
CREATE TABLE IF NOT EXISTS dreams(id TEXT PRIMARY KEY, request TEXT NOT NULL, snapshot TEXT NOT NULL, not_before INTEGER NOT NULL, status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS lessons(id TEXT PRIMARY KEY, dream_id TEXT NOT NULL REFERENCES dreams(id), body TEXT NOT NULL);
UPDATE dreams SET status='pending' WHERE status='running';`)
	if err != nil {
		db.Close()
		return nil, err
	}
	opened = true
	return &Store{db: db, lock: lock}, nil
}

func (s *Store) Close() error {
	err := s.db.Close()
	lockErr := s.lock.Close()
	return errors.Join(err, lockErr)
}

func (s *Store) Import(ctx context.Context, e Episode) error {
	if err := e.validate(); err != nil {
		return err
	}
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(body) > 512<<10 {
		return errors.New("episode exceeds 512 KiB")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO episodes(id,body,released) VALUES(?,?,?) ON CONFLICT(id) DO NOTHING`, e.ID, string(body), e.Mode == "simulation")
	if err != nil {
		return err
	}
	var saved string
	if err = s.db.QueryRowContext(ctx, `SELECT body FROM episodes WHERE id=?`, e.ID).Scan(&saved); err != nil {
		return err
	}
	if saved != string(body) {
		return ErrConflict
	}
	return nil
}

// Release is called only by the authenticated owner after the call is done.
// A transcript containing the word "done" has no effect on this flag.
func (s *Store) Release(ctx context.Context, id string) error {
	r, err := s.db.ExecContext(ctx, `UPDATE episodes SET released=1 WHERE id=?`, id)
	return changed(r, err)
}

func changed(r sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) Feedback(ctx context.Context, f Feedback) error {
	if !identifier.MatchString(f.ID) || !validText(f.Text, 16000) {
		return errors.New("invalid feedback")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var body string
	if err = tx.QueryRowContext(ctx, `SELECT body FROM episodes WHERE id=?`, f.EpisodeID).Scan(&body); err != nil {
		return err
	}
	var e Episode
	if err = json.Unmarshal([]byte(body), &e); err != nil {
		return err
	}
	for _, v := range e.Evidence {
		if v.ID == f.ID {
			return ErrConflict
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO feedback(episode_id,id,text) VALUES(?,?,?) ON CONFLICT(episode_id,id) DO NOTHING`, f.EpisodeID, f.ID, f.Text)
	if err != nil {
		return err
	}
	var saved string
	if err = tx.QueryRowContext(ctx, `SELECT text FROM feedback WHERE episode_id=? AND id=?`, f.EpisodeID, f.ID).Scan(&saved); err != nil {
		return err
	}
	if saved != f.Text {
		return ErrConflict
	}
	return tx.Commit()
}

func (s *Store) Queue(ctx context.Context, req DreamRequest) (Job, error) {
	if !identifier.MatchString(req.ID) || len(req.EpisodeIDs) == 0 || len(req.EpisodeIDs) > 10 {
		return Job{}, errors.New("dream requires ID and 1–10 episodes")
	}
	raw, _ := json.Marshal(req)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT request FROM dreams WHERE id=?`, req.ID).Scan(&existing)
	if err == nil {
		if existing != string(raw) {
			return Job{}, ErrConflict
		}
		if err = tx.Commit(); err != nil {
			return Job{}, err
		}
		return s.Job(ctx, req.ID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Job{}, err
	}
	snapshot := Snapshot{Episodes: []Episode{}}
	seen := map[string]bool{}
	for _, id := range req.EpisodeIDs {
		if seen[id] {
			return Job{}, errors.New("duplicate episode ID")
		}
		seen[id] = true
		var body string
		var released bool
		if err = tx.QueryRowContext(ctx, `SELECT body,released FROM episodes WHERE id=?`, id).Scan(&body, &released); err != nil {
			return Job{}, err
		}
		if !released {
			return Job{}, ErrIneligible
		}
		var e Episode
		if err = json.Unmarshal([]byte(body), &e); err != nil {
			return Job{}, err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,text FROM feedback WHERE episode_id=? ORDER BY id`, id)
		if err != nil {
			return Job{}, err
		}
		for rows.Next() {
			v := Evidence{Kind: "feedback"}
			if err = rows.Scan(&v.ID, &v.Text); err != nil {
				rows.Close()
				return Job{}, err
			}
			e.Evidence = append(e.Evidence, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return Job{}, err
		}
		snapshot.Episodes = append(snapshot.Episodes, e)
	}
	body, _ := json.Marshal(snapshot)
	if len(body) > 512<<10 {
		return Job{}, errors.New("dream evidence exceeds 512 KiB")
	}
	at := req.NotBefore
	if at.IsZero() {
		at = time.Now()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO dreams(id,request,snapshot,not_before,status) VALUES(?,?,?,?,'pending')`, req.ID, string(raw), string(body), at.UnixMilli())
	if err != nil {
		return Job{}, err
	}
	if err = tx.Commit(); err != nil {
		return Job{}, err
	}
	return s.Job(ctx, req.ID)
}

func (s *Store) Job(ctx context.Context, id string) (Job, error) {
	j := Job{ID: id}
	var raw string
	var ms int64
	err := s.db.QueryRowContext(ctx, `SELECT status,snapshot,not_before,error FROM dreams WHERE id=?`, id).Scan(&j.Status, &raw, &ms, &j.Error)
	if err != nil {
		return Job{}, err
	}
	j.NotBefore = time.UnixMilli(ms).UTC()
	err = json.Unmarshal([]byte(raw), &j.Snapshot)
	return j, err
}

func (s *Store) Retry(ctx context.Context, id string) error {
	r, err := s.db.ExecContext(ctx, `UPDATE dreams SET status='pending',error='' WHERE id=? AND status='failed'`, id)
	return changed(r, err)
}

func (s *Store) Lessons(ctx context.Context) ([]Lesson, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,dream_id,body FROM lessons ORDER BY id LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Lesson{}
	for rows.Next() {
		var l Lesson
		var body string
		if err = rows.Scan(&l.ID, &l.DreamID, &body); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(body), &l.Proposal); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) Lesson(ctx context.Context, id string) (Lesson, error) {
	l := Lesson{ID: id}
	var body string
	err := s.db.QueryRowContext(ctx, `SELECT dream_id,body FROM lessons WHERE id=?`, id).Scan(&l.DreamID, &body)
	if err != nil {
		return l, err
	}
	err = json.Unmarshal([]byte(body), &l.Proposal)
	return l, err
}

// finish commits the complete result and status together; a failed proposal cannot
// leave a partially published dream. Runtime publication is a separate operation.
func (s *Store) finish(ctx context.Context, j Job, proposals []Proposal) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := map[string]bool{}
	for i, p := range proposals {
		key := strings.ToLower(strings.TrimSpace(p.Lesson))
		if seen[key] {
			continue
		}
		seen[key] = true
		body, _ := json.Marshal(p)
		id := lessonID(j.ID, i)
		if _, err = tx.ExecContext(ctx, `INSERT INTO lessons(id,dream_id,body) VALUES(?,?,?)`, id, j.ID, string(body)); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE dreams SET status='completed',error='' WHERE id=?`, j.ID); err != nil {
		return err
	}
	return tx.Commit()
}
