package duplex

import "time"

func (e *Engine) startOutputTranscript() {
	// This versioned stream covers every Player output path, including openers
	// and cached/live fillers. Older logs retain the journal's legacy fallback.
	e.log.Debug("transcript: assistant output enabled", "version", 1)
}

func (e *Engine) logOutputTranscript(turnID, text string, playAt time.Time) {
	// The private call log keeps the same append-only transcript as the wire.
	// Emit only from Player.OnSegment, when its first audio is committed to
	// output. This does not assert that the physical handset has played it.
	// Preparation/cache diagnostics intentionally remain free of spoken text.
	e.log.Debug("transcript: assistant segment started", "version", 1,
		"seq", e.transcriptSeq.Add(1), "turn", turnID, "play_at", playAt.Format(time.RFC3339Nano), "text", text)
}
