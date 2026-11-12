// Package dreaming stores immutable call evidence and deferred, reviewable lessons.
// Model output is never automatically promoted to runtime instructions.
package dreaming

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

var (
	ErrConflict   = errors.New("ID already exists with different content")
	ErrIneligible = errors.New("episode is not released for review")
	identifier    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)
)

type Evidence struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // user, assistant, tool, timing, or feedback
	Text string `json:"text"`
}

type Episode struct {
	ID       string     `json:"id"`
	Mode     string     `json:"mode"` // real or simulation
	ClosedAt time.Time  `json:"closed_at"`
	Evidence []Evidence `json:"evidence"`
	// Audio is a local reference only. The worker never opens or uploads it.
	AudioRefs     []string `json:"audio_refs,omitempty"`
	ArchiveJSON   string   `json:"archive_json,omitempty"`
	ArchiveSHA256 string   `json:"archive_sha256,omitempty"`
}

type Feedback struct {
	EpisodeID string `json:"episode_id"`
	ID        string `json:"id"`
	Text      string `json:"text"`
}

type DreamRequest struct {
	ID         string    `json:"id"`
	EpisodeIDs []string  `json:"episode_ids"`
	NotBefore  time.Time `json:"not_before"`
}

type Snapshot struct {
	Episodes []Episode `json:"episodes"`
}

type Reference struct {
	EpisodeID  string `json:"episode_id"`
	EvidenceID string `json:"evidence_id"`
}

type RoutingCase struct {
	Utterance string `json:"utterance"`
	Match     bool   `json:"match"`
}

type Proposal struct {
	Title       string        `json:"title"`
	Observation string        `json:"observation"`
	Lesson      string        `json:"lesson"`
	Evidence    []Reference   `json:"evidence"`
	Match       []string      `json:"match"`
	Exclude     []string      `json:"exclude,omitempty"`
	Cases       []RoutingCase `json:"cases"`
}

type Lesson struct {
	ID      string `json:"id"`
	DreamID string `json:"dream_id"`
	Proposal
}

type Job struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	NotBefore time.Time `json:"not_before"`
	Snapshot  Snapshot  `json:"snapshot"`
	Error     string    `json:"error,omitempty"`
}

// Decode rejects unknown fields and concatenated JSON documents at all boundaries.
func Decode(r io.Reader, v any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

func validText(s string, max int) bool { return strings.TrimSpace(s) != "" && len(s) <= max }

func (e Episode) validate() error {
	if e.ArchiveJSON != "" {
		if !json.Valid([]byte(e.ArchiveJSON)) || e.ArchiveSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(e.ArchiveJSON))) {
			return errors.New("archive JSON checksum mismatch")
		}
	} else if e.ArchiveSHA256 != "" {
		return errors.New("archive JSON missing")
	}
	if !identifier.MatchString(e.ID) || (e.Mode != "real" && e.Mode != "simulation") || e.ClosedAt.IsZero() || e.ClosedAt.After(time.Now()) || len(e.Evidence) == 0 || len(e.Evidence) > 500 || len(e.AudioRefs) > 8 {
		return errors.New("invalid completed episode")
	}
	seen := map[string]bool{}
	for _, v := range e.Evidence {
		if !identifier.MatchString(v.ID) || seen[v.ID] || !validText(v.Text, 16000) {
			return errors.New("invalid or duplicate evidence")
		}
		seen[v.ID] = true
		switch v.Kind {
		case "user", "assistant", "tool", "timing":
		default:
			return errors.New("invalid evidence kind; submit owner feedback separately")
		}
	}
	for _, ref := range e.AudioRefs {
		if !validText(ref, 1024) {
			return errors.New("invalid audio reference")
		}
	}
	return nil
}

// Validate checks provenance and routing, not whether the model's explanation is true.
// Human review and behavioral evaluation remain distinct from these checks.
func (p Proposal) Validate(s Snapshot) error {
	if !validText(p.Title, 240) || strings.ContainsAny(p.Title, "\r\n") || !validText(p.Observation, 2000) || !validText(p.Lesson, 3000) || len(p.Evidence) == 0 || len(p.Evidence) > 32 || len(p.Match) == 0 || len(p.Match)+len(p.Exclude) > 16 || len(p.Cases) < 2 || len(p.Cases) > 20 {
		return errors.New("invalid bounded lesson")
	}
	refs := map[Reference]bool{}
	for _, e := range s.Episodes {
		for _, v := range e.Evidence {
			refs[Reference{e.ID, v.ID}] = true
		}
	}
	for _, ref := range p.Evidence {
		if !refs[ref] {
			return errors.New("lesson cites unknown evidence")
		}
	}
	var include, exclude []*regexp.Regexp
	for i, patterns := range [][]string{p.Match, p.Exclude} {
		for _, pattern := range patterns {
			if !validText(pattern, 512) {
				return errors.New("invalid routing pattern")
			}
			r, err := regexp.Compile(pattern)
			if err != nil {
				return errors.New("invalid routing regex")
			}
			if r.MatchString("") {
				return errors.New("routing cannot match empty text")
			}
			if i == 0 {
				include = append(include, r)
			} else {
				exclude = append(exclude, r)
			}
		}
	}
	positive, negative := false, false
	for _, c := range p.Cases {
		if !validText(c.Utterance, 2000) {
			return errors.New("invalid routing case")
		}
		match := false
		for _, r := range include {
			match = match || r.MatchString(c.Utterance)
		}
		for _, r := range exclude {
			if r.MatchString(c.Utterance) {
				match = false
			}
		}
		if match != c.Match {
			return errors.New("routing case failed")
		}
		positive = positive || c.Match
		negative = negative || !c.Match
	}
	if !positive || !negative {
		return errors.New("routing needs positive and negative cases")
	}
	return nil
}
