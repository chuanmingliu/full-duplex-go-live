package journal

import (
	"strings"
	"time"
)

// readOutputTranscript consumes the private, versioned stream emitted by the
// Player's first-output callback, shared by openers, fillers and answer audio.
// Legacy synthesis/cache logs describe readiness and remain the fallback only
// for calls that predate this stream. They must never double-count its words.
func (cb *callBuild) readOutputTranscript(l Line) {
	if l.N("version") != 1 {
		return
	}
	if l.Msg == "transcript: assistant output enabled" {
		cb.transcriptLog = true
		return
	}
	seq, ok := l.Int("seq")
	at, err := time.Parse(time.RFC3339Nano, l.Get("play_at"))
	id, text := l.Get("turn"), l.Get("text")
	if !ok || seq <= cb.transcriptSeq || err != nil || id == "" || strings.TrimSpace(text) == "" {
		return
	}
	cb.transcriptLog, cb.transcriptSeq = true, seq
	t := cb.turn(id)
	if t.outputAt.IsZero() {
		t.outputAt = at
	}
	t.outputText = append(t.outputText, text)
}

func (cb *callBuild) assistantText(t *turnBuild) string {
	if cb.transcriptLog {
		// A started sentence may be cut in its middle. The existing truncation
		// report is the best-known heard prefix, including an empty prefix.
		if t.interrupted {
			return t.heardText
		}
		return strings.Join(t.outputText, "")
	}
	text := strings.Join(t.said, "")
	if t.confirmedOpener() {
		text = t.openerText + text
	}
	if t.interrupted && t.heardText != "" {
		text = t.heardText
	}
	return text
}
