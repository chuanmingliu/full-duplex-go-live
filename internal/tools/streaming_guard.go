package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const StreamingGuardName = "streaming-guard"

const DefaultStreamingGuard = `{
  "enabled": true,
  "rules": [
    {"match": "我不猜，"},
    {"match": "我不猜。"},
    {"match": "我不猜"}
  ]
}`

type StreamingGuard struct {
	Version int                  `json:"version,omitempty"`
	Enabled bool                 `json:"enabled"`
	Rules   []StreamingGuardRule `json:"rules"`
	byLead  map[byte][]StreamingGuardRule
}

type StreamingGuardRule struct {
	Match       string `json:"match"`
	Replacement string `json:"replacement,omitempty"`
}

func ParseStreamingGuard(raw []byte) (StreamingGuard, error) {
	var c StreamingGuard
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return StreamingGuard{}, err
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || d.Decode(new(any)) != io.EOF {
		return StreamingGuard{}, fmt.Errorf("streaming guard must be a JSON object")
	}
	if len(c.Rules) > 64 {
		return StreamingGuard{}, fmt.Errorf("streaming guard supports up to 64 rules")
	}
	seen := make(map[string]bool, len(c.Rules))
	c.byLead = make(map[byte][]StreamingGuardRule)
	for i, rule := range c.Rules {
		if strings.TrimSpace(rule.Match) == "" || utf8.RuneCountInString(rule.Match) > 80 {
			return StreamingGuard{}, fmt.Errorf("rule %d needs a match of 1–80 characters", i+1)
		}
		if utf8.RuneCountInString(rule.Replacement) > 80 {
			return StreamingGuard{}, fmt.Errorf("rule %d replacement exceeds 80 characters", i+1)
		}
		if seen[rule.Match] {
			return StreamingGuard{}, fmt.Errorf("rule %d repeats a match", i+1)
		}
		seen[rule.Match] = true
		c.byLead[rule.Match[0]] = append(c.byLead[rule.Match[0]], rule)
	}
	for i, rule := range c.Rules {
		for match := range seen {
			if strings.Contains(rule.Replacement, match) {
				return StreamingGuard{}, fmt.Errorf("rule %d replacement contains a blocked match", i+1)
			}
		}
	}
	return c, nil
}

var builtinStreamingGuard = func() StreamingGuard {
	c, err := ParseStreamingGuard([]byte(DefaultStreamingGuard))
	if err != nil {
		panic(err)
	}
	return c
}()

func BuiltinStreamingGuard() StreamingGuard { return builtinStreamingGuard }

func FetchStreamingGuard(ctx context.Context, base, token string) (StreamingGuard, error) {
	body, version, err := fetchVoiceConfig(ctx, base, token, StreamingGuardName)
	if err != nil {
		return StreamingGuard{}, err
	}
	c, err := ParseStreamingGuard(body)
	c.Version = version
	return c, err
}

func fetchVoiceConfig(ctx context.Context, base, token, name string) (json.RawMessage, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 350*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/configs/"+name, nil)
	if err != nil {
		return nil, 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("%s: HTTP %d", name, resp.StatusCode)
	}
	var payload struct {
		Config struct {
			Version int             `json:"version"`
			Body    json.RawMessage `json:"body"`
		} `json:"config"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(&payload); err != nil {
		return nil, 0, err
	}
	if payload.Config.Version < 1 {
		return nil, 0, fmt.Errorf("%s: missing config version", name)
	}
	return payload.Config.Body, payload.Config.Version, nil
}

// StreamingTextGuard applies leftmost, longest literal matches to one model
// response. Only a possible match at the end of a chunk is withheld.
type StreamingTextGuard struct {
	byLead  map[byte][]StreamingGuardRule
	pending string
}

func (c StreamingGuard) Stream() StreamingTextGuard {
	if !c.Enabled {
		return StreamingTextGuard{}
	}
	return StreamingTextGuard{byLead: c.byLead}
}

func (s *StreamingTextGuard) Push(text string) string { return s.transform(text, false) }
func (s *StreamingTextGuard) Flush() string           { return s.transform("", true) }

func (s *StreamingTextGuard) transform(text string, final bool) string {
	if len(s.byLead) == 0 {
		return text
	}
	text = s.pending + text
	s.pending = ""
	var out strings.Builder
	start := 0
	for i := 0; i < len(text); {
		tail := text[i:]
		var longest StreamingGuardRule
		for _, rule := range s.byLead[tail[0]] {
			if strings.HasPrefix(tail, rule.Match) && len(rule.Match) > len(longest.Match) {
				longest = rule
			}
			if !final && len(tail) < len(rule.Match) && strings.HasPrefix(rule.Match, tail) {
				s.pending = tail
				if start == 0 {
					return text[:i]
				}
				out.WriteString(text[start:i])
				return out.String()
			}
		}
		if longest.Match == "" {
			i++
			continue
		}
		out.WriteString(text[start:i])
		out.WriteString(longest.Replacement)
		i += len(longest.Match)
		start = i
	}
	if start == 0 {
		return text
	}
	out.WriteString(text[start:])
	return out.String()
}
