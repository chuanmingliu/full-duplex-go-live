// Package segment turns a stream of LLM text deltas into speakable segments.
//
// This is the single highest-leverage piece of latency work in a cascaded voice
// stack. The model produces tokens; TTS wants sentences. Wait for a whole
// sentence and the user hears nothing for a second; cut at every token and
// prosody falls apart and the TTS connection thrashes. The compromise here is
// an aggressively short first segment — enough to get a syllable out the door —
// followed by progressively longer ones.
package segment

import (
	"strings"
	"unicode"
)

// Config tunes segmentation.
type Config struct {
	// FirstChunkChars is the smallest first segment. Small is good: the first
	// segment sets time-to-first-audio for the whole turn.
	FirstChunkChars int
	// MinChunkChars is the floor for every later segment.
	MinChunkChars int
	// MaxChunkChars force-flushes text that never reaches punctuation.
	MaxChunkChars int
}

// DefaultConfig returns balanced values for mixed Chinese/English speech.
func DefaultConfig() Config {
	return Config{FirstChunkChars: 8, MinChunkChars: 24, MaxChunkChars: 120}
}

// Strong sentence terminators in both scripts.
const strongTerminators = "。！？!?…\n"

// Weak boundaries usable for an early or forced flush.
const weakTerminators = "，,、；;：:—"

// Segmenter accumulates deltas and emits speakable segments.
// It belongs to one goroutine.
type Segmenter struct {
	cfg   Config
	buf   []rune
	first bool
	// depth tracks bracket nesting so a segment never ends mid-parenthesis.
	depth int
}

// New builds a segmenter. Zero fields in cfg fall back to DefaultConfig.
func New(cfg Config) *Segmenter {
	d := DefaultConfig()
	if cfg.FirstChunkChars <= 0 {
		cfg.FirstChunkChars = d.FirstChunkChars
	}
	if cfg.MinChunkChars <= 0 {
		cfg.MinChunkChars = d.MinChunkChars
	}
	if cfg.MaxChunkChars <= 0 {
		cfg.MaxChunkChars = d.MaxChunkChars
	}
	if cfg.MaxChunkChars < cfg.MinChunkChars {
		cfg.MaxChunkChars = cfg.MinChunkChars
	}
	return &Segmenter{cfg: cfg, first: true}
}

// Reset clears buffered text and returns to first-segment behaviour. Call it
// when a turn is abandoned, so the next turn does not inherit half a sentence.
func (s *Segmenter) Reset() {
	s.buf = s.buf[:0]
	s.first = true
	s.depth = 0
}

// Pending reports buffered text not yet emitted.
func (s *Segmenter) Pending() string { return string(s.buf) }

// Push appends a delta and returns every segment now ready to speak.
func (s *Segmenter) Push(delta string) []string {
	var out []string
	for _, r := range delta {
		s.buf = append(s.buf, r)
		switch r {
		case '(', '（', '[', '【', '“', '「':
			s.depth++
		case ')', '）', ']', '】', '”', '」':
			if s.depth > 0 {
				s.depth--
			}
		}
		if seg, ok := s.tryFlush(); ok {
			out = append(out, seg)
		}
	}
	return out
}

// FlushFirst emits whatever is buffered as the first segment, ignoring
// punctuation. It exists so time-to-first-audio cannot be held hostage by a
// reply that opens with a long unpunctuated clause.
func (s *Segmenter) FlushFirst() string {
	text := Normalize(string(s.buf))
	s.buf = s.buf[:0]
	s.depth = 0
	s.first = false
	return text
}

// Flush emits whatever is left, typically at end of turn.
func (s *Segmenter) Flush() string {
	text := Normalize(string(s.buf))
	s.buf = s.buf[:0]
	s.depth = 0
	s.first = true
	if text == "" {
		return ""
	}
	return text
}

// tryFlush decides whether the buffer now ends a speakable segment.
//
// It judges the *second to last* rune, using the last one as lookahead. A
// streaming segmenter has no other way to tell "3." in "3.5" from the end of a
// sentence, and mis-cutting there makes TTS read "three point" and stop. The
// cost is that a segment is released one rune late, which is nothing next to
// the synthesis time that follows it.
func (s *Segmenter) tryFlush() (string, bool) {
	n := len(s.buf)
	if n < 2 {
		return "", false
	}
	idx := n - 2 // candidate boundary; s.buf[n-1] is lookahead
	candidate := s.buf[idx]
	length := idx + 1

	minChars := s.cfg.MinChunkChars
	if s.first {
		minChars = s.cfg.FirstChunkChars
	}

	strong := strings.ContainsRune(strongTerminators, candidate)
	weak := strings.ContainsRune(weakTerminators, candidate)
	// A Latin period only ends a sentence when it is not part of a number and
	// not an abbreviation, so "3.5 kg" and "Dr. Chen" stay whole.
	if candidate == '.' && !s.isDecimalPoint(idx) && !s.isAbbreviation(idx) {
		strong = true
	}

	// Never cut inside a bracketed aside unless it has run away entirely.
	if s.depth > 0 && n < s.cfg.MaxChunkChars*2 {
		return "", false
	}

	switch {
	case strong && length >= minChars:
	case weak && s.first && length >= minChars:
		// Only the first segment may end on a comma. Later segments that break
		// there sound clipped.
	case weak && length >= s.cfg.MaxChunkChars:
	case n >= s.cfg.MaxChunkChars*2:
		// Pathological input with no punctuation at all: cut at the last space
		// if there is one, otherwise cut hard rather than growing forever.
		if sp := lastSpace(s.buf); sp > minChars {
			return s.cut(sp, sp+1)
		}
		return s.cut(s.cfg.MaxChunkChars-1, s.cfg.MaxChunkChars)
	default:
		return "", false
	}

	return s.cut(idx, idx+1)
}

// cut emits s.buf[:last+1] and keeps s.buf[from:].
func (s *Segmenter) cut(last, from int) (string, bool) {
	if last < 0 || last >= len(s.buf) || from > len(s.buf) {
		return "", false
	}
	seg := Normalize(string(s.buf[:last+1]))
	s.buf = append(s.buf[:0], s.buf[from:]...)
	s.first = false
	if seg == "" {
		return "", false
	}
	return seg, true
}

func (s *Segmenter) isDecimalPoint(i int) bool {
	if i < 0 || i >= len(s.buf) || s.buf[i] != '.' {
		return false
	}
	if i == 0 || i+1 >= len(s.buf) {
		return false
	}
	return unicode.IsDigit(s.buf[i-1]) && unicode.IsDigit(s.buf[i+1])
}

// isAbbreviation treats a period after a short capitalised word as part of that
// word: "Dr.", "Mr.", "St.", "Inc.", "U.S.". The rule is a heuristic and it is
// wrong for a sentence that genuinely ends in a short capitalised word ("He
// flew to LA."), but that error merely joins two sentences, while the opposite
// error makes the synthesizer stop dead in the middle of a name.
func (s *Segmenter) isAbbreviation(i int) bool {
	if i <= 0 {
		return false
	}
	start := i
	for start > 0 && unicode.IsLetter(s.buf[start-1]) {
		start--
	}
	word := s.buf[start:i]
	if len(word) == 0 || len(word) > 3 {
		return false
	}
	return unicode.IsUpper(word[0])
}

func lastSpace(buf []rune) int {
	for i := len(buf) - 1; i >= 0; i-- {
		if unicode.IsSpace(buf[i]) {
			return i
		}
	}
	return -1
}

// Normalize strips the markup an LLM emits by habit but a TTS engine would read
// aloud, and collapses whitespace. It is intentionally conservative: it removes
// decoration, never words.
func Normalize(text string) string {
	if text == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(text))

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case '*', '_', '`', '#', '~':
			// Markdown emphasis, headings, code fences and strikethrough.
			continue
		case '\n', '\r', '\t':
			b.WriteRune(' ')
			continue
		}
		// Drop a leading list bullet.
		if (r == '-' || r == '+') && isLineStart(runes, i) && i+1 < len(runes) && runes[i+1] == ' ' {
			i++
			continue
		}
		b.WriteRune(r)
	}

	return strings.Join(strings.Fields(b.String()), " ")
}

func isLineStart(runes []rune, i int) bool {
	for j := i - 1; j >= 0; j-- {
		if runes[j] == '\n' {
			return true
		}
		if !unicode.IsSpace(runes[j]) {
			return false
		}
	}
	return true
}

// VisibleLength counts runes, which is what the chunk thresholds mean for CJK
// text where a byte count would be three times too large.
func VisibleLength(s string) int { return len([]rune(s)) }

// TruncateRunes returns the first n runes of s.
func TruncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
