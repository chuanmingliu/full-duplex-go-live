package duplex

import (
	"fmt"
	"sort"
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

	// DelegationID overrides the delegation this turn's backend work is
	// reported under. It is set, before the turn is shared, on a turn that
	// resumes an earlier delegation with its function results; otherwise the
	// id is derived from the turn's own.
	DelegationID string

	// Scripted marks a turn whose words were handed to the assistant (an
	// async tool's ack): a result waiting for a gap does not interrupt it.
	// Opening is a fixed line said at the start of the turn, before the
	// model's first token (a result's opening).
	Scripted bool
	Opening  string
	// Opened is the opener the engine said for this turn at the pause, before
	// the model's first clause; the pipe says it once. OpenedMS is its audio.
	Opened   string
	OpenedMS int64
	// OpenerPlanned is the opener chosen when the answer was begun (so the
	// model can be told to continue from it); said at the pause by sayOpener.
	OpenerPlanned string
	// OpenedAt is when the opener was queued.
	OpenedAt time.Time

	mu sync.Mutex
	// committed, dead, held and commitCh implement the rule that a turn begun
	// on a guess says nothing binding until the guess is confirmed. See
	// emitOrHold.
	committed bool
	dead      bool
	held      []any
	commitCh  chan struct{}

	startedAt       time.Time
	speechEndAt     time.Time
	speechEndMS     int64
	endSilenceMS    int64
	transcriptFinal time.Time
	firstToken      time.Time
	firstSegment    time.Time
	firstAudio      time.Time
	firstAudioOut   time.Time
	firstContent    time.Time
	completedAt     time.Time
}

// Timings is a consistent snapshot of a turn's milestones.
type Timings struct {
	StartedAt       time.Time
	SpeechEndAt     time.Time
	SpeechEndMS     int64
	EndSilenceMS    int64
	TranscriptFinal time.Time
	FirstToken      time.Time
	FirstSegment    time.Time
	FirstAudio      time.Time
	FirstAudioOut   time.Time
	// FirstContent is when the first segment of the answer itself — not the
	// opener said at the pause — was ready to play.
	FirstContent time.Time
	CompletedAt  time.Time
}

// Timings returns the turn's milestones.
func (t *Turn) Timings() Timings {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Timings{
		StartedAt:       t.startedAt,
		SpeechEndAt:     t.speechEndAt,
		SpeechEndMS:     t.speechEndMS,
		EndSilenceMS:    t.endSilenceMS,
		TranscriptFinal: t.transcriptFinal,
		FirstToken:      t.firstToken,
		FirstSegment:    t.firstSegment,
		FirstAudio:      t.firstAudio,
		FirstAudioOut:   t.firstAudioOut,
		FirstContent:    t.firstContent,
		CompletedAt:     t.completedAt,
	}
}

// emitOrHold delivers an event that commits the service to something — a
// delegation, a backend function call — or keeps it back while the turn is
// still a guess.
//
// Speculation starts the backend on a transcript that may yet change. Its audio
// can be cut and its text discarded, but an event cannot be unsent: a client
// that sees session.delegation.created, or a function call inside
// response.event, acts on it. gpt-live-1 never retracts either, so golive does
// not announce one until the turn it belongs to is certain. Held events are
// released, in order, the instant the turn commits, and dropped if it dies.
//
// Emission happens under the turn's lock so a release and a direct emit from
// another goroutine cannot interleave out of order. emit must not block.
func (t *Turn) emitOrHold(ev any, emit func(any)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.dead {
		return
	}
	if !t.committed {
		t.held = append(t.held, ev)
		return
	}
	emit(ev)
}

// commit confirms the turn and releases what was held for it.
func (t *Turn) commit(emit func(any)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.committed || t.dead {
		return
	}
	t.committed = true
	for _, ev := range t.held {
		emit(ev)
	}
	t.held = nil
	if t.commitCh != nil {
		close(t.commitCh)
	}
}

// kill abandons the turn: nothing held for it is ever sent, and nothing more
// will be.
// Dead reports whether the turn has been killed.
func (t *Turn) Dead() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dead
}

func (t *Turn) kill() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.dead {
		return
	}
	t.dead = true
	t.held = nil
	// Whoever is waiting on the turn's confirmation — a function call, the
	// first sentence of its answer — is told now that it is not coming.
	if !t.committed && t.commitCh != nil {
		close(t.commitCh)
	}
}

// Committed reports whether the turn has been confirmed.
func (t *Turn) Committed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.committed
}

// awaitCommit blocks until the turn is confirmed, and reports false if done
// closes first. A turn that was never speculative is confirmed at birth.
func (t *Turn) awaitCommit(done <-chan struct{}) bool {
	t.mu.Lock()
	ch, committed, dead := t.commitCh, t.committed, t.dead
	t.mu.Unlock()
	if committed {
		return true
	}
	if dead || ch == nil {
		return false
	}
	select {
	case <-ch:
		// Closed by commit or by kill; only one of them is good news.
		return t.Committed()
	case <-done:
		return false
	}
}

// MarkSpeechEnd records when the user stopped talking, which is the origin for
// every latency this turn reports. It is set once: a turn that began
// speculatively is stamped when speech actually ends, and one begun from a
// final transcript inherits the VAD's timestamp at creation.
func (t *Turn) MarkSpeechEnd(at time.Time, sessionMS, silenceMS int64) {
	t.mu.Lock()
	if t.speechEndAt.IsZero() {
		t.speechEndAt = at
		t.speechEndMS = sessionMS
		t.endSilenceMS = silenceMS
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

// MarkFirstSegment records when the first speakable segment was handed to the
// synthesizer. The gap from FirstToken to here is time spent waiting for a
// sentence boundary; the gap from here to FirstAudio is the provider. Without
// the split, a slow reply and a late comma look identical.
func (t *Turn) MarkFirstSegment() { t.stampOnce(&t.firstSegment) }

// MarkFirstAudio records the first synthesized audio for this turn.
func (t *Turn) MarkFirstAudio() { t.stampOnce(&t.firstAudio) }

// MarkFirstContent records when the answer's own first segment (after any
// opener) was ready.
func (t *Turn) MarkFirstContent() { t.stampOnce(&t.firstContent) }

// MarkCompleted records when the turn finished, and reports whether this call
// was the one that finished it. A turn can be handed to both completion paths —
// a truncation report and a natural end arriving for the same playback turn —
// and the false return is what keeps the second one from emitting a duplicate
// set of metrics.
func (t *Turn) MarkCompleted() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.completedAt.IsZero() {
		return false
	}
	t.completedAt = time.Now()
	return true
}

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
		commitCh:    make(chan struct{}),
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
		commitCh:    make(chan struct{}),
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
	// The whole comparison is under the lock, not just the pointer: State is
	// written by Commit and Abandon on the loop goroutine while generation
	// goroutines ask this.
	t.mu.RLock()
	cur := t.current
	live := cur != nil &&
		cur.ID == turn.ID &&
		cur.Revision == turn.Revision &&
		cur.State != TurnAbandoned
	t.mu.RUnlock()
	return live && t.gen.Valid(turn.Generation)
}

// rollingMS keeps the recent history of one duration, in milliseconds, so the
// engine can ask what is normal *for this session* rather than what was normal
// on the machine where a default was chosen.
//
// It is deliberately short. A backend's latency depends on the system prompt,
// the conversation length and whatever the provider is doing this minute, all
// of which change within a call; a long window would still be describing the
// first few turns by the time it mattered.
type rollingMS struct {
	vals []int64
	max  int
}

// Add records an observation.
func (r *rollingMS) Add(ms int64) {
	if ms < 0 {
		return
	}
	if r.max == 0 {
		r.max = 8
	}
	r.vals = append(r.vals, ms)
	if len(r.vals) > r.max {
		r.vals = r.vals[len(r.vals)-r.max:]
	}
}

// N reports how many observations are held.
func (r *rollingMS) N() int { return len(r.vals) }

// P50 is the median, or zero with no observations.
func (r *rollingMS) P50() int64 {
	if len(r.vals) == 0 {
		return 0
	}
	v := append([]int64(nil), r.vals...)
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	return v[len(v)/2]
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
	// lastSeen is when the current pause was first observed; changed is when
	// the transcript last moved inside it, or zero if it has not.
	lastSeen time.Time
	changed  time.Time
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
	w.changed = time.Time{}
	w.fired = false
}

// Unsettle restarts the stability clock without clearing the fired flag.
//
// The caller uses this the moment the speaker is audibly talking again. It
// means the window that matters is measured from the start of a pause, not
// from the last hypothesis: a recognizer running a few hundred milliseconds
// behind looks perfectly stable mid-sentence, and speculating on that prefix
// is how nearly every guess ends up wrong.
func (w *StabilityWatch) Unsettle() {
	w.lastText = ""
	w.lastSeen = time.Time{}
	w.changed = time.Time{}
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

// ObserveFor is Observe with the windows chosen by the caller, and without the
// length floor: the engine decides those from what the text says, which the
// watch cannot see.
//
// There are two windows because there are two questions. stableFor is how long
// the pause must have lasted: the speaker has to be audibly quiet for that long
// before anything is guessed. settle is how long the transcript must have been
// still, and it is the shorter of the two, because of what it is guarding
// against. A recognizer runs behind the speaker, so the words of the last
// syllable arrive some way into the silence that follows it — and once they
// have, with no more speech to process, nothing further is coming. The old
// rule restarted the whole of stableFor at that arrival, which put the guess
// at "recognizer lag plus the full window": with a typical recognizer, later
// than the moment the turn closed anyway, so the head start was nothing.
func (w *StabilityWatch) ObserveFor(text string, now time.Time, stableFor, settle time.Duration) bool {
	if w.fired {
		return false
	}
	if w.lastSeen.IsZero() {
		// First look at this pause.
		w.lastSeen = now
		w.changed = time.Time{}
		w.lastText = text
		return false
	}
	if text != w.lastText {
		w.lastText = text
		w.changed = now
		return false
	}
	if now.Sub(w.lastSeen) < stableFor {
		return false
	}
	if !w.changed.IsZero() && now.Sub(w.changed) < settle {
		return false
	}
	w.fired = true
	return true
}

// Rearm lets the watch fire again in the same utterance.
//
// It used to fire once and stay fired, which made a guess on the wrong pause
// doubly expensive: the speculation was wasted, and the pause that really ended
// the turn then got no head start at all, because the one shot was spent. A
// speaker who stops to think mid-sentence is common enough that this was the
// usual way for speculation to buy nothing.
func (w *StabilityWatch) Rearm() {
	w.fired = false
	w.lastText = ""
	w.lastSeen = time.Time{}
	w.changed = time.Time{}
}

// Fired reports whether this utterance currently has a speculative turn out.
func (w *StabilityWatch) Fired() bool { return w.fired }
