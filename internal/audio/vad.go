package audio

import (
	"math"
	"sync/atomic"
	"time"
)

// The echo return estimator's constants. See trackEchoReturn.
const (
	// echoOnsetMarginDB is how far over the noise floor a frame must sit to be
	// the assistant's voice arriving back, and, before that, to say the line
	// was not quiet. It is lower than the margin speech is judged by: an echo
	// is quieter than a caller, and nothing is decided on it.
	echoOnsetMarginDB = 8.0
	// echoQuietBeforeMS is how long the input must have been quiet when the
	// assistant starts for a sound that follows to be taken as its echo.
	echoQuietBeforeMS = 200.0
	// A return sooner than one frame cannot be told from sound that was
	// already on its way in, and one later than a second and a half is not an
	// echo of the start of anything.
	echoReturnMinMS = 20.0
	echoReturnMaxMS = 1500.0
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

	// SilenceMS, on DecisionStopped, is the trailing silence that closed the
	// utterance: how long ago the speaker's last sound was.
	SilenceMS float64
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
	// FloorTrackMS is how long an open utterance may go without a single
	// frame under the bar before the floor is raised to the quietest frame
	// in that time. Speech has gaps between words that fall to the line's
	// own level; a subway, a street or a carrier's comfort noise does not,
	// and the floor, which adapts only on frames it believes are not speech,
	// can otherwise never learn a noise that sits above the bar: a real call
	// held an utterance open for 30 s that way. Zero disables it.
	FloorTrackMS int
	// TraceMS is how often a sample of the gate — levels, floor, bar, state —
	// is kept for the record (see Trace). Zero keeps none.
	TraceMS int
	// EchoTailMS keeps that raised bar up for this long after the assistant
	// stops producing audio. On a phone line the caller is still hearing it —
	// a gateway's playout buffer, the carrier, the handset — and what comes
	// back through their microphone in that time is the end of the assistant's
	// own sentence, arriving after this side believes it has finished.
	EchoTailMS int
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
		SnapshotMS:         40,
		NoiseFloorDB:       -60,
		MaxNoiseFloorDB:    -28,
		SpeechMarginDB:     10,
		MaxZCR:             0.55,
		GapToleranceMS:     160,
		BargeInMarginDB:    6,
		BargeInMinSpeechMS: 320,
		FloorTrackMS:       2000,
		TraceMS:            100,
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
	if c.FloorTrackMS == 0 {
		c.FloorTrackMS = d.FloorTrackMS
	}
	if c.TraceMS == 0 {
		c.TraceMS = d.TraceMS
	}
}

// GateSample is the state of the gate over one slice of the call, kept so
// that a call can be looked at afterwards: what the line measured against
// what the gate believed. Levels are dBFS to a tenth.
type GateSample struct {
	// MS is the slice's start on the audio clock.
	MS int `json:"t"`
	// Hi and Lo are the loudest and quietest frame in the slice.
	Hi float64 `json:"hi"`
	Lo float64 `json:"lo"`
	// Floor is the noise floor and Bar the level a frame had to exceed to be
	// voiced, at the end of the slice.
	Floor float64 `json:"f"`
	Bar   float64 `json:"b"`
	// VoicedMS is how much of the slice was judged voiced.
	VoicedMS int `json:"v"`
	// Open says an utterance was open at the end of the slice, Speaking that
	// the assistant was producing audio, Raised that the floor was raised to
	// the quietest recent frame during the slice (see FloorTrackMS).
	Open     bool `json:"o,omitempty"`
	Speaking bool `json:"s,omitempty"`
	Raised   bool `json:"r,omitempty"`
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
	// voicedRunMS is how long the open utterance has been continuously voiced
	// since its last unvoiced frame.
	voicedRunMS float64
	// minSilenceMS overrides cfg.MinSilenceMS for the open utterance when
	// positive. See SetMinSilenceMS.
	minSilenceMS int

	startMS int64
	clockMS float64

	noiseFloorDB float64
	// echoFloorDB is how loud our own output comes back through the caller's
	// speaker, measured while we are talking. It is a diagnostic and takes no
	// part in the voiced decision — see Push.
	echoFloorDB float64

	// speaking mirrors whether the assistant is currently producing audio. It
	// is the one field written from another goroutine — the player's — because
	// listening and speaking are concurrent by design, so it is atomic rather
	// than plain.
	speaking atomic.Bool
	// clockAtomic mirrors clockMS for the goroutine that flips speaking, and
	// echoTailUntil is the audio-clock moment the raised bar comes back down.
	clockAtomic   atomic.Int64
	echoTailUntil atomic.Int64

	// echoArmAt is the input clock at the moment the assistant last began to
	// produce audio, left by the goroutine that flips speaking for Push to
	// collect, and echoNotArmed when there is nothing to collect. The rest of
	// the echo return estimator is Push's own. See trackEchoReturn.
	echoArmAt atomic.Int64
	// quietMS is how long the input has gone without a frame over the onset
	// bar.
	quietMS float64
	// echoArmed is true while a return is being waited for, and echoArmedAtMS
	// is the clock it is timed from.
	echoArmed     bool
	echoArmedAtMS float64
	// echoReturnMS and echoReturnDB are a measurement nobody has taken yet.
	echoReturnMS    float64
	echoReturnDB    float64
	echoReturnReady bool
	// echoMiss is an attempt that produced no measurement, and why, for
	// whoever wants to know; nobody has taken it yet when echoMissReady.
	echoMiss      EchoMiss
	echoMissReady bool

	// The window-minimum tracker (FloorTrackMS): the quietest frame of each
	// 100 ms block, the last few blocks, and the block being filled.
	blockMin   float64
	blockMS    float64
	blockMins  []float64
	blockPos   int
	blockCount int
	floorRaise struct {
		from, to float64
		ready    bool
	}

	// The trace (TraceMS): the sample being filled and the ones kept.
	trace   []GateSample
	cur     GateSample
	curMS   float64
	curSet  bool
	curRais bool
}

// FloorRaise is the last time the floor was raised to the quietest recent
// frame, once: from and to in dBFS. Nobody has taken it when !ok.
func (v *VAD) TakeFloorRaise() (from, to float64, ok bool) {
	if !v.floorRaise.ready {
		return 0, 0, false
	}
	v.floorRaise.ready = false
	return v.floorRaise.from, v.floorRaise.to, true
}

// FloorTrackMS is the window the floor tracker uses, after defaults.
func (v *VAD) FloorTrackMS() int { return v.cfg.FloorTrackMS }

// Trace is every gate sample kept so far (see GateSample). The slice is the
// detector's own; read it, do not keep it.
func (v *VAD) Trace() []GateSample {
	return v.trace
}

// maxTraceSamples bounds the record: two hours at 100 ms.
const maxTraceSamples = 2 * 60 * 60 * 10

// sample folds one frame into the trace.
func (v *VAD) sample(level float64, voiced bool) {
	if v.cfg.TraceMS <= 0 {
		return
	}
	if !v.curSet {
		v.cur = GateSample{MS: int(v.clockMS - v.frameMS), Hi: level, Lo: level}
		v.curSet = true
	}
	if level > v.cur.Hi {
		v.cur.Hi = level
	}
	if level < v.cur.Lo {
		v.cur.Lo = level
	}
	if voiced {
		v.cur.VoicedMS += int(v.frameMS)
	}
	v.curMS += v.frameMS
	if v.curMS < float64(v.cfg.TraceMS) {
		return
	}
	marginDB, _ := v.thresholds()
	v.cur.Floor = round1(v.noiseFloorDB)
	v.cur.Bar = round1(v.noiseFloorDB + marginDB)
	v.cur.Hi, v.cur.Lo = round1(v.cur.Hi), round1(v.cur.Lo)
	v.cur.Open = v.open
	v.cur.Speaking = v.speaking.Load()
	v.cur.Raised = v.curRais
	if len(v.trace) < maxTraceSamples {
		v.trace = append(v.trace, v.cur)
	}
	v.curSet, v.curMS, v.curRais = false, 0, false
}

func round1(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return -99
	}
	return math.Round(x*10) / 10
}

// trackFloor is the window-minimum tracker. An open utterance that has not
// had one frame under the bar for FloorTrackMS is not speech, or not only
// speech: the floor goes to the quietest frame of that time, and the frames
// at the noise's level stop reading as voiced.
func (v *VAD) trackFloor(level, marginDB float64) {
	if v.cfg.FloorTrackMS <= 0 {
		return
	}
	const blockMS = 100
	if v.blockMS == 0 {
		v.blockMin = level
	} else if level < v.blockMin {
		v.blockMin = level
	}
	v.blockMS += v.frameMS
	if v.blockMS < blockMS {
		return
	}
	n := v.cfg.FloorTrackMS / blockMS
	if n < 2 {
		n = 2
	}
	if len(v.blockMins) != n {
		v.blockMins = make([]float64, n)
		v.blockPos, v.blockCount = 0, 0
	}
	v.blockMins[v.blockPos] = v.blockMin
	v.blockPos = (v.blockPos + 1) % n
	if v.blockCount < n {
		v.blockCount++
	}
	v.blockMS = 0
	if !v.open || v.blockCount < n {
		return
	}
	quietest := v.blockMins[0]
	for _, m := range v.blockMins[1:] {
		if m < quietest {
			quietest = m
		}
	}
	if quietest <= v.noiseFloorDB+marginDB {
		return
	}
	to := quietest
	if to > v.cfg.MaxNoiseFloorDB {
		to = v.cfg.MaxNoiseFloorDB
	}
	if to <= v.noiseFloorDB {
		return
	}
	v.floorRaise.from, v.floorRaise.to, v.floorRaise.ready = v.noiseFloorDB, to, true
	v.noiseFloorDB = to
	v.curRais = true
	v.blockCount = 0
}

// EchoMiss is one start of the assistant's speech whose echo was not timed.
//
// The first call with the estimator in it reported nothing at all, on a line
// that plainly echoes, and nothing in the log could say why. An instrument
// that is silent when it fails cannot be told from a line without echo.
type EchoMiss struct {
	// Reason is why, in words for a log line.
	Reason string
	// QuietMS is how long the input had been under the bar when the assistant
	// began, LevelDB the level of the frame that settled it, and NoiseFloorDB
	// the floor the bar stands on.
	QuietMS      float64
	LevelDB      float64
	NoiseFloorDB float64
}

// TakeEchoMiss hands out, once, the last attempt to time the echo that came to
// nothing. Like TakeEchoReturn it belongs to the goroutine that calls Push.
func (v *VAD) TakeEchoMiss() (EchoMiss, bool) {
	if !v.echoMissReady {
		return EchoMiss{}, false
	}
	v.echoMissReady = false
	return v.echoMiss, true
}

func (v *VAD) missEcho(reason string, level float64) {
	v.echoMiss = EchoMiss{Reason: reason, QuietMS: v.quietMS, LevelDB: level, NoiseFloorDB: v.noiseFloorDB}
	v.echoMissReady = true
}

// echoNotArmed is echoArmAt with nothing in it. The clock starts at zero, so
// zero is a moment the assistant can begin at.
const echoNotArmed = -1

// NewVAD builds a detector. cfg is copied; later mutation has no effect.
func NewVAD(cfg VADConfig) *VAD {
	cfg.applyDefaults()
	frameSamples := cfg.SampleRate * cfg.FrameMS / 1000
	prerollCap := cfg.SpeechPadMS / cfg.FrameMS
	if prerollCap < 1 {
		prerollCap = 1
	}
	v := &VAD{
		cfg:          cfg,
		frameSamples: frameSamples,
		frameMS:      float64(cfg.FrameMS),
		preroll:      make([][]float32, prerollCap),
		prerollCap:   prerollCap,
		noiseFloorDB: cfg.NoiseFloorDB,
		echoFloorDB:  cfg.NoiseFloorDB,
	}
	v.echoArmAt.Store(echoNotArmed)
	return v
}

// FrameSamples is the frame size the VAD expects, in samples.
func (v *VAD) FrameSamples() int { return v.frameSamples }

// FrameMS is the frame size the VAD expects, in milliseconds.
func (v *VAD) FrameMS() float64 { return v.frameMS }

// FrameBytes is the frame size the VAD expects, in PCM16 bytes.
func (v *VAD) FrameBytes() int { return v.frameSamples * BytesPerSample }

// SetAssistantSpeaking tells the VAD whether the assistant is currently
// producing audio. This is the single most important switch for simulated full
// duplex: listening never stops, but the bar for interrupting rises while we
// are talking, so acoustic echo does not read as a barge-in.
func (v *VAD) SetAssistantSpeaking(speaking bool) {
	was := v.speaking.Load()
	if !speaking && was && v.cfg.EchoTailMS > 0 {
		v.echoTailUntil.Store(v.clockAtomic.Load() + int64(v.cfg.EchoTailMS))
	}
	// Only the start of an answer is timed. Being told again that it is still
	// speaking must not move the origin to the middle of a sentence, whose
	// echo is already arriving.
	if speaking && !was {
		v.echoArmAt.Store(v.clockAtomic.Load())
	}
	v.speaking.Store(speaking)
}

// assistantAudible reports whether the caller can still be hearing the
// assistant: it is producing audio, or stopped less than EchoTailMS ago.
func (v *VAD) assistantAudible() bool {
	return v.speaking.Load() || int64(v.clockMS) < v.echoTailUntil.Load()
}

// ClockMS is how much audio the detector has been given, in milliseconds. It
// is the clock to time the caller by: it advances with what they said, not
// with how long the process took to hear it.
func (v *VAD) ClockMS() float64 { return v.clockMS }

// ActiveMS reports how much of the open utterance has been voiced, and zero
// when none is open.
func (v *VAD) ActiveMS() float64 {
	if !v.open {
		return 0
	}
	return v.activeMS
}

// Stop closes the open utterance now, without waiting for the silence that
// would normally close it, and returns the same decision that silence would
// have. It returns DecisionNone when no utterance is open.
//
// The detector decides on energy, and a line that is never quiet — a room, a
// car, a carrier's noise — never gives it the silence it is waiting for. The
// caller who knows from other evidence that the speaker has finished, such as a
// transcript that stopped growing, ends the utterance here.
func (v *VAD) Stop() Decision {
	if !v.open {
		return Decision{Kind: DecisionNone}
	}
	return v.stop()
}

func (v *VAD) stop() Decision {
	d := Decision{
		Kind:      DecisionStopped,
		Utterance: v.utterance,
		Snapshot:  v.snapshotAcc,
		ActiveMS:  v.activeMS,
		StartMS:   v.startMS,
		EndMS:     int64(v.clockMS),
		BargeIn:   v.bargeIn,
		SilenceMS: v.silenceMS,
	}
	v.Reset()
	return d
}

// NoiseFloorDB exposes the adaptive floor for diagnostics.
func (v *VAD) NoiseFloorDB() float64 { return v.noiseFloorDB }

// EchoFloorDB exposes the learned level of the assistant's own voice returning
// through the caller's speaker. Worth logging: when it sits far above
// NoiseFloorDB, the caller is on a speakerphone or has echo cancellation off,
// and the gap is exactly what was cutting answers short before it existed.
func (v *VAD) EchoFloorDB() float64 { return v.echoFloorDB }

// TakeEchoReturn hands out the time the assistant's voice took to come back,
// once per measurement: how long after it began producing audio the first
// sound arrived on a line that had been quiet, and how loud that sound was. ok
// is false when there is nothing new. It belongs to the goroutine that calls
// Push.
//
// This is the only clock anybody has on the phone leg. What it spans depends on
// whose echo it is — see "Measuring the phone leg" in TELEPHONY.md — and it
// reads long: an onset is soft, and the first frame over the bar is some tens
// of milliseconds after the sound began.
func (v *VAD) TakeEchoReturn() (ms float64, levelDB float64, ok bool) {
	if !v.echoReturnReady {
		return 0, 0, false
	}
	v.echoReturnReady = false
	return v.echoReturnMS, v.echoReturnDB, true
}

// trackEchoReturn times the assistant's own voice around the loop: out of
// golive, down the line, and back in as echo. frameStartMS is the clock at the
// start of the frame just consumed and level is that frame's.
//
// Nothing outside golive reports a timestamp, so the phone leg cannot be timed
// from both ends. But the line returns what is said into it, and both ends of
// that trip are here: the moment the assistant starts, and the moment the
// input stops being quiet.
//
// The second is only the first's echo if the line was quiet to begin with. A
// caller still making sound when the answer starts — a breath, a word said
// over it — would be timed as an echo that came back at once, so a measurement
// is begun only on a line that has been quiet for echoQuietBeforeMS with no
// utterance open, and is given up rather than guessed at otherwise. A caller
// who speaks inside the wait is still taken for the echo; that is what the
// median is for.
//
// It reads levels and writes nothing the voiced decision looks at.
func (v *VAD) trackEchoReturn(frameStartMS, level float64) {
	loud := level > v.noiseFloorDB+echoOnsetMarginDB

	if at := v.echoArmAt.Swap(echoNotArmed); at != echoNotArmed {
		// quietMS does not count this frame yet: the question is what the line
		// was doing before the assistant began.
		v.echoArmed = v.quietMS >= echoQuietBeforeMS && !v.open
		v.echoArmedAtMS = float64(at)
		switch {
		case v.open:
			v.missEcho("the caller was mid-utterance when the assistant began", level)
		case !v.echoArmed:
			v.missEcho("the line had not been quiet when the assistant began", level)
		}
	}

	if v.echoArmed {
		waited := frameStartMS - v.echoArmedAtMS
		switch {
		case waited > echoReturnMaxMS:
			// Nothing came back. A sound from here on is somebody speaking.
			v.echoArmed = false
			v.missEcho("nothing came back within the window", level)
		case loud:
			// The first sound is the only candidate there is: a second one
			// would be timed from the wrong place.
			if waited >= echoReturnMinMS {
				v.echoReturnMS = waited
				v.echoReturnDB = level
				v.echoReturnReady = true
			} else {
				v.missEcho("a sound arrived in the frame the assistant began in", level)
			}
			v.echoArmed = false
		}
	}

	if loud {
		v.quietMS = 0
	} else {
		v.quietMS += v.frameMS
	}
}

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

// VoicedRunMS reports how long the open utterance has been continuously voiced
// since its last unvoiced frame, and zero when no utterance is open or the
// current frame is unvoiced.
//
// It is the counterpart of TrailingSilenceMS: that one says the speaker has
// paused, this one says they have started again. One voiced frame is not
// evidence of that — a click reads as voiced — so a caller that acts on a
// resumption should ask for a few frames' worth.
func (v *VAD) VoicedRunMS() float64 {
	if !v.open {
		return 0
	}
	return v.voicedRunMS
}

// CandidateMS reports how much voiced audio the detector is holding as a
// possible utterance that has not been confirmed yet, and zero when there is
// none.
//
// An utterance is only declared after MinSpeechMS of it, which is a fifth of a
// second in which the engine already knows something is probably coming. That
// is long enough to dial a recognizer.
func (v *VAD) CandidateMS() float64 {
	if v.open {
		return 0
	}
	return v.pendingMS
}

// SetMinSilenceMS overrides how much trailing silence closes the utterance that
// is open now. Zero or less restores the configured value, and so does the end
// of the utterance: the override never outlives the turn it was set for.
//
// The fixed threshold is the same for "what time is it" and for "I wanted to
// ask about, um". A caller who knows more than the detector does — the words so
// far — can use this to wait longer for the second without waiting longer for
// the first.
func (v *VAD) SetMinSilenceMS(ms int) {
	if ms <= 0 {
		v.minSilenceMS = 0
		return
	}
	v.minSilenceMS = ms
}

// MinSilenceMS is the trailing silence that currently closes an utterance.
func (v *VAD) MinSilenceMS() int {
	if v.minSilenceMS > 0 {
		return v.minSilenceMS
	}
	return v.cfg.MinSilenceMS
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
	v.voicedRunMS = 0
	v.minSilenceMS = 0
}

func (v *VAD) thresholds() (marginDB float64, minSpeechMS int) {
	marginDB = v.cfg.SpeechMarginDB
	minSpeechMS = v.cfg.MinSpeechMS
	if v.assistantAudible() {
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
	frameStartMS := v.clockMS
	v.clockMS += v.frameMS
	v.clockAtomic.Store(int64(v.clockMS))

	level := DBFS(RMS(frame))
	zcr := ZeroCrossingRate(frame)
	marginDB, minSpeechMS := v.thresholds()
	// Before the floor adapts to this frame, like the voiced decision below.
	v.trackEchoReturn(frameStartMS, level)

	// The floor starts at the configured value rather than at the first frame.
	// Seeding from frame one looks smarter and is actively wrong: a caller who
	// is already mid-sentence when the socket opens would calibrate the floor
	// to their own voice and then never be heard.
	voiced := level > v.noiseFloorDB+marginDB && zcr <= v.cfg.MaxZCR

	// Measure how loud we come back through the caller's speaker.
	//
	// This is a diagnostic, deliberately kept out of the voiced decision above.
	// The noise floor cannot learn this itself, because of a trap in its own
	// rules: it adapts only on frames it believes are *not* speech, so once
	// echo is loud enough to read as voiced it stops feeding the floor, the
	// floor never rises to meet it, and the echo reads as voiced forever. A
	// real call shows the signature plainly — noise_floor_db pinned at −60
	// while barge-ins open at active_ms=320 exactly, the configured minimum,
	// again and again. That is not somebody starting to talk, whose energy
	// overshoots the bar; it is a signal sitting precisely on it, and it was
	// cutting the greeting short with the assistant's own audio.
	//
	// Making it part of the decision was tried and abandoned. Energy alone
	// cannot separate steady echo from a steady voice at the same level, so
	// every variant either let the first burst through or muted a caller
	// talking over the assistant — and deafening the caller is much the worse
	// failure. What actually separates them is the transcript, which is why
	// the floor hold in the engine is the fix and this is the instrument that
	// tells you to reach for barge_in_margin_db.
	//
	// Adapts on every frame while we speak, voiced or not, because during our
	// own output echo is the expected content. Only while the utterance is
	// closed: once a barge-in has opened, the input is the caller.
	if v.speaking.Load() && !v.open {
		// Upward only. The question is how loud our echo comes back at its
		// loudest, not what it averages: the gaps between our own words are
		// near-silent, and letting them pull the measurement down would report
		// a quiet line for a room that is plainly echoing.
		const attack = 0.05
		if level > v.echoFloorDB {
			v.echoFloorDB += (level - v.echoFloorDB) * attack
		}
		switch {
		case math.IsNaN(v.echoFloorDB) || math.IsInf(v.echoFloorDB, 0):
			v.echoFloorDB = v.cfg.NoiseFloorDB
		case v.echoFloorDB > v.cfg.MaxNoiseFloorDB:
			v.echoFloorDB = v.cfg.MaxNoiseFloorDB
		}
	} else if !v.speaking.Load() {
		// Decay while we are quiet, so a one-off bang does not pin the
		// measurement high for the rest of the call.
		const decay = 0.002
		v.echoFloorDB += (v.cfg.NoiseFloorDB - v.echoFloorDB) * decay
	}

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

	// The window-minimum tracker runs on the level before this frame's
	// decision was made; a raise applies from the next frame.
	v.trackFloor(level, marginDB)
	v.sample(level, voiced)

	if !v.open {
		return v.pushClosed(frame, voiced, minSpeechMS)
	}
	return v.pushOpen(frame, voiced)
}

func (v *VAD) pushClosed(frame []float32, voiced bool, minSpeechMS int) Decision {
	// A frame lives in exactly one place: the pre-roll ring, or the candidate.
	//
	// It used to go into both. Every frame was pushed to the ring first and the
	// voiced ones were then appended to the candidate as well, so when the
	// candidate was promoted the utterance was "the last 240 ms" followed by
	// "the candidate" — and with a 200 ms minimum those are nearly the same
	// audio. The recognizer was handed the first fifth of a second of every
	// utterance twice, back to back, with 40 ms of real padding in front
	// instead of 240. The snapshot test could not see it, because the snapshots
	// faithfully reproduced the doubled utterance.
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
				// Not speech after all. Its audio is still the most recent
				// audio there is, so it goes back to being pre-roll.
				v.spillPendingToPreroll()
				v.pendingMS = 0
				v.pendingGapMS = 0
				v.pending = nil
				v.pushPreroll(frame)
			} else {
				v.pending = append(v.pending, frame...)
			}
		} else {
			v.pushPreroll(frame)
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
	v.voicedRunMS = v.activeMS
	// Measured from the audio actually handed out, which includes the pad and
	// any tolerated gaps inside the candidate, not only its voiced frames.
	heldMS := float64(len(v.utterance)) / float64(v.cfg.SampleRate) * 1000
	v.startMS = int64(math.Max(0, v.clockMS-heldMS))
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
		v.voicedRunMS += v.frameMS
	} else {
		v.silenceMS += v.frameMS
		v.voicedRunMS = 0
	}

	totalMS := float64(len(v.utterance)) / float64(v.cfg.SampleRate) * 1000
	closing := v.silenceMS >= float64(v.MinSilenceMS()) || totalMS >= float64(v.cfg.MaxSpeechMS)

	if closing {
		return v.stop()
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

// spillPendingToPreroll returns a discarded candidate's frames to the ring, in
// order, so the pre-roll stays "the audio just before now" with no hole in it.
func (v *VAD) spillPendingToPreroll() {
	for off := 0; off+v.frameSamples <= len(v.pending); off += v.frameSamples {
		v.pushPreroll(v.pending[off : off+v.frameSamples])
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
