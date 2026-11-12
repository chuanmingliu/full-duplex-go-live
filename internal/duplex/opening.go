package duplex

import (
	"strings"
	"time"
)

// openingInput retains final caller words while the fixed opening owns the
// floor. Several utterances are answered together after its playback completes.
type openingInput struct {
	text              []string
	endAt             time.Time
	finalAt           time.Time
	endMS, silenceMS  int64
	startMS, activeMS int64
	awaitingEnd       bool
}

func (e *Engine) protectOpening(turn *Turn) {
	if e.openingStarted {
		return
	}
	e.openingStarted = true
	if e.opts.Cfg.Duplex.ProtectOpening {
		e.openingTurn = turn
		e.log.Info("opening: protection started", "turn", turn.ID)
	}
}

// SayOpening releases the fixed opening of an answered call. Other scripted
// speech continues through Say and follows the regular interruption policy.
func (e *Engine) SayOpening(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	e.post(func() {
		e.sayOnLoop(text)
		e.protectOpening(e.tracker.Current())
	})
}

func (e *Engine) openingProtected() bool {
	turn := e.openingTurn
	// Playback may clear its active state before its completion reaches the
	// engine loop. Keep the guard until completePlayback has recorded history.
	return turn != nil && e.gen.Valid(turn.Generation)
}

func (e *Engine) deferOpeningInput(text string) bool {
	if !e.openingProtected() && e.openingInput == nil {
		return false
	}
	if e.openingInput == nil {
		e.openingInput = &openingInput{}
	}
	input := e.openingInput
	input.text = append(input.text, text)
	input.finalAt = time.Now()
	input.awaitingEnd = e.userOpen
	e.stampOpeningInput()
	e.log.Info("opening: caller input deferred", "utterances", len(input.text))
	return true
}

func (e *Engine) stampOpeningInput() {
	input := e.openingInput
	if input == nil {
		return
	}
	input.endAt, input.endMS, input.silenceMS = e.speechEndAt, e.speechEndMS, e.speechEndSilenceMS
	input.startMS, input.activeMS = e.utterStartMS, e.lastActiveMS
}

// drainOpeningInput runs through the existing completion wake and tick paths.
// It resumes only after playback accounting and caller recognition settle.
func (e *Engine) drainOpeningInput() bool {
	if turn := e.openingTurn; turn != nil {
		pipe := e.speech.Load()
		if pipe != nil && pipe.turn == turn && !pipe.alive() && !pipe.ended {
			e.log.Warn("opening: synthesis ended before completion", "turn", turn.ID)
			e.openingTurn = nil
			e.abortSpeech()
		}
	}
	if e.openingProtected() || e.userOpen || e.awaitingFinal() || e.playbackReceiptPending() {
		return false
	}
	if e.openingTurn != nil {
		e.log.Info("opening: protection finished", "turn", e.openingTurn.ID)
		e.openingTurn = nil
	}
	if input := e.openingInput; input != nil {
		e.openingInput = nil
		if e.hangingUp() || e.ctx.Err() != nil {
			return false
		}
		endAt, endMS, silenceMS := e.speechEndAt, e.speechEndMS, e.speechEndSilenceMS
		startMS, activeMS := e.utterStartMS, e.lastActiveMS
		e.speechEndAt, e.speechEndMS, e.speechEndSilenceMS = input.endAt, input.endMS, input.silenceMS
		e.utterStartMS, e.lastActiveMS = input.startMS, input.activeMS
		e.log.Info("opening: answering deferred caller input", "utterances", len(input.text))
		text := strings.Join(input.text, " ")
		e.onFinalTranscript(nil, text)
		if turn := e.tracker.Current(); turn != nil && turn.Transcript == text {
			turn.mu.Lock()
			turn.transcriptFinal = input.finalAt
			turn.mu.Unlock()
		}
		e.speechEndAt, e.speechEndMS, e.speechEndSilenceMS = endAt, endMS, silenceMS
		e.utterStartMS, e.lastActiveMS = startMS, activeMS
		return true
	}
	if e.openingTyped && !e.assistantHasFloor() {
		e.openingTyped = false
		e.startTypedTurn()
		return true
	}
	return false
}
