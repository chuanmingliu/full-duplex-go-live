package simulator

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
)

// Warning aggregates recoverable provider failures without treating the run as
// failed. Codes are fixed and never contain provider messages or transcript text.
type Warning struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

var responseIdentity = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func validWarningCode(code string) bool {
	switch code {
	case "tts_open_failed", "tts_synthesis_failed", "tts_chunk_failed", "tts_empty_segment":
		return true
	default:
		return false
	}
}

// Warning records a dropped synthesis segment. responseID is the engine's
// audio item/turn ID, not the Realtime protocol's response wrapper ID.
func (c *RunController) Warning(ctx context.Context, b Binding, code, responseID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validWarningCode(code) || (responseID != "" && !responseIdentity.MatchString(responseID)) {
		return errors.New("invalid recoverable warning identity")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.runs[b.RunID]
	if !ok {
		return ErrNotFound
	}
	if !exactBinding(r.state.Binding, b) || b.SessionID == "" || (r.state.Phase != Connected && r.state.Phase != Answering) {
		return ErrInvalidTransition
	}
	if responseID != "" {
		if len(r.observer.failedResponses) >= 512 && !r.observer.failedResponses[responseID] {
			return errors.New("warning response capacity reached")
		}
		r.observer.failResponse(responseID)
	}
	found := false
	for i := range r.warnings {
		if r.warnings[i].Code == code {
			// Saturation bounds serialized counts even under a broken provider loop.
			if r.warnings[i].Count < 1000000000 {
				r.warnings[i].Count++
			}
			found = true
			break
		}
	}
	if !found {
		r.warnings = append(r.warnings, Warning{Code: code, Count: 1})
	}
	c.record(r, "warning", code)
	return c.persistSnapshot(r)
}

func (o *observer) failResponse(id string) {
	o.failedResponses[id] = true
	for i := range o.assistantTurns {
		if slices.Contains(o.assistantTurns[i].responseIDs, id) {
			o.assistantTurns[i].Failed = true
		}
	}
	for i := range o.customerTurns {
		if slices.Contains(o.customerTurns[i].responseIDs, id) {
			o.customerTurns[i].Failed = true
		}
	}
}

func (o *observer) associateResponse(id string) {
	if id == "" {
		return
	}
	for _, turns := range [][]BoundaryTurn{o.assistantTurns, o.customerTurns} {
		if len(turns) > 0 {
			p := &turns[len(turns)-1]
			if !slices.Contains(p.responseIDs, id) {
				if len(p.responseIDs) < 512 {
					p.responseIDs = append(p.responseIDs, id)
				} else {
					p.Failed = true
				}
			}
			p.Failed = p.Failed || o.failedResponses[id]
		}
	}
}

func warningFailures(warnings []Warning) []string {
	result := make([]string, 0, len(warnings))
	for _, w := range warnings {
		result = append(result, fmt.Sprintf("warning: %s (count=%d)", w.Code, w.Count))
	}
	return result
}

// HasFatalFailures recognizes only exact projections of supported structured
// warnings. Old, malformed, duplicated or unrecognized failure entries fail shut.
func (r Report) HasFatalFailures() bool {
	if len(r.Warnings) > 4 || len(r.Failures) != len(r.Warnings) {
		return true
	}
	expected := make(map[string]bool, len(r.Warnings))
	codes := make(map[string]bool, len(r.Warnings))
	for _, w := range r.Warnings {
		if !validWarningCode(w.Code) || w.Count < 1 || w.Count > 1000000000 || codes[w.Code] {
			return true
		}
		codes[w.Code] = true
		expected[fmt.Sprintf("warning: %s (count=%d)", w.Code, w.Count)] = true
	}
	for _, failure := range r.Failures {
		if !expected[failure] {
			return true
		}
		delete(expected, failure)
	}
	return len(expected) != 0
}
