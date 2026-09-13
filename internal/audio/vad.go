package audio

import (
	"math"
	"sync/atomic"
	"time"
)

// DecisionKind is what the VAD concluded about the frame it just consumed.
type DecisionKind int

const (
	// DecisionNone means silence, or speech too short to have opened a turn yet.
	DecisionNone DecisionKind = iota
	// DecisionStarted means an utterance just opened.
	DecisionStarted
	// DecisionSpeaking carries a progressive snapshot of an open utterance so
	// streaming ASR can transcribe while the user is still talking.
	DecisionSpeaking
	// DecisionStopped closes an utterance and carries the whole thing.
	DecisionStopped
)

// Decision is the VAD's verdict on one frame.
type Decision struct {
	Kind DecisionKind

	// Utterance is the whole padded utterance on DecisionStopped.
	Utterance []float32
	// Snapshot is the audio not yet handed out, and is set on Started,
	// Speaking and Stopped alike. Concatenating every Snapshot across one
	// utterance reproduces Utterance exactly, so a streaming ASR adapter can
	// forward snapshots as they arrive and a batch adapter can wait for
	// Utterance, with no duplicated or dropped audio either way.
	Snapshot []float32

	// ActiveMS is how much voiced audio the utterance contains (padding excluded).
	ActiveMS float64
	// StartMS and EndMS are session-relative timestamps, matching the
	// start_ms/end_ms fields the live protocol puts on transcript deltas.
	StartMS int64
	EndMS   int64

	// BargeIn marks an utterance that opened while the assistant was speaking.
	BargeIn bool
}

// VADConfig tunes detection. Zero values are replaced by the defaults in
// DefaultVADConfig, so a partially-filled struct is safe.
type VADConfig struct {
	SampleRate int
	FrameMS    int

	// MinSpeechMS is how much voiced audio must accumulate before an utterance
	// is declared open. It filters coughs, clicks and door slams.
	MinSpeechMS int
	// MinSilenceMS is the trailing silence that closes an utterance.
	MinSilenceMS int
	// SpeechPadMS of audio before the detected onset is prepended to the
	// utterance, so ASR does not lose the attack of the first phoneme.
	SpeechPadMS int
	// MaxSpeechMS force-closes a runaway utterance.
	MaxSpeechMS int
	// SnapshotMS is how often an open utterance emits a progressive snapshot.
	SnapshotMS int

	// NoiseFloorDB seeds the adaptive floor and is also its lower bound.
	// Bounding it matters more than seeding it: a muted or gated microphone
	// sends digital silence, and an unbounded floor would chase that toward
	// -180 dBFS and then treat the faintest hiss as speech.
	NoiseFloorDB float64
	// MaxNoiseFloorDB bounds the floor from above so a loud, sustained talker
	// cannot drag it up over their own voice.
	MaxNoiseFloorDB float64
	// SpeechMarginDB is how far above the running noise floor a frame must sit
	// to count as voiced.
	SpeechMarginDB float64
	// MaxZCR rejects frames whose zero-crossing rate looks like hiss rather
	// than voiced speech.
	MaxZCR float64
	// GapToleranceMS is how long a candidate utterance may go unvoiced before
	// it is discarded as noise rather than treated as a gap between syllables.
	GapToleranceMS int

	// BargeInMarginDB is added to SpeechMarginDB while the assistant is
	// speaking. Without it, the assistant's own voice leaking back through a
	// speakerphone reads as a barge-in and the session talks over itself.
	BargeInMarginDB float64
	// BargeInMinSpeechMS likewise raises MinSpeechMS during playback.
	BargeInMinSpeechMS int
}

// DefaultVADConfig returns values tuned for 16 kHz conversational speech on a
// laptop or phone handset.
func DefaultVADConfig() VADConfig {
	return VADConfig{
		SampleRate:         RatePipeline,
		FrameMS:            20,
		MinSpeechMS:        200,
		MinSilenceMS:       380,
		SpeechPadMS:        240,
		MaxSpeechMS:        30000,
		SnapshotMS:         200,
		NoiseFloorDB:       -60,
		MaxNoiseFloorDB:    -28,
		SpeechMarginDB:     10,
		MaxZCR:             0.55,
		GapToleranceMS:     160,
		BargeInMarginDB:    6,
		BargeInMinSpeechMS: 320,
	}
}

func (c *VADConfig) applyDefaults() {
	d := DefaultVADConfig()
	if c.SampleRate <= 0 {
		c.SampleRate = d.SampleRate
	}
	if c.FrameMS <= 0 {
		c.FrameMS = d.FrameMS
	}
	if c.MinSpeechMS <= 0 {
		c.MinSpeechMS = d.MinSpeechMS
	}
	if c.MinSilenceMS <= 0 {
		c.MinSilenceMS = d.MinSilenceMS
	}
	if c.SpeechPadMS < 0 {
		c.SpeechPadMS = d.SpeechPadMS
	}
	if c.MaxSpeechMS <= 0 {
		c.MaxSpeechMS = d.MaxSpeechMS
	}
	if c.SnapshotMS <= 0 {
		c.SnapshotMS = d.SnapshotMS
	}
	if c.NoiseFloorDB == 0 {
		c.NoiseFloorDB = d.NoiseFloorDB
	}
	if c.MaxNoiseFloorDB == 0 || c.MaxNoiseFloorDB < c.NoiseFloorDB {
		c.MaxNoiseFloorDB = d.MaxNoiseFloorDB
	}
	if c.SpeechMarginDB <= 0 {
		c.SpeechMarginDB = d.SpeechMarginDB
	}
	if c.MaxZCR <= 0 {
		c.MaxZCR = d.MaxZCR
	}
	if c.GapToleranceMS <= 0 {
		c.GapToleranceMS = d.GapToleranceMS
	}
	if c.BargeInMarginDB < 0 {
		c.BargeInMarginDB = d.BargeInMarginDB
	}
	if c.BargeInMinSpeechMS <= 0 {
		c.BargeInMinSpeechMS = d.BargeInMinSpeechMS
	}
}

// VAD is a streaming energy/ZCR voice-activity detector with an adaptive noise
// floor.
//
// It deliberately avoids a neural detector: golive must run one VAD per
// concurrent session on commodity hardware, the duplex engine already
// cross-checks onsets against streaming ASR partials, and a mis-fire costs a
// cancelled speculative turn rather than a wrong answer.
//
// A VAD instance belongs to exactly one goroutine.
type VAD struct {
	cfg VADConfig

	frameSamples int
	frameMS      float64

	// preroll is a ring of recent frames used to pad an utterance backwards.
	preroll    [][]float32
	prerollCap int
	prerollPos int
	prerollLen int

	open         bool
	bargeIn      bool
	utterance    []float32
	activeMS     float64
	silenceMS    float64
	pendingMS    float64 // voiced audio seen while still below MinSpeechMS
	pendingGapMS float64 // unvoiced audio inside the current candidate
	pending      []float32
	snapshotAcc  []float32
	sinceSnap    float64

	startMS int64
	clockMS float64

	noiseFloorDB float64

	// speaking mirrors whether the assistant is currently producing audio. It
	// is the one field written from another goroutine — the player's — because
	// listening and speaking are concurrent by design, so it is atomic rather
	// than plain.
	speaking atomic.Bool
}

// NewVAD builds a detector. cfg is copied; later mutation has no effect.
func NewVAD(cfg VADConfig) *VAD {
	cfg.applyDefaults()
	frameSamples := cfg.SampleRate * cfg.FrameMS / 1000
	prerollCap := cfg.SpeechPadMS / cfg.FrameMS
	if prerollCap < 1 {
		prerollCap = 1
	}
	return &VAD{
		cfg:          cfg,
		frameSamples: frameSamples,
		frameMS:      float64(cfg.FrameMS),
		preroll:      make([][]float32, prerollCap),
		prerollCap:   prerollCap,
		noiseFloorDB: cfg.NoiseFloorDB,
	}
}

// FrameSamples is the frame size the VAD expects, in samples.
func (v *VAD) FrameSamples() int { return v.frameSamples }

// FrameBytes is the frame size the VAD expects, in PCM16 bytes.
func (v *VAD) FrameBytes() int { return v.frameSamples * BytesPerSample }

// SetAssistantSpeaking tells the VAD whether the assistant is currently
// producing audio. This is the single most important switch for simulated full
// duplex: listening never stops, but the bar for interrupting rises while we
// are talking, so acoustic echo does not read as a barge-in.
func (v *VAD) SetAssistantSpeaking(speaking bool) { v.speaking.Store(speaking) }

// NoiseFloorDB exposes the adaptive floor for diagnostics.
func (v *VAD) NoiseFloorDB() float64 { return v.noiseFloorDB }

// TrailingSilenceMS reports how long the open utterance has been unvoiced, and
// zero when no utterance is open.
//
// This is the acoustic evidence that the speaker has paused, and it is a far
// better trigger for speculation than a transcript that stopped changing: a
// recognizer goes quiet both when the speaker stops talking and when it is
// merely behind, and only one of those is worth guessing on.
func (v *VAD) TrailingSilenceMS() float64 {
	if !v.open {
		return 0
	}
	return v.silenceMS
}

// Reset drops any open utterance without emitting it.
func (v *VAD) Reset() {
	v.open = false
	v.bargeIn = false
	v.utterance = nil
	v.pending = nil
	v.snapshotAcc = nil
	v.activeMS = 0
	v.pendingMS = 0
	v.pendingGapMS = 0
	v.silenceMS = 0
	v.sinceSnap = 0
}

func (v *VAD) thresholds() (marginDB float64, minSpeechMS int) {
	marginDB = v.cfg.SpeechMarginDB
	minSpeechMS = v.cfg.MinSpeechMS
	if v.speaking.Load() {
		marginDB += v.cfg.BargeInMarginDB
		if v.cfg.BargeInMinSpeechMS > minSpeechMS {
			minSpeechMS = v.cfg.BargeInMinSpeechMS
		}
	}
	return marginDB, minSpeechMS
}

// Push consumes one frame and returns the resulting decision. The frame must
// be FrameSamples() long; shorter frames are zero-padded, longer ones truncated.
func (v *VAD) Push(frame []float32) Decision {
	if len(frame) != v.frameSamples {
		fixed := make([]float32, v.frameSamples)
		copy(fixed, frame)
		frame = fixed
	}
	v.clockMS += v.frameMS

	level := DBFS(RMS(frame))
	zcr := ZeroCrossingRate(frame)
	marginDB, minSpeechMS := v.thresholds()

	// The floor starts at the configured value rather than at the first frame.
	// Seeding from frame one looks smarter and is actively wrong: a caller who
	// is already mid-sentence when the socket opens would calibrate the floor
	// to their own voice and then never be heard.
	voiced := level > v.noiseFloorDB+marginDB && zcr <= v.cfg.MaxZCR

	// Adapt the floor only on frames we believe are not speech, and only
	// upward slowly / downward quickly, so a sustained talker cannot drag the
	// floor up over their own voice.
	if !voiced && !v.open {
		const attack, release = 0.02, 0.25
		if level > v.noiseFloorDB {
			v.noiseFloorDB += (level - v.noiseFloorDB) * attack
		} else {
			v.noiseFloorDB += (level - v.noiseFloorDB) * release
		}
		switch {
		case math.IsNaN(v.noiseFloorDB) || math.IsInf(v.noiseFloorDB, 0):
			v.noiseFloorDB = v.cfg.NoiseFloorDB
		case v.noiseFloorDB < v.cfg.NoiseFloorDB:
			v.noiseFloorDB = v.cfg.NoiseFloorDB
		case v.noiseFloorDB > v.cfg.MaxNoiseFloorDB:
			v.noiseFloorDB = v.cfg.MaxNoiseFloorDB
		}
	}

	if !v.open {
		return v.pushClosed(frame, voiced, minSpeechMS)
	}
	return v.pushOpen(frame, voiced)
}

func (v *VAD) pushClosed(frame []float32, voiced bool, minSpeechMS int) Decision {
	v.pushPreroll(frame)
	if !voiced {
		// Speech is not continuously voiced — stops, unvoiced consonants and
		// the troughs between syllables all read as silence at frame scale. So
		// a short gap is tolerated inside a candidate, and only a real pause
		// discards it. Without the tolerance the candidate resets every
		// syllable and no utterance ever reaches the minimum; without the
		// limit, two coughs a second apart would sum past it.
		if v.pendingMS > 0 {
			v.pendingGapMS += v.frameMS
			if v.pendingGapMS > float64(v.cfg.GapToleranceMS) {
				v.pendingMS = 0
				v.pendingGapMS = 0
				v.pending = nil
			} else {
				v.pending = append(v.pending, frame...)
			}
		}
		return Decision{Kind: DecisionNone}
	}

	v.pendingGapMS = 0
	v.pending = append(v.pending, frame...)
	v.pendingMS += v.frameMS
	if v.pendingMS < float64(minSpeechMS) {
		return Decision{Kind: DecisionNone}
	}

	// Promote the candidate to a real utterance, prepending the pre-roll so the
	// onset is not clipped.
	v.open = true
	v.bargeIn = v.speaking.Load()
	v.utterance = append(v.prerollAudio(), v.pending...)
	v.activeMS = v.pendingMS
	v.silenceMS = 0
	v.pending = nil
	v.pendingMS = 0
	v.pendingGapMS = 0
	v.snapshotAcc = nil
	v.sinceSnap = 0
	padMS := float64(v.prerollLen) * v.frameMS
	v.startMS = int64(math.Max(0, v.clockMS-v.activeMS-padMS))
	v.clearPreroll()

	// The opening snapshot is everything accumulated so far: the pre-roll pad
	// plus the audio that proved the utterance real.
	opening := make([]float32, len(v.utterance))
	copy(opening, v.utterance)

	return Decision{
		Kind:     DecisionStarted,
		Snapshot: opening,
		ActiveMS: v.activeMS,
		StartMS:  v.startMS,
		EndMS:    int64(v.clockMS),
		BargeIn:  v.bargeIn,
	}
}

func (v *VAD) pushOpen(frame []float32, voiced bool) Decision {
	v.utterance = append(v.utterance, frame...)
	v.snapshotAcc = append(v.snapshotAcc, frame...)
	v.sinceSnap += v.frameMS

	if voiced {
		v.activeMS += v.frameMS
		v.silenceMS = 0
	} else {
		v.silenceMS += v.frameMS
	}

	totalMS := float64(len(v.utterance)) / float64(v.cfg.SampleRate) * 1000
	closing := v.silenceMS >= float64(v.cfg.MinSilenceMS) || totalMS >= float64(v.cfg.MaxSpeechMS)

	if closing {
		utterance := v.utterance
		tail := v.snapshotAcc
		activeMS := v.activeMS
		startMS := v.startMS
		bargeIn := v.bargeIn
		v.Reset()
		return Decision{
			Kind:      DecisionStopped,
			Utterance: utterance,
			Snapshot:  tail,
			ActiveMS:  activeMS,
			StartMS:   startMS,
			EndMS:     int64(v.clockMS),
			BargeIn:   bargeIn,
		}
	}

	if v.sinceSnap >= float64(v.cfg.SnapshotMS) {
		snapshot := v.snapshotAcc
		v.snapshotAcc = nil
		v.sinceSnap = 0
		return Decision{
			Kind:     DecisionSpeaking,
			Snapshot: snapshot,
			ActiveMS: v.activeMS,
			StartMS:  v.startMS,
			EndMS:    int64(v.clockMS),
			BargeIn:  v.bargeIn,
		}
	}

	return Decision{Kind: DecisionNone}
}

func (v *VAD) pushPreroll(frame []float32) {
	buf := make([]float32, len(frame))
	copy(buf, frame)
	v.preroll[v.prerollPos] = buf
	v.prerollPos = (v.prerollPos + 1) % v.prerollCap
	if v.prerollLen < v.prerollCap {
		v.prerollLen++
	}
}

func (v *VAD) prerollAudio() []float32 {
	if v.prerollLen == 0 {
		return nil
	}
	out := make([]float32, 0, v.prerollLen*v.frameSamples)
	start := (v.prerollPos - v.prerollLen + v.prerollCap) % v.prerollCap
	for i := 0; i < v.prerollLen; i++ {
		out = append(out, v.preroll[(start+i)%v.prerollCap]...)
	}
	return out
}

func (v *VAD) clearPreroll() {
	for i := range v.preroll {
		v.preroll[i] = nil
	}
	v.prerollPos = 0
	v.prerollLen = 0
}

// ElapsedMS is the session-relative clock the VAD has advanced, derived purely
// from the audio it has consumed rather than wall time. Timestamps stay correct
// when a client bursts a backlog of audio after a network stall.
func (v *VAD) ElapsedMS() int64 { return int64(v.clockMS) }

// Elapsed is ElapsedMS as a duration.
func (v *VAD) Elapsed() time.Duration { return time.Duration(v.clockMS) * time.Millisecond }
