package duplex

import (
	"strings"
	"unicode"
)

// PhraseSet recognises the things a listener says that are not a request to
// speak: "嗯", "对", "uh huh". They are the audible proof someone is still
// there, and treating them as an interruption is one of the most grating
// failures a voice agent has — you murmur agreement and it stops dead,
// mid-sentence, waiting for a question you never asked.
//
// A genuine full-duplex model does not need any of this. It hears the murmur in
// the same stream as everything else and simply keeps talking, because holding
// the floor through an acknowledgement is a thing it learned rather than a rule
// it was given. golive has no such model, so it approximates the judgement with
// a list: these specific noises do not take the floor, everything else does.
// That is a blunt instrument — it knows nothing of prosody, and "对" said
// flatly and "对?" said sharply are the same string to it — but the failure it
// prevents is common and the failure it introduces is rare.
//
// The hard part is that the decision has to be made on a *partial* transcript,
// because by the time a final one arrives the answer has already been cut off.
// So the set answers two different questions: whether what was heard is a
// backchannel, and whether it could still turn into one. The second is what
// buys the engine permission to keep talking for another moment.
type PhraseSet struct {
	// phrases are normalized at construction, longest first, so the repetition
	// check below never matches a short phrase inside a longer one.
	phrases []string
}

// NewPhraseSet normalizes and de-duplicates a candidate list. An empty set
// matches nothing, which disables the feature.
func NewPhraseSet(raw []string) *PhraseSet {
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		n := NormalizePhrase(p)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	// Longest first: "嗯嗯" must be tried before "嗯", or the repetition rule
	// below would decompose it and never consult the explicit entry.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && len(out[j]) > len(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return &PhraseSet{phrases: out}
}

// Empty reports whether the set can match anything at all.
func (s *PhraseSet) Empty() bool { return s == nil || len(s.phrases) == 0 }

// Phrases returns the normalized candidates, for diagnostics.
func (s *PhraseSet) Phrases() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.phrases...)
}

// NormalizePhrase reduces a transcript to the form the set compares on:
// lower-cased, with punctuation and every kind of space removed.
//
// The spaces go because a recognizer's word boundaries are not stable ("uh huh"
// and "uhhuh" are the same noise) and because Chinese transcripts carry none
// anyway. The punctuation goes because a recognizer will happily decide your
// grunt was a sentence and end it with a full stop.
func NormalizePhrase(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Matches reports whether the text is a backchannel and nothing else.
//
// Repetition counts: "嗯嗯嗯" and "yeahyeah" are the same act as one "嗯", and a
// recognizer emits whichever the speaker happened to produce. A phrase repeated
// is still only a phrase; a phrase followed by anything else is a sentence, and
// the caller must yield to it.
func (s *PhraseSet) Matches(text string) bool {
	if s.Empty() {
		return false
	}
	return s.consumes(NormalizePhrase(text))
}

// consumes reports whether n is one or more candidates end to end.
func (s *PhraseSet) consumes(n string) bool {
	if n == "" {
		return false
	}
	for _, p := range s.phrases {
		if n == p {
			return true
		}
		if strings.HasPrefix(n, p) && s.consumes(n[len(p):]) {
			return true
		}
	}
	return false
}

// CouldBecome reports whether a partial transcript is still consistent with
// ending up as a backchannel — either because it already is one, or because
// some candidate starts with it.
//
// This is the question that matters while the assistant is still talking. A
// false answer is decisive and permanent: the speaker has said something no
// backchannel starts with, so they are taking the floor and the answer must
// stop now rather than at the end of the utterance. It is what makes "嗯，等一下"
// interrupt on the third character instead of a second later.
func (s *PhraseSet) CouldBecome(text string) bool {
	if s.Empty() {
		return false
	}
	n := NormalizePhrase(text)
	if n == "" {
		return true // nothing heard yet; no evidence either way
	}
	return s.couldBecome(n)
}

func (s *PhraseSet) couldBecome(n string) bool {
	if n == "" {
		return true
	}
	for _, p := range s.phrases {
		if strings.HasPrefix(p, n) {
			return true
		}
		if strings.HasPrefix(n, p) && s.couldBecome(n[len(p):]) {
			return true
		}
	}
	return false
}
