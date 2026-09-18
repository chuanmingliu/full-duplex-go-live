package prosody

import (
	"strings"
	"unicode/utf8"
)

// Sentence and clause terminators, in both scripts the service speaks. The
// segmenter has its own, more careful, version of this for splitting text into
// synthesis segments; these are only ever used to choose an insertion point,
// where being one word off is a slightly odd pause and not a defect.
const (
	sentenceEnders = ".!?。！？"
	clauseEnders   = ",;:，；：—"
)

type span struct{ start, end int }

// sentences returns the byte span of each sentence, trailing space included.
func sentences(s string) []span {
	var out []span
	start := 0
	for i, r := range s {
		if !strings.ContainsRune(sentenceEnders, r) {
			continue
		}
		end := i + utf8.RuneLen(r)
		for end < len(s) && s[end] == ' ' {
			end++
		}
		if strings.TrimSpace(s[start:end]) != "" {
			out = append(out, span{start, end})
		}
		start = end
	}
	if start < len(s) && strings.TrimSpace(s[start:]) != "" {
		out = append(out, span{start, len(s)})
	}
	return out
}

// firstWordEnd returns the offset just past the reply's first word when that
// word is in the list, and -1 otherwise.
func firstWordEnd(low string, words []string) int {
	trimmed := strings.TrimLeft(low, " ")
	lead := len(low) - len(trimmed)
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" || !strings.HasPrefix(trimmed, w) {
			continue
		}
		rest := trimmed[len(w):]
		// A whole word, not a prefix of a longer one: "so" must not match
		// "sorry".
		if rest == "" {
			return lead + len(w)
		}
		r, _ := utf8.DecodeRuneInString(rest)
		if strings.ContainsRune(" ,.;:!?，。；：！？", r) {
			return lead + len(w) + runeWidthIfPunct(r)
		}
	}
	return -1
}

// runeWidthIfPunct keeps a comma on the left of an inserted pause, so that
// "Well, let me check" becomes "Well,<#0.3#>let me check" rather than
// "Well<#0.3#>, let me check".
func runeWidthIfPunct(r rune) int {
	if r == ' ' {
		return 0
	}
	return utf8.RuneLen(r)
}

// firstClauseEnd returns the offset just past the first clause delimiter, or
// the end of the first sentence when the sentence has no internal break.
func firstClauseEnd(s string) int {
	for i, r := range s {
		if strings.ContainsRune(clauseEnders, r) {
			return i + utf8.RuneLen(r)
		}
		if strings.ContainsRune(sentenceEnders, r) {
			return i + utf8.RuneLen(r)
		}
	}
	return -1
}

// lastSentenceStart returns the start of the final sentence, when there is
// more than one.
func lastSentenceStart(s string) int {
	ss := sentences(s)
	if len(ss) < 2 {
		return -1
	}
	return ss[len(ss)-1].start
}

// lastClauseStart returns the start of the final clause of the final sentence.
// With no internal break it falls back to the final sentence, and with only
// one sentence there is nowhere sensible to put a marker, so it declines.
func lastClauseStart(s string) int {
	ss := sentences(s)
	if len(ss) == 0 {
		return -1
	}
	last := ss[len(ss)-1]
	body := s[last.start:last.end]
	best := -1
	for i, r := range body {
		if strings.ContainsRune(clauseEnders, r) {
			best = i + utf8.RuneLen(r)
		}
	}
	if best >= 0 {
		return last.start + best
	}
	return lastSentenceStart(s)
}

// endOfSentenceContaining returns the offset just before the terminal
// punctuation of the first sentence holding any of the words — where a sound
// tag belongs, reacting to the clause rather than announcing it.
func endOfSentenceContaining(s string, words []string) int {
	low := strings.ToLower(s)
	hit := -1
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" {
			continue
		}
		if i := strings.Index(low, w); i >= 0 && (hit < 0 || i < hit) {
			hit = i
		}
	}
	if hit < 0 {
		return -1
	}
	for _, sp := range sentences(s) {
		if hit < sp.start || hit >= sp.end {
			continue
		}
		body := strings.TrimRight(s[sp.start:sp.end], " ")
		if body == "" {
			return -1
		}
		r, size := utf8.DecodeLastRuneInString(body)
		if strings.ContainsRune(sentenceEnders, r) {
			return sp.start + len(body) - size
		}
		return sp.start + len(body)
	}
	return -1
}

// markerNear reports whether markup already sits within a clause of off. It is
// what stops a rule from piling a pause on top of one the model wrote.
const nearWindow = 40

func markerNear(s string, off int) bool {
	lo := off - nearWindow
	if lo < 0 {
		lo = 0
	}
	hi := off + nearWindow
	if hi > len(s) {
		hi = len(s)
	}
	w := s[lo:hi]
	return strings.Contains(w, "<#") || parenRe.MatchString(w)
}

// occupied reports whether an insertion has already been chosen close enough
// to off that both would read as one stumble.
func occupied(picked []insertion, off int) bool {
	for _, p := range picked {
		d := p.off - off
		if d < 0 {
			d = -d
		}
		if d < nearWindow/2 {
			return true
		}
	}
	return false
}

// countMarkers counts the markup already in a reply, whoever wrote it.
func countMarkers(s string) int {
	return len(pauseRe.FindAllString(s, -1)) + len(parenRe.FindAllString(s, -1))
}

// hasTag reports whether the reply already carries a sound tag.
func hasTag(s string) bool { return parenRe.MatchString(s) }
