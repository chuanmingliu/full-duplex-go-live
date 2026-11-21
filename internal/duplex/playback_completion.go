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
	if e.openingTurn == turn {
		defer func() {
			e.log.Info("opening: protection finished", "turn", turn.ID, "truncated", receipt.truncated)
			e.openingTurn = nil
		}()
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
		if e.tools != nil && r.TurnID == e.tools.lastResultTurn {
			e.tools.lastResultComplete = false
		}
		wasNatural := turn.Timings().CompletedNaturally
		if !turn.MarkCompleted() {
			if wasNatural {
				e.noteQuotePlayback(turn, r, true)
			}
			return
		}
	} else {
		// A superseded revision with no emitted output cannot claim completion.
		if r.TotalMS == 0 || !turn.markCompletedNaturally() {
			return
		}
	}
	if e.tools != nil && r.TurnID == e.tools.lastResultTurn {
		e.tools.lastResultComplete = !receipt.truncated
	}
	e.noteQuotePlayback(turn, r, receipt.truncated)
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

// Drain only playback receipts on the engine loop. Interrupt returns after the
// player's truncation callback, so a new request can see the updated prefix
// even when both actions occur within the same control callback.
func (e *Engine) drainPlaybackCompletions(closing bool) {
	e.playbackMu.Lock()
	pending := e.playbackPending
	e.playbackPending = nil
	e.playbackMu.Unlock()
	for _, receipt := range pending {
		e.completePlayback(receipt, closing)
	}
}

// finishPlaybackAtClose runs only after the loop and player have stopped. A
// naturally completed receipt wins over the last active snapshot through the
// same once-only Turn completion guard used during the call.
func (e *Engine) finishPlaybackAtClose(last PlaybackReport) {
	e.drainPlaybackCompletions(true)
	turn := last.validTurn()
	if turn == nil || IsBackchannel(last.TurnID) || turn.Timings().FirstAudioOut.IsZero() || turn.Dead() && !turn.Committed() {
		return
	}
	// Preserve the existing truncation event/log as well as the latency row.
	// This callback only queues accounting; it cannot launch new work here.
	e.onTruncated(last)
	e.drainPlaybackCompletions(true)
}
