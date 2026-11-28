package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const TurnTakingName = "turn-taking"

const DefaultTurnTaking = `{
  "enabled": true,
  "wait_ms": 5000,
  "phrases": ["等下", "等一下", "你听我说", "你先别说话"],
  "acknowledgment": "好的，您先说"
}`

type TurnTaking struct {
	Version        int      `json:"-"`
	Enabled        bool     `json:"enabled"`
	WaitMS         int      `json:"wait_ms"`
	Phrases        []string `json:"phrases"`
	Acknowledgment string   `json:"acknowledgment"`
}

func ParseTurnTaking(raw []byte) (TurnTaking, error) {
	var c *TurnTaking
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return TurnTaking{}, err
	}
	if c == nil || d.Decode(new(any)) != io.EOF {
		return TurnTaking{}, fmt.Errorf("turn-taking must be a JSON object")
	}
	if (c.Enabled || c.WaitMS != 0) && (c.WaitMS < 500 || c.WaitMS > 30000) {
		return TurnTaking{}, fmt.Errorf("turn-taking wait must be 500–30000 milliseconds")
	}
	if (c.Enabled && len(c.Phrases) == 0) || len(c.Phrases) > 32 {
		return TurnTaking{}, fmt.Errorf("turn-taking requires 1–32 phrases when enabled")
	}
	seen := make(map[string]bool, len(c.Phrases))
	for i, phrase := range c.Phrases {
		phrase = strings.TrimFunc(phrase, cueDelimiter)
		if phrase == "" || utf8.RuneCountInString(phrase) > 40 || seen[phrase] {
			return TurnTaking{}, fmt.Errorf("turn-taking phrase %d must be unique and 1–40 characters", i+1)
		}
		c.Phrases[i] = phrase
		seen[phrase] = true
	}
	c.Acknowledgment = strings.TrimSpace(c.Acknowledgment)
	if utf8.RuneCountInString(c.Acknowledgment) > 60 {
		return TurnTaking{}, fmt.Errorf("turn-taking acknowledgment exceeds 60 characters")
	}
	return *c, nil
}

func FetchTurnTaking(ctx context.Context, base, token string) (TurnTaking, error) {
	body, version, err := fetchVoiceConfig(ctx, base, token, TurnTakingName)
	if err != nil {
		return TurnTaking{}, err
	}
	c, err := ParseTurnTaking(body)
	c.Version = version
	return c, err
}

func cueDelimiter(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }

// SplitCue recognizes standalone cues or cues separated from a follow-up by
// punctuation/space. It never strips a phrase from the middle of a question.
func (c TurnTaking) SplitCue(text string) (string, bool) {
	if !c.Enabled || text == "" {
		return text, false
	}
	leading := strings.TrimLeftFunc(text, unicode.IsSpace)
	var longest int
	for _, phrase := range c.Phrases {
		tail, ok := strings.CutPrefix(leading, phrase)
		if !ok || len(phrase) <= longest {
			continue
		}
		r, _ := utf8.DecodeRuneInString(tail)
		if tail == "" || cueDelimiter(r) {
			longest = len(phrase)
		}
	}
	if longest == 0 {
		return text, false
	}
	return strings.TrimLeftFunc(leading[longest:], cueDelimiter), true
}
