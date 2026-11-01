package main

// synth_test.go makes calls to measure. Nothing in it is speech: a "voice" is
// a buzz switched on and off at the rate of syllables, which is the only
// property of speech the probe uses. Every call is a function of its seed, so
// a failure can be had again.

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/rand"
	"time"
)

const (
	// voiceDB is the assistant's level while sounding, give or take the few
	// dB by which one syllable differs from the next, and the reference for
	// every other level in a scene. The caller is 2 dB under it.
	voiceDB = -20.0
)

// echo is one return of the assistant's voice: how late, and how far below
// the assistant's own level.
type echo struct {
	delay time.Duration
	below float64 // dB
}

// scene describes a call.
type scene struct {
	seed    int64
	rate    int
	length  time.Duration
	echoes  []echo
	noiseDB float64 // level of the line's noise in dBFS; 0 for none
	// wanderDB makes the noise a metro rather than a hiss: its level drifts
	// by this much (one standard deviation) on the timescale of a syllable,
	// so that it has onsets of its own.
	wanderDB float64
	// overlap is the share of the assistant's stretches the caller talks
	// over, from somewhere in the middle, as well as taking turns.
	overlap float64
}

func db(level float64) float64 { return math.Pow(10, level/20) }

const sqrt3 = 1.7320508075688772

// white is noise of unit power. It is uniform rather than Gaussian because a
// test suite draws a hundred million of these and the probe, which looks at
// the power of 20 ms at a time, cannot tell the difference.
func white(rng *rand.Rand) float64 { return sqrt3 * (2*rng.Float64() - 1) }

// voice fills dst with syllables: bursts of 80–250 ms of a sawtooth and a
// little hiss, 40–200 ms apart, now and then a longer breath. Each syllable
// has its own pitch, a soft edge and a loudness of its own between 6 dB under
// and 3 dB over an RMS of 1.
func voice(rng *rand.Rand, dst []float64, rate int) {
	ms := func(lo, hi int) int { return (lo + rng.Intn(hi-lo+1)) * rate / 1000 }
	f0 := 110 + 110*rng.Float64()
	for at := ms(0, 60); at < len(dst); {
		n := ms(80, 250)
		edge := ms(8, 20)
		pitch := f0 * (0.85 + 0.3*rng.Float64())
		loud := db(-6 + 9*rng.Float64())
		phase := rng.Float64()
		for i := 0; i < n && at+i < len(dst); i++ {
			gain := loud
			if i < edge {
				gain *= 0.5 - 0.5*math.Cos(math.Pi*float64(i)/float64(edge))
			} else if n-i < edge {
				gain *= 0.5 - 0.5*math.Cos(math.Pi*float64(n-i)/float64(edge))
			}
			// A sawtooth: every harmonic of the pitch, for the price of none.
			_, saw := math.Modf(phase + pitch*float64(i)/float64(rate))
			dst[at+i] += gain * (sqrt3*(2*saw-1) + 0.15*white(rng))
		}
		at += n + ms(40, 200)
		if rng.Intn(7) == 0 {
			at += ms(120, 320)
		}
	}
}

// render makes the two tracks of a scene as float samples, full scale 1.
//
// The call takes turns: the assistant speaks for 2–6 s, the caller answers
// for 1–4 s, with the pauses people leave. The assistant's track is exactly
// silent between its stretches, as a stored one is. The input is the caller,
// the line's noise, and whatever echoes the scene has.
func render(sc scene) (out, in []float64) {
	n := int(sc.length.Seconds() * float64(sc.rate))
	out, in = make([]float64, n), make([]float64, n)
	layout := rand.New(rand.NewSource(sc.seed))
	assistant := rand.New(rand.NewSource(sc.seed*7919 + 1))
	caller := rand.New(rand.NewSource(sc.seed*104729 + 2))
	line := rand.New(rand.NewSource(sc.seed*1299709 + 3))
	sec := func(lo, hi float64) int { return int((lo + (hi-lo)*layout.Float64()) * float64(sc.rate)) }

	said := make([]float64, n)
	for at := sec(0.2, 1.0); at < n; {
		a := min(sec(2, 6), n-at)
		voice(assistant, out[at:at+a], sc.rate)
		if layout.Float64() < sc.overlap {
			from := at + sec(0.3, 1.0)
			if to := min(from+sec(0.6, 2.0), n); from < to {
				voice(caller, said[from:to], sc.rate)
			}
		}
		at += a + sec(0.3, 0.9)
		if at >= n {
			break
		}
		c := min(sec(1, 4), n-at)
		voice(caller, said[at:at+c], sc.rate)
		at += c + sec(0.5, 1.2)
	}

	assistantGain, callerGain := db(voiceDB), db(voiceDB-2)
	for i := range out {
		out[i] *= assistantGain
	}
	for i := range in {
		in[i] = said[i] * callerGain
	}
	for _, e := range sc.echoes {
		shift := int(e.delay.Seconds() * float64(sc.rate))
		gain := db(-e.below)
		for i := shift; i < n; i++ {
			in[i] += gain * out[i-shift]
		}
	}
	if sc.noiseDB != 0 {
		// The wander is a random walk pulled back to zero, with a memory of
		// about 100 ms, stepped once a millisecond.
		every := sc.rate / 1000
		keep := math.Exp(-1.0 / 100)
		step := sc.wanderDB * math.Sqrt(1-keep*keep)
		drift, gain := 0.0, db(sc.noiseDB)
		for i := range in {
			if sc.wanderDB != 0 && i%every == 0 {
				drift = drift*keep + step*line.NormFloat64()
				gain = db(sc.noiseDB + drift)
			}
			in[i] += gain * white(line)
		}
	}
	return out, in
}

// pcm16 is a track as the store writes it.
func pcm16(samples []float64) []byte {
	buf := make([]byte, len(samples)*2)
	for i, s := range samples {
		v := math.Round(s * 32768)
		v = math.Max(-32768, math.Min(32767, v))
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(int16(v)))
	}
	return buf
}

const testHop = 5 * time.Millisecond

// probe runs the measurement on a scene, through the same bytes a stored call
// would be read from.
func probe(sc scene, maxLag time.Duration) finding {
	out, in := render(sc)
	hop := int(testHop.Seconds() * float64(sc.rate))
	po, _, _ := hopPower(bytes.NewReader(pcm16(out)), hop)
	pi, _, _ := hopPower(bytes.NewReader(pcm16(in)), hop)
	return analyse(po, pi, testHop, maxLag)
}
