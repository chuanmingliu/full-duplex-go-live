package prosody

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// pauseRe matches a well-formed pause marker. The API allows two decimal
	// places of a second and nothing finer, so the pattern is the validator.
	pauseRe = regexp.MustCompile(`<#(\d{1,2}(?:\.\d{1,2})?)#>`)
	// parenRe matches a parenthesised token that could be a sound tag. It
	// deliberately matches unknown ones too: those are exactly what has to be
	// caught before a provider reads them out loud.
	parenRe = regexp.MustCompile(`\(([A-Za-z][A-Za-z-]{1,13})\)`)
	// markdownRe strips the emphasis and heading characters a model will
	// occasionally emit. It is applied to text tokens only — never to the
	// whole string, because a pause marker is made of '#' and stripping it
	// globally would quietly destroy every one of them.
	markdownRe = regexp.MustCompile("[*_`~]|^#{1,6}\\s+|^>\\s+")
	// spaceBeforePunctRe tidies the history text after markup is removed: a
	// tag sitting before a full stop leaves a space stranded in front of it.
	spaceBeforePunctRe = regexp.MustCompile(`\s+([.,!?;:。，！？；：、])`)
)

// TokenKind distinguishes the three things a synthesis segment can contain.
type TokenKind int

const (
	// TokText is speakable text: it produces audio and belongs in history.
	TokText TokenKind = iota
	// TokPause produces silence and no characters.
	TokPause
	// TokSoundTag produces audio that is not text: it belongs in neither the
	// character count nor the history.
	TokSoundTag
)

// Token is one run of a segment.
type Token struct {
	Kind    TokenKind
	Text    string  // as written, markup included
	Name    string  // sound tag name, lower-cased
	Seconds float64 // pause length
}

// Tokenize splits a segment into text, pauses and parenthesised tokens.
//
// Everything else in this package is built on it, because almost every bug in
// this area comes from treating a marked-up string as plain text: stripping
// markdown across a pause marker, splitting a chunk through a tag, or counting
// "(sighs)" as seven characters the caller heard.
func Tokenize(s string) []Token {
	var out []Token
	for i := 0; i < len(s); {
		rest := s[i:]
		pIdx := pauseRe.FindStringIndex(rest)
		gIdx := parenRe.FindStringIndex(rest)
		next, kind := -1, TokText
		if pIdx != nil {
			next, kind = pIdx[0], TokPause
		}
		if gIdx != nil && (next < 0 || gIdx[0] < next) {
			next, kind = gIdx[0], TokSoundTag
		}
		if next < 0 {
			out = append(out, Token{Kind: TokText, Text: rest})
			break
		}
		if next > 0 {
			out = append(out, Token{Kind: TokText, Text: rest[:next]})
		}
		switch kind {
		case TokPause:
			m := pauseRe.FindStringSubmatch(rest[next:])
			secs, _ := strconv.ParseFloat(m[1], 64)
			out = append(out, Token{Kind: TokPause, Text: m[0], Seconds: secs})
			i += next + len(m[0])
		default:
			m := parenRe.FindStringSubmatch(rest[next:])
			out = append(out, Token{Kind: TokSoundTag, Text: m[0], Name: strings.ToLower(m[1])})
			i += next + len(m[0])
		}
	}
	return out
}

// Sanitize returns the text to synthesize and the text to record in history.
//
// It is the last thing to run before a segment reaches a provider, whatever
// wrote the markup, and it is the component that prevents the three failures
// that are otherwise guaranteed in production: an unknown tag spoken aloud as
// words, consecutive pause markers rejected by the API, and a segment of pure
// markup that synthesizes to nothing and is indistinguishable from a cancelled
// one.
//
// The second return value is what goes into conversation history. Keeping
// markup out of it is not tidiness: a model shown its own stage directions
// starts writing more of them, and the effect compounds within a few turns.
//
// Both values are empty when the segment has nothing speakable left, which the
// caller must treat as "do not send this segment" rather than as an error.
func (c Config) Sanitize(text, model string) (tts, plain string) {
	allowTags := TagsSupported(model)
	lo, hi := c.PauseClampMS[0], c.PauseClampMS[1]
	if lo == 0 && hi == 0 {
		lo, hi = APIMinPauseMS, APIMaxPauseMS
	}

	var kept []Token
	for _, t := range Tokenize(text) {
		switch t.Kind {
		case TokPause:
			ms := clampMS(int(t.Seconds*1000+0.5), lo, hi)
			// Sum a run of pauses instead of emitting them back to back: the
			// API rejects consecutive markers outright.
			if n := len(kept); n > 0 && kept[n-1].Kind == TokPause {
				ms = clampMS(int(kept[n-1].Seconds*1000+0.5)+ms, lo, hi)
				kept = kept[:n-1]
			}
			kept = append(kept, pauseToken(ms))
		case TokSoundTag:
			if allowTags && KnownTag(t.Name) {
				kept = append(kept, t)
			}
			// Anything else is dropped silently. A tag the provider will not
			// render is worse than no tag at all.
		default:
			if clean := cleanText(t.Text); clean != "" {
				if n := len(kept); n > 0 && kept[n-1].Kind == TokText {
					kept[n-1].Text += clean
					continue
				}
				kept = append(kept, Token{Kind: TokText, Text: clean})
			}
		}
	}

	// A marker may not open or close a segment: it has to sit between
	// speakable text.
	for len(kept) > 0 && kept[0].Kind != TokText {
		kept = kept[1:]
	}
	for len(kept) > 0 && kept[len(kept)-1].Kind != TokText {
		kept = kept[:len(kept)-1]
	}

	var tb, pb strings.Builder
	gap := false
	for _, t := range kept {
		tb.WriteString(t.Text)
		if t.Kind != TokText {
			// The history text loses the markup, but it must not lose the word
			// boundary the markup stood on: a pause marker eats the space
			// around it, so "Well,<#0.30#>let me" would otherwise be recorded
			// as "Well,let me".
			gap = true
			continue
		}
		if gap && needsSpace(pb.String(), t.Text) {
			pb.WriteString(" ")
		}
		gap = false
		pb.WriteString(t.Text)
	}
	tts = strings.TrimSpace(tb.String())
	plain = strings.TrimSpace(spaceBeforePunctRe.ReplaceAllString(collapseSpaces(pb.String()), "$1"))
	if plain == "" {
		return "", ""
	}
	return tts, plain
}

// SpokenPrefix returns the plain text the caller actually heard, given how
// many milliseconds of a segment were played before it was cut.
//
// This is the correction markup forces on truncation accounting. The player
// reports a spoken prefix by proportion, which is sound while a segment is
// nothing but text — but "<#0.8#>" is seven characters of silence and
// "(sighs)" is seven characters of audio that is not text, so a character
// proportion over a marked-up segment reports a prefix nobody said. Walking
// tokens with their durations keeps "history records only what was heard"
// true, which is the whole point of paced playback.
//
// msPerChar is the segment's own measured rate — its duration divided by its
// speakable character count — and falls back to a plausible default.
func SpokenPrefix(tts string, playedMS int, msPerChar float64) string {
	if msPerChar <= 0 {
		msPerChar = 60
	}
	var b strings.Builder
	acc := 0.0
	for _, t := range Tokenize(tts) {
		switch t.Kind {
		case TokPause:
			acc += t.Seconds * 1000
		case TokSoundTag:
			acc += TagDurationMS(t.Name)
		default:
			for _, r := range t.Text {
				if acc >= float64(playedMS) {
					return strings.TrimSpace(b.String())
				}
				acc += msPerChar
				b.WriteRune(r)
			}
			continue
		}
		if acc >= float64(playedMS) {
			return strings.TrimSpace(b.String())
		}
	}
	return strings.TrimSpace(b.String())
}

// SafeBoundary reports whether a chunk may end at byte i.
//
// The chunker needs this because a tag split across two task_continue messages
// is rendered as its own fragments: a chunk ending "<#0" and one starting
// "0.6#>" put both into the caller's ear as characters. It is the same rule
// the segmenter already applies to "3.5" and "Dr.", for the same reason.
func SafeBoundary(buf string, i int) bool {
	if i < 0 || i > len(buf) {
		return false
	}
	head := buf[:i]
	if strings.LastIndex(head, "<#") > strings.LastIndex(head, "#>") {
		return false
	}
	if strings.LastIndex(head, "(") > strings.LastIndex(head, ")") {
		return false
	}
	return true
}

func pauseToken(ms int) Token {
	secs := float64(ms) / 1000
	return Token{Kind: TokPause, Text: fmt.Sprintf("<#%.2f#>", secs), Seconds: secs}
}

func clampMS(ms, lo, hi int) int {
	if ms < lo {
		return lo
	}
	if ms > hi {
		return hi
	}
	return ms
}

// cleanText removes markdown and control characters from speakable text. The
// 10% invalid-character limit the API enforces is reached surprisingly quickly
// by a model that decides to answer in a bulleted list.
func cleanText(s string) string {
	s = markdownRe.ReplaceAllString(s, "")
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' {
			b.WriteRune(' ')
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func collapseSpaces(s string) string {
	var b strings.Builder
	prev := false
	for _, r := range s {
		if r == ' ' {
			if prev {
				continue
			}
			prev = true
		} else {
			prev = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// needsSpace reports whether removing markup between two runs of text would
// weld two words together. Punctuation on either side already separates them,
// and adding a space there would read as " ." in the transcript.
func needsSpace(before, after string) bool {
	if before == "" || after == "" {
		return false
	}
	b, _ := utf8.DecodeLastRuneInString(before)
	a, _ := utf8.DecodeRuneInString(after)
	if unicode.IsSpace(b) || unicode.IsSpace(a) {
		return false
	}
	// Chinese and Japanese do not separate words with spaces, so inserting one
	// where a marker used to be would read as a gap that was never there.
	if isCJK(b) || isCJK(a) {
		return false
	}
	return !strings.ContainsRune(closingPunct, a)
}

// isCJK reports whether a rune belongs to a script written without spaces,
// including the full-width punctuation that goes with it.
func isCJK(r rune) bool {
	if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
		return true
	}
	return (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF)
}

// closingPunct is punctuation that attaches to the word before it.
const closingPunct = ".,!?;:)]}\u3002\uff0c\uff01\uff1f\uff1b\uff1a\u3001"
