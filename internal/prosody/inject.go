package prosody

import (
	"sort"
	"strings"
)

// Rule conditions.
const (
	WhenReplyStartsWith   = "reply_starts_with"
	WhenContainsAny       = "contains_any"
	WhenEndsWithQuestion  = "ends_with_question"
	WhenSentenceCountOver = "sentence_count_over"
	WhenToolLatencyOver   = "tool_latency_ms_over"
)

// Rule placements.
const (
	AtAfterMatch           = "after_match"
	AtAfterFirstClause     = "after_first_clause"
	AtEndOfMatchedSentence = "end_of_matched_sentence"
	AtBeforeMatch          = "before_match"
	AtBeforeFinalClause    = "before_final_clause"
	AtBeforeLastSentence   = "before_last_sentence"
)

// Rule is one profile entry: a condition, what to insert, and where.
//
// Arg is whatever the condition needs — a list of words, or a number — and is
// typed as any because it arrives from JSON. The accessors below tolerate both
// what encoding/json produces and what a Go literal produces, so a rule set
// written in a test reads the same as one loaded from a profile.
type Rule struct {
	When   string `json:"when"`
	Arg    any    `json:"arg,omitempty"`
	Insert string `json:"insert"`
	At     string `json:"at"`
}

func (r Rule) validate() error {
	switch r.When {
	case WhenReplyStartsWith, WhenContainsAny, WhenEndsWithQuestion,
		WhenSentenceCountOver, WhenToolLatencyOver:
	default:
		return errUnknown("when", r.When)
	}
	switch r.At {
	case AtAfterMatch, AtAfterFirstClause, AtEndOfMatchedSentence,
		AtBeforeMatch, AtBeforeFinalClause, AtBeforeLastSentence:
	default:
		return errUnknown("at", r.At)
	}
	if strings.TrimSpace(r.Insert) == "" {
		return errUnknown("insert", r.Insert)
	}
	return nil
}

// TurnContext is everything outside the reply text that a rule may consult.
//
// It is passed by value and never mutated. That is deliberate: Inject is pure,
// which is what lets the whole rule set be tested with no provider, no clock
// and no network — the same bargain the segmenter makes.
type TurnContext struct {
	// TurnIndex counts assistant turns in the session, from zero.
	TurnIndex int
	// ToolLatencyMS is how long backend or tool work took before this reply.
	ToolLatencyMS int
	// LastTagTurn is the TurnIndex of the last reply that carried a sound tag,
	// or -1 if none has. The caller owns this state so that Inject need not.
	LastTagTurn int
	// FirstSegment says this text opens the reply.
	//
	// It exists because a streamed reply is injected one segment at a time —
	// the whole text is never in hand at once — and a rule about how a reply
	// opens must not fire again on its third sentence.
	FirstSegment bool
}

// Result is a rewritten reply and an account of what was done to it.
//
// Pauses and Tags are worth logging per turn. In ModeBoth, a Tags count that
// stays at zero means the model is doing the work and the rule set can go; a
// FromModel count that stays at zero means PromptBlock is not earning its
// characters.
type Result struct {
	Text      string
	Pauses    int
	Tags      int
	FromModel int
	// Applied names the rules that fired, as "when→insert". A rule set is
	// tuned by watching which rules actually fire on real replies, and
	// nothing else in the pipeline can tell you that.
	Applied []string
}

// AppliedSummary renders the fired rules for a log field.
func (r Result) AppliedSummary() string { return strings.Join(r.Applied, " ") }

// Markers returns how many markers Inject added.
func (r Result) Markers() int { return r.Pauses + r.Tags }

type insertion struct {
	off int
	ins string
	tag bool
}

// Inject applies the rule set to one reply. It is pure: same inputs, same
// output, every time.
//
// Rules are evaluated in order and the earliest wins a contested position, so
// a profile expresses priority by ordering. An insertion is dropped when the
// budget is spent, when a marker already sits in the same clause (which is how
// ModeBoth keeps the model's own choices and only fills gaps), or when a sound
// tag is still inside its cooldown.
func (c Config) Inject(reply string, turn TurnContext) Result {
	res := Result{Text: reply, FromModel: countMarkers(reply)}
	if c.Mode != ModeRules && c.Mode != ModeBoth {
		return res
	}
	budget := c.MaxMarkersPerReply - res.FromModel
	if budget <= 0 || strings.TrimSpace(reply) == "" {
		return res
	}

	tagged := hasTag(reply)
	cooling := turn.LastTagTurn >= 0 && turn.TurnIndex-turn.LastTagTurn < c.TagCooldownTurns

	var picked []insertion
	for _, r := range c.Rules {
		if len(picked) >= budget {
			break
		}
		if !r.matches(reply, turn) {
			continue
		}
		isTag := strings.HasPrefix(strings.TrimSpace(r.Insert), "(")
		if isTag && (tagged || cooling) {
			continue
		}
		off := r.position(reply)
		if off < 0 || off > len(reply) {
			continue
		}
		if markerNear(reply, off) || occupied(picked, off) {
			continue
		}
		picked = append(picked, insertion{off: off, ins: strings.TrimSpace(r.Insert), tag: isTag})
		res.Applied = append(res.Applied, r.When+"→"+strings.TrimSpace(r.Insert))
		if isTag {
			tagged = true
		}
	}
	if len(picked) == 0 {
		return res
	}

	// Apply back to front so earlier offsets stay valid.
	sort.Slice(picked, func(i, j int) bool { return picked[i].off > picked[j].off })
	out := reply
	for _, p := range picked {
		next := spliceAt(out, p.off, p.ins, p.tag)
		if next == out {
			// spliceAt refused it: the position turned out to be a segment
			// edge, where a marker is invalid. Not counted, not reported.
			continue
		}
		out = next
		if p.tag {
			res.Tags++
		} else {
			res.Pauses++
		}
	}
	res.Text = out
	return res
}

// spliceAt inserts at off, minding the spacing each kind of markup wants.
//
// A pause marker replaces the word gap it lands in, because the API wants it
// between speakable segments and a space either side reads as two boundaries.
// A sound tag is a word-like thing and keeps its space.
func spliceAt(s string, off int, ins string, tag bool) string {
	left, right := s[:off], s[off:]
	if tag {
		left = strings.TrimRight(left, " ")
		// A space before the tag is an English convention. Chinese and
		// Japanese are written without them, and one inserted here survives
		// into the history text as a gap nobody typed.
		if left != "" && !endsCJK(left) && !startsCJK(right) {
			ins = " " + ins
		}
		return left + ins + right
	}
	left = strings.TrimRight(left, " ")
	right = strings.TrimLeft(right, " ")
	if left == "" || right == "" {
		// A marker at either end is invalid; drop it rather than send it.
		return s
	}
	return left + ins + right
}

func (r Rule) matches(reply string, turn TurnContext) bool {
	low := strings.ToLower(reply)
	switch r.When {
	case WhenReplyStartsWith:
		return turn.FirstSegment && firstWordEnd(low, argStrings(r.Arg)) >= 0
	case WhenContainsAny:
		for _, a := range argStrings(r.Arg) {
			if a != "" && strings.Contains(low, strings.ToLower(a)) {
				return true
			}
		}
		return false
	case WhenEndsWithQuestion:
		t := strings.TrimRight(strings.TrimSpace(reply), `"')]`)
		return strings.HasSuffix(t, "?") || strings.HasSuffix(t, "？")
	case WhenSentenceCountOver:
		return len(sentences(reply)) > argInt(r.Arg)
	case WhenToolLatencyOver:
		return turn.ToolLatencyMS > argInt(r.Arg)
	}
	return false
}

func (r Rule) position(reply string) int {
	switch r.At {
	case AtAfterMatch:
		return firstWordEnd(strings.ToLower(reply), argStrings(r.Arg))
	case AtAfterFirstClause:
		return firstClauseEnd(reply)
	case AtEndOfMatchedSentence:
		return endOfSentenceContaining(reply, argStrings(r.Arg))
	case AtBeforeMatch:
		return beforeMatch(reply, argStrings(r.Arg))
	case AtBeforeFinalClause:
		return lastClauseStart(reply)
	case AtBeforeLastSentence:
		return lastSentenceStart(reply)
	}
	return -1
}

func errUnknown(field, value string) error {
	return &ruleError{field: field, value: value}
}

type ruleError struct{ field, value string }

func (e *ruleError) Error() string { return e.field + " " + quote(e.value) + " is not recognised" }

func quote(s string) string { return `"` + s + `"` }

// argStrings normalizes a rule argument to a word list, accepting both a Go
// []string literal and the []any encoding/json produces.
func argStrings(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{t}
	}
	return nil
}

// argInt normalizes a rule argument to a number, accepting the float64 that
// encoding/json produces for every JSON number.
func argInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case float64:
		return int(t)
	}
	return 0
}
