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
	tts, plain, _ = c.SanitizeReport(text, model)
	return tts, plain
}

// Drop is one piece of markup the sanitizer removed, and why.
type Drop struct {
	Token  string
	Reason string
}

// Why a piece of markup was dropped.
const (
	DropUnknownTag   = "not a known tag"
	DropModelNoTags  = "model does not render tags"
	DropSegmentEdge  = "marker at a segment edge"
	DropNoSpeakable  = "nothing speakable left"
)

// Report is what the sanitizer did, for the log.
//
// A tag that silently disappears is the hardest thing about operating this
// layer: the caller hears a flat sentence and nothing anywhere says a tag was
// removed, or which of the four reasons applied. This is that record.
type Report struct {
	Pauses    int      // pause markers in the text that was sent
	Tags      []string // sound tags in the text that was sent
	Dropped   []Drop
	Collapsed int // runs of consecutive pauses summed into one
	Clamped   int // pauses moved to the profile's floor or ceiling
}

// Notable reports whether anything happened that is worth a log line.
func (r Report) Notable() bool {
	return len(r.Dropped) > 0 || r.Collapsed > 0 || r.Clamped > 0
}

// TagNames returns the tags kept, for a log field.
func (r Report) TagNames() string { return strings.Join(r.Tags, ",") }

// DropSummary renders the drops as "tag:reason" pairs, for a log field.
func (r Report) DropSummary() string {
	if len(r.Dropped) == 0 {
		return ""
	}
	parts := make([]string, 0, len(r.Dropped))
	for _, d := range r.Dropped {
		parts = append(parts, d.Token+":"+d.Reason)
	}
	return strings.Join(parts, " ")
}

// SanitizeReport is Sanitize with an account of what it removed.
func (c Config) SanitizeReport(text, model string) (tts, plain string, rep Report) {
	allowTags := TagsSupported(model)
	lo, hi := c.PauseClampMS[0], c.PauseClampMS[1]
	if lo == 0 && hi == 0 {
		lo, hi = APIMinPauseMS, APIMaxPauseMS
	}

	var kept []Token
	for _, t := range Tokenize(text) {
		switch t.Kind {
		case TokPause:
			asked := int(t.Seconds*1000 + 0.5)
			ms := clampMS(asked, lo, hi)
			if ms != asked {
				rep.Clamped++
			}
			// Sum a run of pauses instead of emitting them back to back: the
			// API rejects consecutive markers outright.
			if n := len(kept); n > 0 && kept[n-1].Kind == TokPause {
				ms = clampMS(int(kept[n-1].Seconds*1000+0.5)+ms, lo, hi)
				kept = kept[:n-1]
				rep.Collapsed++
			}
			kept = append(kept, pauseToken(ms))
		case TokSoundTag:
			switch {
			case allowTags && KnownTag(t.Name):
				kept = append(kept, t)
			case KnownTag(t.Name):
				// A real tag the provider cannot render. Dropped: it would be
				// read out as words.
				rep.Dropped = append(rep.Dropped, Drop{t.Text, DropModelNoTags})
			case allowTags:
				// An unknown parenthesised word on a model that was told about
				// tags. It is a tag the model invented, so it goes the same way.
				rep.Dropped = append(rep.Dropped, Drop{t.Text, DropUnknownTag})
			default:
				// Nothing here knows anything about tags, so this is an
				// ordinary parenthetical word and belongs in the speech.
				kept = append(kept, Token{Kind: TokText, Text: t.Text})
			}
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
		rep.Dropped = append(rep.Dropped, Drop{kept[0].Text, DropSegmentEdge})
		kept = kept[1:]
	}
	for len(kept) > 0 && kept[len(kept)-1].Kind != TokText {
		rep.Dropped = append(rep.Dropped, Drop{kept[len(kept)-1].Text, DropSegmentEdge})
		kept = kept[:len(kept)-1]
	}

	for _, t := range kept {
		switch t.Kind {
		case TokPause:
			rep.Pauses++
		case TokSoundTag:
			rep.Tags = append(rep.Tags, t.Name)
		}
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
	// The same tidy-up as the history text below: dropping a tag from
	// "odd (yawns)." leaves a space stranded before the full stop.
	tts = strings.TrimSpace(spaceBeforePunctRe.ReplaceAllString(collapseSpaces(tb.String()), "$1"))
	plain = strings.TrimSpace(spaceBeforePunctRe.ReplaceAllString(collapseSpaces(pb.String()), "$1"))
	if plain == "" {
		if strings.TrimSpace(text) != "" {
			rep.Dropped = append(rep.Dropped, Drop{strings.TrimSpace(text), DropNoSpeakable})
		}
		return "", "", rep
	}
	return tts, plain, rep
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

// MSPerChar estimates how long one speakable character of a segment took to
// say, given the segment's measured audio duration.
//
// It is what makes SpokenPrefix usable from the player: the player knows a
// segment's total duration and nothing about its markup, and dividing that
// duration by the raw character count would charge the silence of a pause and
// the audio of a tag to the characters around them.
func MSPerChar(markup string, totalMS float64) float64 {
	chars := 0
	extra := 0.0
	for _, t := range Tokenize(markup) {
		switch t.Kind {
		case TokPause:
			extra += t.Seconds * 1000
		case TokSoundTag:
			extra += TagDurationMS(t.Name)
		default:
			chars += len([]rune(t.Text))
		}
	}
	if chars == 0 {
		return 0
	}
	speech := totalMS - extra
	if speech < 0 {
		speech = 0
	}
	return speech / float64(chars)
}
