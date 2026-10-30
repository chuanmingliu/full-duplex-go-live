package journal

import (
	"io"
	"os"
	"sort"
)

// LatencyEvidence separates heard fillers from answer content. These are
// server-output measurements; neither distribution includes handset playout.
type LatencyEvidence struct {
	Version      int                 `json:"version"`
	Recomputed   bool                `json:"recomputed,omitempty"`
	FirstAudio   LatencyDistribution `json:"first_audio"`
	FirstContent LatencyDistribution `json:"first_content"`
	Missing      int                 `json:"missing"`
	Unverified   int                 `json:"unverified"`
	Duplicates   int                 `json:"duplicates"`
	Cancelled    int                 `json:"cancelled"`
	Joined       int                 `json:"tool_results_joined"`
	InvalidLinks int                 `json:"invalid_tool_links"`
}

type LatencyDistribution struct {
	N    int      `json:"n"`
	P50  *float64 `json:"p50_ms"`
	P95  *int     `json:"p95_ms"`
	Max  *int     `json:"max_ms"`
	Over int      `json:"at_or_above_target"`
}

func latencyDistribution(values []int) LatencyDistribution {
	d := LatencyDistribution{N: len(values)}
	if len(values) == 0 {
		return d
	}
	sort.Ints(values)
	p50 := float64(values[len(values)/2])
	if len(values)%2 == 0 {
		p50 = (float64(values[len(values)/2-1]) + p50) / 2
	}
	p95, max := percentile(values, .95), values[len(values)-1]
	d.P50, d.P95, d.Max = &p50, &p95, &max
	for _, n := range values {
		if n >= TargetMS {
			d.Over++
		}
	}
	return d
}

func boolKnown(l Line, key string) bool { return l.Get(key) == "true" || l.Get(key) == "false" }

// answeredSpeech keeps modern zero-ms caller responses and excludes result
// turns. The old ASR/LLM heuristic is retained only for older log formats.
func answeredSpeech(l Line) bool {
	if !boolKnown(l, "response_to_speech") {
		return l.N("asr_final_ms") != 0 || l.N("llm_first_token_ms") != 0
	}
	return l.Get("response_to_speech") == "true" && l.Get("first_audio_present") == "true" &&
		!(l.Get("truncated") == "true" && l.Get("first_content_present") == "false")
}

func (cb *callBuild) latencyEvidence() LatencyEvidence {
	e := LatencyEvidence{Version: 1}
	joined := cb.joinedContent(&e)
	var audio, content []int
	for _, id := range cb.order {
		t := cb.turns[id]
		if t.latency == nil {
			continue
		}
		l := *t.latency
		e.Duplicates += t.latencyCount - 1
		if l.Get("response_to_speech") == "false" {
			continue
		}
		// Legacy records remain visible in the original charts, but cannot
		// establish presence or caller origin for this evidence summary.
		if id == "" || l.Get("response_to_speech") != "true" || !boolKnown(l, "first_audio_present") ||
			!boolKnown(l, "first_content_present") || !boolKnown(l, "opened") {
			e.Unverified++
			continue
		}
		if l.Get("truncated") == "true" && (l.Get("first_audio_present") == "false" || l.Get("first_content_present") == "false") {
			e.Cancelled++
			continue
		}
		pause, hasPause := l.Int("end_silence_ms")
		if !hasPause || pause < 0 || t.latencyCount != 1 {
			e.Unverified++
			continue
		}
		a, hasAudio := l.Int("first_audio_out_ms")
		c, hasContent := l.Int("first_content_ms")
		hasAudio = hasAudio && l.Get("first_audio_present") == "true" && a+pause >= 0
		hasContent = hasContent && l.Get("first_content_present") == "true"
		if j, ok := joined[id]; ok {
			c, hasContent = j, true
		}
		hasContent = hasContent && c+pause >= 0 && (!hasAudio || c >= a)
		if hasAudio {
			audio = append(audio, pause+a)
		}
		if hasContent {
			content = append(content, pause+c)
		}
		if !hasAudio || !hasContent {
			e.Missing++
		}
	}
	e.FirstAudio, e.FirstContent = latencyDistribution(audio), latencyDistribution(content)
	return e
}

// joinedContent requires the explicit monotonic relationship. Log wall times,
// adjacency and a result's zero-ms cached audio cannot supply a caller origin.
func (cb *callBuild) joinedContent(e *LatencyEvidence) map[string]int {
	out := map[string]int{}
	tasks, origins, children := map[string]int{}, map[string]int{}, map[string]int{}
	for _, l := range cb.deliveries {
		tasks[l.Get("task")]++
		origins[l.Get("origin_turn")]++
		children[l.Get("turn")]++
	}
	for _, l := range cb.deliveries {
		origin, child := cb.turns[l.Get("origin_turn")], cb.turns[l.Get("turn")]
		offset, known := l.Int("origin_to_result_start_ms")
		valid := l.Get("task") != "" && l.Get("origin_turn") != "" && l.Get("turn") != "" &&
			l.Get("origin_turn") != l.Get("turn") && tasks[l.Get("task")] == 1 &&
			origins[l.Get("origin_turn")] == 1 && children[l.Get("turn")] == 1 &&
			known && offset >= 0 && origin != nil && child != nil &&
			origin.latency != nil && child.latency != nil && origin.latencyCount == 1 && child.latencyCount == 1
		if valid {
			o, c := *origin.latency, *child.latency
			oa, hasOA := o.Int("first_audio_out_ms")
			pause, hasPause := o.Int("end_silence_ms")
			ca, hasCA := c.Int("first_audio_out_ms")
			cc, hasCC := c.Int("first_content_ms")
			cp, hasCP := c.Int("end_silence_ms")
			cancelledOrigin := o.Get("first_content_present") == "false" && o.Get("truncated") == "true"
			valid = o.Get("response_to_speech") == "true" && o.Get("first_audio_present") == "true" &&
				boolKnown(o, "first_content_present") && !cancelledOrigin && boolKnown(o, "truncated") && boolKnown(o, "opened") &&
				hasOA && hasPause && pause >= 0 && oa+pause >= 0 &&
				c.Get("response_to_speech") == "false" && c.Get("first_audio_present") == "true" &&
				c.Get("first_content_present") == "true" && boolKnown(c, "opened") && boolKnown(c, "truncated") &&
				hasCP && cp == 0 && hasCA && hasCC && ca >= 0 && cc >= ca && offset+cc >= oa
			if valid {
				if o.Get("first_content_present") == "true" {
					continue // direct content already observed; never replace it
				}
				out[l.Get("origin_turn")] = offset + cc
				e.Joined++
			}
		}
		if !valid {
			e.InvalidLinks++
			e.Unverified++
		}
	}
	return out
}

// Enrich an older saved call from its immutable archive. Re-indexing is
// limited to this session and never replaces the archive's source files.
func (s *Server) withLatencyEvidence(c *Call) *Call {
	if c.Latency != nil || s.archive == nil || c.Session == "" {
		return c
	}
	f, err := os.Open(s.archive.Path(c.ID, archLog))
	if err != nil {
		return c
	}
	defer f.Close()
	const limit = 64 << 20
	if st, err := f.Stat(); err != nil || st.Size() > limit {
		return c
	}
	ix := NewIndexer()
	if err := ix.ReadLog(io.LimitReader(f, limit)); err != nil {
		return c
	}
	for _, indexed := range ix.Calls() {
		if indexed.ID == c.ID && indexed.Session == c.Session {
			copy := *c
			copy.Latency = indexed.Latency
			copy.Latency.Recomputed = true
			copy.Turns, copy.Summary = indexed.Turns, indexed.Summary
			copy.StageMedians, copy.Transcript, copy.Markers = indexed.StageMedians, indexed.Transcript, indexed.Markers
			return &copy
		}
	}
	return c
}
