package audio

import (
	"math"
	"testing"
)

// rampIn is like tone() but the amplitude itself fades up to amp over
// rampMS on a smootherstep curve (6t^5-15t^4+10t^3), rather than starting
// there. The curve matters more than it looks: a linear ramp, and even a
// raised-cosine one, has nonzero *curvature* right at t=0 — and curvature
// is exactly what a second-derivative onset detector reads, which would
// make the test measure the ramp's construction instead of its gentleness.
// Smootherstep is the standard choice when both the first and second
// derivative need to vanish at the boundary, so nothing about this signal
// snaps on at any timescale up to acceleration.
func rampIn(rate, ms int, amp, rampMS float64) []float32 {
	n := rate * ms / 1000
	out := make([]float32, n)
	for i := range out {
		t := float64(i) / float64(rate)
		env := 0.62 + 0.38*math.Sin(2*math.Pi*4.5*t)
		var gain float64
		switch {
		case t >= rampMS/1000:
			gain = 1
		default:
			x := t / (rampMS / 1000)
			gain = 6*x*x*x*x*x - 15*x*x*x*x + 10*x*x*x
		}
		v := gain * amp * env * (math.Sin(2*math.Pi*140*t) +
			0.5*math.Sin(2*math.Pi*280*t) +
			0.25*math.Sin(2*math.Pi*560*t)) / 1.75
		out[i] = float32(v)
	}
	return out
}

// steadyTone is tone() without the syllable-rate envelope modulation: a
// plain sustained note. Used as a stand-in for quiet-but-not-digitally-
// silent background (room tone, mic self-noise) — quiet()'s literal zero
// gives an adaptive floor nothing to calibrate against, which no real
// microphone ever produces.
func steadyTone(rate, ms int, amp float64) []float32 {
	n := rate * ms / 1000
	out := make([]float32, n)
	for i := range out {
		t := float64(i) / float64(rate)
		v := amp * (math.Sin(2*math.Pi*140*t) +
			0.5*math.Sin(2*math.Pi*280*t) +
			0.25*math.Sin(2*math.Pi*560*t)) / 1.75
		out[i] = float32(v)
	}
	return out
}

// frames chunks samples into frameSamples-sized frames, dropping any short
// remainder, matching what Push() requires.
func frames(samples []float32, frameSamples int) [][]float32 {
	var out [][]float32
	for off := 0; off+frameSamples <= len(samples); off += frameSamples {
		out = append(out, samples[off:off+frameSamples])
	}
	return out
}

// countStrong reports how many frames before onsetFrame scored Strong (the
// false-positive count against a quiet baseline) and the index of the
// first Strong frame at or after onsetFrame (the detector's reaction
// delay), or -1 if it never fired.
func countStrong(d OnsetDetector, fs [][]float32, onsetFrame int) (falsePositives, firstAfterOnset int) {
	firstAfterOnset = -1
	for i, f := range fs {
		s := d.Push(f)
		if !s.Strong {
			continue
		}
		if i < onsetFrame {
			falsePositives++
		} else if firstAfterOnset == -1 {
			firstAfterOnset = i
		}
	}
	return falsePositives, firstAfterOnset
}

// quietPrefixMS is long enough (75 frames at 20ms) for every detector's
// bootstrap-and-adapt floor to settle against a steady low-level baseline
// before the signal under test begins.
const quietPrefixMS = 1500

func quietBaseline(rate int) []float32 {
	return steadyTone(rate, quietPrefixMS, 0.02)
}

func TestValidOnsetMethod(t *testing.T) {
	cases := map[string]bool{
		"":         true,
		"tkeo":     true,
		"envelope": true,
		"flux":     true,
		"neural":   false,
		"TKEO":     false, // case-sensitive: the JSON value is used verbatim
	}
	for s, want := range cases {
		if got := ValidOnsetMethod(s); got != want {
			t.Errorf("ValidOnsetMethod(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestNoneDetectorNeverFires(t *testing.T) {
	const frameMS = 20
	frameSamples := RatePipeline * frameMS / 1000
	sig := append(quietBaseline(RatePipeline), tone(RatePipeline, 400, 0.9)...)

	d := NewOnsetDetector(OnsetOff, RatePipeline, 0)
	if _, i := countStrong(d, frames(sig, frameSamples), quietPrefixMS/frameMS); i != -1 {
		t.Fatalf("OnsetOff detector fired Strong at frame %d, want never", i)
	}
	if unknown := NewOnsetDetector(OnsetMethod("bogus"), RatePipeline, 0); unknown.Push(tone(RatePipeline, 20, 0.9)).Strong {
		t.Fatal("unknown method should behave like off, not fire")
	}
}

func TestOnsetDetectorsFireOnSharpAttack(t *testing.T) {
	const frameMS = 20
	frameSamples := RatePipeline * frameMS / 1000
	onsetFrame := quietPrefixMS / frameMS

	for _, method := range []OnsetMethod{OnsetTKEO, OnsetEnvelope, OnsetFlux} {
		t.Run(string(method), func(t *testing.T) {
			// A settled quiet baseline, then a full-amplitude tone with no
			// fade-in at all — the sample right at the boundary jumps from
			// the baseline straight to full swing.
			sig := append(quietBaseline(RatePipeline), tone(RatePipeline, 400, 0.9)...)
			d := NewOnsetDetector(method, RatePipeline, 0)
			falsePositives, first := countStrong(d, frames(sig, frameSamples), onsetFrame)

			if falsePositives > 0 {
				t.Errorf("%s: fired Strong %d times against the settled quiet baseline, want 0", method, falsePositives)
			}
			if first == -1 {
				t.Fatalf("%s: never fired on an abrupt attack", method)
			}
			// A couple of frames of grace covers detector-specific
			// alignment (TKEO's replicate-padded edge, flux needing one
			// prior frame primed).
			if delay := first - onsetFrame; delay > 2 {
				t.Fatalf("%s: took %d frames to react to an abrupt attack, want at most 2", method, delay)
			}
		})
	}
}

// TestOnsetDetectorsAlsoCatchAGradualOnset checks these detectors are not
// limited to knife-edge attacks. A first attempt at this test asserted the
// opposite — that a gentle fade-in should take materially longer to fire
// than an abrupt one, or not fire at all — modelled on onset detection in
// music, where a struck note and a bowed one really do separate cleanly on
// attack sharpness. It does not hold up here: every one of these operators
// works in a log-compressed domain, and log-compression makes the *first*
// perceptible rise away from a settled floor look large in dB regardless of
// how physically smooth its curvature is (going from near-nothing to
// slightly-less-near-nothing is always a big ratio). Chasing that distinction
// down would mean either a much smaller margin — which reopens the false
// positives against steady background the other test exists to catch — or a
// genuinely different feature (attack duration, not just attack size), which
// is more machinery than a cheap barge-in fast-path trigger is worth.
//
// What actually matters for that fast path held up fine: no false positives
// against a settled steady background (echo, room tone), and a prompt
// reaction to anything new emerging from it — a real voice starting softly
// included, not just a slammed door.
func TestOnsetDetectorsAlsoCatchAGradualOnset(t *testing.T) {
	const frameMS = 20
	frameSamples := RatePipeline * frameMS / 1000
	onsetFrame := quietPrefixMS / frameMS

	for _, method := range []OnsetMethod{OnsetTKEO, OnsetEnvelope, OnsetFlux} {
		t.Run(string(method), func(t *testing.T) {
			sig := append(quietBaseline(RatePipeline), rampIn(RatePipeline, 900, 0.9, 800)...)
			d := NewOnsetDetector(method, RatePipeline, 0)
			falsePositives, first := countStrong(d, frames(sig, frameSamples), onsetFrame)

			if falsePositives > 0 {
				t.Errorf("%s: fired Strong %d times against the settled quiet baseline, want 0", method, falsePositives)
			}
			if first == -1 {
				t.Fatalf("%s: never reacted to a gradual onset at all", method)
			}
		})
	}
}
