package duplex

import (
	"strings"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
)

// SegKind discriminates the items on the speak channel.
type SegKind int

const (
	// SegAudio carries PCM to emit.
	SegAudio SegKind = iota
	// SegBegin announces the text whose audio is about to stream, so a cut
	// landing mid-segment can still report what was heard.
	SegBegin
	// SegMark closes the segment opened by SegBegin.
	SegMark
	// SegAbandon withdraws a SegBegin whose audio never arrived, so the
	// announced text is not counted as spoken.
	//
	// It exists because the announcement has to come first. SegBegin is what
	// claims the floor for an answer, and claiming it only once audio exists
	// leaves a window in which a holding filler can take it mid-answer — the
	// defect this player already carries a long comment about. So the text is
	// announced up front and withdrawn here if the synthesis turns out to be
	// silent, rather than never being announced.
	SegAbandon
	// SegEnd closes the turn.
	SegEnd
)

// Segment is one item on the speak channel.
type Segment struct {
	Kind     SegKind
	Gen      uint64
	TurnID   string
	Revision int
	Text     string
	PCM      []byte
}

// TruncationReport describes how a turn was cut short.
type TruncationReport struct {
	TurnID   string
	PlayedMS int64
	TotalMS  int64
	// SpokenText is the prefix of the turn the user actually heard. This, not
	// the full generated text, is what belongs in conversation history.
	SpokenText string
}

// PlayerConfig tunes the output channel.
type PlayerConfig struct {
	// Rate is the client's PCM rate; audio is converted before it is queued.
	Rate int
	// ChunkMS is the size of one session.output_audio.delta.
	ChunkMS int
	// Paced sends audio in roughly real time instead of as fast as the socket
	// drains.
	Paced bool
	// LeadMS is how far ahead of the notional playhead the server may run,
	// absorbing network jitter without losing truncation accuracy.
	LeadMS int
}

// Player is the speak channel. It owns one goroutine, emits audio at roughly
// real time, and knows at every instant how much of the assistant's turn the
// user has actually heard.
//
// That last property is why pacing is on by default. Without it the whole
// answer lands in the client's buffer the moment it is generated, and "the user
// interrupted 900 ms in" becomes unanswerable on the server: history would
// record sentences nobody heard, and the next turn would be reasoning from a
// conversation that did not happen. Pacing costs nothing in perceived latency —
// the first chunk still leaves as soon as it exists — and buys correct history.
type Player struct {
	cfg        PlayerConfig
	emit       func(itemID string, pcm []byte)
	onSpeaking func(bool)
	onTruncate func(TruncationReport)
	onTurnDone func(turnID string, totalMS int64, text string)

	mu     sync.Mutex
	cond   *sync.Cond
	queue  []Segment
	closed bool

	// graceTurn is a turn allowed to finish the sentence it is speaking even
	// though its generation is already stale. It exists for the
	// "finish_sentence" policy, where the assistant yields the floor politely
	// rather than mid-word.
	graceTurn  string
	graceMarks int

	// dropTurn is a backchannel or filler whose remaining audio must not be
	// emitted, because the answer it was covering for is now ready. It is not
	// the generation counter's job: a filler's generation is perfectly valid,
	// it has simply stopped being useful.
	dropTurn string

	activeTurn  string
	turnStarted time.Time
	playhead    time.Time
	emittedMS   float64
	spoken      []spokenSpan
	pendingText string
	pendingFrom float64
	speaking    bool
}

type spokenSpan struct {
	text  string
	endMS float64
}

// NewPlayer builds a player. emit is called for each outbound audio chunk.
func NewPlayer(cfg PlayerConfig, emit func(itemID string, pcm []byte)) *Player {
	if cfg.ChunkMS <= 0 {
		cfg.ChunkMS = 40
	}
	if cfg.LeadMS <= 0 {
		cfg.LeadMS = 300
	}
	if cfg.Rate <= 0 {
		cfg.Rate = 24000
	}
	p := &Player{cfg: cfg, emit: emit}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// OnSpeaking registers a callback for speak-channel transitions. It is invoked
// with the player's lock held, so it must not call back into the player.
func (p *Player) OnSpeaking(f func(bool)) { p.onSpeaking = f }

// OnTruncate registers a callback for interrupted turns.
func (p *Player) OnTruncate(f func(TruncationReport)) { p.onTruncate = f }

// OnTurnDone registers a callback for turns that finished uninterrupted.
func (p *Player) OnTurnDone(f func(turnID string, totalMS int64, text string)) { p.onTurnDone = f }

// Run drives the player until Close. It blocks; start it on its own goroutine.
func (p *Player) Run(gen *Generation) {
	for {
		p.mu.Lock()
		for len(p.queue) == 0 && !p.closed {
			p.setSpeakingLocked(false)
			p.cond.Wait()
		}
		if p.closed {
			p.setSpeakingLocked(false)
			p.mu.Unlock()
			return
		}
		seg := p.queue[0]
		p.queue = p.queue[1:]
		p.mu.Unlock()

		if !p.allowed(seg, gen) {
			continue
		}
		switch seg.Kind {
		case SegBegin:
			p.beginSegment(seg)
		case SegMark:
			p.markSegment(seg)
		case SegAbandon:
			p.abandonSegment(seg)
		case SegEnd:
			p.finishTurn(seg)
		default:
			p.playAudio(gen, seg)
		}
	}
}

// allowed reports whether a queued item may still be played. Normally that
// means its generation is current; a turn under grace is the one exception.
func (p *Player) allowed(seg Segment, gen *Generation) bool {
	p.mu.Lock()
	if p.dropTurn != "" && p.dropTurn == seg.TurnID {
		p.mu.Unlock()
		return false
	}
	p.mu.Unlock()
	if gen.Valid(seg.Gen) {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.graceTurn != "" && p.graceTurn == seg.TurnID
}

// IsBackchannel reports whether a turn ID belongs to an acknowledgement or a
// holding filler rather than to an answer.
func IsBackchannel(turnID string) bool { return strings.HasPrefix(turnID, "bc_") }

// PreemptBackchannel cuts short an acknowledgement or holding filler that is
// still playing, and reports whether there was one.
//
// A filler exists to cover a wait, and it stops being worth anything the moment
// the wait is over. Left to finish, it does the opposite of its job: a real call
// showed the answer synthesized at 3652 ms and not reaching the wire until
// 5620 ms, because "我看一下" was still occupying the floor. Nearly two seconds
// added by the thing meant to hide the delay.
//
// The cut is at the next chunk boundary, so at most one chunk of the filler is
// lost, and a paced client still hears its buffered tail. Trailing off
// mid-syllable because the answer arrived is what a person does anyway.
//
// No truncation is reported: a filler is not part of the conversation and must
// not reach history, which is the same reason bc_ turns are skipped there.
func (p *Player) PreemptBackchannel() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !IsBackchannel(p.activeTurn) {
		return false
	}
	id := p.activeTurn
	p.dropTurn = id
	kept := p.queue[:0]
	for _, seg := range p.queue {
		if seg.TurnID != id {
			kept = append(kept, seg)
		}
	}
	p.queue = kept
	p.resetTurnLocked()
	p.setSpeakingLocked(false)
	p.cond.Signal()
	return true
}

// Active reports whether the assistant still holds the floor: speaking now,
// holding audio the listener has not reached yet, or with more queued behind it.
//
// This is the question to ask when the user starts a new query — not
// Speaking(), which goes false in the gap between two synthesized sentences and
// would let a stale answer resume over the new one.
func (p *Player) Active() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.speaking || len(p.queue) > 0 || p.activeTurn != ""
}

// GraceFinishSegment lets turnID complete the sentence it is currently speaking
// even once its generation goes stale, then drops the rest of it. Call it
// before bumping the generation.
func (p *Player) GraceFinishSegment(turnID string) {
	p.mu.Lock()
	p.graceTurn = turnID
	p.graceMarks = 1
	p.mu.Unlock()
}

// Enqueue adds an item to the speak channel, and reports whether it was
// accepted.
//
// A backchannel arriving after an answer has taken the floor is refused, and
// that refusal is load-bearing rather than tidy-minded. maybeHoldingFiller
// checks the floor is free before deciding to speak, but speakBackchannel then
// synthesizes on its own goroutine, which takes a few hundred milliseconds —
// ample time for the answer it was covering for to arrive and start playing. Its
// audio then landed in the middle of that answer.
//
// The audible result is bad enough: "费用看方案。" — "让我查一下" — "您先看下微信…".
// The invisible result is worse. Every turn switch resets the player's
// per-turn accounting, so the answer's emittedMS restarted and its spoken spans
// were dropped; a real call reported output_audio_ms: 801 for a forty-seven
// character answer, and fed conversation history a five-character fragment of
// what had actually been said. The model, believing it had never explained
// itself, explained itself again on the next turn — which is exactly what that
// call's transcript shows.
//
// So the floor belongs to an answer from its first segment to its last. A
// backchannel may take a free floor, and PreemptBackchannel hands it back the
// moment an answer needs it, but the two never overlap.
func (p *Player) Enqueue(seg Segment) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	if IsBackchannel(seg.TurnID) && (p.dropTurn == seg.TurnID || p.realTurnHoldsFloorLocked()) {
		// Drop this one and everything else already queued for it, so a
		// SegBegin that slipped in earlier cannot leave the turn half-open.
		p.dropTurn = seg.TurnID
		kept := p.queue[:0]
		for _, q := range p.queue {
			if q.TurnID != seg.TurnID {
				kept = append(kept, q)
			}
		}
		p.queue = kept
		return false
	}
	p.queue = append(p.queue, seg)
	p.cond.Signal()
	return true
}

// realTurnHoldsFloorLocked reports whether an answer is playing or waiting to.
func (p *Player) realTurnHoldsFloorLocked() bool {
	if p.activeTurn != "" && !IsBackchannel(p.activeTurn) {
		return true
	}
	for _, seg := range p.queue {
		if !IsBackchannel(seg.TurnID) {
			return true
		}
	}
	return false
}

// Interrupt cuts the current turn. The caller must bump the generation first so
// upstream producers stop; this clears the queue and reports, through
// OnTruncate, exactly how much the listener heard.
func (p *Player) Interrupt() {
	p.mu.Lock()
	p.queue = nil
	p.graceTurn = ""
	turnID := p.activeTurn
	if turnID == "" {
		p.setSpeakingLocked(false)
		p.mu.Unlock()
		return
	}
	playedMS := p.heardMSLocked()
	totalMS := p.emittedMS
	text := p.spokenPrefixLocked(playedMS)
	p.resetTurnLocked()
	p.setSpeakingLocked(false)
	p.mu.Unlock()

	p.reportTruncation(TruncationReport{
		TurnID:     turnID,
		PlayedMS:   int64(playedMS),
		TotalMS:    int64(totalMS),
		SpokenText: text,
	})
}

// reportTruncation forwards a truncation, unless there was nothing to truncate.
//
// A turn cancelled between being queued and producing its first chunk has an
// active turn ID and no audio behind it. Reporting that as a truncation is how
// a clean cancellation ended up in the log as "truncated at 0ms of 0ms", and
// how a turn nobody heard emitted a full set of all-zero latencies — noise that
// makes a real truncation harder to spot.
func (p *Player) reportTruncation(r TruncationReport) {
	if p.onTruncate == nil {
		return
	}
	if r.TotalMS == 0 && r.SpokenText == "" {
		return
	}
	p.onTruncate(r)
}

// Close stops the player.
func (p *Player) Close() {
	p.mu.Lock()
	p.closed = true
	p.queue = nil
	p.mu.Unlock()
	p.cond.Broadcast()
}

// Speaking reports whether audio is currently going out.
func (p *Player) Speaking() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.speaking
}

// PendingMS is how much emitted-but-not-yet-heard audio is outstanding.
func (p *Player) PendingMS() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.activeTurn == "" {
		return 0
	}
	remaining := p.emittedMS - p.heardMSLocked()
	if remaining < 0 {
		return 0
	}
	return int64(remaining)
}

func (p *Player) ensureTurnLocked(turnID string) {
	if p.activeTurn == turnID {
		return
	}
	p.resetTurnLocked()
	// dropTurn is deliberately not cleared here. Turn ids are unique for the
	// life of a session — item_N and bc_N both come from monotonic counters —
	// so a stale entry can never match a later turn, and keeping it means a
	// dropped backchannel stays dropped even if one of its segments is still
	// making its way through the queue.
	p.activeTurn = turnID
	p.turnStarted = time.Now()
	p.playhead = p.turnStarted
}

func (p *Player) beginSegment(seg Segment) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureTurnLocked(seg.TurnID)
	p.pendingText = seg.Text
	p.pendingFrom = p.emittedMS
}

// abandonSegment drops the text announced by a SegBegin that produced no audio.
//
// fullSpokenLocked counts pendingText, because a segment cut off mid-flight did
// reach the listener's ears in part. A segment that emitted nothing did not, and
// leaving it pending puts a sentence the caller never heard into conversation
// history — after which the model answers its own unheard sentence.
func (p *Player) abandonSegment(seg Segment) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.activeTurn != seg.TurnID {
		return
	}
	p.pendingText = ""
	p.pendingFrom = p.emittedMS
}

func (p *Player) markSegment(seg Segment) {
	p.mu.Lock()
	if p.activeTurn != seg.TurnID {
		p.mu.Unlock()
		return
	}
	text := p.pendingText
	if seg.Text != "" {
		text = seg.Text
	}
	if text != "" {
		p.spoken = append(p.spoken, spokenSpan{text: text, endMS: p.emittedMS})
	}
	p.pendingText = ""
	p.pendingFrom = p.emittedMS

	if p.graceTurn != seg.TurnID {
		p.mu.Unlock()
		return
	}
	p.graceMarks--
	if p.graceMarks > 0 {
		p.mu.Unlock()
		return
	}

	// The sentence is finished and the floor is handed over. Everything
	// emitted has reached the client and will be heard, so this reports the
	// whole of it as played rather than only the part the playhead has
	// reached — nothing is being cut off.
	p.graceTurn = ""
	p.queue = nil
	turnID := p.activeTurn
	totalMS := p.emittedMS
	spoken := p.fullSpokenLocked()
	p.resetTurnLocked()
	p.setSpeakingLocked(false)
	p.mu.Unlock()

	p.reportTruncation(TruncationReport{
		TurnID:     turnID,
		PlayedMS:   int64(totalMS),
		TotalMS:    int64(totalMS),
		SpokenText: spoken,
	})
}

func (p *Player) playAudio(gen *Generation, seg Segment) {
	format := audio.PCM16(p.cfg.Rate)
	chunkBytes := format.BytesForMS(p.cfg.ChunkMS)
	if chunkBytes <= 0 {
		chunkBytes = len(seg.PCM)
	}
	// Never split a sample in half.
	chunkBytes -= chunkBytes % audio.BytesPerSample

	p.mu.Lock()
	p.ensureTurnLocked(seg.TurnID)
	p.setSpeakingLocked(true)
	p.mu.Unlock()

	for off := 0; off < len(seg.PCM); off += chunkBytes {
		if !p.allowed(seg, gen) {
			return
		}
		end := off + chunkBytes
		if end > len(seg.PCM) {
			end = len(seg.PCM)
		}
		chunk := seg.PCM[off:end]

		if p.cfg.Paced {
			p.mu.Lock()
			target := p.playhead.Add(-time.Duration(p.cfg.LeadMS) * time.Millisecond)
			p.mu.Unlock()
			if wait := time.Until(target); wait > 0 {
				timer := time.NewTimer(wait)
				<-timer.C
				timer.Stop()
				if !p.allowed(seg, gen) {
					return
				}
			}
		}

		p.emit(seg.TurnID, chunk)

		p.mu.Lock()
		chunkMS := format.DurationMS(chunk)
		p.emittedMS += chunkMS
		p.playhead = p.playhead.Add(time.Duration(chunkMS*1000) * time.Microsecond)
		p.mu.Unlock()
	}
}

// finishTurn waits out the tail of a completed turn so the speak channel stays
// "on" until the listener has actually heard the last chunk, then reports it.
func (p *Player) finishTurn(seg Segment) {
	p.mu.Lock()
	if p.activeTurn != seg.TurnID {
		p.mu.Unlock()
		return
	}
	// A turn under grace ends here when the soft stop landed between segments,
	// so no further mark was ever coming. It still ended early because the user
	// spoke, so it is reported the same way as one that ran to a mark — a
	// grace that resolved only on a mark would wait for one that never arrives.
	graced := p.graceTurn == seg.TurnID
	p.graceTurn = ""
	p.graceMarks = 0

	remaining := p.emittedMS - p.heardMSLocked()
	totalMS := p.emittedMS
	text := p.fullSpokenLocked()
	p.mu.Unlock()

	if p.cfg.Paced && remaining > 0 {
		timer := time.NewTimer(time.Duration(remaining) * time.Millisecond)
		<-timer.C
		timer.Stop()
	}

	p.mu.Lock()
	if p.activeTurn != seg.TurnID {
		p.mu.Unlock()
		return
	}
	p.resetTurnLocked()
	p.setSpeakingLocked(false)
	p.mu.Unlock()

	if graced {
		p.reportTruncation(TruncationReport{
			TurnID:     seg.TurnID,
			PlayedMS:   int64(totalMS),
			TotalMS:    int64(totalMS),
			SpokenText: text,
		})
		return
	}
	if p.onTurnDone != nil {
		p.onTurnDone(seg.TurnID, int64(totalMS), text)
	}
}

func (p *Player) heardMSLocked() float64 {
	if p.turnStarted.IsZero() {
		return 0
	}
	if !p.cfg.Paced {
		return p.emittedMS
	}
	heard := float64(time.Since(p.turnStarted).Milliseconds())
	if heard > p.emittedMS {
		heard = p.emittedMS
	}
	if heard < 0 {
		heard = 0
	}
	return heard
}

// spokenPrefixLocked reconstructs the text the listener heard: whole segments
// that finished before the cut, plus a proportional slice of the one that was
// still playing. Character count is a crude proxy for duration, but it is
// stable across scripts and far better than claiming the whole segment landed.
func (p *Player) spokenPrefixLocked(playedMS float64) string {
	var out string
	var prevEnd float64
	for _, span := range p.spoken {
		if span.endMS <= playedMS {
			out += span.text
			prevEnd = span.endMS
			continue
		}
		out += partialText(span.text, playedMS-prevEnd, span.endMS-prevEnd)
		return out
	}
	if p.pendingText != "" && playedMS > p.pendingFrom {
		out += partialText(p.pendingText, playedMS-p.pendingFrom, p.emittedMS-p.pendingFrom)
	}
	return out
}

func partialText(text string, playedMS, totalMS float64) string {
	if totalMS <= 0 || playedMS <= 0 {
		return ""
	}
	frac := playedMS / totalMS
	if frac > 1 {
		frac = 1
	}
	runes := []rune(text)
	keep := int(float64(len(runes)) * frac)
	if keep <= 0 {
		return ""
	}
	return string(runes[:keep])
}

func (p *Player) fullSpokenLocked() string {
	var out string
	for _, span := range p.spoken {
		out += span.text
	}
	if p.pendingText != "" {
		out += p.pendingText
	}
	return out
}

func (p *Player) resetTurnLocked() {
	p.activeTurn = ""
	p.graceTurn = ""
	p.graceMarks = 0
	p.turnStarted = time.Time{}
	p.playhead = time.Time{}
	p.emittedMS = 0
	p.spoken = nil
	p.pendingText = ""
	p.pendingFrom = 0
}

func (p *Player) setSpeakingLocked(state bool) {
	if p.speaking == state {
		return
	}
	p.speaking = state
	if p.onSpeaking != nil {
		p.onSpeaking(state)
	}
}
