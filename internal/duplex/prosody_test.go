package duplex

import (
	"sync/atomic"
	"testing"

	"github.com/chuanmingliu/golive/internal/prosody"
)

func newTestProsody(cfg prosody.Config) *prosodyState {
	last := &atomic.Int64{}
	last.Store(-1)
	return &prosodyState{cfg: cfg, first: true, turn: 1, last: last}
}

// The budget is per reply, but a streamed reply is injected one segment at a
// time, so the count has to be carried across them.
func TestProsodyBudgetSpansSegments(t *testing.T) {
	cfg := prosody.Default()
	cfg.MaxMarkersPerReply = 1
	st := newTestProsody(cfg)

	first := st.inject("Well, I have looked at that.", 0)
	if first == "Well, I have looked at that." {
		t.Fatalf("first segment got no marker: %q", first)
	}
	second := st.inject("Unfortunately it is fully booked.", 0)
	if second != "Unfortunately it is fully booked." {
		t.Fatalf("budget spent twice in one reply: %q", second)
	}
}

// A rule about how a reply opens must not fire again on its later segments.
func TestProsodyOpeningRuleOnlyOnFirstSegment(t *testing.T) {
	st := newTestProsody(prosody.Default())
	_ = st.inject("Right, here it is.", 0)
	if got := st.inject("Well, that is the other one.", 0); got != "Well, that is the other one." {
		t.Fatalf("opening rule fired on a later segment: %q", got)
	}
}

func TestProsodyDisabledLeavesTextAlone(t *testing.T) {
	cfg := prosody.Default()
	cfg.Mode = prosody.ModeOff
	st := newTestProsody(cfg)
	const in = "Well, unfortunately that is booked."
	if got := st.inject(in, 5000); got != in {
		t.Fatalf("disabled injector rewrote the text: %q", got)
	}
	// Sanitizing still runs: a model that was never told about markup still
	// writes the occasional stage direction.
	tts, plain := st.sanitize("That is odd (yawns).", "speech-2.8-turbo")
	if tts != "That is odd." || plain != "That is odd." {
		t.Fatalf("sanitizer did not clean a disabled turn: %q / %q", tts, plain)
	}
}

// Reporting a spoken prefix by character proportion charges a pause's silence
// to the characters around it, so the caller is recorded as having heard less
// than they did.
func TestPartialSpokenUsesDurationNotCharacters(t *testing.T) {
	const text = "abcdefghij"
	const markup = "abcde<#0.80#>fghij"

	if got := partialSpoken(text, markup, 300, 1800); got != "abc" {
		t.Fatalf("markup-aware prefix wrong: %q", got)
	}
	if got := partialText(text, 300, 1800); got != "a" {
		t.Fatalf("this test is meaningless unless the naive answer differs, got %q", got)
	}
	// No markup: the old path, unchanged.
	if got := partialSpoken(text, "", 900, 1800); got != "abcde" {
		t.Fatalf("plain segment regressed: %q", got)
	}
}
