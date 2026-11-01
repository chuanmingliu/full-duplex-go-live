package main

import (
	"fmt"
	"math"
	"strings"
	"time"
)

func pctOrDash(v float64) string {
	if math.IsNaN(v) {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", v*100)
}

func countOf(c counter) string {
	if c.Total == 0 {
		return "—"
	}
	return fmt.Sprintf("%s (%d/%d)", pctOrDash(c.rate()), c.Good, c.Total)
}

func msOrDash(d dist, which string) string {
	if d.N == 0 {
		return "—"
	}
	switch which {
	case "p50":
		return fmt.Sprintf("%d", d.P50)
	case "p95":
		return fmt.Sprintf("%d", d.P95)
	case "max":
		return fmt.Sprintf("%d", d.Max)
	}
	return "—"
}

func meanOrDash(vals []int64) string {
	if len(vals) == 0 {
		return "—"
	}
	return fmt.Sprintf("%.2f s", mean(vals)/1000)
}

func summarize(results []*scenarioResult, w windows, opt runOptions, episodes, sessions int, elapsed time.Duration) string {
	var b strings.Builder
	all := &panel{}
	for _, r := range results {
		all.merge(r.Panel)
	}
	fmt.Fprintf(&b, "# golive full-duplex benchmark\n\n")
	fmt.Fprintf(&b, "%d scenarios, %d sessions × %d episodes each, %s. Caller waits %d ms after an answer; over-the-agent events at %d ms into the answer. Windows: yield %.1f s, murmur/tic/aside yield %.1f s, silent-period response %.1f s (τ-voice defaults).\n\n",
		len(results), sessions, episodes, elapsed.Round(time.Second), opt.WaitToRespond.Milliseconds(), opt.InterruptAt.Milliseconds(),
		w.NoYield.Seconds(), w.BackchannelYield.Seconds(), w.TicResponse.Seconds())

	fmt.Fprintf(&b, "## The panel (τ-voice names; Full-Duplex-Bench's beside them)\n\n")
	fmt.Fprintf(&b, "| metric | | all | %s |\n", strings.Join(headers(results), " | "))
	fmt.Fprintf(&b, "| --- | --- | ---: |%s\n", strings.Repeat(" ---: |", len(results)))
	row := func(name, dir string, f func(p *panel) string) {
		fmt.Fprintf(&b, "| %s | %s | %s |", name, dir, f(all))
		for _, r := range results {
			fmt.Fprintf(&b, " %s |", f(r.Panel))
		}
		b.WriteString("\n")
	}
	row("R_R response rate (FDB turn-taking TOR)", "↑", func(p *panel) string { return countOf(p.Response) })
	row("L_R response latency, mean (FDB turn-taking latency)", "↓", func(p *panel) string { return meanOrDash(p.Response.LatenciesMS) })
	row("R_Y yield rate (FDB interruption TOR)", "↑", func(p *panel) string { return countOf(p.Yield) })
	row("L_Y yield latency, mean (FDB interruption latency)", "↓", func(p *panel) string { return meanOrDash(p.Yield.LatenciesMS) })
	row("I_A agent interruption rate", "↓", func(p *panel) string {
		if p.Response.Total == 0 {
			return "—"
		}
		return fmt.Sprintf("%.0f%% (%d/%d)", p.agentIntRate()*100, p.AgentInt, p.Response.Total)
	})
	row("S_BC murmurs talked through (FDB backchannel TOR = 1−S_BC)", "↑", func(p *panel) string { return countOf(p.Backchannel) })
	row("S_VT tics ignored", "↑", func(p *panel) string {
		c := counter{Total: p.TicOver.Total + p.TicSilent.Total, Good: p.TicOver.Good + p.TicSilent.Good}
		return countOf(c)
	})
	row("S_ND asides ignored", "↑", func(p *panel) string {
		c := counter{Total: p.NDOver.Total + p.NDSilent.Total, Good: p.NDOver.Good + p.NDSilent.Good}
		return countOf(c)
	})
	row("FDB pause handling: pause held (1−TOR)", "↑", func(p *panel) string { return countOf(p.PauseHold) })
	row("last sound → first audio p50 / p95 ms (project headline)", "↓", func(p *panel) string {
		d := describe(p.LastSoundMS)
		if d.N == 0 {
			return "—"
		}
		return fmt.Sprintf("%d / %d (n=%d)", d.P50, d.P95, d.N)
	})
	row("after an interruption: last sound → first audio p50 ms", "↓", func(p *panel) string {
		d := describe(p.AfterIntMS)
		if d.N == 0 {
			return "—"
		}
		return fmt.Sprintf("%d (n=%d)", d.P50, d.N)
	})
	row("detector close after last sound, p50 ms", "", func(p *panel) string {
		d := describe(p.SpeechEnd)
		if d.N == 0 {
			return "—"
		}
		return fmt.Sprintf("%d", d.P50)
	})
	row("answers cut short", "", func(p *panel) string { return fmt.Sprintf("%d", p.Truncated) })
	row("tool task pass^1 / pass^2 / pass^4", "↑", func(p *panel) string {
		if p.ToolAttempts == 0 {
			return "—"
		}
		return fmt.Sprintf("%s / %s / %s (%d/%d)", pctOrDash(passPowK(p.ToolAttempts, p.ToolPasses, 1)),
			pctOrDash(passPowK(p.ToolAttempts, p.ToolPasses, 2)), pctOrDash(passPowK(p.ToolAttempts, p.ToolPasses, 4)), p.ToolPasses, p.ToolAttempts)
	})
	row("errors", "", func(p *panel) string { return fmt.Sprintf("%d", len(p.Errors)) })

	fmt.Fprintf(&b, "\nSelectivity (τ-voice's column) = mean of S_BC, S_VT, S_ND = %s. Responsiveness = mean of R_R, R_Y = %s. τ-voice hides a rate backed by fewer than 10 events; counts are in brackets.\n",
		meanRates(all.Backchannel.rate(), all.ticRate(), all.ndRate()), meanRates(all.Response.rate(), all.Yield.rate()))

	fmt.Fprintf(&b, "\n## Scenarios\n\n")
	for _, r := range results {
		name := r.Name
		if r.Variant != "" {
			name += " @ " + r.Variant
		}
		fmt.Fprintf(&b, "- **%s** — %s\n", name, r.About)
		if len(r.Panel.Errors) > 0 {
			fmt.Fprintf(&b, "  - errors: %s\n", strings.Join(uniq(r.Panel.Errors), "; "))
		}
	}

	fmt.Fprintf(&b, "\n## Reading it\n\n")
	fmt.Fprintf(&b, "- Response and yield latencies are from the caller's last voiced frame (or first, for a yield) on the harness's own clock to the first (or last) output audio chunk received; a client plays at most %d ms behind that.\n", 300)
	fmt.Fprintf(&b, "- A yield is counted at `golive.output_audio.truncated`, where a client clears its buffer, as τ-voice's adapter does.\n")
	fmt.Fprintf(&b, "- A figure from the mock providers says what the engine does, not what a vendor costs: the stage latencies are the injected ones in configs/sim.env.\n")
	return b.String()
}

func headers(results []*scenarioResult) []string {
	var h []string
	for _, r := range results {
		name := r.Name
		if r.Variant != "" {
			name += " @ " + r.Variant
		}
		h = append(h, name)
	}
	return h
}

func meanRates(vals ...float64) string {
	var s float64
	n := 0
	for _, v := range vals {
		if !math.IsNaN(v) {
			s += v
			n++
		}
	}
	if n == 0 {
		return "—"
	}
	if n < len(vals) {
		return fmt.Sprintf("%.0f%% (partial, %d of %d parts)", s/float64(n)*100, n, len(vals))
	}
	return fmt.Sprintf("%.0f%%", s/float64(n)*100)
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
