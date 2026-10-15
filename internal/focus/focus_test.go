package focus

import (
	"testing"
)

// frames makes 80 ms frames from startMS with the given per-speaker activity
// (1 = speaking) for n frames.
func frames(startMS int64, n int, active ...int) []Frame {
	out := make([]Frame, 0, n)
	for i := 0; i < n; i++ {
		p := make([]float32, 3)
		for _, s := range active {
			p[s] = 0.9
		}
		out = append(out, Frame{StartMS: startMS + int64(i)*80, EndMS: startMS + int64(i+1)*80, P: p})
	}
	return out
}

func feed(t *Tracker, fs ...[]Frame) {
	for _, f := range fs {
		for _, fr := range f {
			t.Observe(fr)
		}
	}
}

func TestTheFirstVoiceToSpeakEnoughIsThePrimary(t *testing.T) {
	tr := NewTracker(Config{})
	if tr.Primary() != -1 {
		t.Fatal("a primary before anyone spoke")
	}
	feed(tr, frames(0, 5, 0)) // 400 ms: not yet
	if tr.Primary() != -1 {
		t.Fatalf("enrolled on 400 ms")
	}
	feed(tr, frames(400, 3, 0)) // 640 ms
	if tr.Primary() != 0 {
		t.Fatalf("primary %d", tr.Primary())
	}
	a := tr.Attribute(0, 640)
	if !a.Known || a.Coverage < 0.99 || a.Primary < 0.99 || a.Others != 0 {
		t.Fatalf("attribution %+v", a)
	}
}

func TestTheAssistantsEchoIsNotASpeaker(t *testing.T) {
	tr := NewTracker(Config{})
	// The assistant speaks 0–2000 ms; the model hears a voice (speaker 0)
	// throughout that stretch: the echo. The caller (speaker 1) answers after.
	tr.Mask(0, 2000)
	feed(tr, frames(0, 25, 0))
	if tr.Primary() != -1 {
		t.Fatalf("the echo was enrolled as speaker %d", tr.Primary())
	}
	feed(tr, frames(2000, 10, 1))
	if tr.Primary() != 1 {
		t.Fatalf("primary %d; wanted the caller, speaker 1", tr.Primary())
	}
}

func TestABackgroundVoiceIsAttributedToOthers(t *testing.T) {
	tr := NewTracker(Config{})
	feed(tr, frames(0, 10, 0)) // caller enrolled
	// 1000–2000: a second voice alone
	feed(tr, frames(1000, 12, 1))
	a := tr.Attribute(1000, 1960)
	if a.Primary != 0 || a.Others < 0.99 || a.Coverage < 0.99 || !a.Known {
		t.Fatalf("attribution %+v", a)
	}
	// 2000–3000: both at once counts as the primary's
	feed(tr, frames(2000, 12, 0, 1))
	a = tr.Attribute(2000, 2960)
	if a.Primary < 0.99 {
		t.Fatalf("overlap %+v", a)
	}
	if tr.Primary() != 0 || tr.Handovers() != 0 {
		t.Fatalf("a second of another voice changed the primary")
	}
}

func TestCoverageSaysHowMuchIsLabelledYet(t *testing.T) {
	tr := NewTracker(Config{})
	feed(tr, frames(0, 10, 0))
	// Labels reach 800 ms; asked about 500–1500, half is known.
	a := tr.Attribute(500, 1500)
	if a.Coverage < 0.29 || a.Coverage > 0.31 {
		t.Fatalf("coverage %.2f", a.Coverage)
	}
	if tr.LabelledTo() != 800 {
		t.Fatalf("labelled to %d", tr.LabelledTo())
	}
}

func TestAHandoverTakesSecondsOfTheOtherVoiceAlone(t *testing.T) {
	tr := NewTracker(Config{HandoverMS: 2000})
	feed(tr, frames(0, 10, 0))
	feed(tr, frames(800, 20, 1)) // 1600 ms of speaker 1 alone: not yet
	if tr.Primary() != 0 {
		t.Fatal("handed over early")
	}
	feed(tr, frames(2400, 1, 0))  // the primary speaks once: the clock resets
	feed(tr, frames(2480, 24, 1)) // 1920 ms: still not
	if tr.Primary() != 0 {
		t.Fatal("handed over before HandoverMS of the other voice alone")
	}
	feed(tr, frames(4400, 2, 1)) // past 2000 ms
	if tr.Primary() != 1 || tr.Handovers() != 1 {
		t.Fatalf("primary %d handovers %d", tr.Primary(), tr.Handovers())
	}
}

func TestNothingIsAttributedBeforeAPrimaryIsKnown(t *testing.T) {
	tr := NewTracker(Config{})
	feed(tr, frames(0, 3, 1))
	a := tr.Attribute(0, 240)
	if a.Known {
		t.Fatal("known before enrolment")
	}
}
