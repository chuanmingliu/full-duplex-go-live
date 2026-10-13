package duplex

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
)

var (
	ErrSimulatorDisabled  = errors.New("duplex: simulator speech capability is disabled")
	ErrSimulatorQueueFull = errors.New("duplex: simulator speech queue is full")
)

const (
	maxSimulatorActions = 128
	maxSimulatorQueued  = 32
	maxSimulatorPCM     = 2 << 20
	maxSimulatorText    = 4096
	maxSimulatorHold    = 15 * time.Second
)

// SimulatorSpeechRequest reserves prepared speech on this session's clock.
// The context must live until the action ends; cancel it or call
// CancelSimulatorSpeech to withdraw the reservation. DueAt never waits for
// input silence. ExpiresAt bounds even a busy output channel.
type SimulatorSpeechRequest struct {
	ActionID              string
	Text                  string
	DueAt                 time.Time
	ExpiresAt             time.Time
	MaxHold               time.Duration
	RequireRemoteSpeaking bool
}

// SimulatorSpeechEvent reports application audio boundaries, not handset
// playout. started is emitted with the first actual output audio chunk.
type SimulatorSpeechEvent struct {
	Type     string    `json:"type"`
	ActionID string    `json:"action_id"`
	Status   string    `json:"status"`
	Reason   string    `json:"reason,omitempty"`
	At       time.Time `json:"at"`
	DueAt    time.Time `json:"due_at,omitempty"`
}

type simulatorAction struct {
	req          SimulatorSpeechRequest
	pcm          []byte
	status       string
	reserved     bool
	ctx          context.Context
	cancelPrep   context.CancelFunc
	stopWatching func() bool
	turn         *Turn
	until        time.Time
}

type simulatorCommand struct {
	ctx  context.Context
	run  func() error
	done chan error
}

type simulatorFinal struct {
	run  *asrRun
	text string
}

type simulatorState struct {
	commands       chan simulatorCommand
	timer          *time.Timer
	actions        map[string]*simulatorAction
	queue          []*simulatorAction
	active         *simulatorAction
	resumePending  bool
	finals         []simulatorFinal
	finalBytes     int
	pcmBytes       int
	preparing      bool
	typedRequested bool
	// The output boundary serializes cancel/close with emission. Loop state
	// above remains owned by the engine loop.
	outputMu      sync.Mutex
	closed        bool
	failureReason string
	outputAction  *simulatorAction
	started       bool
	scriptTurns   map[string]bool
}

func newSimulatorState() *simulatorState {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	return &simulatorState{commands: make(chan simulatorCommand, maxSimulatorQueued), timer: timer, actions: make(map[string]*simulatorAction), scriptTurns: make(map[string]bool)}
}

func (e *Engine) simulatorCommand(ctx context.Context, f func() error) error {
	if !e.opts.SimulatorEnabled {
		return ErrSimulatorDisabled
	}
	if ctx == nil {
		return errors.New("duplex: simulator context is required")
	}
	if e.ctx == nil {
		return errors.New("duplex: engine is not started")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.ctx.Err(); err != nil {
		return err
	}
	cmd := simulatorCommand{ctx: ctx, run: f, done: make(chan error, 1)}
	select {
	case e.simulator.commands <- cmd:
	default:
		return ErrSimulatorQueueFull
	}
	select {
	case err := <-cmd.done:
		return err
	case <-e.ctx.Done():
		return e.ctx.Err()
	}
}

func (e *Engine) runSimulatorCommand(cmd simulatorCommand) {
	if err := cmd.ctx.Err(); err != nil {
		cmd.done <- err
		return
	}
	if err := e.ctx.Err(); err != nil {
		cmd.done <- err
		return
	}
	cmd.done <- cmd.run()
}

// PrepareSimulatorSpeech synthesizes one bounded, session-owned PCM buffer.
// It opens a private synthesis stream so preparation cannot consume an
// ordinary turn's persistent stream. No audio is emitted by preparation.
func (e *Engine) PrepareSimulatorSpeech(ctx context.Context, actionID, text string) error {
	if !e.opts.SimulatorEnabled {
		return ErrSimulatorDisabled
	}
	if ctx == nil {
		return errors.New("duplex: simulator context is required")
	}
	text = strings.TrimSpace(text)
	if actionID == "" || len(actionID) > 128 || len(text) > maxSimulatorText || !speakable(text) {
		return errors.New("duplex: invalid simulator action or text")
	}
	prepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var action *simulatorAction
	var already bool
	err := e.simulatorCommand(ctx, func() error {
		if old := e.simulator.actions[actionID]; old != nil {
			if old.req.Text != text {
				return errors.New("duplex: action ID already belongs to different text")
			}
			if old.status == "preparing" {
				return errors.New("duplex: action preparation is already in progress")
			}
			if old.status == "failed" || old.status == "cancelled" {
				return errors.New("duplex: action is terminal")
			}
			already = true
			return nil
		}
		if len(e.simulator.actions) >= maxSimulatorActions || e.simulator.preparing || e.simulator.pcmBytes >= maxSimulatorPCM {
			return ErrSimulatorQueueFull
		}
		action = &simulatorAction{req: SimulatorSpeechRequest{ActionID: actionID, Text: text}, status: "preparing", cancelPrep: cancel}
		e.simulator.actions[actionID] = action
		e.simulator.preparing = true
		return nil
	})
	if err != nil || already {
		return err
	}
	stopSession := context.AfterFunc(e.ctx, cancel)
	defer stopSession()
	pcm, err := e.prepareSimulatorPCM(prepCtx, text)
	synthErr := err
	err = e.simulatorCommand(e.ctx, func() error {
		e.simulator.preparing = false
		action.cancelPrep = nil
		if action.status != "preparing" {
			return errors.New("duplex: preparation was cancelled")
		}
		if synthErr != nil {
			e.finishSimulatorAction(action, "failed", synthErr.Error())
			return synthErr
		}
		if e.simulator.pcmBytes+len(pcm) > maxSimulatorPCM {
			e.finishSimulatorAction(action, "failed", "prepared audio budget exceeded")
			return ErrSimulatorQueueFull
		}
		action.pcm = pcm
		e.simulator.pcmBytes += len(pcm)
		action.status = "prepared"
		e.simulatorEvent(action, "prepared", "")
		return nil
	})
	return err
}

func (e *Engine) prepareSimulatorPCM(ctx context.Context, text string) ([]byte, error) {
	if e.deps.TTS == nil {
		return nil, errors.New("duplex: no TTS provider configured")
	}
	stream, err := e.deps.TTS.Open(ctx, provider.TTSOptions{SampleRate: e.ttsRate(), Voice: e.opts.Voice, Speed: e.opts.Cfg.Speed, Language: e.opts.Language})
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	chunks, err := stream.Synthesize(ctx, text)
	if err != nil {
		return nil, err
	}
	limit := e.opts.ClientRate * 2 * int(maxSimulatorHold/time.Second)
	if limit > maxSimulatorPCM {
		limit = maxSimulatorPCM
	}
	var pcm []byte
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case chunk, ok := <-chunks:
			if !ok {
				if len(pcm) == 0 {
					return nil, errors.New("duplex: synthesis returned no audio")
				}
				return pcm, nil
			}
			if chunk.Err != nil {
				return nil, chunk.Err
			}
			if len(chunk.PCM) == 0 {
				continue
			}
			if len(chunk.PCM)%2 != 0 || stream.SampleRate() <= 0 || len(chunk.PCM) > maxSimulatorPCM {
				return nil, errors.New("duplex: invalid or oversized synthesized audio")
			}
			if int64(len(chunk.PCM))*int64(e.opts.ClientRate)/int64(stream.SampleRate()) > int64(limit-len(pcm)) {
				return nil, errors.New("duplex: prepared utterance exceeds audio bound")
			}
			converted, err := audio.ResamplePCM16(chunk.PCM, stream.SampleRate(), e.opts.ClientRate)
			if err != nil {
				return nil, err
			}
			if len(pcm)+len(converted) > limit {
				return nil, errors.New("duplex: prepared utterance exceeds audio bound")
			}
			pcm = append(pcm, converted...)
		}
	}
}

// ScheduleSimulatorSpeech accepts only previously prepared matching text.
// Repeating the same action reservation is idempotent even after completion.
func (e *Engine) ScheduleSimulatorSpeech(ctx context.Context, req SimulatorSpeechRequest) error {
	return e.simulatorCommand(ctx, func() error {
		req.Text = strings.TrimSpace(req.Text)
		action := e.simulator.actions[req.ActionID]
		if action == nil || action.req.Text != req.Text {
			return errors.New("duplex: simulator action is not prepared")
		}
		if action.status != "prepared" {
			if action.reserved && action.req == req {
				return nil
			}
			return errors.New("duplex: simulator action already reserved or terminal")
		}
		if req.DueAt.IsZero() || req.ExpiresAt.IsZero() || !req.ExpiresAt.After(req.DueAt) || req.MaxHold <= 0 || req.MaxHold > maxSimulatorHold {
			return errors.New("duplex: invalid simulator speech timing")
		}
		if len(e.simulator.queue) >= maxSimulatorQueued {
			return ErrSimulatorQueueFull
		}
		action.req = req
		action.reserved = true
		action.ctx = ctx
		action.status = "scheduled"
		e.simulator.queue = append(e.simulator.queue, action)
		sort.SliceStable(e.simulator.queue, func(i, j int) bool { return e.simulator.queue[i].req.DueAt.Before(e.simulator.queue[j].req.DueAt) })
		action.stopWatching = context.AfterFunc(ctx, func() {
			// Close this action's output gate immediately, including a chunk that
			// raced generation validation; then notify the serialized loop.
			e.simulator.outputMu.Lock()
			if e.simulator.outputAction == action {
				e.simulator.outputAction = nil
			}
			e.simulator.outputMu.Unlock()
			cmd := simulatorCommand{ctx: e.ctx, run: func() error {
				e.finishSimulatorAction(action, "cancelled", "action context cancelled")
				e.advanceSimulator(time.Now())
				return nil
			}, done: make(chan error, 1)}
			select {
			case e.simulator.commands <- cmd:
			case <-e.ctx.Done():
			default:
				e.failSimulator("simulator command queue overflow")
			}
		})
		e.simulatorEvent(action, "scheduled", "")
		e.advanceSimulator(time.Now())
		return nil
	})
}

// CancelSimulatorSpeech cancels one action, or all actions for an empty ID.
// On return its output gate is closed and queued audio cannot revive it.
func (e *Engine) CancelSimulatorSpeech(ctx context.Context, actionID string) error {
	return e.simulatorCommand(ctx, func() error {
		if actionID != "" && e.simulator.actions[actionID] == nil {
			return errors.New("duplex: unknown simulator action")
		}
		for id, action := range e.simulator.actions {
			if actionID == "" || actionID == id {
				e.finishSimulatorAction(action, "cancelled", "explicit cancellation")
			}
		}
		e.advanceSimulator(time.Now())
		return nil
	})
}

func (e *Engine) simulatorOwnsOutput() bool { return e.simulator != nil && e.simulator.active != nil }

// simulatorDefersTurns also covers the interval between release and the posted
// history/resume callbacks, so newer input cannot overtake buffered finals.
func (e *Engine) simulatorDefersTurns() bool {
	return e.simulator != nil && (e.simulator.active != nil || e.simulator.resumePending)
}

func (e *Engine) simulatorEvent(a *simulatorAction, status, reason string) {
	e.deps.Emit(SimulatorSpeechEvent{Type: "simulator.speech", ActionID: a.req.ActionID, Status: status, Reason: reason, At: time.Now(), DueAt: a.req.DueAt})
}

func (e *Engine) advanceSimulator(now time.Time) {
	s := e.simulator
	if s == nil || e.ctx.Err() != nil {
		return
	}
	if a := s.active; a != nil {
		if a.ctx.Err() != nil {
			e.finishSimulatorAction(a, "cancelled", "action context cancelled")
		} else if !now.Before(a.req.ExpiresAt) {
			e.finishSimulatorAction(a, "expired", "reservation expired")
		} else if !now.Before(a.until) {
			e.finishSimulatorAction(a, "timed_out", "maximum hold expired")
		}
	}
	kept := s.queue[:0]
	for _, a := range s.queue {
		if a.status != "scheduled" {
			continue
		}
		if a.ctx.Err() != nil {
			e.finishSimulatorAction(a, "cancelled", "action context cancelled")
			continue
		}
		if !now.Before(a.req.ExpiresAt) {
			e.finishSimulatorAction(a, "expired", "reservation expired")
			continue
		}
		if s.active == nil && !now.Before(a.req.DueAt) {
			if a.req.RequireRemoteSpeaking && !e.userOpen {
				e.finishSimulatorAction(a, "skipped", "remote speech ended before the trigger")
				continue
			}
			e.startSimulatorSpeech(a, now)
		} else {
			kept = append(kept, a)
		}
	}
	s.queue = kept
	e.rearmSimulator(now)
}

func (e *Engine) startSimulatorSpeech(a *simulatorAction, now time.Time) {
	e.cancelBackend()
	e.abortSpeech()
	turn := e.tracker.Begin("", false)
	e.specTurn = nil
	e.hold = nil
	e.commitTurn(turn)
	a.turn = turn
	a.status = "playing"
	a.until = now.Add(a.req.MaxHold)
	e.simulator.active = a
	e.simulator.outputMu.Lock()
	e.simulator.outputAction = a
	e.simulator.scriptTurns[turn.ID] = true
	e.simulator.started = false
	e.simulator.outputMu.Unlock()
	// Reuse the committed speech-pipe cached-audio path without storing the
	// prepared utterance in the process-wide phrase cache.
	pipeCtx, cancel := context.WithCancel(e.ctx)
	pipe := &speechPipe{e: e, turn: turn, ctx: pipeCtx, cancel: cancel, done: make(chan struct{}), prepared: true}
	e.speech.Store(pipe)
	pipe.speakCached(a.req.Text, PhraseAudio{PCM: a.pcm, Rate: e.opts.ClientRate})
	pipe.closePrepared()
	turn.AppendResponse(a.req.Text)
}

// closePrepared closes a pipe whose only segment is already in the player.
func (p *speechPipe) closePrepared() {
	p.e.player.Enqueue(Segment{Kind: SegEnd, Gen: p.turn.Generation, TurnID: p.turn.ID, Revision: p.turn.Revision})
	close(p.done)
}

func (e *Engine) finishSimulatorAction(a *simulatorAction, status, reason string) {
	if a.status != "prepared" && a.status != "preparing" && a.status != "scheduled" && a.status != "playing" {
		return
	}
	if a.cancelPrep != nil {
		a.cancelPrep()
	}
	if a.stopWatching != nil {
		a.stopWatching()
	}
	if e.simulator.active == a {
		e.simulator.outputMu.Lock()
		e.simulator.outputAction = nil
		e.simulator.outputMu.Unlock()
		if status != "completed" {
			e.gen.Bump()
			e.abortSpeech()
		}
		e.simulator.resumePending = true
		e.simulator.active = nil
		// Interrupt's heard-prefix callback is posted before this resume.
		e.post(e.resumeSimulatorFinals)
	}
	e.simulator.pcmBytes -= len(a.pcm)
	a.pcm = nil
	a.status = status
	e.simulatorEvent(a, status, reason)
}

func (e *Engine) rearmSimulator(now time.Time) {
	s := e.simulator
	if !s.timer.Stop() {
		select {
		case <-s.timer.C:
		default:
		}
	}
	var next time.Time
	add := func(at time.Time) {
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	if a := s.active; a != nil {
		add(a.until)
		add(a.req.ExpiresAt)
	}
	for _, a := range s.queue {
		add(a.req.ExpiresAt)
		if s.active == nil {
			add(a.req.DueAt)
		}
	}
	if !next.IsZero() {
		delay := next.Sub(now)
		if delay < 0 {
			delay = 0
		}
		s.timer.Reset(delay)
	}
}

func (e *Engine) deferSimulatorFinal(run *asrRun, text string) bool {
	if !e.simulatorDefersTurns() {
		return false
	}
	if text == "" {
		return true
	}
	s := e.simulator
	if len(s.finals) >= maxSimulatorQueued || s.finalBytes+len(text) > maxSimulatorPCM/16 {
		e.failSimulator("pending final transcript queue overflow")
		return true
	}
	copyRun := *run
	s.finals = append(s.finals, simulatorFinal{run: &copyRun, text: text})
	s.finalBytes += len(text)
	return true
}

func (e *Engine) resumeSimulatorFinals() {
	s := e.simulator
	if s == nil || s.active != nil || e.ctx.Err() != nil {
		return
	}
	s.resumePending = false
	if len(s.finals) == 0 {
		if s.typedRequested {
			s.typedRequested = false
			e.startTypedTurn()
		} else {
			e.drainDeferred()
		}
		return
	}
	finals := s.finals
	s.finals = nil
	s.finalBytes = 0
	texts := make([]string, len(finals))
	for i, final := range finals {
		texts[i] = final.text
	}
	if s.typedRequested {
		texts = append(texts, e.typed...)
		e.typed = nil
		s.typedRequested = false
	}
	e.onFinalTranscript(finals[len(finals)-1].run, strings.Join(texts, "\n"))
}

func (e *Engine) failSimulator(reason string) {
	e.stopSimulatorOnError(reason)
	e.deps.Emit(live.NewError("server_error", "simulator_overflow", reason, ""))
}

// stopSimulatorOnError closes output immediately, leaving the player's heard
// prefix available to Close rather than truncating into an already dead loop.
func (e *Engine) stopSimulatorOnError(reason string) {
	s := e.simulator
	s.outputMu.Lock()
	s.closed = true
	s.failureReason = reason
	s.outputAction = nil
	s.outputMu.Unlock()
	e.cancel()
}

func (e *Engine) simulatorAudioAllowed(itemID string) bool {
	s := e.simulator
	if s.closed || e.ctx.Err() != nil {
		return false
	}
	if s.scriptTurns[itemID] || strings.HasPrefix(itemID, "simulator_") {
		a := s.outputAction
		if a == nil || a.turn.ID != itemID || a.ctx.Err() != nil || !time.Now().Before(a.until) || !time.Now().Before(a.req.ExpiresAt) {
			return false
		}
		return true
	}
	return s.outputAction == nil
}

func (e *Engine) closeSimulator() {
	if e.simulator == nil {
		return
	}
	s := e.simulator
	s.timer.Stop()
	s.outputMu.Lock()
	failureReason := s.failureReason
	s.outputMu.Unlock()
	for _, a := range s.actions {
		if a.cancelPrep != nil {
			a.cancelPrep()
		}
		if a.stopWatching != nil {
			a.stopWatching()
		}
		if a.status == "prepared" || a.status == "preparing" || a.status == "scheduled" || a.status == "playing" {
			if failureReason != "" {
				a.status = "failed"
				e.simulatorEvent(a, "failed", failureReason)
			} else {
				a.status = "cancelled"
				e.simulatorEvent(a, "cancelled", "session closed")
			}
		}
		a.pcm = nil
	}
	s.queue = nil
	s.finals = nil
	s.pcmBytes = 0
	s.active = nil
	s.resumePending = false
}
