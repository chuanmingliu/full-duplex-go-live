package dreaming

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type SourceCall struct {
	ID       string    `json:"id"`
	ClosedAt time.Time `json:"closed_at"`
	Mode     string    `json:"mode"`
	Ready    bool      `json:"ready"`
}
type AutomaticCall struct {
	ID       string    `json:"id"`
	ClosedAt time.Time `json:"closed_at"`
	Status   string    `json:"status"`
	Error    string    `json:"error,omitempty"`
	DreamID  string    `json:"dream_id,omitempty"`
}
type CollectorState struct {
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}

// Automatic uses completed, archived calls from the Journal. Since is persisted
// on first setup, so enabling this feature never silently replays old archives.
type Automatic struct {
	Store      *Store
	JournalURL string
	Activity   *Activity
	Client     *http.Client
	mu         sync.Mutex
	state      CollectorState
}

func (a *Automatic) State() CollectorState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}
func (a *Automatic) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		a.Collect(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (a *Automatic) Collect(ctx context.Context) {
	err := a.collect(ctx)
	state := CollectorState{CheckedAt: time.Now().UTC()}
	if err != nil {
		state.Error = err.Error()
	}
	a.mu.Lock()
	a.state = state
	a.mu.Unlock()
}
func (a *Automatic) collect(ctx context.Context) error {
	settings, err := a.Store.Settings(ctx)
	if err != nil {
		return fmt.Errorf("Cannot read automatic dreaming settings")
	}
	if !settings.Enabled {
		return nil
	}
	if a.Activity.Check(ctx).Status != "idle" {
		return nil
	}
	if a.JournalURL == "" {
		return fmt.Errorf("Configure GOLIVE_DREAM_JOURNAL_URL")
	}
	for offset := 0; ; offset += 100 {
		var page struct {
			Calls []SourceCall `json:"calls"`
			More  bool         `json:"more"`
		}
		endpoint := a.JournalURL + "/api/dreaming/source?since=" + url.QueryEscape(settings.Since.Format(time.RFC3339Nano)) + fmt.Sprintf("&offset=%d", offset)
		if err = ReadJSON(ctx, a.Client, endpoint, &page); err != nil {
			return fmt.Errorf("Completed-call source unavailable")
		}
		if len(page.Calls) > 100 || (page.More && len(page.Calls) == 0) {
			return fmt.Errorf("Invalid completed-call page")
		}
		for _, call := range page.Calls {
			if !identifier.MatchString(call.ID) || call.ClosedAt.Before(settings.Since) {
				continue
			}
			var status string
			_ = a.Store.db.QueryRowContext(ctx, `SELECT status FROM automatic_calls WHERE id=?`, call.ID).Scan(&status)
			if status == "queued" {
				continue
			}
			status = "waiting_archive"
			detail := ""
			dreamID := ""
			switch {
			case call.Mode == "simulation" && !settings.IncludeSimulation:
				status = "excluded"
			case call.Ready:
				dreamID, err = a.capture(ctx, call)
				status = "queued"
				if err != nil {
					status = "capture_failed"
					detail = err.Error()
				}
			}
			_, err = a.Store.db.ExecContext(ctx, `INSERT INTO automatic_calls(id,closed_at,status,error,dream_id) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,error=excluded.error,dream_id=excluded.dream_id`, call.ID, call.ClosedAt.Format(time.RFC3339Nano), status, detail, dreamID)
			if err != nil {
				return fmt.Errorf("Cannot save automatic capture status")
			}
		}
		if !page.More {
			return nil
		}
	}
}
func (a *Automatic) capture(ctx context.Context, call SourceCall) (string, error) {
	hash := sha256.Sum256([]byte(call.ID))
	id := fmt.Sprintf("auto-%x", hash[:16])
	if _, err := a.Store.Job(ctx, id); err == nil {
		return id, nil
	}
	var source struct {
		ArchiveJSON string   `json:"archive_json"`
		AudioRefs   []string `json:"audio_refs"`
		Feedback    *struct {
			Note   string `json:"note"`
			Rating int    `json:"rating"`
		} `json:"feedback"`
	}
	if err := ReadJSON(ctx, a.Client, a.JournalURL+"/api/dreaming/source/"+url.PathEscape(call.ID), &source); err != nil {
		return "", fmt.Errorf("Archived evidence unavailable; will retry")
	}
	var episode Episode
	var saved string
	err := a.Store.db.QueryRowContext(ctx, `SELECT body FROM episodes WHERE id=?`, call.ID).Scan(&saved)
	switch {
	case err == nil:
		err = json.Unmarshal([]byte(saved), &episode)
	case errors.Is(err, sql.ErrNoRows):
		episode, err = ProjectArchive(json.RawMessage(source.ArchiveJSON), call.Mode, source.AudioRefs)
	}
	if err != nil || episode.ID != call.ID {
		return "", fmt.Errorf("Archived evidence is invalid or exceeds the review limits")
	}
	if saved == "" {
		if err = a.Store.Import(ctx, episode); err != nil {
			return "", fmt.Errorf("Cannot import immutable call evidence")
		}
	}
	if source.Feedback != nil && (source.Feedback.Note != "" || source.Feedback.Rating != 0) {
		text := fmt.Sprintf("Owner rating: %d/5. %s", source.Feedback.Rating, source.Feedback.Note)
		hash := sha256.Sum256([]byte(text))
		if err = a.Store.Feedback(ctx, Feedback{EpisodeID: call.ID, ID: fmt.Sprintf("owner-%x", hash[:8]), Text: text}); err != nil {
			return "", fmt.Errorf("Cannot capture owner feedback")
		}
	}
	// Enabling automatic review is standing permission for completed new calls.
	if err = a.Store.Release(ctx, episode.ID); err != nil {
		return "", fmt.Errorf("Cannot release completed call")
	}
	_, err = a.Store.Queue(ctx, DreamRequest{ID: id, EpisodeIDs: []string{call.ID}})
	if err != nil {
		return "", fmt.Errorf("Cannot queue completed call")
	}
	return id, nil
}
func (s *Store) AutomaticCalls(ctx context.Context) ([]AutomaticCall, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,closed_at,status,error,dream_id FROM automatic_calls ORDER BY closed_at DESC,id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AutomaticCall{}
	for rows.Next() {
		var c AutomaticCall
		var at string
		if err = rows.Scan(&c.ID, &at, &c.Status, &c.Error, &c.DreamID); err != nil {
			return nil, err
		}
		c.ClosedAt, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ProjectArchive preserves original JSON and stable evidence IDs, as the manual
// importer does. Audio stays in the local archive.
func ProjectArchive(raw json.RawMessage, mode string, audio []string) (Episode, error) {
	var call struct {
		ID         string            `json:"id"`
		EndedAt    time.Time         `json:"ended_at"`
		Transcript []json.RawMessage `json:"transcript"`
		Turns      []json.RawMessage `json:"turns"`
		Quotation  []json.RawMessage `json:"quotation"`
		Warnings   []json.RawMessage `json:"warnings"`
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		return Episode{}, err
	}
	e := Episode{ID: call.ID, Mode: mode, ClosedAt: call.EndedAt, AudioRefs: audio, ArchiveJSON: string(raw), ArchiveSHA256: fmt.Sprintf("%x", sha256.Sum256(raw))}
	for i, line := range call.Transcript {
		var who struct {
			Who string `json:"who"`
		}
		if err := json.Unmarshal(line, &who); err != nil {
			return e, err
		}
		kind := map[string]string{"caller": "user", "assistant": "assistant"}[who.Who]
		if kind == "" {
			continue
		}
		e.Evidence = append(e.Evidence, Evidence{ID: fmt.Sprintf("transcript-%d", i), Kind: kind, Text: string(line)})
	}
	for _, field := range []struct {
		name, kind string
		items      []json.RawMessage
	}{{"turns", "timing", call.Turns}, {"quotation", "tool", call.Quotation}, {"warnings", "timing", call.Warnings}} {
		for i, item := range field.items {
			e.Evidence = append(e.Evidence, Evidence{ID: fmt.Sprintf("%s-%d", field.name, i), Kind: field.kind, Text: string(item)})
		}
	}
	return e, e.validate()
}

func (a *Automatic) CanReview(ctx context.Context) bool {
	settings, err := a.Store.Settings(ctx)
	if err != nil {
		return false
	}
	// The automatic toggle pauses automatic jobs as well as new capture.
	state := a.Activity.Check(ctx)
	return settings.Enabled && state.Status == "idle" && time.Since(state.IdleSince) >= time.Duration(settings.IdleSeconds)*time.Second
}
func NewAutomatic(store *Store, journal string, engines []string) (*Automatic, error) {
	if journal != "" {
		if err := ValidateServiceURL(journal); err != nil {
			return nil, err
		}
	}
	for _, engine := range engines {
		if err := ValidateServiceURL(engine); err != nil {
			return nil, err
		}
	}
	client := LocalClient()
	return &Automatic{Store: store, JournalURL: strings.TrimRight(journal, "/"), Client: client, Activity: &Activity{URLs: engines, Client: client}}, nil
}
