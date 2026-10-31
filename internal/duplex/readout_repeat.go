package duplex

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// readoutRepeatFilter removes at most one exact leading copy of a readout that
// the same turn has already queued. While a match is incomplete, matched is
// enough to retain it: those bytes are already in readout. It never searches
// later text, normalizes numbers, or removes a partial/mismatched prefix.
type readoutRepeatFilter struct {
	readout string
	matched int
	done    bool
}

func newReadoutRepeatFilter(readout string) readoutRepeatFilter {
	// A bare amount or unfinished phrase could prefix a legitimate revision.
	// Require a sentence boundary; a period directly after a digit could be
	// the start of a fractional amount, so leave that case unchanged too.
	end := strings.TrimRightFunc(readout, unicode.IsSpace)
	last, size := utf8.DecodeLastRuneInString(end)
	switch last {
	case '。', '！', '？', '!', '?':
	case '.':
		previous, _ := utf8.DecodeLastRuneInString(end[:len(end)-size])
		if unicode.IsDigit(previous) {
			return readoutRepeatFilter{done: true}
		}
	default:
		return readoutRepeatFilter{done: true}
	}
	return readoutRepeatFilter{readout: readout}
}

func (f *readoutRepeatFilter) Push(text string) string {
	if f.done {
		return text
	}
	remaining := f.readout[f.matched:]
	n := min(len(remaining), len(text))
	if remaining[:n] != text[:n] {
		f.done = true
		return f.readout[:f.matched] + text
	}
	f.matched += n
	if f.matched < len(f.readout) {
		return ""
	}
	f.done = true
	return text[n:]
}

// Flush preserves an unfinished match at normal EOF or provider failure. The
// caller must not flush after cancellation or when this turn becomes stale.
func (f *readoutRepeatFilter) Flush() string {
	if f.done {
		return ""
	}
	f.done = true
	return f.readout[:f.matched]
}
