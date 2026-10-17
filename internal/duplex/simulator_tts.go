package duplex

import "time"

// SimulatorWarningEvent is a recoverable segment failure, kept inside the
// simulator report path. It deliberately carries no vendor error or speech text.
// SegmentID is one-based within TurnID. A partial segment may have produced
// audio, but its text is not claimed as fully spoken.
type SimulatorWarningEvent struct {
	Type      string    `json:"type"`
	Code      string    `json:"code"`
	TurnID    string    `json:"turn_id"`
	SegmentID int       `json:"segment_id"`
	At        time.Time `json:"at"`
}

// recoverSimulatorTTS withdraws an incomplete segment and lets the same pipe
// attempt its following segments. The broken provider connection is discarded
// by the caller; this is not a retry of speech the remote may partly have heard.
func (p *speechPipe) recoverSimulatorTTS(code string, announced bool) bool {
	if p.ctx.Err() != nil || !p.current() {
		return false
	}
	if announced {
		// The full duration of a failed streamed segment is unknown. Preserve PCM
		// accounting, but omit its uncertain text from the conversation history.
		p.e.player.Enqueue(Segment{Kind: SegAbandon, Gen: p.turn.Generation, TurnID: p.turn.ID, Revision: p.turn.Revision})
	}
	p.e.deps.Emit(SimulatorWarningEvent{Type: "simulator.warning", Code: code, TurnID: p.turn.ID, SegmentID: p.segmentID, At: time.Now()})
	return true
}
