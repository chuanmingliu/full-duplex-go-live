package duplex

import "github.com/chuanmingliu/golive/internal/tools"

// contentSuppressedLocked also rejects late producer output and segments the
// player dequeued before the handoff acquired its lock.
func (p *Player) contentSuppressedLocked(seg Segment) bool {
	return seg.content && seg.TurnID == p.handoffTurn && seg.Gen == p.handoffGen
}

// finishBeforeContent preserves the opener, including its queued tail, while
// discarding a body that has not committed even one chunk to output. The normal
// end marker reports what was really played, without a truncation or invented
// zero-audio completion. An opener must already have emitted audio.
func (p *Player) finishBeforeContent(turn *Turn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.activeTurn != turn.ID || p.emittedMS < 1 || p.contentStarted ||
		p.graceTurn != "" || p.drainingTurn != "" || p.dropTurn == turn.ID ||
		(p.handoffTurn == turn.ID && p.handoffGen == turn.Generation) {
		return false
	}
	for _, seg := range p.queue {
		if seg.TurnID != turn.ID || seg.Gen != turn.Generation {
			return false // another owner has already queued work
		}
	}
	p.handoffTurn, p.handoffGen = turn.ID, turn.Generation
	if p.pendingContent {
		p.pendingText = ""
		p.pendingFrom = p.emittedMS
		p.pendingAnnounced = false
		p.pendingContent = false
	}
	kept := p.queue[:0]
	for _, seg := range p.queue {
		if !seg.content && seg.Kind != SegEnd {
			kept = append(kept, seg)
		}
	}
	clear(p.queue[len(kept):])
	p.queue = append(kept, Segment{Kind: SegEnd, TurnID: turn.ID, Gen: turn.Generation, Revision: turn.Revision})
	p.cond.Signal()
	return true
}

// tryReadyQuoteHandoff is deliberately narrower than ordinary result delivery:
// a successful deterministic readout may replace only the unplayed body of its
// own calling turn. Once any body audio starts, the existing clause protection
// remains in force. Caller, memory or floor changes also keep that protection.
func (e *Engine) tryReadyQuoteHandoff(delivery taskDelivery) {
	task, origin := delivery.task, delivery.origin
	if e.userOpen || e.awaitingFinal() || e.hold != nil || e.liveSpeculation() != nil ||
		e.muted || e.hangingUp() || e.ctx.Err() != nil || task.Result.Err != nil ||
		tools.ResultStatus(task.Result.Output) != "quote" ||
		!e.tools.ackPending[task.ID] || e.tools.ackGraceStarted[task.ID] ||
		task.Tool.OpeningFor(task.Result.Output) == "" || task.Tool.ReadoutFor(task.Result.Output) == "" {
		return
	}
	turn := e.tracker.Current()
	if turn == nil || !e.tracker.IsCurrent(turn) || turn.ID != origin.turnID || turn.Scripted || turn.Opened == "" {
		return
	}
	timing := turn.Timings()
	if origin.speechEndAt.IsZero() || !timing.SpeechEndAt.Equal(origin.speechEndAt) ||
		timing.FirstAudioOut.IsZero() || !timing.FirstContentOut.IsZero() || !timing.CompletedAt.IsZero() {
		return
	}
	_, version := e.memorySnapshot()
	if version != task.Request.MemoryVersion {
		return
	}
	if len(e.continuations) == 0 || e.continuations[0].turn != turn ||
		e.continuations[0].scriptedTask != task.ID {
		return
	}
	pipe := e.speech.Load()
	if pipe != nil && pipe.turn != turn {
		return
	}
	if !e.player.finishBeforeContent(turn) {
		return
	}
	e.tools.handoff = turn
	if pipe != nil {
		pipe.Abort()
	}
	e.log.Info("tools: ready result replaced the unplayed calling body", "task", task.ID, "turn", turn.ID)
}
