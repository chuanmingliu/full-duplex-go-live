package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const SilenceReactionName = "silence-reaction"

// SilenceReaction is snapshotted from Leads at call start. Templates are literal
// spoken lines, not model instructions or executable templates.
type SilenceReaction struct {
	Version     int      `json:"version,omitempty"`
	Enabled     bool     `json:"enabled"`
	MinWaitMS   int      `json:"min_wait_ms"`
	MaxWaitMS   int      `json:"max_wait_ms"`
	MaxAttempts int      `json:"max_attempts"`
	Templates   []string `json:"templates"`
}

const DefaultSilenceReaction = `{
  "enabled": true,
  "min_wait_ms": 8000,
  "max_wait_ms": 12000,
  "max_attempts": 2,
  "templates": ["您好，您还在听吗？", "您在吗？如果有什么问题，可以直接告诉我。"]
}`

func ParseSilenceReaction(raw []byte) (SilenceReaction, error) {
	var c SilenceReaction
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	// A minimal disabled config is useful as a rollback switch.
	if !c.Enabled {
		return c, nil
	}
	if c.MinWaitMS < 1000 || c.MaxWaitMS < c.MinWaitMS || c.MaxWaitMS > 120000 {
		return c, fmt.Errorf("silence-reaction: require 1000 <= min_wait_ms <= max_wait_ms <= 120000")
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 3 {
		return c, fmt.Errorf("silence-reaction: max_attempts must be 1–3")
	}
	if len(c.Templates) < 1 || len(c.Templates) > 10 {
		return c, fmt.Errorf("silence-reaction: provide 1–10 templates")
	}
	for i, line := range c.Templates {
		line = strings.TrimSpace(line)
		if line == "" || utf8.RuneCountInString(line) > 120 {
			return c, fmt.Errorf("silence-reaction: template %d must contain 1–120 characters", i+1)
		}
		c.Templates[i] = line
	}
	return c, nil
}

func FetchSilenceReaction(base, token string, timeout time.Duration) (SilenceReaction, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/api/configs/"+SilenceReactionName, nil)
	if err != nil {
		return SilenceReaction{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return SilenceReaction{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return SilenceReaction{}, fmt.Errorf("silence-reaction: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return SilenceReaction{}, err
	}
	var env struct {
		Config struct {
			Version int             `json:"version"`
			Body    json.RawMessage `json:"body"`
		} `json:"config"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Config.Body) > 0 {
		c, err := ParseSilenceReaction(env.Config.Body)
		c.Version = env.Config.Version
		return c, err
	}
	return ParseSilenceReaction(raw)
}
