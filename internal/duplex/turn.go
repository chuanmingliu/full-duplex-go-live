package duplex

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Generation is a cancel scope for everything downstream of a user turn.
//
// A cascaded voice stack has four independent stages in flight at once. When
// the user interrupts, all four must stop, and they must stop without the
// coordination cost of plumbing a context through every provider call. A
// monotonic counter does it: each stage captures the generation it was born in
// and refuses to emit once the counter has moved on. Late results from a
// cancelled generation are dropped rather than raced against the new turn.
type Generation struct {
	n atomic.Uint64
}

// Current returns the live generation.
func (g *Generation) Current() uint64 { return g.n.Load() }

// Bump invalidates everything in flight and returns the new generation.
func (g *Generation) Bump() uint64 { return g.n.Add(1) }

// Valid reports whether work born in gen may still emit.
func (g *Generation) Valid(gen uint64) bool { return g.n.Load() == gen }

// TurnState is where a turn is in its life.
type TurnState int

const (
	// TurnSpeculative was started from a partial transcript and may still be
	// revised or abandoned.
	TurnSpeculative TurnState = iota
	// TurnCommitted was started from, or confirmed by, a final transcript.
	TurnCommitted
	// TurnAbandoned was superseded.
	TurnAbandoned
)

func (s TurnState) String() string {
	switch s {
	case TurnSpeculative:
		return "speculative"
	case TurnCommitted:
		return "committed"
	case TurnAbandoned:
		return "abandoned"
	default:
		return "unknown"
	}
}

// Turn is one user utterance and the assistant response it produced.
//
// A turn is written from several goroutines at once — the engine loop opens it,
// a backend goroutine fills in the text, the synthesis goroutine stamps first
// audio — so the fields that move during its life sit behind a mutex and are
// read through Timings. The identity fields (ID, Revision, Transcript,
// Generation) are set once at construction and are safe to read directly.
type Turn struct {
	ID       string
	Revision int
	State    TurnState

	// Transcript is the user text this revision was generated from.
	Transcript string
	// Generation is the cancel scope the turn's work runs in.
	Generation uint64

	// Response accumulates the assistant text actually produced. Use
	// AppendResponse to write it.
	Response string
	// Spoken is the prefix of Response the user actually heard. On a clean
	// turn it equals Response; after a barge-in it is shorter, and it is what
	// goes into conversation history. Use SetSpoken to write it.
	Spoken string

	// Speculative records whether the turn began before the final transcript.
	Speculative bool

	mu              sync.Mutex
	startedAt       time.Time
	speechEndAt     time.Time
	speechEndMS     int64
	transcriptFinal time.Time
	firstToken      time.Time
	firstAudio      time.Time
	firstAudioOut   time.Time
	completedAt     time.Time
}

// Timings is a consistent snapshot of a turn's milestones.
type Timings struct {
	StartedAt       time.Time
	SpeechEndAt     time.Time
	SpeechEndMS     int64
	TranscriptFinal time.Time
	FirstToken      time.Time
	FirstAudio      time.Time
	FirstAudioOut   time.Time
	CompletedAt     time.Time
}

// Timings returns the turn's milestones.
func (t *Turn) Timings() Timings {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Timings{
		StartedAt:       t.startedAt,
		SpeechEndAt:     t.speechEndAt,
		SpeechEndMS:     t.speechEndMS,
		TranscriptFinal: t.transcriptFinal,
		FirstToken:      t.firstToken,
		FirstAudio:      t.firstAudio,
		FirstAudioOut:   t.firstAudioOut,
		CompletedAt:     t.completedAt,
	}
}

// MarkSpeechEnd records when the user stopped talking, which is the origin for
// every latency this turn reports. It is set once: a turn that began
// speculatively is stamped when speech actually ends, and one begun from a
// final transcript inherits the VAD's timestamp at creation.
func (t *Turn) MarkSpeechEnd(at time.Time, sessionMS int64) {
	t.mu.Lock()
	if t.speechEndAt.IsZero() {
		t.speechEndAt = at
		t.speechEndMS = sessionMS
	}
	t.mu.Unlock()
}

// MarkFirstAudioOut records the first audio byte written to the client. This
// is deliberately the wire moment, not the synthesis moment: the paced player
// may hold audio back, and the caller hears the wire.
func (t *Turn) MarkFirstAudioOut() { t.stampOnce(&t.firstAudioOut) }

// MarkTranscriptFinal records when the final transcript landed.
func (t *Turn) MarkTranscriptFinal() { t.stamp(&t.transcriptFinal) }

// MarkFirstToken records the first backend token, the think-channel latency.
func (t *Turn) MarkFirstToken() { t.stampOnce(&t.firstToken) }

// MarkFirstAudio records the first synthesized audio for this turn.
func (t *Turn) MarkFirstAudio() { t.stampOnce(&t.firstAudio) }

// MarkCompleted records when the turn finished.
func (t *Turn) MarkCompleted() { t.stamp(&t.completedAt) }

// AppendResponse accumulates generated text.
func (t *Turn) AppendResponse(text string) {
	t.mu.Lock()
	t.Response += text
	t.mu.Unlock()
}

// SetSpoken records the prefix the listener actually heard.
func (t *Turn) SetSpoken(text string) {
	t.mu.Lock()
	t.Spoken = text
	t.mu.Unlock()
}

func (t *Turn) stamp(field *time.Time) {
	t.mu.Lock()
	*field = time.Now()
	t.mu.Unlock()
}

func (t *Turn) stampOnce(field *time.Time) {
	t.mu.Lock()
	if field.IsZero() {
		*field = time.Now()
	}
	t.mu.Unlock()
}

// Tracker issues turn IDs and enforces that only the newest revision of the
// newest turn may reach playback.
//
// Revisions exist because speculation is allowed to be wrong. A turn started
// from the partial "帮我查一下明天" is revised when the final transcript turns
// out to be "帮我查一下明天的天气"; the revision bump invalidates the first
// attempt's audio before any of it is spoken.
type Tracker struct {
	mu      sync.RWMutex
	seq     uint64
	current *Turn
	gen     *Generation
}

// NewTracker builds a tracker sharing gen as its cancel scope.
func NewTracker(gen *Generation) *Tracker {
	return &Tracker{gen: gen}
}

// Begin opens a new turn, invalidating any work still in flight.
func (t *Tracker) Begin(transcript string, speculative bool) *Turn {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.current != nil && t.current.State != TurnAbandoned {
		t.current.State = TurnAbandoned
	}
	t.seq++
	turn := &Turn{
		ID:          fmt.Sprintf("item_%d", t.seq),
		Revision:    0,
		State:       TurnSpeculative,
		Transcript:  transcript,
		Generation:  t.gen.Bump(),
		Speculative: speculative,
		startedAt:   time.Now(),
	}
	if !speculative {
		turn.State = TurnCommitted
	}
	t.current = turn
	return turn
}

// Revise supersedes the current turn with a corrected transcript, keeping the
// turn ID so a client's transcript row survives the correction. It returns the
// new revision, or nil if the turn named is no longer current.
func (t *Tracker) Revise(turnID, transcript string, committed bool) *Turn {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.current == nil || t.current.ID != turnID {
		return nil
	}
	prev := t.current
	turn := &Turn{
		ID:          prev.ID,
		Revision:    prev.Revision + 1,
		State:       TurnSpeculative,
		Transcript:  transcript,
		Generation:  t.gen.Bump(),
		Speculative: !committed,
		startedAt:   prev.Timings().StartedAt,
	}
	if committed {
		turn.State = TurnCommitted
		turn.MarkTranscriptFinal()
	}
	t.current = turn
	return turn
}

// Commit promotes a speculative turn whose transcript turned out to be right,
// without bumping the generation. This is the fast path that makes speculation
// worth doing: when the guess holds, the audio already in flight keeps playing
// and the final transcript costs nothing.
func (t *Tracker) Commit(turnID string, revision int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.current == nil || t.current.ID != turnID || t.current.Revision != revision {
		return false
	}
	if t.current.State == TurnSpeculative {
		t.current.State = TurnCommitted
		t.current.MarkTranscriptFinal()
	}
	return true
}

// Abandon marks the current turn dead and bumps the generation. Used on
// barge-in and on session close.
func (t *Tracker) Abandon() *Turn {
	t.mu.Lock()
	defer t.mu.Unlock()

	turn := t.current
	if turn != nil {
		turn.State = TurnAbandoned
	}
	t.gen.Bump()
	return turn
}

// Current returns the live turn, or nil.
func (t *Tracker) Current() *Turn {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.current
}

// IsCurrent reports whether the given turn revision is still the live one and
// its generation is still valid. Every stage checks this before emitting.
func (t *Tracker) IsCurrent(turn *Turn) bool {
	if turn == nil {
		return false
	}
	t.mu.RLock()
	cur := t.current
	t.mu.RUnlock()
	return cur != nil &&
		cur.ID == turn.ID &&
		cur.Revision == turn.Revision &&
		cur.State != TurnAbandoned &&
		t.gen.Valid(turn.Generation)
}

// StabilityWatch decides when a partial transcript has settled enough to
// speculate on.
//
// The rule is deliberately dumb — the text stopped changing for N ms and is at
// least M characters — because the sophisticated version (predicting whether
// the user is done from prosody) is exactly the thing a real full-duplex model
// does natively and a cascade cannot. Being dumb and cheap is fine here: a
// wrong guess costs one cancelled generation, not a wrong answer.
type StabilityWatch struct {
	stableFor time.Duration
	minChars  int

	lastText string
	lastSeen time.Time
	fired    bool
}

// NewStabilityWatch builds a watch.
func NewStabilityWatch(stableFor time.Duration, minChars int) *StabilityWatch {
	return &StabilityWatch{stableFor: stableFor, minChars: minChars}
}

// Reset clears state between utterances.
func (w *StabilityWatch) Reset() {
	w.lastText = ""
	w.lastSeen = time.Time{}
	w.fired = false
}

// Observe records a partial transcript and reports whether it has now been
// stable long enough to speculate on. It fires at most once per utterance.
func (w *StabilityWatch) Observe(text string, now time.Time) bool {
	if w.fired {
		return false
	}
	if text != w.lastText {
		w.lastText = text
		w.lastSeen = now
		return false
	}
	if w.lastSeen.IsZero() {
		w.lastSeen = now
		return false
	}
	if len([]rune(text)) < w.minChars {
		return false
	}
	if now.Sub(w.lastSeen) >= w.stableFor {
		w.fired = true
		return true
	}
	return false
}

// Fired reports whether this utterance already produced a speculative turn.
func (w *StabilityWatch) Fired() bool { return w.fired }
