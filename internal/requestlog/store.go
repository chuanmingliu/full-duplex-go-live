// Package requestlog durably records rendered OpenAI-compatible requests by
// session. Request bodies stay private on disk; operational logs use IDs only.
package requestlog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type Scope struct {
	SessionID  string `json:"session_id"`
	TurnID     string `json:"turn_id,omitempty"`
	ResponseID string `json:"response_id,omitempty"`
	Purpose    string `json:"purpose,omitempty"`
}
type scopeKey struct{}

func WithScope(ctx context.Context, scope Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}
func ScopeFrom(ctx context.Context) Scope { scope, _ := ctx.Value(scopeKey{}).(Scope); return scope }

type Metadata struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Scope
	CreatedAt         time.Time `json:"created_at"`
	FinishedAt        time.Time `json:"finished_at,omitempty"`
	Provider          string    `json:"provider"`
	Model             string    `json:"model"`
	Endpoint          string    `json:"endpoint"`
	Lane              int       `json:"lane"`
	KeySlot           int       `json:"key_slot"`
	RequestBytes      int       `json:"request_bytes"`
	RequestSHA256     string    `json:"request_sha256"`
	Outcome           string    `json:"outcome"`
	HTTPStatus        int       `json:"http_status,omitempty"`
	ProviderRequestID string    `json:"provider_request_id,omitempty"`
	DurationMS        int64     `json:"duration_ms,omitempty"`
}
type Store struct{ dir string }
type Record struct {
	dir      string
	metadata Metadata
	started  time.Time
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("request recording requires a directory")
	}
	path, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	return &Store{dir: path}, nil
}
func sessionDirectory(id string) string {
	if id == "" {
		return "unscoped"
	}
	if safeID.MatchString(id) {
		return id
	}
	return fmt.Sprintf("session-%x", sha256.Sum256([]byte(id)))
}

// Begin commits both exact request bytes and metadata before HTTP dispatch.
// A process exit before Finish leaves an honest "prepared" outcome.
func (s *Store) Begin(scope Scope, metadata Metadata, body []byte) (*Record, error) {
	if s == nil {
		return nil, nil
	}
	started := time.Now()
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	metadata.Version = 1
	metadata.ID = "llmreq_" + hex.EncodeToString(random)
	metadata.Scope = scope
	metadata.CreatedAt = started.UTC()
	metadata.Outcome = "prepared"
	metadata.RequestBytes = len(body)
	metadata.RequestSHA256 = fmt.Sprintf("%x", sha256.Sum256(body))
	session := filepath.Join(s.dir, sessionDirectory(scope.SessionID))
	if err := os.MkdirAll(session, 0700); err != nil {
		return nil, err
	}
	if err := syncDirectory(s.dir); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(session, ".pending-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	if err = writeFile(filepath.Join(staging, "request.json"), body); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, err
	}
	if err = writeFile(filepath.Join(staging, "metadata.json"), raw); err != nil {
		return nil, err
	}
	if err = syncDirectory(staging); err != nil {
		return nil, err
	}
	target := filepath.Join(session, metadata.ID)
	if err = os.Rename(staging, target); err != nil {
		return nil, err
	}
	if err = syncDirectory(session); err != nil {
		return nil, err
	}
	return &Record{dir: target, metadata: metadata, started: started}, nil
}
func (r *Record) ID() string {
	if r == nil {
		return ""
	}
	return r.metadata.ID
}
func (r *Record) Finish(status int, providerID, outcome string) error {
	if r == nil {
		return nil
	}
	r.metadata.HTTPStatus = status
	r.metadata.ProviderRequestID = providerID
	r.metadata.Outcome = outcome
	r.metadata.FinishedAt = time.Now().UTC()
	r.metadata.DurationMS = time.Since(r.started).Milliseconds()
	raw, err := json.MarshalIndent(r.metadata, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(r.dir, "metadata.json")
	temporary := path + ".tmp"
	if err = writeFile(temporary, raw); err != nil {
		return err
	}
	if err = os.Rename(temporary, path); err != nil {
		return err
	}
	return syncDirectory(r.dir)
}
func writeFile(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(body)
	syncErr := file.Sync()
	return errors.Join(writeErr, syncErr, file.Close())
}
func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
