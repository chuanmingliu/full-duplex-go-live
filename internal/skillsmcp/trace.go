package skillsmcp

import (
	"regexp"
	"time"
)

// Trace contains timings and resource identities only. It belongs to one
// preparation and is never shared between concurrent turns.
type Trace struct {
	ContextTruncated  bool        `json:"context_truncated,omitempty"`
	Retrieval         string      `json:"retrieval,omitempty"`
	EmbeddingProvider string      `json:"embedding_provider,omitempty"`
	EmbeddingRevision string      `json:"embedding_revision,omitempty"`
	FallbackReason    string      `json:"fallback_reason,omitempty"`
	Revision          int         `json:"revision,omitempty"`
	Steps             []TraceStep `json:"steps"`
	started           time.Time
}
type TraceStep struct {
	Stage   string `json:"stage"`
	URI     string `json:"uri,omitempty"`
	Digest  string `json:"digest,omitempty"`
	US      int64  `json:"us"`
	Outcome string `json:"outcome"`
}

var traceURI = regexp.MustCompile(`^skill://[a-z0-9_-]{1,64}/SKILL\.md$`)

func (t *Trace) Begin(stage, uri string, at time.Time) {
	if t == nil {
		return
	}
	t.End("ok", at)
	if !traceURI.MatchString(uri) {
		uri = ""
	}
	t.Steps = append(t.Steps, TraceStep{Stage: stage, URI: uri})
	t.started = at
}
func (t *Trace) End(outcome string, at time.Time) {
	if t == nil || t.started.IsZero() {
		return
	}
	s := &t.Steps[len(t.Steps)-1]
	s.US = at.Sub(t.started).Microseconds()
	s.Outcome = outcome
	t.started = time.Time{}
}
