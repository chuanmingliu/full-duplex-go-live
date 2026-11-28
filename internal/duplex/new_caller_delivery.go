package duplex

import "time"

// A result's old queue deadline must not consume a newer caller's answer
// immediately after ASR finalization. Give that answer the existing delivery
// wait from its own endpoint, or until playback completes, whichever comes
// first. This never delays a result already linked to that exact caller.
func (e *Engine) newerCallerAnswerPending(delivery taskDelivery, wait time.Duration) bool {
	turn := e.tracker.Current()
	if turn == nil || turn.Scripted || turn.Dead() || !e.gen.Valid(turn.Generation) || turn.ID == delivery.origin.turnID {
		return false
	}
	if state := delivery.coalesced; state != nil {
		for _, origin := range state.origins {
			if origin.turnID == turn.ID {
				return false
			}
		}
	}
	timing := turn.Timings()
	return !timing.SpeechEndAt.IsZero() && timing.CompletedAt.IsZero() && time.Since(timing.SpeechEndAt) < wait
}
