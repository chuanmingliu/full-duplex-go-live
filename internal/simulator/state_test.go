package simulator

import (
	"errors"
	"testing"
)

func TestAdvanceGreetingBarrier(t *testing.T) {
	b := Binding{RunID: "r1", CallID: "c1", SessionID: "s1"}
	for _, order := range [][]string{{"answered", "voice_ready"}, {"voice_ready", "answered"}} {
		s := State{Phase: Answering, Binding: b}
		var count int
		for _, kind := range append(order, order...) {
			var effects []Effect
			var err error
			s, effects, err = Advance(s, Event{Kind: kind, Binding: b})
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range effects {
				if e.Kind == "open_greeting" {
					count++
				}
			}
		}
		if count != 1 || s.Phase != Connected {
			t.Fatalf("barrier %+v count=%d", s, count)
		}
	}
}
func TestAdvanceCancelledCannotAnswer(t *testing.T) {
	b := Binding{RunID: "r", CallID: "c"}
	s := State{Phase: Ringing, Binding: b}
	s, _, err := Advance(s, Event{Kind: "stop", Binding: b, AtMS: 1499})
	if err != nil {
		t.Fatal(err)
	}
	_, effects, _ := Advance(s, Event{Kind: "answer_due", Binding: b, AtMS: 1500})
	for _, e := range effects {
		if e.Kind == "accept" || e.Kind == "open_greeting" {
			t.Fatal(e.Kind)
		}
	}
}
func TestAdvanceBindingAndExpiry(t *testing.T) {
	s := State{Phase: Waiting, Binding: Binding{RunID: "r"}}
	if _, _, err := Advance(s, Event{Kind: "incoming", Binding: Binding{RunID: "old", CallID: "c"}}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal(err)
	}
	s, _, _ = Advance(s, Event{Kind: "expired", Binding: s.Binding})
	if _, e, err := Advance(s, Event{Kind: "incoming", Binding: Binding{RunID: "r", CallID: "c"}}); err == nil || len(e) > 0 {
		t.Fatal("expired reservation resurrected")
	}
	s = State{Phase: Ringing, Binding: Binding{RunID: "r", CallID: "c"}}
	if _, _, err := Advance(s, Event{Kind: "incoming", Binding: Binding{RunID: "r", CallID: "other"}}); err == nil {
		t.Fatal("second call admitted")
	}
}
