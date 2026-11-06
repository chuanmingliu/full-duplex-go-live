package journal

import (
	"bufio"
	"io"
	"os"
	"strings"
)

// FillerEvent records only explicit filler evidence. Queued audio is not proof
// that the handset played it; the recording remains the way to hear that.
type FillerEvent struct {
	TS                  float64 `json:"t_s"`
	Turn                string  `json:"turn,omitempty"`
	Kind                string  `json:"kind"`
	Source              string  `json:"source"`
	Text                string  `json:"text,omitempty"`
	Scenario            string  `json:"scenario,omitempty"`
	Outcome             string  `json:"outcome"`
	Reason              string  `json:"reason,omitempty"`
	AudioMS             int     `json:"audio_ms,omitempty"`
	ContinuationMatched bool    `json:"continuation_matched,omitempty"`
}

func (cb *callBuild) readFiller(l Line) {
	t := l.Get("turn")
	f := FillerEvent{TS: cb.at(l.Time), Turn: t, Kind: "opener", Source: "configured_phrase", Text: l.Get("text")}
	switch l.Msg {
	case "speak: the opener fits the caller's words":
		if cb.openerScenarios == nil {
			cb.openerScenarios = map[string]string{}
		}
		cb.openerScenarios[t] = l.Get("scenario")
		return
	case "speak: opened the answer from the phrase cache at the pause":
		f.Source, f.Outcome, f.AudioMS = "phrase_cache", "queued", l.N("audio_ms")
		f.Scenario = cb.openerScenarios[t]
	case "speak: the opener became a backchannel; the caller went on":
		if prior := cb.lastOpener(t); prior != nil {
			prior.Outcome, prior.Reason = "became_backchannel", l.Get("reason")
			return
		}
		f.Outcome, f.Reason = "became_backchannel", l.Get("reason")
		f.Source, f.AudioMS = "unknown", l.N("opener_ms")
	case "speak: the model's opener was already said at the pause":
		if prior := cb.lastOpener(t); prior != nil {
			prior.ContinuationMatched = true
		}
		return
	case "speak: no opener; the floor is busy":
		f.Outcome, f.Reason = "skipped", "floor busy"
	case "speak: no opener on a short fragment after a backchannel":
		f.Outcome, f.Reason = "skipped", "short fragment after a backchannel"
	case "speak: no opener in the cache yet":
		f.Outcome, f.Reason = "skipped", "phrase not cached"
	case "speak: no opener of another kind for the fragment":
		f.Outcome, f.Reason = "skipped", "no different phrase for the fragment"
	case "backchannel: acknowledging while the user holds the floor":
		f.Kind, f.Outcome = "backchannel", "requested"
	case "backchannel: holding the floor while the backend works":
		f.Kind, f.Outcome = "holding_filler", "requested"
	case "backchannel: holding filler suppressed; this session has no answer to compare against yet":
		f.Kind, f.Outcome, f.Reason = "holding_filler", "skipped", "no earlier answer to compare with"
	case "backchannel: holding filler suppressed; the last one was not followed by an answer":
		f.Kind, f.Outcome, f.Reason = "holding_filler", "skipped", "previous filler had no answer"
	case "backchannel: dropped; the answer took the floor first", "backchannel: dropped; the answer took the floor while it was synthesizing":
		f.Kind, f.Outcome, f.Reason = "backchannel", "dropped", "answer took the floor"
	case "speak: cutting the filler short; the answer is ready":
		f.Kind, f.Outcome, f.Reason = "holding_filler", "cut_short", "answer ready"
	default:
		return
	}
	cb.call.Fillers = append(cb.call.Fillers, f)
}

func (cb *callBuild) lastOpener(turn string) *FillerEvent {
	for i := len(cb.call.Fillers) - 1; i >= 0; i-- {
		f := &cb.call.Fillers[i]
		if f.Turn == turn && f.Kind == "opener" && f.Source == "phrase_cache" {
			return f
		}
	}
	return nil
}

// Older saved JSON did not index fillers. Read this call's archived log on
// demand without rewriting its archive or borrowing another call's evidence.
func (s *Server) withFillerEvidence(c *Call) *Call {
	if c.FillerLogVersion > 0 || s.archive == nil || c.Session == "" {
		return c
	}
	p := s.archive.Path(c.ID, archLog)
	if p == "" {
		return c
	}
	f, err := os.Open(p)
	if err != nil {
		return c
	}
	defer f.Close()
	const limit = 64 << 20
	if st, err := f.Stat(); err != nil || st.Size() > limit {
		return c
	}
	cb := callBuild{call: *c}
	cb.call.Fillers = nil
	scan := bufio.NewScanner(io.LimitReader(f, limit))
	scan.Buffer(make([]byte, 64<<10), 4<<20)
	for scan.Scan() {
		l, ok := ParseLine(strings.TrimSpace(scan.Text()))
		if ok && l.Get("session") == c.Session {
			cb.readFiller(l)
		}
	}
	if scan.Err() != nil {
		return c
	}
	cb.call.FillerLogVersion = 1
	return &cb.call
}
