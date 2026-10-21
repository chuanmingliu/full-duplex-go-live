package main

import (
	"github.com/chuanmingliu/golive/internal/benchrec"
	"math"
	"sort"
	"time"
)

// The metric panel is τ-voice's (sierra-research/tau2-bench,
// docs/interaction-metrics.md), with Full-Duplex-Bench's names given beside
// it where the two agree. The detection windows are τ-voice's defaults. One
// difference: τ-voice samples the two parties on 200 ms ticks; this harness
// has the exact instants, so no window is rounded to a tick.
type windows struct {
	NoYield          time.Duration // a real interruption must stop the agent within this
	BackchannelYield time.Duration // stopping within this after a murmur is an error
	TicYield         time.Duration
	NonDirectedYield time.Duration
	TicResponse      time.Duration // answering a silent-period tic within this is an error
	NonDirectedResp  time.Duration
}

var defaultWindows = windows{
	NoYield:          2 * time.Second,
	BackchannelYield: time.Second,
	TicYield:         time.Second,
	NonDirectedYield: time.Second,
	TicResponse:      2 * time.Second,
	NonDirectedResp:  2 * time.Second,
}

// counter is an event tally with the latencies behind it.
type counter struct {
	Total, Good int
	LatenciesMS []int64
}

func (c *counter) add(good bool, lat time.Duration) {
	c.Total++
	if good {
		c.Good++
		if lat >= 0 {
			c.LatenciesMS = append(c.LatenciesMS, lat.Milliseconds())
		}
	}
}

func (c counter) rate() float64 {
	if c.Total == 0 {
		return math.NaN()
	}
	return float64(c.Good) / float64(c.Total)
}

// panel is one scenario's (or the whole run's) scores.
type panel struct {
	Response     counter // R_R, L_R: a user turn answered before the user spoke again (FDB: turn-taking TOR, latency)
	Yield        counter // R_Y, L_Y: a real interruption stopped the agent in time (FDB: interruption TOR, latency)
	AgentInt     int     // I_A numerator: agent segments begun while the caller was speaking
	Backchannel  counter // S_BC: murmurs talked through
	TicOver      counter // S_VT over the agent: tics talked through
	TicSilent    counter // S_VT in silence: tics left unanswered
	NDOver       counter // S_ND over the agent
	NDSilent     counter // S_ND in silence
	PauseHold    counter // FDB pause handling: no takeover inside an utterance's pause (good = held)
	LastSoundMS  []int64 // caller's last sound → first audio, the project's headline, on answered turns
	AfterIntMS   []int64 // interruption's last sound → first audio of the answer to it
	SpeechEnd    []int64 // golive.speech.stopped − caller's last sound: the detector's close delay
	Truncated    int
	ToolPasses   int
	ToolAttempts int
	Errors       []string
}

func (p *panel) merge(q *panel) {
	mergeCounter(&p.Response, &q.Response)
	mergeCounter(&p.Yield, &q.Yield)
	p.AgentInt += q.AgentInt
	mergeCounter(&p.Backchannel, &q.Backchannel)
	mergeCounter(&p.TicOver, &q.TicOver)
	mergeCounter(&p.TicSilent, &q.TicSilent)
	mergeCounter(&p.NDOver, &q.NDOver)
	mergeCounter(&p.NDSilent, &q.NDSilent)
	mergeCounter(&p.PauseHold, &q.PauseHold)
	p.LastSoundMS = append(p.LastSoundMS, q.LastSoundMS...)
	p.AfterIntMS = append(p.AfterIntMS, q.AfterIntMS...)
	p.SpeechEnd = append(p.SpeechEnd, q.SpeechEnd...)
	p.Truncated += q.Truncated
	p.ToolPasses += q.ToolPasses
	p.ToolAttempts += q.ToolAttempts
	p.Errors = append(p.Errors, q.Errors...)
}

func mergeCounter(a, b *counter) {
	a.Total += b.Total
	a.Good += b.Good
	a.LatenciesMS = append(a.LatenciesMS, b.LatenciesMS...)
}

// selectivity is τ-voice's S_VT / S_ND: one rate over both contexts.
func (p *panel) ticRate() float64 {
	c := counter{Total: p.TicOver.Total + p.TicSilent.Total, Good: p.TicOver.Good + p.TicSilent.Good}
	return c.rate()
}

func (p *panel) ndRate() float64 {
	c := counter{Total: p.NDOver.Total + p.NDSilent.Total, Good: p.NDOver.Good + p.NDSilent.Good}
	return c.rate()
}

// agentIntRate is I_A: agent-interrupts-caller events per response-eligible
// caller turn.
func (p *panel) agentIntRate() float64 {
	if p.Response.Total == 0 {
		return math.NaN()
	}
	return float64(p.AgentInt) / float64(p.Response.Total)
}

// score extracts the panel from one session's timeline.
func score(tl *timeline, w windows) *panel {
	p := &panel{}
	tl.mu.Lock()
	defer tl.mu.Unlock()
	users := append([]segment(nil), tl.User...)
	agents := append([]segment(nil), tl.Agent...)
	sort.Slice(users, func(i, j int) bool { return users[i].Start.Before(users[j].Start) })
	sort.Slice(agents, func(i, j int) bool { return agents[i].Start.Before(agents[j].Start) })
	p.Errors = append(p.Errors, tl.Errors...)
	p.ToolPasses, p.ToolAttempts = tl.ToolPasses, tl.ToolAttempts

	// The first agent segment that starts in (after, before].
	firstAgentAfter := func(after, before time.Time) (segment, bool) {
		for _, a := range agents {
			if a.Start.After(after) && (before.IsZero() || !a.Start.After(before)) {
				return a, true
			}
		}
		return segment{}, false
	}
	// The agent segment audible at t, if any.
	agentAt := func(t time.Time) (segment, bool) {
		for _, a := range agents {
			if !a.Start.After(t) && a.End.After(t) {
				return a, true
			}
		}
		return segment{}, false
	}
	for _, a := range agents {
		if a.Truncated {
			p.Truncated++
		}
		// I_A: the agent began speaking while the caller was.
		for _, u := range users {
			if u.Kind != kindTurn {
				continue
			}
			speaking := !u.Start.After(a.Start) && u.End.After(a.Start)
			if u.HasPause && !u.PauseAt.IsZero() && !a.Start.Before(u.PauseAt) && (u.PauseEnd.IsZero() || a.Start.Before(u.PauseEnd)) {
				speaking = false // inside the caller's own pause: counted under pause handling instead
			}
			if speaking {
				p.AgentInt++
			}
		}
	}

	for i, u := range users {
		var next time.Time
		if i+1 < len(users) {
			next = users[i+1].Start
		}
		over, overSeg := agentAt(u.Start)
		_ = over
		switch u.Kind {
		case kindTurn:
			if u.HasPause && !u.PauseAt.IsZero() {
				// FDB pause handling: did the agent take the turn in the
				// pause? An answer begun in it is heard after the caller has
				// gone on, so anything that starts before the utterance ends
				// was decided in the pause.
				_, took := firstAgentAfter(u.PauseAt, u.End)
				p.PauseHold.add(!took, -1)
			}
			if overSeg {
				// A real interruption: the agent must stop within the window.
				stop := over.End.Sub(u.Start)
				good := over.End.Before(u.Start.Add(w.NoYield))
				p.Yield.add(good, stop)
				if !good {
					// Unresolved: τ-voice excludes it from response events.
					continue
				}
			}
			// Response: did the agent answer before the caller spoke again?
			if _, still := agentAt(u.End); still && !overSeg {
				// The caller finished under the agent's voice (an agent
				// interruption that never yielded): not eligible.
				continue
			}
			a, ok := firstAgentAfter(u.End, next)
			lat := time.Duration(-1)
			if ok {
				lat = a.Start.Sub(u.End)
				if overSeg {
					p.AfterIntMS = append(p.AfterIntMS, lat.Milliseconds())
				} else {
					p.LastSoundMS = append(p.LastSoundMS, lat.Milliseconds())
				}
			}
			p.Response.add(ok, lat)
		case kindBackchannel:
			if overSeg {
				// Error if the agent stopped within the window of the murmur.
				stopped := over.End.Before(u.Start.Add(w.BackchannelYield))
				p.Backchannel.add(!stopped, -1)
			}
		case kindTic:
			if overSeg {
				stopped := over.End.Before(u.Start.Add(w.TicYield))
				p.TicOver.add(!stopped, -1)
			} else {
				_, answered := firstAgentAfter(u.Start, u.End.Add(w.TicResponse))
				p.TicSilent.add(!answered, -1)
			}
		case kindNonDirected:
			if overSeg {
				stopped := over.End.Before(u.Start.Add(w.NonDirectedYield))
				p.NDOver.add(!stopped, -1)
			} else {
				_, answered := firstAgentAfter(u.Start, u.End.Add(w.NonDirectedResp))
				p.NDSilent.add(!answered, -1)
			}
		}
	}

	// The detector's close delay: golive.speech.stopped against the caller's
	// last sound of the nearest turn before it.
	for _, st := range tl.SpeechStopped {
		var best time.Duration = -1
		for _, u := range users {
			if u.Kind != kindTurn || st.Before(u.End) {
				continue
			}
			d := st.Sub(u.End)
			if best < 0 || d < best {
				best = d
			}
		}
		if best >= 0 && best < 3*time.Second {
			p.SpeechEnd = append(p.SpeechEnd, best.Milliseconds())
		}
	}
	return p
}

// --- distributions -----------------------------------------------------------

type dist struct {
	N                  int
	Min, P50, P90, P95 int64
	Max                int64
}

func describe(vals []int64) dist {
	if len(vals) == 0 {
		return dist{}
	}
	v := append([]int64(nil), vals...)
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	pct := func(p float64) int64 {
		i := int(math.Ceil(p*float64(len(v)))) - 1
		if i < 0 {
			i = 0
		}
		if i >= len(v) {
			i = len(v) - 1
		}
		return v[i]
	}
	return dist{N: len(v), Min: v[0], P50: pct(0.5), P90: pct(0.9), P95: pct(0.95), Max: v[len(v)-1]}
}

func mean(vals []int64) float64 {
	if len(vals) == 0 {
		return math.NaN()
	}
	var s int64
	for _, v := range vals {
		s += v
	}
	return float64(s) / float64(len(vals))
}

// passPowK is τ-bench's pass^k: the chance that k independent attempts at a
// task all succeed, estimated from c successes in n attempts as
// C(c,k)/C(n,k). With one task it is just that; with several it is averaged.
func passPowK(n, c, k int) float64 {
	if n < k || k <= 0 {
		return math.NaN()
	}
	num, den := 1.0, 1.0
	for i := 0; i < k; i++ {
		num *= float64(c - i)
		den *= float64(n - i)
	}
	if num < 0 {
		return 0
	}
	return num / den
}

// record is the panel as the standing evaluation's L2 scorecard: rates 0–1,
// latencies in ms, the project's headline beside them.
func (p *panel) record(build, source, report string) benchrec.Record {
	m := map[string]float64{}
	c := map[string]int{}
	put := func(name string, ctr counter) {
		if ctr.Total > 0 {
			m[name] = ctr.rate()
		}
		c["n_"+name] = ctr.Total
	}
	put("R_R", p.Response)
	put("R_Y", p.Yield)
	put("S_BC", p.Backchannel)
	put("S_VT", counter{Total: p.TicOver.Total + p.TicSilent.Total, Good: p.TicOver.Good + p.TicSilent.Good})
	put("S_ND", counter{Total: p.NDOver.Total + p.NDSilent.Total, Good: p.NDOver.Good + p.NDSilent.Good})
	put("pause_held", p.PauseHold)
	if len(p.Response.LatenciesMS) > 0 {
		m["L_R_ms"] = mean(p.Response.LatenciesMS)
	}
	if len(p.Yield.LatenciesMS) > 0 {
		m["L_Y_ms"] = mean(p.Yield.LatenciesMS)
	}
	if p.Response.Total > 0 {
		m["I_A"] = p.agentIntRate()
	}
	if d := describe(p.LastSoundMS); d.N > 0 {
		m["p50_ms"], m["p95_ms"] = float64(d.P50), float64(d.P95)
		c["n_turns"] = d.N
	}
	if p.ToolAttempts > 0 {
		m["pass_1"] = float64(p.ToolPasses) / float64(p.ToolAttempts)
		c["n_tool"] = p.ToolAttempts
	}
	return benchrec.Record{Layer: "L2", Build: build, Source: source, Metrics: m, Counts: c, Report: report,
		Thresholds: map[string]string{"S_BC": ">=0.9", "S_VT": ">=0.9", "R_R": ">=0.9", "R_Y": ">=0.9", "p95_ms": "<=1200"}}
}
