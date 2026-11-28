package duplex

import (
	"time"

	"github.com/chuanmingliu/golive/internal/tools"
)

// Turn-taking state belongs to the engine loop; configuration is loaded before
// the loop starts. A partial can cut playback, but only a final reserves time.
type turnTakingState struct {
	config     tools.TurnTaking
	until      time.Time
	ackPending bool
	deferred   *asrRun
}

func (e *Engine) loadTurnTaking() {
	base := e.opts.Cfg.ToolsBaseURL
	if base == "" || e.deps.ASR == nil {
		return
	}
	cfg, err := tools.FetchTurnTaking(e.ctx, base, e.toolsToken())
	if err != nil {
		e.log.Debug("turn-taking: configuration unavailable", "err", err)
		return
	}
	e.turnTaking.config = cfg
	e.log.Info("turn-taking: configuration loaded", "version", cfg.Version, "enabled", cfg.Enabled, "wait_ms", cfg.WaitMS)
}

func (e *Engine) waitingForCallerCue() bool {
	run := e.asrRun
	return e.turnTaking.deferred != nil ||
		(run != nil && run.cueSeen && !run.finalSeen) ||
		(!e.turnTaking.until.IsZero() && time.Now().Before(e.turnTaking.until))
}

func (e *Engine) cutForCallerCue(run *asrRun, text string) {
	if e.hold != nil || e.ignoring() || e.muted || e.openingProtected() || e.hangingUp() || (run != nil && run.cueSeen) {
		return
	}
	if _, cue := e.turnTaking.config.SplitCue(text); !cue {
		return
	}
	if run != nil {
		run.cueSeen = true
	}
	if turn := e.tracker.Current(); e.player.Active() || (turn != nil && e.tracker.IsCurrent(turn) && e.assistantHasFloor()) {
		e.interrupt()
		e.log.Info("turn-taking: cut playback for caller cue")
	}
}

// deferCallerCue leaves the final under the existing speaker/echo hold until
// it has a verdict. A rejected background voice must not reserve the floor.
func (e *Engine) deferCallerCue(run *asrRun, text string) bool {
	if e.hold == nil {
		return false
	}
	if _, cue := e.turnTaking.config.SplitCue(text); !cue {
		return false
	}
	e.turnTaking.deferred = run
	return true
}

func (e *Engine) settleCallerCue() {
	run := e.turnTaking.deferred
	if run == nil {
		return
	}
	if e.hold != nil && !e.hold.kept {
		return
	}
	e.turnTaking.deferred = nil
	if e.hold != nil || e.ignoring() {
		return
	}
	e.emitInputTranscript(run, run.lastText, true)
	e.onFinalTranscript(run, run.lastText)
}

// acceptCallerCue consumes a cue by itself or strips it from the start of a
// question spoken in the same breath. The original ASR transcript is kept.
func (e *Engine) acceptCallerCue(run *asrRun, text string) (string, bool) {
	if e.ignoring() || e.hold != nil || e.muted || e.hangingUp() {
		return text, false
	}
	state := &e.turnTaking
	remainder, cue := state.config.SplitCue(text)
	if cue {
		e.cutForCallerCue(run, text)
	}
	if cue && remainder == "" {
		if run != nil {
			if run.cueFinal {
				return "", true
			}
			run.cueFinal = true
		}
		e.abandonSpeculation("the caller asked for the floor")
		e.noteCallerSpoke()
		if e.tools != nil {
			e.tools.silence.attempts = 0
			e.tools.silence.lastTemplate = ""
		}
		state.until = time.Now().Add(time.Duration(state.config.WaitMS) * time.Millisecond)
		state.ackPending = state.config.Acknowledgment != ""
		e.log.Info("turn-taking: waiting for caller", "wait_ms", state.config.WaitMS, "acknowledgment", state.ackPending)
		e.maybeTurnTakingAck()
		return "", true
	}
	if e.waitingForCallerCue() && e.bcSet.Matches(text) {
		e.abandonSpeculation("the caller still holds the floor")
		return "", true
	}
	state.until = time.Time{}
	state.ackPending = false
	if cue {
		return remainder, false
	}
	return text, false
}

func (e *Engine) maybeTurnTakingAck() {
	state := &e.turnTaking
	if !state.ackPending {
		return
	}
	if !time.Now().Before(state.until) || e.hangingUp() {
		state.ackPending = false
		return
	}
	if e.userOpen || e.awaitingFinal() || e.vad.CandidateMS() > 0 || e.assistantHasFloor() || e.playbackReceiptPending() || e.muted {
		return
	}
	state.ackPending = false
	e.sayOnLoop(state.config.Acknowledgment)
}

func (e *Engine) expireCallerWait() {
	state := &e.turnTaking
	if !state.until.IsZero() && !time.Now().Before(state.until) {
		state.until = time.Time{}
		state.ackPending = false
		e.noteCallerSpoke() // ordinary silence rules get their full interval
	}
}
