package config

import "testing"

// TestConversationalExtrasAreOffByDefault pins the three behaviours that make
// the agent speak when it was not asked something.
//
// Each was built to make delegation feel less like a half-duplex cascade, and
// each was heard as the opposite. An acknowledgement lands on top of the
// caller; a filler is heard as the agent having nothing to say, and on the
// first turn of a call it is the first thing the caller hears from it; the
// floor hold delays a genuine interruption by however long the recognizer takes
// to disagree. They stay in the code, switchable in one boolean, and stay off
// until someone asks for them.
func TestConversationalExtrasAreOffByDefault(t *testing.T) {
	d := Default().Duplex

	if d.Backchannel {
		t.Error("backchannel is on by default; the assistant would speak while the caller is talking")
	}
	if d.HoldingFiller {
		t.Error("holding_filler is on by default; the assistant would cover backend waits unasked")
	}
	if len(d.UserBackchannelPhrases) != 0 {
		t.Errorf("user_backchannel_phrases has %d entries by default; deciding that a word "+
			"the caller said is not an interruption is opt-in", len(d.UserBackchannelPhrases))
	}

	// The lists for the two switched-off behaviours stay populated: turning one
	// back on must not mean retyping what to say.
	if len(d.BackchannelPhrases) == 0 || len(d.HoldingFillerPhrases) == 0 {
		t.Error("a phrase list was emptied along with its switch; turning the behaviour " +
			"back on should be one boolean")
	}

	// The floor hold is the exception, and stays on. It arrived with the phrase
	// list above but is not the same feature: what it does is wait for the
	// transcript before yielding, and the case it was written for has no phrase
	// list in it — a cough, or the assistant's own voice through a
	// speakerphone, opening the VAD on a greeting that the recognizer then
	// finds no words in. Without it the greeting is cut mid-name.
	if d.UserBackchannelHoldMS <= 0 {
		t.Error("user_backchannel_hold_ms is off by default; a wordless noise would cut " +
			"the assistant off mid-sentence")
	}
}
