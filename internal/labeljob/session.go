// Package labeljob defines the private handoff from a closed call to the CRM.
package labeljob

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
)

type Turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	ItemID  string `json:"item_id"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
}

const SwitchFile = "label-task-version"

type Session struct {
	SchemaVersion int                `json:"schema_version"`
	ID            string             `json:"id"`
	CallID        string             `json:"call_id"`
	CreatedAt     time.Time          `json:"created_at"`
	ClosedAt      time.Time          `json:"closed_at"`
	Reason        string             `json:"reason"`
	Config        live.SessionConfig `json:"config"`
	Usage         *live.UsageBody    `json:"usage,omitempty"`
	History       []provider.Message `json:"history"`
	Turns         []Turn             `json:"turns"`
}

func (s Session) Enqueue(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, SwitchFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	version, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return err
	}
	if version <= 0 {
		return nil
	}
	s.SchemaVersion = version
	file, err := os.CreateTemp(dir, ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = json.NewEncoder(file).Encode(s); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), filepath.Join(dir, "session-"+s.ID+".json"))
}
