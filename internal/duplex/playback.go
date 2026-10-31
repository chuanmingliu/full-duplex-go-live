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
	owner    *Turn
	Kind     SegKind
	Gen      uint64
	TurnID   string
	Revision int
	Text     string
	PCM      []byte
	// content distinguishes the model/result body from the opener already
	// spoken at the pause. Only an entirely unplayed body may be handed off.
	content bool
	// readout captures deterministic-result content before the producer moves
	// on. Output stamps are committed with the playback owner, not at enqueue.
	readout bool
}

// PlaybackReport binds heard output to the exact turn revision that owned it.
type PlaybackReport struct {
	owner      *Turn
	Generation uint64
	Revision   int
	TurnID     string
	PlayedMS   int64
	TotalMS    int64
	// SpokenText is the prefix of the turn the user actually heard. This, not
	// the full generated text, is what belongs in conversation history.
	SpokenText string
}

type TruncationReport = PlaybackReport

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
	emit       func(itemID string, pcm []byte, playAt time.Time)
	onSpeaking func(bool)
	onTruncate func(TruncationReport)
	onTurnDone func(PlaybackReport)
	onSegment  func(turnID, text string, playAt time.Time)

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

	activeTurn     string
	owner          *Turn
	activeGen      uint64
	activeRevision int
	adoptedFrom    *Turn
	adoptedTo      *Turn
	turnStarted    time.Time
	playhead       time.Time
	emittedMS      float64
	// drainingTurn is a backchanneled opener's turn whose queue was dropped
	// and whose last paced audio is still in the air; the floor is free.
	drainingTurn string
	spoken       []spokenSpan
	pendingText  string
	pendingFrom  float64
	// pendingAnnounced records that the segment in pendingText has been
	// reported through onSegment, which happens on its first audio.
	pendingAnnounced  bool
	speaking          bool
	pendingContent    bool
	contentStarted    bool
	handoffTurn       string
	handoffGen        uint64
	retiredOpenerTurn string
	retiredOpenerGen  uint64
}

type spokenSpan struct {
	text  string
	endMS float64
}

// NewPlayer builds a player. emit is called for each outbound audio chunk, with
// the moment the listener will hear it — a little after the moment it is sent,
// because the player runs ahead of the playhead by up to LeadMS.
func NewPlayer(cfg PlayerConfig, emit func(itemID string, pcm []byte, playAt time.Time)) *Player {
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

// OnSegment registers a callback for a segment of text starting to be heard.
//
// It fires on the segment's first audio, not when the text is queued, and that
// is the point of it: the transcript a client is shown should be what was said.
// Text is generated a sentence or more ahead of the voice, so reporting it as
// it was written put words in the transcript that an interruption then meant
// nobody ever heard. It is invoked with the player's lock held, so it must not
// call back into the player.
func (p *Player) OnSegment(f func(turnID, text string, playAt time.Time)) { p.onSegment = f }

// OnTurnDone registers a callback for turns that finished uninterrupted.
func (p *Player) OnTurnDone(f func(PlaybackReport)) { p.onTurnDone = f }

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
		p.queue[0] = Segment{}
		p.queue = p.queue[1:]
		p.mu.Unlock()

		if !p.allowed(seg, gen) {
			continue
		}
		switch seg.Kind {
		case SegBegin:
			p.beginSegment(gen, seg)
		case SegMark:
			p.markSegment(gen, seg)
		case SegAbandon:
			p.abandonSegment(gen, seg)
		case SegEnd:
			p.finishTurn(gen, seg)
		default:
			p.playAudio(gen, seg)
		}
	}
}

// allowed reports whether a queued item may still be played. Normally that
// means its generation is current; a turn under grace is the one exception.
func (p *Player) allowed(seg Segment, gen *Generation) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.allowedLocked(seg, gen)
}

func (p *Player) allowedLocked(seg Segment, gen *Generation) bool {
	if p.closed || !seg.validOwner() || (p.dropTurn != "" && p.dropTurn == seg.TurnID) || p.contentSuppressedLocked(seg) || p.retiredOpenerLocked(seg) {
		return false
	}
	if p.adoptedOpenerLocked(seg) && gen.Valid(p.adoptedTo.Generation) {
		return true
	}
	return gen.Valid(seg.Gen) || (p.graceTurn != "" && p.graceTurn == seg.TurnID && p.matchesSegmentLocked(seg))
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
	clear(p.queue[len(kept):])
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

// FreeForOpener reports whether a new turn's opener may be said now: nothing
// is queued, and the only turn the player still holds, if any, is a
// backchanneled opener draining its last paced milliseconds
// (FinishOpenerAsBackchannel). Active stays true through that drain, which
// is right for a filler and wrong for the opener of the turn that followed.
func (p *Player) FreeForOpener() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) > 0 {
		return false
	}
	return p.activeTurn == "" || p.activeTurn == p.drainingTurn
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

// FinishOpenerAsBackchannel keeps only what turnID is saying this instant —
// its opener — and lets the turn go: anything queued behind it is dropped,
// and if the opener has already played out the turn ends now, so the floor
// reads free for the next turn's opener. The caller went on inside the
// opener's breath; the word stands as an 嗯 of listening and nothing of the
// guessed answer follows it. Before this, a turn whose opener had finished
// kept the player "active" with nothing to play, and the next turn — the
// continuation the caller finished — opened with nothing (the 10:07
// simulated call of 10-26: eleven such turns). No truncation is reported:
// the whole of what was said was heard.
// False means the exact opener no longer owns playback or body output has
// already committed. The caller must then use normal interruption handling.
func (p *Player) FinishOpenerAsBackchannel(turnID string, generation uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.activeTurn != turnID || p.activeGen != generation || p.contentStarted {
		return false
	}
	// Reserve retirement under the same lock as first body output. Neither
	// a dequeued segment nor a late producer may restart this generation
	// between returning here and the caller cancelling its backend work.
	p.retiredOpenerTurn, p.retiredOpenerGen = turnID, generation
	kept := p.queue[:0]
	for _, q := range p.queue {
		if q.TurnID != turnID || q.Gen != generation {
			kept = append(kept, q)
		}
	}
	clear(p.queue[len(kept):])
	p.queue = kept
	// The opener's mark has usually passed by now (it is one word, and the
	// caller went on after it), so a grace on the next mark would never
	// resolve; what may remain is paced audio still in the air. Let that
	// play out, then end the turn.
	remaining := p.emittedMS - p.heardMSLocked()
	if remaining <= 0 {
		p.resetTurnLocked()
		p.setSpeakingLocked(false)
		return true
	}
	// Until then the turn is draining, not speaking: the floor is free for
	// the next turn's opener (FreeForOpener). The 22:05 simulated call of
	// 10-27: five fragments closed 170–200 ms after their backchannel, inside
	// the ≤300 ms (playback_lead_ms) of audio in the air, and sayOpener found
	// the player active and said nothing — first audio 742–1131 ms.
	p.drainingTurn = turnID
	go func() {
		time.Sleep(time.Duration(remaining) * time.Millisecond)
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.activeTurn == turnID && p.activeGen == generation && len(p.queue) == 0 {
			p.resetTurnLocked()
			p.setSpeakingLocked(false)
		}
	}()
	return true
}

func (p *Player) retiredOpenerLocked(seg Segment) bool {
	return p.retiredOpenerTurn != "" && seg.TurnID == p.retiredOpenerTurn && seg.Gen == p.retiredOpenerGen
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
	if p.closed || !seg.validOwner() || p.retiredOpenerLocked(seg) {
		return false
	}
	if p.contentSuppressedLocked(seg) {
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
		clear(p.queue[len(kept):])
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
		p.resetTurnLocked()
		p.setSpeakingLocked(false)
		p.mu.Unlock()
		return
	}
	playedMS := p.heardMSLocked()
	totalMS := p.emittedMS
	owner, ownerGen, ownerRevision := p.owner, p.activeGen, p.activeRevision
	text := p.spokenPrefixLocked(playedMS)
	p.resetTurnLocked()
	p.setSpeakingLocked(false)
	p.mu.Unlock()

	p.reportTruncation(TruncationReport{owner: owner, Generation: ownerGen, Revision: ownerRevision,
		TurnID:     turnID,
		PlayedMS:   int64(playedMS),
		TotalMS:    int64(totalMS),
		SpokenText: text,
	})
}

// Heard reports the turn now playing and the part of it that has reached the
// listener, without disturbing it. It is what a snapshot of the conversation
// taken mid-sentence needs: the words so far are part of the conversation even
// though the turn has not ended.
func (p *Player) Heard() (turnID, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.activeTurn == "" {
		return "", ""
	}
	return p.activeTurn, p.spokenPrefixLocked(p.heardMSLocked())
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
func (p *Player) Close() { p.closeWithReport() }

// closeWithReport captures the final exact playback owner before clearing it.
// It stops at the same boundary as Close; it neither drains nor emits audio.
// An opener explicitly reclassified as a backchannel is not an unfinished reply.
func (p *Player) closeWithReport() PlaybackReport {
	p.mu.Lock()
	var report PlaybackReport
	if !p.closed && p.activeTurn != "" && p.drainingTurn != p.activeTurn && p.emittedMS > 0 {
		played := p.heardMSLocked()
		report = PlaybackReport{owner: p.owner, Generation: p.activeGen, Revision: p.activeRevision,
			TurnID: p.activeTurn, PlayedMS: int64(played), TotalMS: int64(p.emittedMS), SpokenText: p.spokenPrefixLocked(played)}
	}
	p.closed = true
	p.queue = nil
	p.resetTurnLocked()
	p.mu.Unlock()
	p.cond.Broadcast()
	return report
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

func (p *Player) ensureTurnLocked(seg Segment) {
	if p.adoptedOpenerLocked(seg) && p.activeTurn == "" {
		// A begin marker may already be dequeued when the revision inherits
		// its opener. Its first dispatch claims the explicit new owner.
		p.activeTurn, p.owner = p.adoptedTo.ID, p.adoptedTo
		p.activeGen, p.activeRevision = p.owner.Generation, p.owner.Revision
		p.turnStarted = time.Now()
		p.playhead = p.turnStarted
	}
	if p.matchesSegmentLocked(seg) {
		return
	}
	p.resetTurnLocked()
	// Keep dropTurn: a late backchannel producer must stay dropped.
	p.activeTurn, p.owner = seg.TurnID, seg.owner
	p.activeGen, p.activeRevision = seg.Gen, seg.Revision
	p.turnStarted = time.Now()
	p.playhead = p.turnStarted
}

func (p *Player) beginSegment(gen *Generation, seg Segment) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.allowedLocked(seg, gen) {
		return
	}
	p.ensureTurnLocked(seg)
	p.pendingText = seg.Text
	p.pendingFrom = p.emittedMS
	p.pendingAnnounced = false
	p.pendingContent = seg.content
}

// abandonSegment drops the text announced by a SegBegin that produced no audio.
//
// fullSpokenLocked counts pendingText, because a segment cut off mid-flight did
// reach the listener's ears in part. A segment that emitted nothing did not, and
// leaving it pending puts a sentence the caller never heard into conversation
// history — after which the model answers its own unheard sentence.
func (p *Player) abandonSegment(gen *Generation, seg Segment) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.allowedLocked(seg, gen) || !p.matchesSegmentLocked(seg) {
		return
	}
	p.pendingText = ""
	p.pendingFrom = p.emittedMS
	p.pendingContent = false
}

func (p *Player) markSegment(gen *Generation, seg Segment) {
	p.mu.Lock()
	if !p.allowedLocked(seg, gen) || !p.matchesSegmentLocked(seg) {
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
	p.pendingContent = false
	if !seg.content {
		p.adoptedFrom, p.adoptedTo = nil, nil
	}

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
	owner, ownerGen, ownerRevision := p.owner, p.activeGen, p.activeRevision
	spoken := p.fullSpokenLocked()
	p.resetTurnLocked()
	p.setSpeakingLocked(false)
	p.mu.Unlock()

	p.reportTruncation(TruncationReport{owner: owner, Generation: ownerGen, Revision: ownerRevision,
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
	if !p.allowedLocked(seg, gen) {
		p.mu.Unlock()
		return
	}
	p.ensureTurnLocked(seg)
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

		p.mu.Lock()
		if !p.allowedLocked(seg, gen) || !p.matchesSegmentLocked(seg) {
			// The turn was cut while this chunk waited its turn.
			p.mu.Unlock()
			return
		}
		if p.cfg.Paced {
			// Synthesis can resume after all previously emitted audio drained.
			// Keep the new output on the current clock, and exclude that silent
			// gap from the elapsed-audio clock used by interruption accounting.
			if now := time.Now(); now.After(p.playhead) {
				p.turnStarted = p.turnStarted.Add(now.Sub(p.playhead))
				p.playhead = now
			}
		}
		playAt := p.playhead
		owner := p.owner
		// Reserve the first body output under the same lock that can hand an
		// unplayed body over to a ready result. Once reserved, it must finish.
		if seg.content {
			p.contentStarted = true
		}
		if !p.pendingAnnounced && p.pendingText != "" {
			p.pendingAnnounced = true
			if p.onSegment != nil {
				p.onSegment(seg.TurnID, p.pendingText, playAt)
			}
		}
		if owner != nil {
			owner.markOutput(seg.content, seg.readout)
		}
		// This chunk is committed to output. Account for it before releasing
		// ownership: the sink may synchronously trigger an interruption.
		chunkMS := format.DurationMS(chunk)
		p.emittedMS += chunkMS
		p.playhead = p.playhead.Add(time.Duration(chunkMS*1000) * time.Microsecond)
		p.mu.Unlock()
		p.emit(seg.TurnID, chunk, playAt)
	}
}

// finishTurn waits out the tail of a completed turn so the speak channel stays
// "on" until the listener has actually heard the last chunk, then reports it.
func (p *Player) finishTurn(gen *Generation, seg Segment) {
	p.mu.Lock()
	if !p.allowedLocked(seg, gen) || !p.matchesSegmentLocked(seg) {
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
	owner, ownerGen, ownerRevision := p.owner, p.activeGen, p.activeRevision
	text := p.fullSpokenLocked()
	p.mu.Unlock()

	if p.cfg.Paced && remaining > 0 {
		timer := time.NewTimer(time.Duration(remaining) * time.Millisecond)
		<-timer.C
		timer.Stop()
	}

	p.mu.Lock()
	if !p.matchesSegmentLocked(seg) {
		p.mu.Unlock()
		return
	}
	p.resetTurnLocked()
	p.setSpeakingLocked(false)
	p.mu.Unlock()

	if graced {
		p.reportTruncation(TruncationReport{owner: owner, Generation: ownerGen, Revision: ownerRevision,
			TurnID:     seg.TurnID,
			PlayedMS:   int64(totalMS),
			TotalMS:    int64(totalMS),
			SpokenText: text,
		})
		return
	}
	if p.onTurnDone != nil {
		p.onTurnDone(PlaybackReport{owner: owner, Generation: ownerGen, Revision: ownerRevision, TurnID: seg.TurnID, TotalMS: int64(totalMS), PlayedMS: int64(totalMS), SpokenText: text})
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
	p.owner, p.adoptedFrom, p.adoptedTo = nil, nil, nil
	p.activeGen, p.activeRevision = 0, 0
	p.drainingTurn = ""
	p.graceTurn = ""
	p.graceMarks = 0
	p.turnStarted = time.Time{}
	p.playhead = time.Time{}
	p.emittedMS = 0
	p.spoken = nil
	p.pendingText = ""
	p.pendingFrom = 0
	p.pendingAnnounced = false
	p.pendingContent = false
	p.contentStarted = false
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
