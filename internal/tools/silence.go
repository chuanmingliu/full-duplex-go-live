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
	Version int            `json:"version,omitempty"`
	Enabled bool           `json:"enabled"`
	Rounds  []SilenceRound `json:"rounds,omitempty"`
	// Legacy single-pool settings remain readable; do not mix them with rounds.
	MinWaitMS   int      `json:"min_wait_ms,omitempty"`
	MaxWaitMS   int      `json:"max_wait_ms,omitempty"`
	MaxAttempts int      `json:"max_attempts,omitempty"`
	Templates   []string `json:"templates,omitempty"`
}

type SilenceRound struct {
	MinWaitMS int      `json:"min_wait_ms"`
	MaxWaitMS int      `json:"max_wait_ms"`
	Templates []string `json:"templates"`
}

const DefaultSilenceReaction = `{
 "enabled": true,
 "rounds": [
  {"min_wait_ms":8000,"max_wait_ms":12000,"templates":["您好，您还在听吗？"]},
  {"min_wait_ms":8000,"max_wait_ms":12000,"templates":["您在吗？如果有什么问题，可以直接告诉我。"]}
 ]
}`

// Round selects the next unanswered reminder. Exhaustion never cycles back.
func (c SilenceReaction) Round(attempt int) (SilenceRound, bool) {
	if attempt < 0 {
		return SilenceRound{}, false
	}
	if len(c.Rounds) > 0 {
		if attempt >= len(c.Rounds) {
			return SilenceRound{}, false
		}
		return c.Rounds[attempt], true
	}
	if attempt >= c.MaxAttempts {
		return SilenceRound{}, false
	}
	return SilenceRound{MinWaitMS: c.MinWaitMS, MaxWaitMS: c.MaxWaitMS, Templates: c.Templates}, true
}

func validateSilenceRound(r *SilenceRound) error {
	if r.MinWaitMS < 1000 || r.MaxWaitMS < r.MinWaitMS || r.MaxWaitMS > 120000 {
		return fmt.Errorf("silence-reaction: require 1000 <= min_wait_ms <= max_wait_ms <= 120000")
	}
	if len(r.Templates) < 1 || len(r.Templates) > 10 {
		return fmt.Errorf("silence-reaction: provide 1–10 templates per round")
	}
	for i, line := range r.Templates {
		line = strings.TrimSpace(line)
		if line == "" || utf8.RuneCountInString(line) > 120 {
			return fmt.Errorf("silence-reaction: template %d must contain 1–120 characters", i+1)
		}
		r.Templates[i] = line
	}
	return nil
}

func ParseSilenceReaction(raw []byte) (SilenceReaction, error) {
	var c SilenceReaction
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	// A minimal disabled config remains a rollback switch.
	if !c.Enabled {
		return c, nil
	}
	if len(c.Rounds) > 0 {
		if len(c.Rounds) > 3 {
			return c, fmt.Errorf("silence-reaction: provide 1–3 rounds")
		}
		if c.MinWaitMS != 0 || c.MaxWaitMS != 0 || c.MaxAttempts != 0 || len(c.Templates) > 0 {
			return c, fmt.Errorf("silence-reaction: do not mix rounds with legacy settings")
		}
		seen := map[string]bool{}
		for i := range c.Rounds {
			if err := validateSilenceRound(&c.Rounds[i]); err != nil {
				return c, fmt.Errorf("round %d: %w", i+1, err)
			}
			for _, line := range c.Rounds[i].Templates {
				if seen[line] {
					return c, fmt.Errorf("silence-reaction: templates must be distinct within and across rounds")
				}
				seen[line] = true
			}
		}
		return c, nil
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 3 {
		return c, fmt.Errorf("silence-reaction: max_attempts must be 1–3")
	}
	r := SilenceRound{MinWaitMS: c.MinWaitMS, MaxWaitMS: c.MaxWaitMS, Templates: c.Templates}
	if err := validateSilenceRound(&r); err != nil {
		return c, err
	}
	c.Templates = r.Templates
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
