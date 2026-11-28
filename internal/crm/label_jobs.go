package crm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/labeljob"
)

type labelJobs struct {
	taskMu   sync.Mutex
	switchMu sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	wake     chan struct{}
	pending  map[int64]memoryRevision // reconstructed from the checksummed event log
}

func (s *Service) syncLabelSwitch() error {
	if s.cfg.LabelSpoolDir == "" {
		return nil
	}
	s.labelJobs.switchMu.Lock()
	defer s.labelJobs.switchMu.Unlock()
	task, err := s.configs.Get(labelConfigName, 0)
	if err != nil {
		return err
	}
	var control struct{ Enabled bool }
	if err = json.Unmarshal(task.Body, &control); err != nil {
		return err
	}
	version := 0
	if control.Enabled {
		version = task.Version
	}
	if err = os.MkdirAll(s.cfg.LabelSpoolDir, 0700); err != nil {
		return err
	}
	path := filepath.Join(s.cfg.LabelSpoolDir, labeljob.SwitchFile)
	data := []byte(strconv.Itoa(version))
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	return writeFileAtomic(path, data)
}

func (s *Service) startLabelJobs() {
	if s.cfg.LabelSpoolDir == "" && s.cfg.VikingURL == "" {
		return
	}
	j := &s.labelJobs
	ctx, cancel := context.WithCancel(context.Background())
	j.cancel, j.done, j.wake = cancel, make(chan struct{}), make(chan struct{}, 1)
	if err := s.syncLabelSwitch(); err != nil {
		s.log.Error("label switch publication failed", "err", err)
	}
	go func() {
		defer close(j.done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			s.runLabelJobs(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-j.wake:
			}
		}
	}()
}

func (s *Service) stopLabelJobs() {
	if j := &s.labelJobs; j.cancel != nil {
		j.cancel()
		<-j.done
	}
}

func (s *Service) runLabelJobs(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if err := s.syncLabelSwitch(); err != nil {
		s.log.Warn("label switch unavailable", "err", err)
		return
	}
	if !s.labelingEnabled() {
		return
	}
	if dir := s.cfg.LabelSpoolDir; dir != "" {
		jobs, _ := filepath.Glob(filepath.Join(dir, "session-*.json"))
		for _, path := range jobs {
			if ctx.Err() != nil {
				return
			}
			if err := s.processHangup(ctx, path); err != nil {
				s.log.Warn("hangup labeling pending", "job", filepath.Base(path), "err", err)
			}
		}
	}
	if s.cfg.VikingURL == "" {
		return
	}
	s.mu.Lock()
	batch := make([]memoryRevision, 0, len(s.labelJobs.pending))
	for _, rev := range s.labelJobs.pending {
		batch = append(batch, rev)
	}
	s.mu.Unlock()
	for _, rev := range batch {
		if ctx.Err() != nil || !s.labelingEnabled() {
			return
		}
		if err := s.archiveLabel(ctx, rev); err != nil {
			s.log.Warn("OpenViking archive pending", "revision", rev.Seq, "err", err)
		}
	}
}

func (s *Service) processHangup(ctx context.Context, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var job labeljob.Session
	if err = json.Unmarshal(raw, &job); err != nil {
		return err
	}
	if !leadKey.MatchString(job.ID) || job.CallID == "" || job.SchemaVersion < 1 {
		return errors.New("invalid closed session")
	}
	var owner string
	s.mu.Lock()
	for _, c := range s.calls {
		if c.OK && c.CallID == job.CallID && c.Lead != "" {
			if owner != "" && owner != c.Lead {
				s.mu.Unlock()
				return errors.New("ambiguous CRM ownership")
			}
			owner = c.Lead
		}
	}
	s.mu.Unlock()
	// The scheduler originates its calls directly and publishes accepted IDs
	// from its SQLite ledger in the CRM's private directory.
	mapping := filepath.Join(s.cfg.Dir, "scheduled-owners", fmt.Sprintf("%x", sha256.Sum256([]byte(job.CallID))))
	data, err := os.ReadFile(mapping)
	if err == nil {
		scheduled := string(data)
		if !leadKey.MatchString(scheduled) || owner != "" && owner != scheduled {
			return errors.New("conflicting scheduled ownership")
		}
		owner = scheduled
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if owner == "" {
		return errors.New("waiting for a recorded call owner")
	}
	in := labelInput{TranscriptID: job.ID, CallID: job.CallID, SchemaVersion: job.SchemaVersion}
	for _, turn := range job.Turns {
		in.Turns = append(in.Turns, labelTurn{Role: turn.Role, Content: turn.Content})
	}
	if _, err = s.processTranscript(ctx, owner, "hangup", in, raw, true); err != nil {
		return err
	}
	return os.Remove(path)
}

func archiveURI(rev memoryRevision) string {
	return fmt.Sprintf("viking://user/%s/resources/golive/%s/%d.json", rev.Labeling.User.ID, rev.Labeling.TranscriptID, rev.Seq)
}

func (s *Service) archiveLabel(ctx context.Context, rev memoryRevision) error {
	uri := archiveURI(rev)
	key := s.cfg.VikingAPIKey
	if s.cfg.VikingKeysDir != "" {
		raw, err := os.ReadFile(filepath.Join(s.cfg.VikingKeysDir, rev.Labeling.User.ID+".key"))
		if err != nil {
			return err
		}
		key = strings.TrimSpace(string(raw))
	}
	doc, err := json.Marshal(struct {
		Revision int64           `json:"revision"`
		Fields   json.RawMessage `json:"fields"`
		*labelReceipt
	}{rev.Seq, json.RawMessage(rev.Value), rev.Labeling})
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"uri": uri, "content": string(doc), "mode": "replace", "wait": true})
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(s.cfg.VikingURL, "/")+"/api/v1/content/write", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var ack struct {
		Status string
		Result struct{ URI string }
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&ack) != nil || ack.Status != "ok" || ack.Result.URI != uri {
		return fmt.Errorf("unconfirmed OpenViking write (HTTP %d)", resp.StatusCode)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ev, err := s.events.Append(Event{Type: "label.archived", Lead: rev.Labeling.User.ID, Ref: strconv.FormatInt(rev.Seq, 10), Note: uri})
	if err == nil {
		s.applyMemory(ev)
	}
	return err
}

func (s *Service) labelingEnabled() bool {
	task, err := s.configs.Get(labelConfigName, 0)
	var control struct{ Enabled bool }
	return err == nil && json.Unmarshal(task.Body, &control) == nil && control.Enabled
}
