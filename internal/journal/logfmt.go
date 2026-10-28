// Package journal is the call journal: a record of every phone call golive
// has answered, read from golive's own log and its stored recordings, and
// served with a page for looking at them.
//
// It exists because the work of making calls faster ran on evidence that was
// thrown away as soon as it had been read: a log slice analysed once, a
// retrospective typed into a conversation. The journal keeps each call — its
// turns timed stage by stage, the audio, what went wrong and what was done
// about it — where the next round of work can look at it.
//
// It reads; it does not take part in a call. golive does not know it exists.
package journal

import (
	"strconv"
	"strings"
	"time"
)

// Line is one line of golive's log: slog's text handler, key=value pairs with
// values quoted when they need to be.
type Line struct {
	Time  time.Time
	Level string
	Msg   string
	kv    map[string]string
}

// Get returns a value, or "" when the line has no such key.
func (l Line) Get(key string) string { return l.kv[key] }

// Int returns a value as an integer, and whether it was one.
func (l Line) Int(key string) (int, bool) {
	v, ok := l.kv[key]
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	return n, err == nil
}

// N is Int for callers with no use for a missing value.
func (l Line) N(key string) int { n, _ := l.Int(key); return n }

// Float returns a value as a number, and whether it was one.
func (l Line) Float(key string) (float64, bool) {
	v, ok := l.kv[key]
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil
}

// Keys reports the line's keys other than time, level and msg.
func (l Line) Keys() []string {
	out := make([]string, 0, len(l.kv))
	for k := range l.kv {
		out = append(out, k)
	}
	return out
}

// ParseLine reads one log line. ok is false for anything that is not one — the
// banner the launcher prints between runs, a panic's stack — and those are
// skipped, not errors.
func ParseLine(s string) (Line, bool) {
	if !strings.HasPrefix(s, "time=") {
		return Line{}, false
	}
	l := Line{kv: make(map[string]string, 12)}
	for i := 0; i < len(s); {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq <= 0 {
			break
		}
		key := s[i : i+eq]
		if strings.ContainsAny(key, " \"") {
			break
		}
		i += eq + 1
		var val string
		if i < len(s) && s[i] == '"' {
			end := i + 1
			for end < len(s) && s[end] != '"' {
				if s[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(s) {
				end = len(s) - 1
			}
			raw := s[i : end+1]
			if u, err := strconv.Unquote(raw); err == nil {
				val = u
			} else {
				val = strings.Trim(raw, `"`)
			}
			i = end + 1
		} else {
			end := strings.IndexByte(s[i:], ' ')
			if end < 0 {
				end = len(s) - i
			}
			val = s[i : i+end]
			i += end
		}
		switch key {
		case "time":
			t, err := time.Parse(time.RFC3339Nano, val)
			if err != nil {
				return Line{}, false
			}
			l.Time = t
		case "level":
			l.Level = val
		case "msg":
			l.Msg = val
		default:
			l.kv[key] = val
		}
	}
	return l, !l.Time.IsZero() && l.Msg != ""
}
