// Package focus keeps the conversation on one voice.
//
// A phone callee is rarely alone: a television, a colleague, a passenger. The
// recognizer transcribes all of it, and the engine would answer all of it. A
// speaker-activity model (a diarizer) run beside the audio says, for every
// frame, who is speaking; this package turns that into two judgments the
// engine can use without putting the model on the turn's critical path:
//
//   - which speaker is the primary — the one who answered the call — and
//   - for an utterance the engine has begun to answer, how much of it was the
//     primary's, so that an answer to somebody else's words can be abandoned
//     before it is heard.
//
// The model's labels arrive late: its input buffer alone is 320 ms behind the
// audio, and compute is on top. That is too late to decide whether a turn is
// over (the final transcript is in hand ~210 ms after the last sound), and
// early enough to veto an answer, which takes ~650 ms more to become audio.
// So nothing here waits for the model; the engine asks Attribute when it has
// something to decide, is told how much of the span has been labelled so far,
// and asks again on the next tick if that is not enough.
//
// The audio the recognizer hears is never changed. A model that suppresses
// other voices also deletes the primary's words sometimes; attributing the
// transcript after the fact cannot.
package focus

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Frame is the model's view of one stretch of audio: the probability that
// each speaker it knows was active, speakers numbered in the order the model
// first heard them.
type Frame struct {
	StartMS int64
	EndMS   int64
	// P is per speaker, index = speaker. Values in [0, 1].
	P []float32
}

// Enroller is a Scorer that can be told whose voice the caller's is: a
// target-speaker service (edge/tsv_server.py). The engine tells it the
// first utterance the caller alone made.
type Enroller interface {
	Enrol(fromMS, toMS int64) error
}

// Scorer is a speaker-activity model fed the caller's audio as it arrives.
type Scorer interface {
	// Push gives the model more audio: 16 kHz PCM16 mono, with the session
	// time its first sample belongs to.
	Push(pcm []byte, atMS int64) error
	// Frames is where labels come out, in time order, each later than the
	// one before. It is closed when the scorer is.
	Frames() <-chan Frame
	// Lag is how far behind the audio the labels run, by the model's own
	// account (its input buffer), excluding compute.
	Lag() time.Duration
	Close() error
}

// Config tunes the tracker.
type Config struct {
	// Active is the probability above which a speaker counts as speaking in a
	// frame.
	Active float32
	// EnrollMS is how much speech the first voice must have before it is the
	// primary.
	EnrollMS int64
	// HandoverMS is how long another voice must carry the conversation alone
	// — the primary silent throughout — before it becomes the primary.
	HandoverMS int64
	// Keep is how much labelled history to hold, for attribution.
	Keep time.Duration
}

// DefaultConfig is what a phone call gets.
func DefaultConfig() Config {
	return Config{Active: 0.5, EnrollMS: 600, HandoverMS: 4000, Keep: 90 * time.Second}
}

// Tracker consumes frames and keeps the primary speaker.
type Tracker struct {
	cfg Config

	mu      sync.Mutex
	frames  []Frame // in time order
	masked  []span  // when the assistant was speaking: its echo is not a speaker
	primary int     // -1 until enrolled
	// spoken is how many ms each speaker has been active, unmasked; masked
	// how many inside the assistant's own speech, where its echo lives.
	spoken   []int64
	maskedMS []int64
	// otherSince is when a speaker other than the primary began carrying the
	// conversation alone; 0 when the primary is heard.
	otherSince int64
	other      int
	handovers  int
	labelledTo int64
}

type span struct{ from, to int64 }

// NewTracker makes a tracker with cfg; zero fields take the defaults.
func NewTracker(cfg Config) *Tracker {
	d := DefaultConfig()
	if cfg.Active == 0 {
		cfg.Active = d.Active
	}
	if cfg.EnrollMS == 0 {
		cfg.EnrollMS = d.EnrollMS
	}
	if cfg.HandoverMS == 0 {
		cfg.HandoverMS = d.HandoverMS
	}
	if cfg.Keep == 0 {
		cfg.Keep = d.Keep
	}
	return &Tracker{cfg: cfg, primary: -1}
}

// Mask says the assistant was speaking from fromMS to toMS (session time): a
// speaker the model hears only then is the assistant's own voice coming back
// down the line, and is not a candidate for the primary.
func (t *Tracker) Mask(fromMS, toMS int64) {
	if toMS <= fromMS {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if n := len(t.masked); n > 0 && t.masked[n-1].to >= fromMS {
		if toMS > t.masked[n-1].to {
			t.masked[n-1].to = toMS
		}
		return
	}
	t.masked = append(t.masked, span{fromMS, toMS})
}

// MaskedWithin reports whether the assistant was speaking at any point of
// the span: what rules a span out as an enrolment.
func (t *Tracker) MaskedWithin(fromMS, toMS int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, m := range t.masked {
		if m.from < toMS && m.to > fromMS {
			return true
		}
	}
	return false
}

func (t *Tracker) isMasked(startMS, endMS int64) bool {
	mid := (startMS + endMS) / 2
	i := sort.Search(len(t.masked), func(i int) bool { return t.masked[i].to > mid })
	return i < len(t.masked) && t.masked[i].from <= mid
}

// Observe takes one frame from the scorer.
func (t *Tracker) Observe(f Frame) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if f.EndMS <= f.StartMS {
		return
	}
	t.frames = append(t.frames, f)
	t.labelledTo = f.EndMS
	// Trim history.
	keep := int64(t.cfg.Keep / time.Millisecond)
	for len(t.frames) > 0 && t.frames[0].EndMS < f.EndMS-keep {
		t.frames = t.frames[1:]
	}
	dur := f.EndMS - f.StartMS
	for len(t.spoken) < len(f.P) {
		t.spoken = append(t.spoken, 0)
		t.maskedMS = append(t.maskedMS, 0)
	}
	if t.isMasked(f.StartMS, f.EndMS) {
		// Who talks while the assistant talks is mostly the assistant,
		// coming back down the line; counted apart so the echo can be told
		// (Echo) and so it never enrols or hands over.
		for s, p := range f.P {
			if p >= t.cfg.Active {
				t.maskedMS[s] += dur
			}
		}
		return
	}
	loudest, loudestP := -1, float32(0)
	for s, p := range f.P {
		if p >= t.cfg.Active {
			t.spoken[s] += dur
			if p > loudestP {
				loudest, loudestP = s, p
			}
		}
	}
	if t.primary < 0 {
		// The first voice to say EnrollMS of speech is the one who answered.
		for s, ms := range t.spoken {
			if ms >= t.cfg.EnrollMS {
				t.primary = s
				break
			}
		}
		return
	}
	// Hand-over: somebody else talking, the primary silent, for HandoverMS.
	primaryActive := t.primary < len(f.P) && f.P[t.primary] >= t.cfg.Active
	switch {
	case primaryActive, loudest < 0:
		t.otherSince = 0
	case t.otherSince == 0 || loudest != t.other:
		t.otherSince, t.other = f.StartMS, loudest
	case f.EndMS-t.otherSince >= t.cfg.HandoverMS:
		t.primary = loudest
		t.otherSince = 0
		t.handovers++
	}
}

// Echo is the speaker the model hears mostly while the assistant speaks —
// the assistant's own voice returning through the caller's handset — or -1
// when no speaker looks like that yet. It takes echoMinMS of such activity
// and echoShare of the speaker's activity inside the assistant's speech;
// the primary is never the echo.
func (t *Tracker) Echo() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.echo()
}

const (
	echoMinMS = 800
	echoShare = 0.7
)

func (t *Tracker) echo() int {
	best, bestMS := -1, int64(0)
	for s := range t.maskedMS {
		if s == t.primary || t.maskedMS[s] < echoMinMS {
			continue
		}
		total := t.maskedMS[s] + t.spoken[s]
		if float64(t.maskedMS[s])/float64(total) < echoShare {
			continue
		}
		if t.maskedMS[s] > bestMS {
			best, bestMS = s, t.maskedMS[s]
		}
	}
	return best
}

// AttributeOver is Attribute for a span the assistant was speaking through:
// the masks are ignored, and the echo speaker (Echo) is read as silence. It
// is what says whether a sound over the answer was the caller — the one
// reason to stop talking — or the assistant's own return, a television, a
// bystander.
func (t *Tracker) AttributeOver(startMS, endMS int64) Attribution {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attribute(startMS, endMS, true, t.echo())
}

// Primary is the primary speaker's index, or -1 before one is known.
func (t *Tracker) Primary() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.primary
}

// Handovers is how many times the primary has changed.
func (t *Tracker) Handovers() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.handovers
}

// LabelledTo is the session time the model's labels reach.
func (t *Tracker) LabelledTo() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.labelledTo
}

// Attribution is who an utterance belonged to.
type Attribution struct {
	// Coverage is the share of the span the model has labelled so far; a
	// judgment on less than most of it is premature.
	Coverage float64
	// Primary is the share of the labelled, voiced frames in which the primary
	// was active; Others the share in which somebody else was active and the
	// primary was not.
	Primary float64
	Others  float64
	// VoicedMS is how much of the span the model heard anybody in.
	VoicedMS int64
	// Known is false before a primary speaker is enrolled: nothing can be
	// attributed, and nothing should be vetoed.
	Known bool
}

// Attribute says whose the audio from startMS to endMS was.
func (t *Tracker) Attribute(startMS, endMS int64) Attribution {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attribute(startMS, endMS, false, -1)
}

func (t *Tracker) attribute(startMS, endMS int64, throughMasks bool, echo int) Attribution {
	a := Attribution{Known: t.primary >= 0}
	if endMS <= startMS {
		return a
	}
	var labelled, primaryMS, othersMS, voiced int64
	for _, f := range t.frames {
		if f.EndMS <= startMS || f.StartMS >= endMS {
			continue
		}
		from, to := max64(f.StartMS, startMS), min64(f.EndMS, endMS)
		d := to - from
		labelled += d
		if !throughMasks && t.isMasked(f.StartMS, f.EndMS) {
			continue
		}
		anyone, prim := false, false
		for s, p := range f.P {
			if s == echo {
				continue
			}
			if p >= t.cfg.Active {
				anyone = true
				if s == t.primary {
					prim = true
				}
			}
		}
		if !anyone {
			continue
		}
		voiced += d
		if prim {
			primaryMS += d
		} else {
			othersMS += d
		}
	}
	a.Coverage = float64(labelled) / float64(endMS-startMS)
	a.VoicedMS = voiced
	if voiced > 0 {
		a.Primary = float64(primaryMS) / float64(voiced)
		a.Others = float64(othersMS) / float64(voiced)
	}
	return a
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// Run pumps a scorer's frames into the tracker until the scorer closes or ctx
// ends. onFrame, if set, is called after each frame is taken.
func Run(ctx context.Context, s Scorer, t *Tracker, onFrame func(Frame)) {
	for {
		select {
		case <-ctx.Done():
			return
		case f, ok := <-s.Frames():
			if !ok {
				return
			}
			t.Observe(f)
			if onFrame != nil {
				onFrame(f)
			}
		}
	}
}
