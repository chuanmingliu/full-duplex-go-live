package duplex

import (
	"fmt"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/focus"
)

// Attribution shares are for display/comparison. Multiplying one by total
// voiced time and truncating can turn exactly 400 ms into 399. The frames
// themselves supply the exact integer durations required by the existing gate.
func TestCallerBargeInUsesExactFrameDurations(t *testing.T) {
	for _, tc := range []struct {
		caller, other int64
		want          bool
	}{
		{400, 820, true}, // 400/1220*1220 rounds just below 400 in float64.
		{400, 940, true},
		{400, 1160, true},
		{399, 821, false},
		{200, 1020, false}, // A brief caller fragment among background speech.
		{160, 160, true},
		{159, 159, false},
		{160, 161, false},
		{0, 1220, false},
	} {
		t.Run(fmt.Sprintf("caller_%d_other_%d", tc.caller, tc.other), func(t *testing.T) {
			tracker := focus.NewTracker(focus.Config{})
			tracker.Pin(0)
			if tc.caller > 0 {
				tracker.Observe(focus.Frame{StartMS: 0, EndMS: tc.caller, P: []float32{1, 0}})
			}
			tracker.Observe(focus.Frame{StartMS: tc.caller, EndMS: tc.caller + tc.other, P: []float32{0, 1}})
			a := tracker.AttributeOver(0, tc.caller+tc.other)
			if got := callerSpeaksOver(a); got != tc.want {
				t.Fatalf("caller acceptance=%v, want %v for exact %d ms caller: %+v", got, tc.want, tc.caller, a)
			}
		})
	}
}

func TestKeptFocusHoldAcceptsExactCallerThreshold(t *testing.T) {
	for _, duration := range []int64{399, 400} {
		t.Run(fmt.Sprint(duration), func(t *testing.T) {
			e, _, _, _ := newAckHandoffTest(t)
			tracker := focus.NewTracker(focus.Config{})
			tracker.Pin(0)
			tracker.Observe(focus.Frame{StartMS: 0, EndMS: 820, P: []float32{0, 1}})
			tracker.Observe(focus.Frame{StartMS: 820, EndMS: 820 + duration, P: []float32{1, 0}})
			e.focus = &focusState{tracker: tracker, scorer: newFakeScorer(func(int64) int { return 0 }, 0)}
			e.userOpen = true
			e.inputMS.Store(820 + duration)
			e.uttVerdict = uttIgnored
			e.speaking.Store(true)
			e.hold = &floorHold{kept: true, started: time.Now()}
			want := bargeInKeep
			if duration == 400 {
				want = bargeInYield
			}
			if got := e.judgeBargeInFocus("Please wait."); got != want {
				t.Fatalf("kept hold verdict=%v, want %v at %d ms", got, want, duration)
			}
			e.hold = nil
			e.judgeUtteranceFocus()
			if (e.uttVerdict == uttCaller) != (duration == 400) {
				t.Fatalf("utterance verdict=%v at %d ms", e.uttVerdict, duration)
			}
		})
	}
}
