package main

// probe.go is the measurement: two tracks in, the lags at which the second
// repeats the first out. Nothing here reads a flag or prints.

import (
	"bufio"
	"encoding/binary"
	"io"
	"math"
	"sort"
	"time"
)

const (
	// envelopeWindow is the stretch of audio one envelope value describes. A
	// syllable lasts 100–250 ms; 20 ms follows its rise without following the
	// pitch periods inside it.
	envelopeWindow = 20 * time.Millisecond

	// floorDB is as quiet as the input track is allowed to read. A track with
	// a hole in it reads as digital zero, which in decibels is minus infinity.
	floorDB = -80.0

	// speakingDB is the level above which the assistant counts as speaking.
	// Its track is silent between answers — exactly silent, the store leaves
	// holes — so anything a synthesizer produces clears this by tens of dB.
	// It is also the floor of the assistant's envelope: what happens below it
	// is not speech and has no echo worth looking for.
	speakingDB = -50.0

	// onsetCap bounds what one moment can contribute to an onset pattern, as
	// a rate of rise: 200 dB a second is 1 dB in a 5 ms hop.
	//
	// A correlation is a sum of products and a sum is at the mercy of its
	// largest terms. Uncapped, the first syllable of each answer is a 30 dB
	// step out of silence and the ones after it a few dB, so the result is
	// decided by one moment per answer — the first sound, timed all over
	// again, which is the measurement that failed. And in the input the
	// caller's own voice arrives 25 dB above the echo: each of its syllables
	// outweighs dozens of the echo's.
	//
	// Capped this low, a rise is an event that lasts as long as the window
	// takes to fill and has the same weight whoever made it. It costs a faint
	// echo nothing: under noise an echo lifts the level by a fraction of a dB
	// a hop, well inside the cap. It also puts the peak where the echo is. A
	// step out of silence is nearly all in its first hop, an echo's rise under
	// noise is spread evenly over the window, and correlating the one shape
	// with the other read 5–15 ms late on a third of the noisy synthetic
	// calls at 6 dB a hop. At 1 dB none did.
	//
	// Tried on the synthetic calls at 1, 2, 3 and 6 dB a hop. The lowest
	// found the faintest echoes (a metro 5 dB over the echo, 90 s: median
	// significance 8.9, against 7.5 at 3 dB and 7.0 at 6) with no more false
	// ones.
	onsetCap = 200.0 // dB per second

	// stretchGap is the silence that separates two stretches of the
	// assistant's speech, and minStretch the shortest stretch worth
	// correlating by itself: under that there are two or three syllables.
	stretchGap = 400 * time.Millisecond
	minStretch = 800 * time.Millisecond

	// peakApart is how close two peaks may be and still be two echoes. One
	// echo's peak is as wide as the envelope window on either side.
	peakApart = 50 * time.Millisecond

	// agreeWithin is how far a stretch's own best lag may sit from a
	// whole-call peak and count as the same echo.
	agreeWithin = 15 * time.Millisecond

	maxPeaks = 3

	// significant is the bar a peak must clear to be reported as an echo, and
	// stretchSignificant the bar for one stretch's own best lag to be counted
	// among the stretches that have an opinion. Both are in the units
	// correlate describes: standard errors above what unrelated tracks give,
	// so that a lag with no echo reads like a draw from a standard normal.
	//
	// 301 lags are searched, so with no echo anywhere the largest of them
	// should be about 3 and almost never 5. Measured on synthetic calls with
	// no echo (TestSweepOfCallsWithAndWithoutEcho, which prints what is
	// quoted here): 2,700 calls of 30–90 s, on quiet, silent and noisy lines,
	// with and without the caller talking over the assistant, and the largest
	// was 4.7. The faintest echo the tests ask for — 5 dB under a metro, in
	// 90 s — read 9 at the median, and 6.4 in the weakest of 80 such calls.
	// 6 sits between: a false echo needs a draw of six standard errors at
	// one of 301 lags, a few calls in ten million if the normal holds that
	// far out, which 2,700 calls cannot show.
	//
	// A stretch is not asked to prove there is an echo — the whole call does
	// that — only to be worth counting, and it cannot reach 6: an answer of a
	// few seconds holds a dozen syllables, and all of them coming back is
	// about √12. At 4, eight stretches in ten pass on a quiet line with an
	// echo, and of 28,023 stretches with none, one did. (At 3.5 it was one in
	// 150 on a noisy line, enough to put a stray lag in the min or the max.)
	significant        = 6.0
	stretchSignificant = 4.0
)

// hopPower reads mono PCM16 little-endian and returns the mean square of each
// hop of it, on a scale where full scale is 1. It reads a stream rather than a
// file so that a two-hour call costs a few megabytes, not a gigabyte.
func hopPower(r io.Reader, hop int) (power []float64, samples int64, err error) {
	br := bufio.NewReaderSize(r, 1<<16)
	buf := make([]byte, hop*2)
	for {
		n, rerr := io.ReadFull(br, buf)
		n -= n % 2 // half a sample at the end of a file still being written
		if n > 0 {
			var sum float64
			for i := 0; i < n; i += 2 {
				v := float64(int16(binary.LittleEndian.Uint16(buf[i:]))) / 32768
				sum += v * v
			}
			// A short last hop is padded with silence, like the shorter track.
			power = append(power, sum/float64(hop))
			samples += int64(n / 2)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			return power, samples, nil
		}
		if rerr != nil {
			return nil, 0, rerr
		}
	}
}

// envelope turns hop powers into a level in dB every hop: the RMS of the
// window that begins there, never below floor. Past the end of the track is
// silence.
func envelope(power []float64, frames, window int, floor float64) []float64 {
	env := make([]float64, frames)
	for i := range env {
		var sum float64
		for j := i; j < i+window && j < len(power); j++ {
			sum += power[j]
		}
		env[i] = floor
		if sum > 0 {
			env[i] = math.Max(floor, 10*math.Log10(sum/float64(window)))
		}
	}
	return env
}

// onsets is the pattern an echo copies: how much the level rose in each hop,
// up to limit, and nothing where it fell or held.
//
// The level itself must not be correlated. Who is talking when is slow
// structure that the two tracks share for reasons that have nothing to do
// with echo — the caller answers when the assistant stops — and it would put
// a broad hump at the caller's reaction time. The rises are syllable-rate
// detail, which an echo repeats and a second voice does not. Falls are
// dropped because a fall is where the other party tends to begin.
func onsets(env []float64, limit float64) []float64 {
	d := make([]float64, len(env))
	for i := 1; i < len(env); i++ {
		if rise := env[i] - env[i-1]; rise > 0 {
			d[i] = math.Min(rise, limit)
		}
	}
	return d
}

// span is a run of frames, [from, to).
type span struct{ from, to int }

// stretches finds where the assistant speaks: runs of frames above speakingDB,
// joined across silences no longer than gap frames.
func stretches(env []float64, gap int) []span {
	var out []span
	for i, db := range env {
		if db <= speakingDB {
			continue
		}
		if n := len(out); n > 0 && i-out[n-1].to <= gap {
			out[n-1].to = i + 1
		} else {
			out = append(out, span{i, i + 1})
		}
	}
	return out
}

// correlate compares the assistant's onset pattern with the input's at every
// lag from 0 to maxLag frames: ref[t] against in[t+lag], over the frames t in
// spans and no others. The minutes in which the caller talks alone are not
// looked at, so they add nothing, not even noise. in must reach maxLag frames
// past the last span.
//
// r is the normalized cross-correlation, Pearson's.
//
// z is how far the same sum of products stands above zero, in its own
// standard errors: the sum divided by the root of its variance were the two
// tracks unrelated, that variance estimated from the products themselves. A
// rise spans the envelope window, so products come in runs of up to memory
// frames; the estimate counts a run as one thing (Bartlett weights over that
// many neighbours) rather than as several agreeing. With no echo z is close
// to a standard normal at each lag, whatever the line sounds like.
//
// z is what decides, and the reason is the quiet line. The obvious measure —
// r against the spread of r over the other lags — was built first and failed
// there. Between syllables a quiet input's pattern is almost nothing, so r is
// almost nothing at most lags, their spread is tiny, and two of the caller's
// syllables that happen to sit one delay after two of the assistant's stand
// ten "deviations" clear: one false echo in forty 30 s calls, and a
// "significant" peak in six single stretches out of ten. Divided by the
// scatter of its own products a coincidence is worth what it is: two events
// agreeing score √2 however loud they were, and an echo scores the root of
// the number of syllables it repeated.
func correlate(ref, in []float64, spans []span, maxLag, memory int) (r, z []float64) {
	r, z = make([]float64, maxLag+1), make([]float64, maxLag+1)
	var n, sa, saa float64
	longest := 0
	for _, s := range spans {
		longest = max(longest, s.to-s.from)
		for _, a := range ref[s.from:s.to] {
			n++
			sa += a
			saa += a * a
		}
	}
	if n < 2 {
		return r, z
	}
	meanA := sa / n
	varA := saa - sa*meanA
	if varA <= 0 {
		return r, z
	}
	products := make([]float64, longest)
	for lag := range r {
		var sb, sbb float64
		for _, s := range spans {
			for _, b := range in[s.from+lag : s.to+lag] {
				sb += b
				sbb += b * b
			}
		}
		meanB := sb / n
		varB := sbb - sb*meanB
		if varB <= 0 {
			continue
		}
		var sum, variance float64
		for _, s := range spans {
			a, b := ref[s.from:s.to], in[s.from+lag:s.to+lag]
			x := products[:len(a)]
			for i := range a {
				x[i] = (a[i] - meanA) * (b[i] - meanB)
				sum += x[i]
				variance += x[i] * x[i]
			}
			for k := 1; k <= memory && k < len(x); k++ {
				var c float64
				for i := k; i < len(x); i++ {
					c += x[i] * x[i-k]
				}
				variance += 2 * (1 - float64(k)/float64(memory+1)) * c
			}
		}
		r[lag] = sum / math.Sqrt(varA*varB)
		if variance > 0 {
			z[lag] = sum / math.Sqrt(variance)
		}
	}
	return r, z
}

// remove takes the echo at lag out of the input's pattern: the least-squares
// multiple of the assistant's pattern, moved to where the echo is.
//
// Speech has a rhythm, so an echo at one delay also correlates, more weakly,
// a syllable earlier and a syllable later — 200–300 ms either side. With the
// echo still in the input those shoulders pass for a second and third echo
// (they did: at 615 ms beside a real one at 320). With it taken out, whatever
// peak is left is something else coming back.
func remove(ref, in []float64, spans []span, lag int) {
	var n, sa, sb, saa, sab float64
	for _, s := range spans {
		a, b := ref[s.from:s.to], in[s.from+lag:s.to+lag]
		for i := range a {
			n++
			sa += a[i]
			sb += b[i]
			saa += a[i] * a[i]
			sab += a[i] * b[i]
		}
	}
	varA := saa - sa*sa/n
	if varA <= 0 {
		return
	}
	slope := (sab - sa*sb/n) / varA
	for _, s := range spans {
		a, b := ref[s.from:s.to], in[s.from+lag:s.to+lag]
		for i := range a {
			b[i] -= slope * a[i]
		}
	}
}

// best is the lag with the highest z that is a local maximum and at least
// apart frames from every lag in taken; -1 if there is none.
func best(z []float64, taken []peak, apart int) int {
	at := -1
search:
	for lag := range z {
		if at >= 0 && z[lag] <= z[at] {
			continue
		}
		if lag > 0 && z[lag-1] > z[lag] || lag+1 < len(z) && z[lag+1] > z[lag] {
			continue
		}
		for _, p := range taken {
			if abs(p.lag-lag) < apart {
				continue search
			}
		}
		at = lag
	}
	return at
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}

// quantile is the q-th quantile of sorted values, by linear interpolation.
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := q * float64(len(sorted)-1)
	lo, hi := int(math.Floor(pos)), int(math.Ceil(pos))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

// peak is one echo: a lag at which the input repeats the assistant.
type peak struct {
	lag int // frames
	// correlation is r at that lag in the call as recorded.
	correlation float64
	// significance is z at that lag: for the strongest echo as recorded, for
	// the others the lower of that and z with the stronger ones removed.
	significance float64
	// agreeing is how many stretches, correlated by themselves, have their
	// best lag within agreeWithin of this one — whether or not that stretch
	// was significant alone, and for a second echo leaving aside the lags of
	// the first. With no echo a stretch's best lag is anywhere, so each lands
	// this close about one time in forty.
	agreeing int
}

// finding is everything the probe has to say about one call, in frames; the
// caller knows how long a frame is.
type finding struct {
	speech    int // frames inside the assistant's stretches
	stretches int
	peaks     []peak // strongest first
	// strongest is the highest significance at any lag, echo or not: when
	// nothing is found it says whether that was a near miss.
	strongest float64

	// eligible is how many stretches were long enough to correlate alone, and
	// stretchLags the best lag of each whose own peak was significant, in
	// ascending order.
	eligible    int
	stretchLags []int
}

// analyse looks for the assistant's track (out) coming back in the caller's
// (in). Both are hop powers; frame is how long a hop lasts.
func analyse(out, in []float64, frame, maxLag time.Duration) finding {
	per := func(d time.Duration) int { return max(1, int((d+frame-1)/frame)) }
	lags := int(maxLag / frame)
	window := per(envelopeWindow)
	limit := onsetCap * frame.Seconds()
	frames := max(len(out), len(in))

	ref := envelope(out, frames, window, speakingDB)
	// The input is carried max-lag past its end, as silence, so that the last
	// answer of a call is searched as far as the first.
	heard := envelope(in, frames+lags, window, floorDB)
	spans := stretches(ref, per(stretchGap))
	a, b := onsets(ref, limit), onsets(heard, limit)

	f := finding{stretches: len(spans)}
	for _, s := range spans {
		f.speech += s.to - s.from
	}
	if len(spans) == 0 {
		return f
	}

	// Each answer by itself first, while the input is as recorded. A real
	// echo is in every one of them at the same delay; an accident of this
	// call's caller and noise is in one.
	var alone [][]float64 // z at every lag, for each stretch that has any
	for _, s := range spans {
		if s.to-s.from < per(minStretch) {
			continue
		}
		f.eligible++
		_, z := correlate(a, b, []span{s}, lags, window)
		at := best(z, nil, 0)
		if at < 0 || z[at] <= 0 {
			continue
		}
		alone = append(alone, z)
		if z[at] >= stretchSignificant {
			f.stretchLags = append(f.stretchLags, at)
		}
	}
	sort.Ints(f.stretchLags)

	// Then the whole call: the strongest echo, and with that one taken out
	// the next, until nothing left is significant.
	//
	// A second echo has to stand out twice: in the call as recorded and with
	// the stronger one removed. The second test is the one remove explains.
	// The first is there because removing an echo leaves something behind:
	// the line's noise shows in the echo's pauses and is drowned in its
	// syllables, a faint pattern that sits a pause-length before the echo
	// and, once nothing else is left to compare it with, reads as
	// significant (6.7, at 250 ms beside a real 320). As recorded, z at that
	// lag is negative.
	var rawR, rawZ []float64
	for len(f.peaks) < maxPeaks {
		r, z := correlate(a, b, spans, lags, window)
		if rawZ == nil {
			rawR, rawZ = r, z
			for _, v := range z {
				f.strongest = math.Max(f.strongest, v)
			}
		} else {
			for lag := range z {
				z[lag] = math.Min(z[lag], rawZ[lag])
			}
		}
		at := best(z, f.peaks, per(peakApart))
		if at < 0 || z[at] < significant {
			break
		}
		p := peak{lag: at, correlation: rawR[at], significance: z[at]}
		for _, z := range alone {
			// Where the stretch points once the echoes already found are set
			// aside: asked only for its single best lag, no stretch could
			// ever vouch for the weaker of two echoes.
			if lag := best(z, f.peaks, per(peakApart)); lag >= 0 && z[lag] > 0 && abs(lag-at) <= per(agreeWithin) {
				p.agreeing++
			}
		}
		f.peaks = append(f.peaks, p)
		remove(a, b, spans, at)
	}
	return f
}
