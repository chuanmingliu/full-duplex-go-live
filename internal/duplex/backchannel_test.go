package duplex

import "testing"

func testSet() *PhraseSet {
	return NewPhraseSet([]string{"嗯", "对", "好的", "ok", "uh huh", "yeah"})
}

func TestPhraseSetMatchesAcknowledgements(t *testing.T) {
	s := testSet()
	for _, text := range []string{
		"嗯",
		"嗯。", // the recognizer decided your grunt was a sentence
		"嗯嗯", // repetition is the same act
		"嗯嗯嗯嗯",
		"对，",
		"好的",
		"OK",    // case is the recognizer's choice, not the speaker's
		"uhhuh", // and so is where it puts the word boundary
		"uh huh",
		"嗯对", // two acknowledgements in a row is still not a question
	} {
		if !s.Matches(text) {
			t.Errorf("Matches(%q) = false, want true", text)
		}
	}
}

func TestPhraseSetRejectsSpeech(t *testing.T) {
	s := testSet()
	// Every one of these begins like an acknowledgement, which is the whole
	// difficulty: the prefix is not the decision.
	for _, text := range []string{
		"嗯，等一下",
		"对了，还有一件事",
		"好的话我们就这样",
		"ok but wait",
		"yeah so what about",
		"嗯？", // a question, and indistinguishable to us — see the doc comment
	} {
		if s.Matches(text) && text != "嗯？" {
			t.Errorf("Matches(%q) = true, want false", text)
		}
	}
	// The known miss, stated rather than hidden: prosody is what separates
	// agreement from a query here, and a phrase list cannot hear it.
	if !s.Matches("嗯？") {
		t.Log("嗯？ reads as an acknowledgement; only prosody distinguishes it")
	}
}

// TestPhraseSetDecidesEarly is the property the feature's latency rests on. A
// partial transcript must be ruled out as soon as it cannot become a
// backchannel, not when the utterance ends — otherwise every real interruption
// pays the full hold, and the feature costs exactly what it was meant to save.
func TestPhraseSetDecidesEarly(t *testing.T) {
	s := testSet()

	couldBecome := []string{"", "嗯", "u", "uh", "uh h", "y", "好"}
	for _, p := range couldBecome {
		if !s.CouldBecome(p) {
			t.Errorf("CouldBecome(%q) = false, want true", p)
		}
	}

	// "嗯，等" — the third character is the first that no candidate starts with,
	// and it is where the answer must stop.
	if !s.CouldBecome("嗯，") {
		t.Fatal(`CouldBecome("嗯，") = false; a trailing comma is not evidence`)
	}
	if s.CouldBecome("嗯，等") {
		t.Fatal(`CouldBecome("嗯，等") = true; 等 rules out every candidate and must interrupt`)
	}

	for _, p := range []string{"等一下", "what", "z"} {
		if s.CouldBecome(p) {
			t.Errorf("CouldBecome(%q) = true, want false", p)
		}
	}
}

func TestPhraseSetEmptyMatchesNothing(t *testing.T) {
	for _, s := range []*PhraseSet{nil, NewPhraseSet(nil), NewPhraseSet([]string{"", "  ", "，"})} {
		if !s.Empty() {
			t.Fatal("expected an empty set")
		}
		if s.Matches("嗯") || s.CouldBecome("嗯") {
			t.Fatal("an empty set must match nothing, so that clearing the list restores plain barge-in")
		}
	}
}

func TestPhraseSetPrefersTheLongerEntry(t *testing.T) {
	// "嗯嗯" is listed explicitly as well as being "嗯" twice. Both routes must
	// agree, and neither may shadow the other.
	s := NewPhraseSet([]string{"嗯", "嗯嗯"})
	if !s.Matches("嗯嗯") || !s.Matches("嗯") || !s.Matches("嗯嗯嗯") {
		t.Fatal("overlapping entries must not shadow each other")
	}
}
