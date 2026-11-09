package duplex

import (
	"math/rand/v2"
	"time"

	"github.com/chuanmingliu/golive/internal/tools"
)

// Only the engine loop advances this state. A deadline is sampled once per
// quiet interval, never on every tick. Caller speech resets the attempt budget.
type silenceReactionState struct {
	config       tools.SilenceReaction
	due          time.Time
	attempts     int
	waitMS       int
	lastTemplate string
}

func (e *Engine) loadSilenceReaction() tools.SilenceReaction {
	cfg := e.opts.Cfg
	if !cfg.HangupRules || cfg.ToolsBaseURL == "" {
		return tools.SilenceReaction{}
	}
	c, err := tools.FetchSilenceReaction(cfg.ToolsBaseURL, e.toolsToken(), 2*time.Second)
	if err != nil {
		// Older Leads deployments and unavailable/invalid configs retain the
		// previous behavior. Do not introduce unsolicited built-in reminders.
		e.log.Warn("silence: config unavailable; reminders disabled", "err", err)
		return tools.SilenceReaction{}
	}
	e.log.Info("silence: config loaded", "version", c.Version, "enabled", c.Enabled)
	return c
}

func (e *Engine) silenceReactionBusy() bool {
	ts := e.tools
	return e.userOpen || e.vad.CandidateMS() > 0 || e.awaitingFinal() || e.hold != nil ||
		e.assistantHasFloor() || e.playbackReceiptPending() || e.liveSpeculation() != nil ||
		len(e.pending) > 0 || len(ts.tasks) > 0 || len(ts.deliveries) > 0 || len(ts.ackPending) > 0
}

// Called after hangup checks and queued-result delivery. The ordinary speech
// pipeline records the reminder and permits the same guarded interruptions as
// any other answer. No synthetic caller message or LLM request is introduced.
func (e *Engine) maybeSilenceReaction(now time.Time) {
	ts := e.tools
	if ts == nil {
		return
	}
	s := &ts.silence
	c := s.config
	round, available := c.Round(s.attempts)
	if !c.Enabled || !available || e.ctx.Err() != nil || e.hangingUp() {
		return
	}
	// Do not speak before an answered call has had its greeting or caller.
	if e.muted || e.eventResponse != nil || (ts.lastCallerAt.IsZero() && ts.lastQuietAt.IsZero()) || e.silenceReactionBusy() {
		s.due = time.Time{}
		return
	}
	if s.due.IsZero() {
		s.waitMS = round.MinWaitMS + rand.IntN(round.MaxWaitMS-round.MinWaitMS+1)
		s.due = now.Add(time.Duration(s.waitMS) * time.Millisecond)
		return
	}
	if now.Before(s.due) {
		return
	}
	index := chooseSilenceTemplate(round.Templates, s.lastTemplate)
	s.lastTemplate = round.Templates[index]
	s.attempts++
	s.due = time.Time{}
	e.log.Info("silence: re-engagement", "attempt", s.attempts, "wait_ms", s.waitMS,
		"template_index", index, "config_version", c.Version, "round", s.attempts)
	// Already on the loop: do not post an unchecked Say that could run after
	// new caller input or a task delivery has acquired the floor.
	e.sayOnLoop(round.Templates[index])
}

// Ordered rounds have disjoint pools. Legacy pools avoid immediate repetition
// whenever another distinct line exists, without breaking one-template configs.
func chooseSilenceTemplate(lines []string, previous string) int {
	candidates := make([]int, 0, len(lines))
	for i, line := range lines {
		if line != previous {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return rand.IntN(len(lines))
	}
	return candidates[rand.IntN(len(candidates))]
}
