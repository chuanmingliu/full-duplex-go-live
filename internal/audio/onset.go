package audio

import "math"

// OnsetMethod names a fast-attack detector that augments the standard
// RMS+ZCR voiced decision, specifically for the barge-in path: a genuine
// interruption starts with a sharp transient (a plosive or fricative
// attack), while the assistant's own voice leaking back through a
// speakerphone arrives with a smooth, room- and speaker-shaped envelope. An
// onset detector reacts to that transient in a handful of samples rather
// than waiting out BargeInMinSpeechMS, so a real interrupt can be trusted
// sooner without lowering the bar that keeps echo out.
//
// The empty string is OnsetOff: no detector runs, and every frame scores
// zero. This keeps a deployment that never sets onset_method byte-for-byte
// identical to before this package existed.
type OnsetMethod string

const (
	OnsetOff      OnsetMethod = ""
	OnsetTKEO     OnsetMethod = "tkeo"
	OnsetEnvelope OnsetMethod = "envelope"
	OnsetFlux     OnsetMethod = "flux"
)

// ValidOnsetMethod reports whether s names a method NewOnsetDetector
// understands, including the empty string (off).
func ValidOnsetMethod(s string) bool {
	switch OnsetMethod(s) {
	case OnsetOff, OnsetTKEO, OnsetEnvelope, OnsetFlux:
		return true
	default:
		return false
	}
}

// OnsetScore is one detector's verdict on the frame it just consumed.
type OnsetScore struct {
	// Value is the frame's raw score in the detector's own log-compressed
	// units, exposed for logging and tuning. Only differences and margins
	// are meaningful across methods — the units are not calibrated to dBFS.
	Value float64
	// Strong is true when Value cleared the detector's adaptive floor by its
	// configured margin: this frame looks like the attack of a fresh
	// utterance, not steady background energy.
	Strong bool
}

// OnsetDetector scores each frame for how sharp its onset transient looks.
// A detector instance belongs to one VAD, one goroutine, same as the VAD
// itself.
type OnsetDetector interface {
	Push(frame []float32) OnsetScore
}

// NewOnsetDetector builds the detector named by method. An unrecognized or
// empty method returns a detector that always reports Strong: false, so
// callers never need a nil check.
func NewOnsetDetector(method OnsetMethod, sampleRate int, marginDB float64) OnsetDetector {
	var raw rawScorer
	def := float64(defaultOnsetMarginDB)
	switch method {
	case OnsetTKEO:
		raw = &tkeoScorer{}
	case OnsetEnvelope:
		raw = &envelopeScorer{}
	case OnsetFlux:
		raw = newFluxScorer(sampleRate)
		// Summed across several Goertzel bands, flux's frame-to-frame
		// jitter on a steady signal runs wider than a single amplitude- or
		// energy-domain reading does, so the margin that keeps TKEO and the
		// envelope detector quiet at rest is too tight here and false-fires
		// on nothing more than window-boundary noise. Measured against a
		// steady tone in onset_test.go; revisit with real call audio.
		def = defaultFluxMarginDB
	default:
		return noneDetector{}
	}
	if marginDB <= 0 {
		marginDB = def
	}
	return &onsetDetector{raw: raw, marginDB: marginDB}
}

// defaultOnsetMarginDB and defaultFluxMarginDB are starting points, not
// calibrated constants: like VADConfig's own SpeechMarginDB and
// BargeInMarginDB, the right value depends on mic gain and room noise, and
// a deployment enabling onset_method should expect to tune onset_margin_db
// against its own call audio rather than trust these blind.
const defaultOnsetMarginDB = 9
const defaultFluxMarginDB = 18

// minOnsetFloorDB is a last-resort floor for pathological cases (a raw
// score so small it underflows toward zero without being exactly zero).
// The Push zero-check above handles the ordinary case; this just keeps the
// floor from wandering to -Inf if it ever slips through.
const minOnsetFloorDB = -160

type noneDetector struct{}

func (noneDetector) Push([]float32) OnsetScore { return OnsetScore{} }

// rawScorer computes one frame's non-negative, unbounded attack feature.
// Bigger means a sharper transient; the unit is whatever the underlying
// operator produces (energy-squared for TKEO, an RMS delta for the
// envelope, a magnitude sum for flux) — onsetDetector log-compresses
// whichever one it is fed and never compares across methods.
type rawScorer interface {
	score(frame []float32) float64
}

// onsetDetector turns a rawScorer into an OnsetScore by log-compressing its
// output and tracking an adaptive floor, deliberately mirroring VAD's own
// noiseFloorDB: attack fast toward a rising floor, release slow toward a
// falling one, and only adapt on frames that are not themselves strong —
// otherwise a sustained loud onset would drag the floor up underneath
// itself and never clear its own margin.
type onsetDetector struct {
	raw      rawScorer
	floorDB  float64
	marginDB float64
	primed   bool
}

func (d *onsetDetector) Push(frame []float32) OnsetScore {
	r := d.raw.score(frame)
	if r <= 0 {
		// No detectable feature at all — most often true digital silence (a
		// muted or gated mic, or simply nothing happening yet). There is
		// nothing to learn from an exact zero: every method's floor is
		// already tracking whatever small nonzero jitter real audio
		// produces even at rest, and letting a zero pull it toward
		// -infinity would make the next real reading, however gentle, look
		// like an enormous jump by comparison. Skip adaptation and report
		// not-strong.
		return OnsetScore{}
	}
	level := 10 * math.Log10(r)

	if !d.primed {
		// Bootstrap on the first real reading rather than an arbitrary
		// constant: these three raw scores live on completely different
		// natural scales (TKEO's amplitude-squared, envelope's RMS delta,
		// flux's multi-band magnitude sum), and a mic's actual gain moves
		// all three further still. Starting from whatever this deployment's
		// audio actually produces beats guessing at a number that fits none
		// of them.
		d.floorDB = level
		d.primed = true
		return OnsetScore{Value: level}
	}

	strong := level > d.floorDB+d.marginDB
	if !strong {
		const attack, release = 0.05, 0.2
		if level > d.floorDB {
			d.floorDB += (level - d.floorDB) * attack
		} else {
			d.floorDB += (level - d.floorDB) * release
		}
		if d.floorDB < minOnsetFloorDB {
			d.floorDB = minOnsetFloorDB
		}
	}
	return OnsetScore{Value: level, Strong: strong}
}

// tkeoScorer implements the Teager-Kaiser Energy Operator,
// psi[n] = x[n]^2 - x[n-1]*x[n+1], a per-sample nonlinear operator with no
// window and no FFT. It is read in the literature as tracking the product
// of a signal's instantaneous amplitude and frequency, which is what makes
// it react to a sharp attack within a few samples rather than the ~20ms an
// RMS frame needs to accumulate: "second derivative on the raw waveform,
// computed cheaply" is a fair one-line description of what it approximates.
//
// psi[n] needs x[n-1] and x[n+1]; the one previous sample carried across
// Push calls covers the left neighbour at a frame's first index, and the
// last index of each frame replicate-pads its missing right neighbour
// (reuses x[n] for x[n+1]) rather than waiting a sample into the next
// frame. That biases one sample in several hundred — the frame score is a
// peak over the whole frame, so it is lost in the noise.
type tkeoScorer struct {
	prev1  float32
	primed bool
}

func (s *tkeoScorer) score(frame []float32) float64 {
	if len(frame) == 0 {
		return 0
	}
	xm1 := s.prev1
	if !s.primed {
		xm1 = frame[0]
	}
	var peak float64
	for i, x := range frame {
		var xp1 float32
		if i == len(frame)-1 {
			xp1 = x
		} else {
			xp1 = frame[i+1]
		}
		psi := float64(x)*float64(x) - float64(xm1)*float64(xp1)
		if psi > peak {
			peak = psi
		}
		xm1 = x
	}
	s.prev1 = frame[len(frame)-1]
	s.primed = true
	return peak
}

// envelopeScorer differentiates the frame RMS twice: once for the slope,
// once more for the slope's own rate of change. That second difference is
// literally the "second derivative on the waveform" idea in its plainest
// form — an accelerating rise in loudness, not just a rise. Only the
// positive half (the envelope speeding up, not slowing down) counts as an
// onset signal; a decaying tail decelerates too and is not one.
type envelopeScorer struct {
	rms1, rms2 float64
	primed     int
}

func (s *envelopeScorer) score(frame []float32) float64 {
	rms := RMS(frame)
	accel := (rms - s.rms1) - (s.rms1 - s.rms2)
	s.rms2, s.rms1 = s.rms1, rms
	if s.primed < 2 {
		s.primed++
		return 0
	}
	if accel < 0 {
		accel = 0
	}
	return accel
}

// fluxScorer computes spectral flux — the frame-to-frame rise in spectral
// magnitude, summed across a handful of bands and half-wave rectified so
// only rising energy counts — using a bank of Goertzel filters rather than
// an FFT. Goertzel is the standard cheap-DFT choice for scoring a handful
// of known frequencies per frame (the same algorithm telephony gear uses
// for DTMF), and it is a better fit here than a full FFT: golive already
// works in the telephone band, and flux only needs coarse spectral shape,
// not a full-resolution spectrum.
//
// Flux is the most robust of the three to a loud-but-steady noise (a hum,
// steady echo) because it looks at spectral shape changing, not overall
// level — and the most expensive, at one Goertzel pass per band per frame.
type fluxScorer struct {
	sampleRate int
	bandHz     []float64
	prevMag    []float64
	primed     bool
}

func newFluxScorer(sampleRate int) *fluxScorer {
	if sampleRate <= 0 {
		sampleRate = RatePipeline
	}
	// Log-spaced across the telephone band and a little above it, where
	// consonant and fricative onset energy concentrates.
	bands := []float64{300, 500, 800, 1200, 1800, 2600, 3400, 4500}
	nyquist := float64(sampleRate) / 2
	kept := bands[:0:0]
	for _, f := range bands {
		if f < nyquist {
			kept = append(kept, f)
		}
	}
	if len(kept) == 0 {
		kept = []float64{nyquist * 0.5}
	}
	return &fluxScorer{sampleRate: sampleRate, bandHz: kept, prevMag: make([]float64, len(kept))}
}

func goertzelMagnitude(frame []float32, freqHz float64, sampleRate int) float64 {
	n := len(frame)
	if n == 0 {
		return 0
	}
	omega := 2 * math.Pi * freqHz / float64(sampleRate)
	coeff := 2 * math.Cos(omega)
	var s1, s2 float64
	for _, x := range frame {
		s0 := float64(x) + coeff*s1 - s2
		s2 = s1
		s1 = s0
	}
	real := s1 - s2*math.Cos(omega)
	imag := s2 * math.Sin(omega)
	return math.Hypot(real, imag) / float64(n)
}

func (s *fluxScorer) score(frame []float32) float64 {
	var flux float64
	for i, f := range s.bandHz {
		mag := goertzelMagnitude(frame, f, s.sampleRate)
		if s.primed {
			if d := mag - s.prevMag[i]; d > 0 {
				flux += d
			}
		}
		s.prevMag[i] = mag
	}
	s.primed = true
	return flux
}
