package duplex

import (
	"sync/atomic"

	"github.com/chuanmingliu/golive/internal/prosody"
)

// prosodyState is the per-session state the prosody package deliberately does
// not keep.
//
// prosody.Inject is pure — reply in, reply out, no clock and no provider — so
// the two things that are inherently stateful live here instead: how many
// markers this reply has already spent, and how long ago the last sound tag
// was. Both have to be counted somewhere, and counting them on the engine
// keeps the rule set table-testable.
type prosodyState struct {
	cfg        prosody.Config
	used       int
	pauses     int
	tags       int
	keptPauses int
	keptTags   int
	first      bool
	turn       int
	last       *atomic.Int64 // session-wide: the turn index that last carried a tag
}

func (e *Engine) newProsodyState() *prosodyState {
	return &prosodyState{
		cfg:   e.opts.Cfg.Prosody,
		first: true,
		turn:  int(e.prosodyTurn.Add(1)),
		last:  &e.prosodyLastTag,
	}
}

// inject rewrites one segment of a reply, within the reply's marker budget.
//
// A streamed reply is never in hand whole, so injection happens per segment and
// the budget is carried across them. That is the one place this differs from
// the offline description of the feature: a rule that would need the whole
// reply — "is this the last sentence" — sees only what has been said so far.
func (s *prosodyState) inject(text string, toolLatencyMS int) (string, prosody.Result) {
	if s == nil || !s.cfg.Enabled() {
		return text, prosody.Result{Text: text}
	}
	cfg := s.cfg
	cfg.MaxMarkersPerReply -= s.used
	if cfg.MaxMarkersPerReply <= 0 {
		s.first = false
		return text, prosody.Result{Text: text}
	}
	res := cfg.Inject(text, prosody.TurnContext{
		TurnIndex:     s.turn,
		ToolLatencyMS: toolLatencyMS,
		LastTagTurn:   int(s.last.Load()),
		FirstSegment:  s.first,
	})
	s.first = false
	s.used += res.Markers()
	if res.Tags > 0 {
		s.last.Store(int64(s.turn))
		s.tags += res.Tags
	}
	s.pauses += res.Pauses
	return res.Text, res
}

// spent reports what this reply used, and what survived to be spoken.
//
// The two differ more often than you would expect, and the gap is the thing
// worth watching: a tag the model cannot render, a marker at a segment edge,
// or a budget spent on insertions the sanitizer then removed.
func (s *prosodyState) spent() (injectedPauses, injectedTags, keptPauses, keptTags int) {
	if s == nil {
		return 0, 0, 0, 0
	}
	return s.pauses, s.tags, s.keptPauses, s.keptTags
}

// sanitize splits a segment into what the synthesizer is sent and what the
// conversation history records.
//
// It runs whatever the mode, including "off": a model that has never been told
// about markup still writes the occasional "(pauses)" or markdown asterisk, and
// those reach the caller's ear as words.
func (s *prosodyState) sanitize(text, model string) (tts, plain string, rep prosody.Report) {
	cfg := prosody.Config{}
	if s != nil {
		cfg = s.cfg
	}
	tts, plain, rep = cfg.SanitizeReport(text, model)
	if s != nil {
		s.keptPauses += rep.Pauses
		s.keptTags += len(rep.Tags)
	}
	return tts, plain, rep
}
