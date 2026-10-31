package duplex

// playbackCompletion is retained independently of ctrlQ: cancelling the loop
// must not discard proof that output started or finished. Only playback
// accounting is drained at close, never arbitrary queued tool/hangup work.
type playbackCompletion struct {
	report    PlaybackReport
	truncated bool
}

func (e *Engine) postPlaybackCompletion(report PlaybackReport, truncated bool) {
	receipt := &playbackCompletion{report: report, truncated: truncated}
	e.playbackMu.Lock()
	e.playbackPending = append(e.playbackPending, receipt)
	e.playbackMu.Unlock()
	e.post(func() {
		e.playbackMu.Lock()
		found := false
		for i, pending := range e.playbackPending {
			if pending == receipt {
				copy(e.playbackPending[i:], e.playbackPending[i+1:])
				e.playbackPending[len(e.playbackPending)-1] = nil
				e.playbackPending = e.playbackPending[:len(e.playbackPending)-1]
				found = true
				break
			}
		}
		e.playbackMu.Unlock()
		if found {
			e.completePlayback(receipt, e.ctx != nil && e.ctx.Err() != nil)
		}
	})
}

func (e *Engine) completePlayback(receipt *playbackCompletion, closing bool) {
	r := receipt.report
	turn := r.validTurn()
	if turn == nil {
		return
	}
	if !closing && !receipt.truncated {
		defer func() {
			if e.tools != nil && e.tools.handoff == turn {
				e.tools.handoff = nil
			}
		}()
	}
	if receipt.truncated {
		if !closing {
			e.noteAckPlayback(r.TurnID, false)
			e.noteReadoutCut(r.TurnID, r.SpokenText)
		}
		if !turn.MarkCompleted() {
			return
		}
	} else {
		// A superseded revision with no emitted output cannot claim completion.
		if r.TotalMS == 0 || !turn.markCompletedNaturally() {
			return
		}
	}
	e.recordTurn(turn, r.TurnID, r.SpokenText)
	if !closing && !receipt.truncated {
		e.noteAckPlayback(r.TurnID, true)
	}
	totalMS := r.TotalMS
	if receipt.truncated {
		totalMS = r.PlayedMS
	}
	e.emitMetrics(turn, totalMS, receipt.truncated)
	if closing || e.ctx != nil && e.ctx.Err() != nil {
		return
	}
	if e.tracker.Current() == turn {
		e.noteAssistantQuiet()
	}
	if !receipt.truncated && e.tracker.IsCurrent(turn) {
		e.checkAssistantHangupRules(r.SpokenText)
	}
}

// finishPlaybackAtClose runs only after the loop and player have stopped. A
// naturally completed receipt wins over the last active snapshot through the
// same once-only Turn completion guard used during the call.
func (e *Engine) finishPlaybackAtClose(last PlaybackReport) {
	e.playbackMu.Lock()
	pending := e.playbackPending
	e.playbackPending = nil
	e.playbackMu.Unlock()
	for _, receipt := range pending {
		e.completePlayback(receipt, true)
	}
	turn := last.validTurn()
	if turn == nil || IsBackchannel(last.TurnID) || turn.Timings().FirstAudioOut.IsZero() || turn.Dead() && !turn.Committed() {
		return
	}
	// Preserve the existing truncation event/log as well as the latency row.
	// This callback only queues accounting; it cannot launch new work here.
	e.onTruncated(last)
	e.playbackMu.Lock()
	pending = e.playbackPending
	e.playbackPending = nil
	e.playbackMu.Unlock()
	for _, receipt := range pending {
		e.completePlayback(receipt, true)
	}
}
