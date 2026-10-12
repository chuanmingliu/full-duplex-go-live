// Package duplex contains the engine that makes a cascaded ASR/LLM/TTS stack
// behave like a full-duplex speech model.
//
// The model gpt-live-1 exposes is one process that listens and speaks at the
// same time and hands hard thinking to a backend. We have no such model, so we
// build the behaviour out of ordinary parts and keep the seams honest. Five
// channels run concurrently for the whole session:
//
//	listen       audio in -> VAD -> streaming ASR, never gated on playback
//	transcribe   partial hypotheses stream out while the user is still talking
//	think        LLM/delegation work, started speculatively before the user stops
//	speak        paced, interruptible TTS output with played-millisecond accounting
//	backchannel  short acknowledgements emitted *while* listening
//
// What makes it read as duplex rather than as a fast half-duplex cascade is
// that no channel gates another. A classic cascade stops listening while it
// speaks; this one does not, which is why barge-in works, why the assistant can
// say "mm-hmm" mid-sentence, and why the generation counter and truncation
// accounting in turn.go and playback.go exist at all.
//
// What it still is not: a real full-duplex model hears prosody and decides to
// yield the floor from acoustics. Here, floor control is energy thresholds and
// timers. That difference shows up as occasional late barge-in detection and as
// speculation that sometimes guesses wrong.
package duplex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/endpoint"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/metrics"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/segment"
)

// Emit delivers a server event to the client. Implementations must be safe for
// concurrent use: the engine emits from the loop goroutine, the player
// goroutine and each generation goroutine.
type Emit func(event any)

// Deps are the collaborators an engine needs.
type Deps struct {
	ASR  provider.ASR
	LLM  provider.LLM
	TTS  provider.TTS
	Emit Emit
	Log  *slog.Logger
}

// Options configure one session's engine.
type Options struct {
	Cfg          config.Config
	SessionID    string
	ClientRate   int
	Instructions string
	Voice        string
	Language     string
	// Delegation is "client" or "responses".
	Delegation string
	// ClientDelegation is "always" or "auto"; it only matters in client mode.
	// Empty takes the profile's duplex.client_delegation.
	ClientDelegation string
	Backchannel      bool
	Speculative      bool
	History          []provider.Message
	// Greeting is spoken once the session is live, unprompted.
	Greeting string
	// OnNewQuery overrides the profile's interruption policy for this session.
	OnNewQuery string
	// Tools are forwarded to the backend in responses mode, in chat-completions
	// form. It is shorthand for Backend.Tools and is used when that is unset.
	Tools []json.RawMessage
	// Backend carries the session's delegation.responses settings.
	Backend BackendOptions
}

// BackendOptions is delegation.responses, as the engine uses it: already
// translated into what the configured provider takes.
type BackendOptions struct {
	// Model overrides the profile's backend_model when non-empty.
	Model string
	// Instructions are the backend's own: business rules and tool workflows,
	// as opposed to the conversational style in session.instructions.
	Instructions string
	// Tools are function definitions in chat-completions form.
	Tools             []json.RawMessage
	ToolChoice        any
	ParallelToolCalls *bool
	// MaxOutputTokens overrides the profile's max_output_tokens when positive.
	MaxOutputTokens int
}

// Engine is one session's duplex orchestrator.
type Engine struct {
	opts Options
	deps Deps
	log  *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	gen     Generation
	tracker *Tracker
	player  *Player

	vad    *audio.VAD
	framer *audio.Framer

	audioCh chan []byte
	asrCh   chan asrEvent

	// Work handed to the loop goroutine. A queue rather than a channel: post
	// is called from the loop itself — an interruption reports its truncation
	// back through here — and a full channel would have the loop waiting for
	// itself to make room.
	ctrlMu  sync.Mutex
	ctrlQ   []func()
	ctrlSig chan struct{}
	// loopDone closes when the loop goroutine has exited, after which its
	// state can be read from anywhere.
	loopDone chan struct{}

	// backendPending counts backend requests that have been started and have
	// not yet produced a speech pipe: the gap in which an answer is certainly
	// coming and nothing else shows it.
	backendPending atomic.Int32

	// --- loop-goroutine state (no locking) ---
	muted       bool
	userOpen    bool
	userSince   time.Time
	speechEndAt time.Time
	speechEndMS int64
	// resumeNoted keeps the "sound resumed" line to one per guess.
	resumeNoted bool
	// speechEndSilenceMS is the trailing silence that utterance was closed on.
	speechEndSilenceMS int64
	asrRun             *asrRun
	// earlyRun is a recognition ended early, in a pause, whose final is still
	// on its way while asrRun — its continuation — listens to what follows.
	// See maybeEarlyFinal.
	earlyRun    *asrRun
	earlyFinals int
	asrSeq      uint64
	// asrStandby is a recognizer connection dialled on the first suspicion of
	// speech, waiting to be adopted by the utterance if there turns out to be
	// one. See maybePredialASR.
	asrStandby *lazyASR
	watch      *StabilityWatch
	specTurn   *Turn
	// patience is the silence threshold currently imposed on the detector, or
	// zero for the configured one. See applyPatience.
	patience int
	// classText and classVal memoize the endpoint classification, which is
	// asked for on every audio frame of a pause and changes only with the text.
	classText string
	classVal  endpoint.Class
	// lastCommitted is the most recent confirmed turn that carried the caller's
	// words. If it is superseded before it is answered, its words still belong
	// in the conversation. See ensureUserRecorded.
	lastCommitted *Turn
	recordedUser  map[string]bool
	bcSet         *PhraseSet
	echoWarned    bool
	hold          *floorHold
	// turnBeforeUtterance is whatever was already being said when the current
	// utterance opened. It is not a candidate for this utterance's origin.
	turnBeforeUtterance *Turn
	history             []provider.Message
	instrMu             sync.RWMutex
	instr               string
	lastBC              time.Time
	bcSeq               int
	// lastAck and lastFiller keep a phrase from being chosen twice running.
	lastAck    string
	lastFiller string
	// fillerSinceAnswer counts holding fillers spoken since the caller last
	// actually heard an answer. Above zero, another one is noise.
	fillerSinceAnswer int
	// delegatedAt is when backend work for the current turn began, and is the
	// clock the holding filler runs against.
	delegatedAt   time.Time
	delegatedTurn *Turn
	filled        bool
	// ttfa is what this session has actually observed between delegating and
	// hearing audio, which is what decides whether a turn is running late.
	ttfa rollingMS
	// specWhy records why the last speculation check declined, so a turn that
	// ends up waiting for the final transcript can say what stopped it.
	specWhy   string
	lastState live.ChannelStateEvent

	// --- delegation state, also loop-goroutine only; see think.go ---
	backend       BackendOptions
	backendCancel context.CancelFunc
	respSeq       int
	// pending holds backend responses that ended in function calls and are
	// waiting for the application's results, by delegation id.
	pending      map[string]*pendingResponse
	pendingOrder []string
	callIndex    map[string]string
	// continuations are pending responses whose results are all in, waiting
	// for a free floor to be resumed.
	continuations []*pendingResponse
	// typed is text the user typed rather than spoke, queued by
	// response.item.create until response.create starts it.
	typed []string
	// commentary is text an application asked to have spoken, waiting for the
	// caller to stop talking and the floor to be free.
	commentary []queuedCommentary
	// appendedChars is how much session.instructions.append has added.
	appendedChars int
	// standbyRetryAt is the earliest a recognizer may be dialled on suspicion
	// again, after one such dial failed.
	standbyRetryAt time.Time
	// delegations are the ids announced to the client and still answerable.
	delegations     map[string]bool
	delegationOrder []string
	lastWarm        time.Time

	// --- shared state ---
	speech   atomic.Pointer[speechPipe]
	speaking atomic.Bool

	ttsMu     sync.Mutex
	ttsStream provider.TTSStream

	latencies []int64
	// specHeld and specAbandoned count this session's guesses by outcome, for
	// the summary at close. Loop goroutine only.
	specHeld      int
	specAbandoned int
	// echoReturns are the times the assistant's voice took to come back on the
	// input, and pausesInside the lengths of the pauses callers carried on
	// from, both in milliseconds and both for the summary at close.
	// silentBeforeMS is the open utterance's trailing silence as of the frame
	// before this one. Loop goroutine only. See measureLine.
	echoReturns    []float64
	echoMisses     int
	pausesInside   []int
	silentBeforeMS float64

	inputMS   atomic.Int64
	outputMS  atomic.Int64
	inTok     atomic.Int64
	outTok    atomic.Int64
	cachedTok atomic.Int64
	// ctxRatio is math.Float64bits of how full the conversational context is.
	ctxRatio atomic.Uint64
	startAt  time.Time

	closeOnce sync.Once
}

type asrEvent struct {
	run  uint64
	res  provider.ASRResult
	done bool
}

type asrRun struct {
	id       uint64
	stream   provider.ASRStream
	itemID   string
	startMS  int64
	endMS    int64
	lastText string
	// changedAt is when lastText last changed — changedAtMS the same moment
	// on the detector's audio clock — and activeAtChange how much of the
	// utterance had been voiced by then: together, how long the caller has
	// gone without producing a new word, and how much sound there has been
	// since. See maybeSettle.
	changedAt      time.Time
	changedAtMS    float64
	activeAtChange float64
	openedAt       time.Time
	sendDone       bool
	// sendDoneAt is when the utterance was closed and a final asked for.
	sendDoneAt time.Time
	// ended is set once the stream has closed its results, final or not.
	ended bool
	// finalSeen is set once the utterance has been concluded, by the
	// recognizer's final transcript or by giving up on one.
	finalSeen bool

	// prevHyp is the hypothesis before lastText; committed is how much of the
	// utterance has gone out on the append-only transcript; shown is the last
	// whole hypothesis sent on the revisable one. See emitInputTranscript.
	prevHyp   string
	committed string
	shown     string
	// held marks that transcript events for this utterance were kept back
	// while a floor hold was undecided, so the first one after it must go out
	// even if the text has not changed since.
	held bool

	// An utterance whose recognition was ended early in a pause carries on in
	// a continuation: prefix is what the recognitions before this one settled
	// on, raw what this one has heard by itself, and activeAtOpen how much of
	// the utterance had been voiced when it took over — so, how much has been
	// since. lastText is always the whole utterance.
	continuation bool
	prefix       string
	raw          string
	activeAtOpen float64
}

// New builds an engine. Call Start before pushing audio.
func New(opts Options, deps Deps) *Engine {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if opts.ClientRate <= 0 {
		opts.ClientRate = opts.Cfg.ClientRate
	}
	if opts.Delegation == "" {
		opts.Delegation = live.DelegationResponses
	}
	if opts.ClientDelegation == "" {
		opts.ClientDelegation = opts.Cfg.Duplex.ClientDelegation
	}
	if opts.Backend.Tools == nil {
		opts.Backend.Tools = opts.Tools
	}

	vcfg := audio.VADConfig{
		SampleRate:         provider.PipelineRate,
		FrameMS:            opts.Cfg.VAD.FrameMS,
		MinSpeechMS:        opts.Cfg.VAD.MinSpeechMS,
		MinSilenceMS:       opts.Cfg.VAD.MinSilenceMS,
		SpeechPadMS:        opts.Cfg.VAD.SpeechPadMS,
		MaxSpeechMS:        opts.Cfg.VAD.MaxSpeechMS,
		SnapshotMS:         opts.Cfg.VAD.SnapshotMS,
		NoiseFloorDB:       opts.Cfg.VAD.NoiseFloorDB,
		MaxNoiseFloorDB:    opts.Cfg.VAD.MaxNoiseFloorDB,
		SpeechMarginDB:     opts.Cfg.VAD.SpeechMarginDB,
		MaxZCR:             opts.Cfg.VAD.MaxZCR,
		GapToleranceMS:     opts.Cfg.VAD.GapToleranceMS,
		BargeInMarginDB:    opts.Cfg.VAD.BargeInMarginDB,
		BargeInMinSpeechMS: opts.Cfg.VAD.BargeInMinSpeechMS,
		EchoTailMS:         opts.Cfg.VAD.EchoTailMS,
	}
	vad := audio.NewVAD(vcfg)

	e := &Engine{
		opts:     opts,
		deps:     deps,
		log:      deps.Log.With("session", opts.SessionID),
		vad:      vad,
		framer:   audio.NewFramer(vad.FrameBytes()),
		audioCh:  make(chan []byte, 64),
		asrCh:    make(chan asrEvent, 64),
		ctrlSig:  make(chan struct{}, 1),
		loopDone: make(chan struct{}),
		history:  append([]provider.Message(nil), opts.History...),
		instr:    opts.Instructions,
		watch: NewStabilityWatch(
			time.Duration(opts.Cfg.Duplex.SpeculativeStableMS)*time.Millisecond,
			opts.Cfg.Duplex.SpeculativeMinChars,
		),
		bcSet:        NewPhraseSet(opts.Cfg.Duplex.UserBackchannelPhrases),
		backend:      opts.Backend,
		pending:      map[string]*pendingResponse{},
		callIndex:    map[string]string{},
		delegations:  map[string]bool{},
		recordedUser: map[string]bool{},
		startAt:      time.Now(),
	}
	e.tracker = NewTracker(&e.gen)
	e.player = NewPlayer(PlayerConfig{
		Rate:    opts.ClientRate,
		ChunkMS: opts.Cfg.Duplex.PlaybackChunkMS,
		Paced:   opts.Cfg.Duplex.PlaybackPaced,
		LeadMS:  opts.Cfg.Duplex.PlaybackLeadMS,
	}, e.emitAudio)
	e.player.OnSpeaking(e.onSpeakingChanged)
	e.player.OnTruncate(e.onTruncated)
	e.player.OnTurnDone(e.onTurnDone)
	e.player.OnSegment(e.onSegmentHeard)
	e.noteContext()
	return e
}

// Start launches the engine goroutines.
func (e *Engine) Start(ctx context.Context) {
	e.ctx, e.cancel = context.WithCancel(ctx)
	e.wg.Add(2)
	go func() { defer e.wg.Done(); e.player.Run(&e.gen) }()
	go func() { defer e.wg.Done(); e.loop() }()
	// The session has just opened and nobody is waiting on anything yet, which
	// makes this the cheapest moment in the whole call to pay for handshakes.
	e.prewarm("session open")
	e.post(func() { e.warmAtOpening("session open") })
}

// prewarm opens the provider connections a turn is about to need, off the
// critical path.
//
// A cascade's first turn is reliably its worst, and the reason is dull: TCP,
// TLS and a protocol greeting to two vendors, all of it sitting between the
// caller's last syllable and their first heard one. The engine gets ample
// warning both times it matters — a session opens a second or more before
// anyone speaks, and the microphone opens a second or more before a reply is
// due — so the handshakes belong there instead.
//
// Failures are logged at debug and otherwise ignored. A prewarm that does not
// work costs exactly what not prewarming cost: the connection is made later,
// when it is needed.
func (e *Engine) prewarm(reason string) {
	warm := func(name string, p any) {
		pw, ok := p.(provider.Prewarmer)
		if !ok {
			return
		}
		go func() {
			started := time.Now()
			if err := pw.Prewarm(e.ctx); err != nil {
				e.log.Debug("prewarm failed; the connection will be made when it is needed",
					"stage", name, "reason", reason, "err", err)
				return
			}
			e.log.Debug("prewarm", "stage", name, "reason", reason,
				"ms", time.Since(started).Milliseconds())
		}()
	}
	warm("think", e.deps.LLM)
	go func() {
		// ttsSession dials on first use, so this both opens the session and
		// warms it. It takes a lock a synthesis may hold, hence its own
		// goroutine.
		stream, err := e.ttsSession()
		if err != nil {
			e.log.Debug("prewarm failed; the connection will be made when it is needed",
				"stage", "speak", "reason", reason, "err", err)
			return
		}
		warm("speak", stream)
	}()
}

// Close stops the engine and releases provider connections.
func (e *Engine) Close() {
	e.closeOnce.Do(func() {
		if e.cancel != nil {
			e.cancel()
		}
		e.player.Close()
		e.wg.Wait()
		// After wg.Wait: the loop goroutine is the only writer of e.latencies
		// and of what lineSummary reads, and it has exited, so reading them
		// here needs no lock.
		e.latencySummary()
		e.lineSummary()
		e.ttsMu.Lock()
		if e.ttsStream != nil {
			_ = e.ttsStream.Close()
			e.ttsStream = nil
		}
		e.ttsMu.Unlock()
	})
}

// Greet speaks the configured greeting, if any.
//
// It is a separate call rather than part of Start so the caller controls when
// it happens: the greeting's audio must not reach the wire before the
// session.started event that tells the client what format that audio is in.
func (e *Engine) Greet() {
	text := strings.TrimSpace(e.opts.Greeting)
	if text == "" {
		return
	}
	e.post(func() {
		// A greeting is a real assistant turn: it can be interrupted, it is
		// truncated honestly if it is, and it enters conversation history so
		// the backend knows what was already said.
		turn := e.tracker.Begin("", false)
		e.commitTurn(turn)
		e.log.Debug("speak: greeting", "turn", turn.ID, "text", text)
		// Cacheable: it is the same sentence on every call, and it is the
		// first thing a caller hears. See PhraseCache.
		pipe := e.newSpeechPipe(turn, true)
		pipe.Push(text)
		pipe.Close()
		turn.AppendResponse(text)
		e.warmAtOpening("greeting")
	})
}

// Say speaks a line the assistant was handed rather than one it composed: the
// opening of a phone call, released when the call is answered. Like the
// greeting it is a real turn — interruptible, truncated honestly, recorded in
// history — and like the greeting it plays from the phrase cache when the line
// was synthesized ahead of time, which is the point of knowing it in advance.
func (e *Engine) Say(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	e.post(func() {
		turn := e.tracker.Begin("", false)
		e.specTurn = nil
		e.commitTurn(turn)
		e.log.Debug("speak: opening line", "turn", turn.ID, "text", text)
		pipe := e.newSpeechPipe(turn, true)
		pipe.Push(text)
		pipe.Close()
		turn.AppendResponse(text)
		e.warmAtOpening("opening line")
	})
}

// Interrupt stops whatever the assistant is saying or about to say, as a
// barge-in would. It is for a client that has decided, by means of its own,
// that the caller has taken the floor.
func (e *Engine) Interrupt() {
	e.post(func() {
		if e.assistantHasFloor() {
			e.interrupt()
		}
	})
}

// PushAudio feeds client audio in the negotiated session format. It never
// blocks for long: a stalled engine must not stall the socket reader, so a full
// queue drops the oldest frame and says so.
func (e *Engine) PushAudio(pcm []byte) {
	if len(pcm) == 0 {
		return
	}
	buf := make([]byte, len(pcm))
	copy(buf, pcm)
	select {
	case e.audioCh <- buf:
	default:
		select {
		case <-e.audioCh:
		default:
		}
		select {
		case e.audioCh <- buf:
		default:
		}
		e.log.Warn("input audio queue full; dropped a frame")
		metrics.Default().AudioDropped()
	}
}

// SetMuted stops or resumes consumption of client audio, and acknowledges the
// command that asked. Muting does not tear down the listen channel, so
// unmuting is instantaneous; it does not stop the assistant speaking, and it
// does not cancel backend work.
func (e *Engine) SetMuted(muted bool, clientEventID string) {
	e.post(func() {
		e.muted = muted
		if muted {
			// A guess made on an utterance that will now never finish can
			// never be confirmed either.
			if e.liveSpeculation() != nil {
				e.abandonSpeculation("the microphone was muted mid-utterance")
			}
			e.closeASR(true)
			e.closeStandby()
			e.vad.Reset()
			e.userOpen = false
			e.patience = 0
			e.hold = nil
		}
		e.publishState()
		kind := live.ServerInputAudioUnmuted
		if muted {
			kind = live.ServerInputAudioMuted
		}
		e.deps.Emit(live.AckEvent{
			Envelope:      live.Envelope{Type: kind},
			ClientEventID: clientEventID,
		})
	})
}

// post runs f on the engine loop goroutine, after everything posted before
// it. It never blocks, so it is safe to call from the loop itself.
func (e *Engine) post(f func()) {
	e.ctrlMu.Lock()
	e.ctrlQ = append(e.ctrlQ, f)
	e.ctrlMu.Unlock()
	select {
	case e.ctrlSig <- struct{}{}:
	default:
	}
}

// runPosted runs what has been posted, in order, including anything posted
// while it runs.
func (e *Engine) runPosted() {
	for e.ctx.Err() == nil {
		e.ctrlMu.Lock()
		batch := e.ctrlQ
		e.ctrlQ = nil
		e.ctrlMu.Unlock()
		if len(batch) == 0 {
			return
		}
		for _, f := range batch {
			f()
		}
	}
}

// --- main loop ---

func (e *Engine) loop() {
	defer close(e.loopDone)
	tick := time.NewTicker(120 * time.Millisecond)
	defer tick.Stop()
	defer e.closeASR(true)
	defer e.closeStandby()

	for {
		select {
		case <-e.ctx.Done():
			return
		case pcm := <-e.audioCh:
			e.onAudio(pcm)
		case ev := <-e.asrCh:
			e.onASREvent(ev)
		case <-e.ctrlSig:
			e.runPosted()
		case <-tick.C:
			e.onTick()
		}
	}
}

func (e *Engine) onAudio(pcm []byte) {
	if e.muted {
		return
	}
	e.inputMS.Add(int64(audio.PCM16(e.opts.ClientRate).DurationMS(pcm)))

	converted, err := audio.ResamplePCM16(pcm, e.opts.ClientRate, provider.PipelineRate)
	if err != nil {
		e.deps.Emit(live.NewError("invalid_request_error", "bad_audio", err.Error(), ""))
		return
	}
	for _, frame := range e.framer.Push(converted) {
		samples, err := audio.DecodePCM16(frame)
		if err != nil {
			continue
		}
		e.handleDecision(e.vad.Push(samples))
		e.measureLine()
	}
	// Per frame, not per tick: the window between "gone quiet" and "turn over"
	// is a couple of hundred milliseconds, and a 120 ms tick would spend most
	// of the head start waiting to notice it was available. The same argument
	// applies to a hold that has run out — every millisecond late is a
	// millisecond spent talking over someone.
	e.expireHold()
	e.maybePredialASR()
	e.applyPatience()
	e.maybeEarlyFinal()
	e.maybeSpeculate()
	e.maybeSettle()
}

func (e *Engine) handleDecision(d audio.Decision) {
	switch d.Kind {
	case audio.DecisionStarted:
		// A guess still unconfirmed here belongs to the utterance before this
		// one — a new utterance cannot open inside the pause of an old one — and
		// that utterance did end; only its final transcript is outstanding, and
		// opening the recognizer for this one is about to discard it. The
		// caller said those words, so the turn stands: confirm it, and let the
		// floor logic below decide what becomes of its answer.
		if spec := e.liveSpeculation(); spec != nil {
			e.tracker.Commit(spec.ID, spec.Revision)
			e.commitTurn(spec)
		}
		e.userOpen = true
		e.userSince = time.Now()
		e.resumeNoted = false
		e.watch.Reset()
		e.specTurn = nil
		e.specWhy = ""
		e.patience = 0
		// Remembered so the utterance's speech-end origin is not applied to
		// whatever was already being said when it began.
		e.turnBeforeUtterance = e.tracker.Current()
		e.log.Debug("listen: utterance opened",
			"start_ms", d.StartMS,
			"active_ms", int(d.ActiveMS),
			"barge_in", d.BargeIn,
			"noise_floor_db", round1(e.vad.NoiseFloorDB()),
			"echo_floor_db", round1(e.vad.EchoFloorDB()),
			"echo_headroom_db", round1(e.vad.EchoFloorDB()-e.vad.NoiseFloorDB()),
			"assistant_speaking", e.speaking.Load())
		e.deps.Emit(live.SpeechEvent{
			Envelope: live.Envelope{Type: live.ExtSpeechStarted},
			StartMS:  d.StartMS,
			BargeIn:  d.BargeIn,
		})
		e.warnIfEcho(d)
		if !e.holdFloor(d.BargeIn) {
			e.yieldFloor(d.BargeIn)
		}
		// A reply to this is now inevitable, and is at least a second away.
		// Anything reconnected here is a handshake the caller does not wait
		// through — which matters most after an idle gap long enough for the
		// synthesis task to have been dropped at the far end.
		e.prewarm("utterance opened")
		e.maybeWarmPrefix()
		e.openASR(d.StartMS)
		e.writeASR(d.Snapshot)
		e.publishState()

	case audio.DecisionSpeaking:
		// Not logged: at a 40 ms cadence this is 25 lines a second, and all
		// they would say is that someone is talking.
		e.writeASR(d.Snapshot)
		if e.asrRun != nil {
			e.asrRun.endMS = d.EndMS
		}

	case audio.DecisionStopped:
		e.userOpen = false
		e.patience = 0
		if e.hold != nil {
			// The final transcript is now imminent — closing the recognizer is
			// what asks for it. Do not time out on the last leg.
			e.hold.sawEvidence()
		}
		// The origin for every latency this turn will report. A speculative
		// turn is already running by now, so it is stamped here rather than at
		// creation.
		e.speechEndAt = time.Now()
		e.speechEndMS = d.EndMS
		e.speechEndSilenceMS = int64(d.SilenceMS)
		// ...but only for a turn this utterance actually produced. A turn that
		// was already live when the caller started talking belongs to an
		// earlier utterance, or to none at all — the greeting is the clearest
		// case, and it is how item_1 came to report first_segment_ms: -742.
		// Stamping it dated audio that had already been spoken to a moment
		// still in the future, so every figure came out negative. It is not
		// waiting for anything the caller said, and it has no origin.
		if turn := e.tracker.Current(); turn != nil && turn != e.turnBeforeUtterance {
			turn.MarkSpeechEnd(e.speechEndAt, e.speechEndMS, e.speechEndSilenceMS)
		}
		e.writeASR(d.Snapshot)
		if e.asrRun != nil {
			e.asrRun.endMS = d.EndMS
		}
		e.log.Debug("listen: utterance closed",
			"start_ms", d.StartMS,
			"end_ms", d.EndMS,
			"active_ms", int(d.ActiveMS),
			"barge_in", d.BargeIn)
		e.deps.Emit(live.SpeechEvent{
			Envelope:  live.Envelope{Type: live.ExtSpeechStopped},
			StartMS:   d.StartMS,
			EndMS:     d.EndMS,
			BargeIn:   d.BargeIn,
			SilenceMS: int64(d.SilenceMS),
		})
		e.closeUtteranceASR(d)
		e.speculateAtClose()
		e.publishState()
	}
}

func (e *Engine) onTick() {
	e.publishState()
	e.expireHold()
	e.expireStandby()
	e.expireFinalWait()
	e.expireEarlyFinal()
	e.observeTTFA()
	e.maybeBackchannel()
	e.maybeHoldingFiller()
	e.drainDeferred()
	// The session's lifetime is the session's business: the server bounds it
	// with a deadline on the context it hands this engine, and says so in
	// session.closed. The engine used to announce the expiry itself and then
	// stop, which produced two closing events — this one, and the server's a
	// moment later with a different reason.
}

// --- listen channel: ASR ---

// --- speak channel ---

// speechPipe converts a stream of text deltas into paced audio. One pipe is
// alive at a time; starting a new one aborts the old.
type speechPipe struct {
	e    *Engine
	turn *Turn
	// begun is set once the pipe has asked for its first synthesis. That one
	// is the one a caller waits on in silence; see provider.WithUrgent.
	begun  bool
	in     chan string
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	// soft asks the pipe to stop after the sentence it is currently
	// synthesizing, rather than abandoning it mid-word.
	soft atomic.Bool
	// cacheable marks text that is the same every time it is said, whose
	// audio is worth keeping. See PhraseCache.
	cacheable bool
}

func (e *Engine) newSpeechPipe(turn *Turn, cacheable bool) *speechPipe {
	if old := e.speech.Load(); old != nil {
		old.Abort()
	}
	ctx, cancel := context.WithCancel(e.ctx)
	p := &speechPipe{
		e:         e,
		turn:      turn,
		in:        make(chan string, 64),
		done:      make(chan struct{}),
		ctx:       ctx,
		cancel:    cancel,
		cacheable: cacheable && e.opts.Cfg.Duplex.PhraseCache,
	}
	e.speech.Store(p)
	go p.run()
	return p
}

// Push hands the pipe more text. Only the goroutine that owns the pipe calls
// it, and it gives up the moment the pipe has stopped consuming — otherwise a
// backend still streaming tokens into an abandoned turn would block forever.
func (p *speechPipe) Push(text string) {
	select {
	case p.in <- text:
	case <-p.done:
	case <-p.ctx.Done():
	}
}

// Close signals end of text; the pipe flushes and closes the turn.
//
// Only the producer closes the input channel. Abort and SoftStop are called
// from the engine loop, a different goroutine, and closing from there raced the
// producer straight into a send on a closed channel.
func (p *speechPipe) Close() {
	p.once.Do(func() { close(p.in) })
}

// Abort stops synthesis immediately and discards buffered text.
func (p *speechPipe) Abort() { p.cancel() }

// SoftStop finishes the sentence in flight and then stops, leaving the rest of
// the answer unsaid.
func (p *speechPipe) SoftStop() { p.soft.Store(true) }

// alive reports whether the pipe is still producing.
func (p *speechPipe) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// firstFlushRetry is how soon a first segment too short to speak at its
// deadline is looked at again.
const firstFlushRetry = 30 * time.Millisecond

// firstFlushFloor is the least a first segment may hold when it is cut for
// time rather than at a boundary.
func (p *speechPipe) firstFlushFloor() int {
	if n := p.e.opts.Cfg.Duplex.StreamFirstChunkChars; n > 3 {
		return n
	}
	return 3
}

func (p *speechPipe) run() {
	// Whatever was waiting for the floor — a function result to read back,
	// something an application asked to have said — gets another look the
	// moment this pipe is finished, rather than at the next tick.
	defer p.e.wake()
	defer close(p.done)
	seg := segment.New(segment.Config{
		FirstChunkChars: p.e.opts.Cfg.Duplex.StreamFirstChunkChars,
		MinChunkChars:   p.e.opts.Cfg.Duplex.StreamMinChunkChars,
		MaxChunkChars:   p.e.opts.Cfg.Duplex.StreamMaxChunkChars,
	})

	// The first segment normally waits for punctuation. On a reply that opens
	// with a long clause it can wait a surprisingly long time, and every
	// millisecond of it is silence the caller hears. This bounds the wait:
	// once text has started arriving, whatever has accumulated is spoken by the
	// deadline whether or not a boundary turned up.
	var firstDeadline <-chan time.Time
	deadline := time.Duration(p.e.opts.Cfg.Duplex.StreamFirstChunkDeadlineMS) * time.Millisecond
	// And a second, shorter bound that starts once the text has passed a
	// clause boundary it was too short to end on. See
	// DuplexConfig.StreamFirstChunkGraceMS.
	var boundaryGrace <-chan time.Time
	grace := time.Duration(p.e.opts.Cfg.Duplex.StreamFirstChunkGraceMS) * time.Millisecond
	spoke := false

consume:
	for {
		if p.soft.Load() {
			break
		}
		select {
		case <-p.ctx.Done():
			return
		case <-boundaryGrace:
			boundaryGrace = nil
			if spoke {
				continue
			}
			early := seg.FlushFirstAtBoundary()
			if early == "" {
				continue
			}
			p.e.log.Debug("speak: first segment cut at an early boundary",
				"turn", p.turn.ID, "text", early, "grace_ms", grace.Milliseconds())
			spoke = true
			firstDeadline = nil
			if !p.speak(early) {
				return
			}
		case <-firstDeadline:
			firstDeadline = nil
			if spoke {
				continue
			}
			// At a boundary if the text has passed one, wherever it has got to
			// if not.
			forced := seg.FlushFirstAtBoundary()
			if forced == "" {
				// Not a syllable, though. A backend that has produced one
				// character by the deadline has it cut from its word — a real
				// call said "加", and then "上了吗？" — and the caller is better
				// served by the few tens of milliseconds the next ones take.
				if floor := p.firstFlushFloor(); len([]rune(seg.Pending())) < floor {
					firstDeadline = time.After(firstFlushRetry)
					continue
				}
				forced = seg.FlushFirst()
			}
			if forced == "" {
				continue
			}
			p.e.log.Debug("speak: first segment flushed on deadline",
				"turn", p.turn.ID, "chars", len([]rune(forced)), "ms", deadline.Milliseconds())
			spoke = true
			boundaryGrace = nil
			if !p.speak(forced) {
				return
			}
		case text, ok := <-p.in:
			if !ok {
				break consume
			}
			if firstDeadline == nil && !spoke && deadline > 0 {
				firstDeadline = time.After(deadline)
			}
			for _, ready := range seg.Push(text) {
				spoke = true
				firstDeadline = nil
				boundaryGrace = nil
				if !p.speak(ready) {
					return
				}
				if p.soft.Load() {
					break consume
				}
			}
			if !spoke && boundaryGrace == nil && grace > 0 && seg.HasEarlyBoundary() {
				boundaryGrace = time.After(grace)
			}
		}
	}
	// A soft stop leaves the remaining text unsaid on purpose: the user has
	// asked something else, and finishing the paragraph would be talking over
	// them.
	if rest := seg.Flush(); rest != "" && !p.soft.Load() {
		if !p.speak(rest) {
			return
		}
	}
	if p.ctx.Err() == nil && (p.soft.Load() || p.e.tracker.IsCurrent(p.turn)) {
		p.e.player.Enqueue(Segment{
			Kind:     SegEnd,
			Gen:      p.turn.Generation,
			TurnID:   p.turn.ID,
			Revision: p.turn.Revision,
		})
	}
}

// confirmed holds a turn's audio until the turn is certain, and reports false if
// it never will be.
//
// An answer begun on a guess is worth starting — the backend's first token and
// the synthesizer's first byte are the two long waits, and both are spent here
// while the caller is still deciding whether they have finished. But none of it
// may be heard until they have. A guess that turns out wrong then costs tokens
// and nothing else: no syllable goes out over a caller who was only pausing, no
// transcript of words that were never said, and nothing for history to be
// corrected about afterwards. The wait is almost always already over by the
// time there is audio to play; when it is not, it ends with the final
// transcript.
func (p *speechPipe) confirmed() bool {
	if p.turn.Committed() {
		return true
	}
	waited := time.Now()
	if !p.turn.awaitCommit(p.ctx.Done()) {
		return false
	}
	p.e.log.Debug("speak: held the answer until the turn was confirmed",
		"turn", p.turn.ID, "held_ms", time.Since(waited).Milliseconds())
	return p.ctx.Err() == nil && p.current()
}

// remembers reports whether text is looked for in the phrase cache and kept
// there once said: everything a pipe of fixed phrases says, and of an ordinary
// answer the segments short enough to be said again — its opening clauses.
func (p *speechPipe) remembers(text string) bool {
	if p.cacheable {
		return true
	}
	d := p.e.opts.Cfg.Duplex
	return d.PhraseCache && d.PhraseCacheLearnChars > 0 && len([]rune(text)) <= d.PhraseCacheLearnChars
}

// current reports whether this pipe may still produce. Under a soft stop the
// turn is allowed to be stale: the player is holding a grace open for exactly
// the sentence being synthesized here, and abandoning it now would cut the
// word in half — which is the thing a soft stop exists to avoid.
func (p *speechPipe) current() bool {
	return p.e.tracker.IsCurrent(p.turn) || p.soft.Load()
}

// speak synthesizes one segment and streams it to the player. It returns false
// when the turn has been superseded and the pipe should stop.
func (p *speechPipe) speak(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return true
	}
	if !speakable(text) {
		// A closing quote, a dash, an ellipsis: a segment the splitter cut off
		// on its own. A synthesizer returns no audio for it, and that used to
		// cost a round trip to find out and a dropped connection afterwards.
		return true
	}
	if p.ctx.Err() != nil || !p.current() {
		return false
	}

	// Words that have been said before, in this voice, do not need saying to
	// the synthesizer again — and on a hit this path needs no synthesis
	// connection at all, which is why a cached greeting can start before the
	// handshake with the vendor has finished.
	remembered := p.remembers(text)
	if remembered {
		if cached, ok := Phrases().Get(p.e.phraseKey(text)); ok {
			return p.speakCached(text, cached)
		}
	}

	stream, err := p.e.ttsSession()
	if err != nil {
		if p.ctx.Err() != nil {
			// The turn was dropped, or the session closed, while dialling.
			// That is not the synthesizer being unavailable.
			return false
		}
		p.e.log.Error("tts open failed", "err", err)
		p.e.deps.Emit(live.NewError("server_error", "tts_unavailable", err.Error(), ""))
		return false
	}

	segStarted := time.Now()
	// Its own context, cancelled on every exit from this function.
	//
	// The contract on TTSStream.Synthesize is that a caller who stops reading
	// must cancel, so the provider can resynchronize its connection. This
	// function has two early returns that used to violate that — the turn being
	// superseded, and a chunk error — and p.ctx belongs to the whole turn, so
	// it was still alive in exactly the case that mattered. The provider's
	// reader then blocked forever writing into a channel nobody was draining,
	// holding the lock that serializes synthesis on a persistent connection.
	//
	// The result is the symptom that prompted this: an answer's first segment
	// plays, every later segment blocks in Synthesize, and the turn is heard as
	// a fragment. It does not recover, because the connection is shared across
	// turns — every answer after it is a fragment too.
	segCtx, cancelSeg := context.WithCancel(p.ctx)
	defer cancelSeg()
	synthCtx := segCtx
	if !p.begun {
		p.begun = true
		synthCtx = provider.WithUrgent(segCtx)
	}
	chunks, err := stream.Synthesize(synthCtx, text)
	if err != nil {
		p.e.log.Error("tts synthesize failed", "err", err)
		p.e.deps.Emit(live.NewError("server_error", "tts_error", err.Error(), ""))
		p.e.dropTTSSession(stream)
		return false
	}

	p.turn.MarkFirstSegment()
	// Synthesis is under way; whether the caller may hear it is a separate
	// question. See confirmed.
	if !p.confirmed() {
		return false
	}
	// The answer is ready, so anything the assistant was saying to cover the
	// wait has done its job. Left playing, a filler delays the very thing it
	// was hiding — measured at nearly two seconds on a real turn.
	if !IsBackchannel(p.turn.ID) && p.e.player.PreemptBackchannel() {
		p.e.log.Debug("speak: cutting the filler short; the answer is ready",
			"turn", p.turn.ID)
	}
	p.e.player.Enqueue(Segment{
		Kind:     SegBegin,
		Gen:      p.turn.Generation,
		TurnID:   p.turn.ID,
		Revision: p.turn.Revision,
		Text:     text,
	})

	var pcmBytes int
	firstChunk := true
	// keep is the segment's audio as the provider produced it, collected only
	// for text worth caching.
	var keep []byte

	for chunk := range chunks {
		if p.ctx.Err() != nil || !p.current() {
			return false
		}
		if chunk.Err != nil {
			p.e.log.Error("tts chunk error", "err", chunk.Err)
			p.e.dropTTSSession(stream)
			return false
		}
		if len(chunk.PCM) == 0 {
			continue
		}
		if remembered {
			keep = append(keep, chunk.PCM...)
		}
		pcm, err := audio.ResamplePCM16(chunk.PCM, stream.SampleRate(), p.e.opts.ClientRate)
		if err != nil {
			continue
		}
		pcmBytes += len(pcm)
		if firstChunk {
			firstChunk = false
			p.e.log.Debug("speak: first audio for segment",
				"turn", p.turn.ID,
				"chars", len([]rune(text)),
				"ms", time.Since(segStarted).Milliseconds())
		}
		p.turn.MarkFirstAudio()
		p.e.player.Enqueue(Segment{
			Kind:     SegAudio,
			Gen:      p.turn.Generation,
			TurnID:   p.turn.ID,
			Revision: p.turn.Revision,
			PCM:      pcm,
		})
	}

	// Every check on the turn's liveness lives inside the loop body above, so a
	// synthesis that produced no chunks at all skipped all of them and fell
	// through to the mark. MiniMax returns exactly that channel — closed, empty,
	// no error — when the segment's context is already cancelled: readAudio
	// tests the context before its first recv. `audio_ms=0 ms=0` in the log is
	// this case, and it appeared twice in one session.
	if firstChunk {
		// The text was announced and none of it was produced. Withdraw the
		// announcement: the player counts pending text as spoken, because a
		// segment cut off mid-flight really was partly heard, and this one was
		// not heard at all.
		//
		// This runs before the liveness check below, and unconditionally. Only
		// one of the player's two truncation paths can currently be reached
		// with text left pending, so a superseded turn is safe today — but that
		// is an argument about which branch runs, and the wrong sentence
		// reaching history is what the whole mechanism exists to prevent. A
		// stale SegAbandon is dropped by the player at no cost.
		p.e.player.Enqueue(Segment{
			Kind:     SegAbandon,
			Gen:      p.turn.Generation,
			TurnID:   p.turn.ID,
			Revision: p.turn.Revision,
		})
	}
	if p.ctx.Err() != nil || !p.current() {
		return false
	}
	if firstChunk {
		// Still the current turn, and still no audio: the provider accepted the
		// text and delivered none of it. Drop the session so the next segment
		// starts from a connection in a known state, and keep going — the rest
		// of the answer is still worth attempting, and history now holds only
		// the part the caller heard.
		p.e.log.Warn("speak: the segment produced no audio, so it is not recorded as spoken",
			"turn", p.turn.ID,
			"text", text,
			"ms", time.Since(segStarted).Milliseconds())
		p.e.dropTTSSession(stream)
		return true
	}

	p.e.player.Enqueue(Segment{
		Kind:     SegMark,
		Gen:      p.turn.Generation,
		TurnID:   p.turn.ID,
		Revision: p.turn.Revision,
		Text:     text,
	})
	// Only a segment that ran to its end is kept: the loop above returns early
	// on every interruption, so reaching here means keep is the whole of it.
	if remembered && len(keep) > 0 {
		Phrases().Put(p.e.phraseKey(text), PhraseAudio{PCM: keep, Rate: stream.SampleRate()})
	}
	p.e.log.Debug("speak: segment synthesized",
		"turn", p.turn.ID,
		"text", text,
		"audio_ms", int(float64(pcmBytes)/float64(audio.BytesPerSample)/float64(p.e.opts.ClientRate)*1000),
		"ms", time.Since(segStarted).Milliseconds())
	return true
}

// speakCached plays a segment from the phrase cache.
func (p *speechPipe) speakCached(text string, cached PhraseAudio) bool {
	pcm, err := audio.ResamplePCM16(cached.PCM, cached.Rate, p.e.opts.ClientRate)
	if err != nil || len(pcm) == 0 {
		return true
	}
	p.turn.MarkFirstSegment()
	if !p.confirmed() {
		return false
	}
	if !IsBackchannel(p.turn.ID) && p.e.player.PreemptBackchannel() {
		p.e.log.Debug("speak: cutting the filler short; the answer is ready", "turn", p.turn.ID)
	}
	seg := Segment{Gen: p.turn.Generation, TurnID: p.turn.ID, Revision: p.turn.Revision}
	begin, body, mark := seg, seg, seg
	begin.Kind, begin.Text = SegBegin, text
	body.Kind, body.PCM = SegAudio, pcm
	mark.Kind, mark.Text = SegMark, text
	p.e.player.Enqueue(begin)
	p.turn.MarkFirstAudio()
	p.e.player.Enqueue(body)
	p.e.player.Enqueue(mark)
	p.e.log.Debug("speak: segment played from the phrase cache",
		"turn", p.turn.ID,
		"text", text,
		"audio_ms", int(audio.PCM16(p.e.opts.ClientRate).DurationMS(pcm)))
	return true
}

// ttsRate is the sample rate this session asks its synthesizer for.
func (e *Engine) ttsRate() int { return SynthesisRate(e.opts.Cfg, e.opts.ClientRate) }

// phraseKey identifies text as this session would have it synthesized.
func (e *Engine) phraseKey(text string) PhraseKey {
	name := ""
	if e.deps.TTS != nil {
		name = e.deps.TTS.Name()
	}
	return PhraseKey{
		Provider: name,
		Voice:    e.opts.Voice,
		Speed:    e.opts.Cfg.Speed,
		Language: e.opts.Language,
		Rate:     e.ttsRate(),
		Text:     text,
	}
}

func (e *Engine) ttsSession() (provider.TTSStream, error) {
	e.ttsMu.Lock()
	defer e.ttsMu.Unlock()
	if e.ttsStream != nil {
		return e.ttsStream, nil
	}
	if e.deps.TTS == nil {
		return nil, fmt.Errorf("duplex: no TTS provider configured")
	}
	opened := time.Now()
	// The session's own rate where the provider can produce it: speech
	// synthesized at 16 kHz and stretched to 24 is 16 kHz speech in a bigger
	// file, and a telephony session asking for 8 kHz was being resampled
	// twice. A provider that cannot is free to return another rate; the
	// stream reports what it actually produces and speak converts.
	ttsOpts := provider.TTSOptions{
		SampleRate: e.ttsRate(),
		Voice:      e.opts.Voice,
		Speed:      e.opts.Cfg.Speed,
		Language:   e.opts.Language,
	}
	stream, err := e.deps.TTS.Open(e.ctx, ttsOpts)
	if err != nil && e.ctx.Err() == nil {
		metrics.Default().Retry("tts")
		e.log.Warn("tts open retrying", "err", err)
		time.Sleep(150 * time.Millisecond)
		stream, err = e.deps.TTS.Open(e.ctx, ttsOpts)
	}
	if err != nil {
		metrics.Default().ProviderError("tts")
		return nil, err
	}
	e.log.Debug("speak: tts session opened",
		"provider", e.deps.TTS.Name(),
		"rate", stream.SampleRate(),
		"voice", e.opts.Voice,
		"ms", time.Since(opened).Milliseconds())
	e.ttsStream = stream
	return stream, nil
}

// resetTTS closes the synthesis connection so the next turn opens a fresh one.
// See Duplex.ResetTTSOnInterrupt for why this is sometimes worth a reconnect.
func (e *Engine) resetTTS() {
	e.ttsMu.Lock()
	stream := e.ttsStream
	e.ttsStream = nil
	e.ttsMu.Unlock()
	if stream != nil {
		e.log.Debug("speak: dropping the tts session after an interruption")
		go func() { _ = stream.Close() }()
		// The caller is mid-sentence and a reply to it is coming, so rebuild
		// the connection now rather than at the first segment of that reply.
		e.prewarm("tts reset")
	}
}

// dropTTSSession discards a session that errored, so the next segment
// reconnects rather than inheriting a broken socket.
func (e *Engine) dropTTSSession(stream provider.TTSStream) {
	e.ttsMu.Lock()
	if e.ttsStream == stream {
		e.ttsStream = nil
	}
	e.ttsMu.Unlock()
	_ = stream.Close()
}

func (e *Engine) abortSpeech() {
	if pipe := e.speech.Load(); pipe != nil {
		pipe.Abort()
	}
	e.player.Interrupt()
}

// policy is the configured response to a new query arriving mid-answer.
func (e *Engine) policy() string {
	p := e.opts.OnNewQuery
	if p == "" {
		p = e.opts.Cfg.Duplex.OnNewQuery
	}
	switch p {
	case "cut", "finish_sentence", "queue":
		return p
	default:
		return "cut"
	}
}

// assistantHasFloor reports whether an answer is still in progress — speaking,
// queued, or still being generated.
//
// This is deliberately broader than "audio is going out right now". The player
// falls silent in the gap between two synthesized sentences, and keying
// interruption on that moment alone was a real bug: start talking in such a gap
// and the stale answer resumed afterwards, ahead of the answer to what you had
// just asked.
//
// "Still being generated" includes the stretch before the backend has said
// anything at all — the request is out and its first token is not back. On a
// real backend that is most of a second, and nothing else marks it: there is no
// audio and no speech pipe yet. Leaving it out meant a caller who spoke again
// in that gap interrupted nothing, and the answer to their first sentence then
// arrived on top of their second; and it meant something waiting for a free
// floor took it, only to be cut off mid-word when that answer turned up.
func (e *Engine) assistantHasFloor() bool {
	if e.player.Active() || e.backendPending.Load() > 0 {
		return true
	}
	pipe := e.speech.Load()
	return pipe != nil && pipe.alive()
}

// floorHold is the state of a deferred interruption: the caller has started
// making noise over the answer, and the engine is keeping the floor for a
// moment while it finds out whether that noise was "嗯" or a question.
type floorHold struct {
	started time.Time
	until   time.Time
	hold    time.Duration
	bargeIn bool
}

// sawEvidence extends the hold to its ceiling, and is called once the
// recognizer has said something consistent with a backchannel.
//
// The two clocks here are different questions, and conflating them was a bug
// worth recording. The short one asks *is this recognizer producing at all* — a
// silent line must not buy licence to talk over someone. The long one is the
// wait for a verdict, and the verdict is the final transcript, which cannot
// arrive until the VAD has closed the turn: vad.min_silence_ms alone eats most
// of the short window, and the speech before it eats the rest. Running the
// short clock all the way to the verdict meant every acknowledgement timed out
// and interrupted, and the feature did nothing whatsoever.
//
// The ceiling is what keeps the long clock honest. Two and a half seconds of
// unbroken "acknowledgement" is not one, and a recognizer stuck re-sending a
// stale hypothesis must not hold the floor forever.
func (h *floorHold) sawEvidence() { h.until = h.started.Add(4 * h.hold) }

// warnIfEcho says so, once, when barge-ins look like the assistant's own voice.
//
// The signature is specific enough to name. A barge-in that opens at exactly
// barge_in_min_speech_ms is a signal sitting precisely on the bar rather than a
// person starting to talk, whose energy overshoots it — and when that coincides
// with a measured echo return well above the room, the caller is on a
// speakerphone or has echo cancellation off, and the assistant is cutting
// itself off. A real call showed active_ms=320 against the configured 320,
// twice in a row, with noise_floor_db pinned at −60.
//
// Deliberately a warning rather than a behaviour. Energy alone cannot separate
// steady echo from a steady voice at the same level; the engine's floor hold
// handles the consequences by refusing to yield to an utterance with no words
// in it, and the acoustic cure is a setting only the operator can judge.
func (e *Engine) warnIfEcho(d audio.Decision) {
	if e.echoWarned || !d.BargeIn {
		return
	}
	atTheBar := int(d.ActiveMS) <= e.opts.Cfg.VAD.BargeInMinSpeechMS+int(e.vad.FrameMS())
	headroom := e.vad.EchoFloorDB() - e.vad.NoiseFloorDB()
	// 8 dB, not 10, because 10 missed the case this was written for: a later
	// call barged in at active_ms=320 against the configured 320 — the bar
	// exactly — with echo_floor_db=-50 over a noise floor of -59.6. Nine and a
	// half decibels of the assistant's own voice coming back is not a quiet
	// room, and the conjunction with atTheBar is what makes this specific;
	// the headroom term only has to rule out a genuinely silent one.
	if !atTheBar || headroom < 8 {
		return
	}
	e.echoWarned = true
	e.log.Warn("listen: this barge-in looks like the assistant's own voice returning",
		"active_ms", int(d.ActiveMS),
		"barge_in_min_speech_ms", e.opts.Cfg.VAD.BargeInMinSpeechMS,
		"echo_floor_db", round1(e.vad.EchoFloorDB()),
		"noise_floor_db", round1(e.vad.NoiseFloorDB()),
		"hint", "raise vad.barge_in_margin_db (try 15-20), or enable echo cancellation on the client")
}

// holdFloor defers the decision to yield, and reports whether it did.
//
// Barge-in is acoustic: the VAD knows someone is talking long before anything
// knows what they said. That is the right trade almost always — stopping
// promptly is most of what makes an agent feel interruptible — but it cannot
// tell a question from an acknowledgement, so a murmured "对" cuts the answer
// dead. A full-duplex model would simply keep talking; lacking one, the engine
// buys itself the few hundred milliseconds needed for the recognizer to say
// which kind of speech this was.
//
// Nothing else changes: the utterance is recorded and transcribed exactly as
// before. The only thing held back is the interruption.
func (e *Engine) holdFloor(bargeIn bool) bool {
	// A new utterance supersedes any hold left over from the last one, decided
	// or not.
	e.hold = nil
	d := e.opts.Cfg.Duplex
	// Deliberately not conditional on the phrase list. The hold began as
	// "was that an acknowledgement", but the question it really answers is
	// "was that speech at all", and that one matters with no list configured.
	//
	// A real call opened with the greeting being cut 768 ms in by an utterance
	// that produced no transcript: the VAD heard something, the recognizer
	// found no words in it, and the caller lost most of the greeting to a cough
	// or to the assistant's own audio coming back through the speaker. It shows
	// up as a speech.stopped with no delegation behind it, and an empty row in
	// the transcript. barge_in_min_speech_ms had already passed it — acoustics
	// alone cannot tell a door closing from a syllable, so the transcript has
	// to be allowed to settle it.
	if d.UserBackchannelHoldMS <= 0 {
		return false
	}
	if !d.AllowBargeIn || !e.assistantHasFloor() {
		return false
	}
	now := time.Now()
	hold := time.Duration(d.UserBackchannelHoldMS) * time.Millisecond
	e.hold = &floorHold{
		started: now,
		until:   now.Add(hold),
		hold:    hold,
		bargeIn: bargeIn,
	}
	e.log.Debug("listen: holding the floor while the transcript decides",
		"barge_in", bargeIn, "hold_ms", d.UserBackchannelHoldMS)
	return true
}

// judgeHold advances a deferred interruption on new transcript text, and
// reports whether the utterance should be swallowed entirely.
//
// Three outcomes, and the interesting one is the middle:
//
//   - Still consistent with a backchannel: keep holding, keep talking.
//   - Decisively not one: yield now. This is why the check runs on partials —
//     "嗯，等一下" becomes an interruption at 等, not a second later when the
//     final transcript lands. The first syllable no candidate starts with is
//     all the evidence needed, and waiting for more would make the feature
//     cost exactly what it was meant to save.
//   - A backchannel, confirmed by the final transcript: the floor was never
//     given up, and the utterance is dropped rather than answered. Replying
//     "嗯?" to someone agreeing with you is its own kind of wrong.
func (e *Engine) judgeHold(text string, final bool) (swallow bool) {
	if e.hold == nil {
		return false
	}
	// No words in it. The VAD heard something and the recognizer found nothing
	// to transcribe, which is a cough, a door, or the assistant's own voice
	// returning through a speaker — and none of those is a reason to stop
	// talking. Because the interruption was being held rather than performed,
	// there is nothing to undo: the answer simply carries on.
	//
	// This is the whole value of deferring. An interrupt taken at the moment
	// the VAD opens cannot be given back once the recognizer reports silence.
	if final && strings.TrimSpace(text) == "" {
		e.log.Debug("listen: no words in that; keeping the floor",
			"held_ms", time.Since(e.hold.started).Milliseconds())
		e.hold = nil
		return true
	}
	if e.bcSet.CouldBecome(text) {
		if !final {
			// The recognizer is producing, and what it produces still looks
			// like an acknowledgement. Wait for the verdict rather than for
			// the much shorter is-anything-arriving window.
			e.hold.sawEvidence()
			return false
		}
		if e.bcSet.Matches(text) {
			e.log.Debug("listen: backchannel, not an interruption; keeping the floor",
				"text", text)
			e.deps.Emit(live.InputBackchannelEvent{
				Envelope: live.Envelope{Type: live.ExtInputBackchannel},
				Text:     strings.TrimSpace(text),
			})
			e.hold = nil
			return true
		}
		// Consistent with a prefix but never completed — an utterance that
		// trailed off. Nothing was asked, but nothing says the caller meant to
		// keep listening either, so yield and let the empty transcript be
		// dropped downstream.
	}
	e.log.Debug("listen: not a backchannel; yielding the floor",
		"text", text, "final", final)
	e.releaseHold("transcript")
	return false
}

// releaseHold gives up a deferred interruption and performs the yield it was
// standing in for.
func (e *Engine) releaseHold(reason string) {
	h := e.hold
	if h == nil {
		return
	}
	e.hold = nil
	e.log.Debug("listen: releasing the floor hold", "reason", reason)
	e.yieldFloor(h.bargeIn)
}

// expireHold yields once the hold has run out of patience.
//
// The bound exists because a recognizer can simply not produce text — a noisy
// line, a speaker too far from the microphone, a provider having a bad minute.
// Continuing to talk over someone on the strength of no evidence is the worse
// failure of the two, so silence resolves as an interruption.
func (e *Engine) expireHold() {
	if e.hold == nil || time.Now().Before(e.hold.until) {
		return
	}
	e.releaseHold("hold expired without a decisive transcript")
}

// yieldFloor handles a new user utterance arriving while an answer is still in
// flight. bargeIn says the VAD opened the utterance while audio was actually
// going out, which is only used for reporting; the decision below rests on
// whether the assistant still holds the floor at all.
func (e *Engine) yieldFloor(bargeIn bool) {
	if !e.opts.Cfg.Duplex.AllowBargeIn || !e.assistantHasFloor() {
		return
	}
	switch e.policy() {
	case "queue":
		e.log.Debug("new query while answering: queueing behind the current answer",
			"barge_in", bargeIn)
	case "finish_sentence":
		e.log.Debug("new query while answering: finishing the sentence, then yielding",
			"barge_in", bargeIn)
		e.softInterrupt()
	default:
		e.log.Debug("new query while answering: cutting the answer",
			"barge_in", bargeIn, "speaking", e.speaking.Load())
		e.interrupt()
	}
}

// softInterrupt stops generating further sentences but lets the one already
// being spoken finish. The generation is not bumped here: the player holds a
// grace on that turn so the sentence survives the bump the new turn will make.
func (e *Engine) softInterrupt() {
	if turn := e.tracker.Current(); turn != nil {
		e.player.GraceFinishSegment(turn.ID)
	}
	if pipe := e.speech.Load(); pipe != nil {
		pipe.SoftStop()
	}
}

// interrupt is barge-in: stop generating, stop speaking, and let the
// truncation callback fix history.
func (e *Engine) interrupt() {
	e.cancelBackend()
	gen := e.gen.Bump()
	e.abortSpeech()
	if e.opts.Cfg.Duplex.ResetTTSOnInterrupt {
		e.resetTTS()
	}
	metrics.Default().BargeIn()
	e.log.Debug("barge-in: generation invalidated", "generation", gen)
}

// emitAudio hands one chunk of output to the session, which encodes it for the
// wire in whatever format the client negotiated.
func (e *Engine) emitAudio(itemID string, pcm []byte, playAt time.Time) {
	ms := audio.PCM16(e.opts.ClientRate).DurationMS(pcm)
	e.outputMS.Add(int64(ms))
	// The first byte on the wire is the moment the caller stops waiting, so it
	// is stamped here rather than when synthesis produced it — the paced player
	// sits between the two.
	if turn := e.tracker.Current(); turn != nil && turn.ID == itemID {
		turn.MarkFirstAudioOut()
	}
	start := playAt.Sub(e.startAt).Milliseconds()
	if start < 0 {
		start = 0
	}
	e.deps.Emit(live.OutputAudioDelta{
		Envelope: live.Envelope{Type: live.ServerOutputAudioDelta},
		ItemID:   itemID,
		StartMS:  start,
		EndMS:    start + int64(ms),
		PCM:      pcm,
	})
}

// onSegmentHeard reports a sentence on the assistant's transcript as its audio
// begins. See Player.OnSegment for why then and not when it was written.
func (e *Engine) onSegmentHeard(turnID, text string, playAt time.Time) {
	at := playAt.Sub(e.startAt).Milliseconds()
	if at < 0 {
		at = 0
	}
	e.deps.Emit(live.TranscriptDelta{
		Envelope: live.Envelope{Type: live.ServerOutputTranscriptDelta},
		Delta:    text,
		StartMS:  at,
		EndMS:    at,
		ItemID:   turnID,
	})
}

func (e *Engine) onSpeakingChanged(speaking bool) {
	e.speaking.Store(speaking)
	// The VAD raises its barge-in bar while we hold the floor. This is the
	// closest a cascade gets to echo cancellation without a reference signal.
	e.vad.SetAssistantSpeaking(speaking)
}

func (e *Engine) onTruncated(r TruncationReport) {
	e.log.Info("speak: turn truncated",
		"turn", r.TurnID,
		"played_ms", r.PlayedMS,
		"emitted_ms", r.TotalMS,
		"heard_text", r.SpokenText)
	e.deps.Emit(live.AudioTruncatedEvent{
		Envelope: live.Envelope{Type: live.ExtAudioTruncated},
		ItemID:   r.TurnID,
		PlayedMS: r.PlayedMS,
		TotalMS:  r.TotalMS,
		Text:     r.SpokenText,
	})
	e.recordTurn(r.TurnID, r.SpokenText)
	e.post(func() {
		turn := e.tracker.Current()
		if turn == nil || turn.ID != r.TurnID {
			return
		}
		if !turn.MarkCompleted() {
			return
		}
		e.emitMetrics(turn, r.PlayedMS, true)
	})
}

func (e *Engine) onTurnDone(turnID string, totalMS int64, text string) {
	e.log.Debug("speak: turn complete", "turn", turnID, "audio_ms", totalMS, "chars", len([]rune(text)))
	e.recordTurn(turnID, text)
	e.post(func() {
		turn := e.tracker.Current()
		if turn == nil || turn.ID != turnID {
			return
		}
		// A superseded revision drains with nothing behind it, and its turn ID
		// still matches the live revision. Letting that count as the turn's
		// completion both published a set of all-zero latencies and, worse,
		// consumed the one completion slot the real answer needed.
		if totalMS == 0 {
			return
		}
		if !turn.MarkCompleted() {
			return
		}
		e.emitMetrics(turn, totalMS, false)
	})
	e.deps.Emit(e.UsageEvent())
}

// emitMetrics reports one turn's stage latencies, all measured from the moment
// the user stopped talking.
//
// A speculative turn can legitimately report a negative first-token time: the
// backend started before the user finished, which is exactly what speculation
// buys. The sign is information, not an error, so it is not clamped.
func (e *Engine) emitMetrics(turn *Turn, totalMS int64, truncated bool) {
	tm := turn.Timings()
	origin := tm.SpeechEndAt
	if origin.IsZero() {
		origin = tm.StartedAt
	}
	ms := func(t time.Time) int64 {
		if t.IsZero() || origin.IsZero() {
			return 0
		}
		return t.Sub(origin).Milliseconds()
	}

	ev := live.TurnMetricsEvent{
		Envelope:        live.Envelope{Type: live.ExtTurnMetrics},
		TurnID:          turn.ID,
		Revision:        turn.Revision,
		Speculative:     turn.Speculative,
		SpeechEndMS:     tm.SpeechEndMS,
		EndSilenceMS:    tm.EndSilenceMS,
		ASRFinalMS:      ms(tm.TranscriptFinal),
		TurnStartMS:     ms(tm.StartedAt),
		LLMFirstTokenMS: ms(tm.FirstToken),
		FirstSegmentMS:  ms(tm.FirstSegment),
		TTSFirstAudioMS: ms(tm.FirstAudio),
		FirstAudioOutMS: ms(tm.FirstAudioOut),
		TurnCompleteMS:  ms(tm.CompletedAt),
		OutputAudioMS:   totalMS,
		Truncated:       truncated,
	}
	e.deps.Emit(ev)

	if !tm.SpeechEndAt.IsZero() {
		metrics.Default().ObserveTurn(ev.FirstAudioOutMS)
	}
	// Only turns a caller actually waited for belong in the latency summary.
	// A greeting or an unsolicited commentary has no speech-end origin, so its
	// figures describe nothing anyone experienced as waiting.
	if !truncated && !tm.FirstAudioOut.IsZero() && !tm.SpeechEndAt.IsZero() {
		e.latencies = append(e.latencies, ev.FirstAudioOutMS)
	}
	// INFO, not DEBUG: the response latency is the one number worth seeing in a
	// default-level log.
	e.log.Info("turn: response latency",
		"turn", turn.ID,
		"first_audio_out_ms", ev.FirstAudioOutMS,
		"end_silence_ms", ev.EndSilenceMS,
		"asr_final_ms", ev.ASRFinalMS,
		"turn_start_ms", ev.TurnStartMS,
		"llm_first_token_ms", ev.LLMFirstTokenMS,
		"first_segment_ms", ev.FirstSegmentMS,
		"tts_first_audio_ms", ev.TTSFirstAudioMS,
		"speculative", turn.Speculative,
		"truncated", truncated)
}

// latencySummary reports the session's response latencies once, at close.
// Median and p95 rather than a mean: one slow cold-start turn drags a mean
// enough to hide what every other turn actually did.
//
// The speculation tally is alongside because the two are one story. Every
// guess that held is a turn that started early; every one abandoned is a
// backend request paid for and thrown away. A session where the second number
// rivals the first has its speculation windows set too eagerly for its
// recognizer, and this line is where that shows.
func (e *Engine) latencySummary() {
	if len(e.latencies) == 0 && e.specHeld+e.specAbandoned == 0 {
		return
	}
	attrs := []any{
		"turns", len(e.latencies),
		"speculation_held", e.specHeld,
		"speculation_abandoned", e.specAbandoned,
	}
	if len(e.latencies) > 0 {
		v := append([]int64(nil), e.latencies...)
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
		at := func(q float64) int64 {
			i := int(q * float64(len(v)-1))
			return v[i]
		}
		attrs = append(attrs,
			"min_ms", v[0],
			"p50_ms", at(0.5),
			"p95_ms", at(0.95),
			"max_ms", v[len(v)-1])
	}
	e.log.Info("session: response latency summary", attrs...)
}

// insidePauseMinMS is the shortest silence counted as a pause inside an
// utterance. Under it are the stops and unvoiced consonants of ordinary
// speech, which no setting of duplex.early_final_ms would be near.
const insidePauseMinMS = 60

// measureLine keeps two measurements that can only be made frame by frame,
// and is called after each one. Neither takes part in any decision.
//
// The first is how long the assistant's voice takes to come back as echo,
// which is the only figure there is for the phone leg; the detector times it
// and it is collected here.
//
// The second is how long callers pause inside an utterance. An early final is
// asked for duplex.early_final_ms into a pause, so lowering it makes every
// turn faster and splits the recognition at every pause that long or longer
// that the caller carries on from. Choosing it takes the lengths of those
// pauses on real calls. A pause is counted when the caller resumes: silence
// on the frame before, voice on this one, in an utterance that is still open.
// The last condition is what keeps out the pause that ended the turn, after
// which the trailing silence also reads zero because there is no utterance.
func (e *Engine) measureLine() {
	if ms, level, ok := e.vad.TakeEchoReturn(); ok {
		e.echoReturns = append(e.echoReturns, ms)
		e.log.Debug("listen: the assistant's voice came back",
			"ms", int(ms), "level_db", round1(level))
	}
	if miss, ok := e.vad.TakeEchoMiss(); ok {
		e.echoMisses++
		e.log.Debug("listen: the assistant's voice coming back was not timed",
			"reason", miss.Reason,
			"quiet_before_ms", int(miss.QuietMS),
			"level_db", round1(miss.LevelDB),
			"noise_floor_db", round1(miss.NoiseFloorDB))
	}
	// VoicedRunMS is above zero only for an open utterance whose latest frame
	// was voiced.
	if e.silentBeforeMS >= insidePauseMinMS && e.vad.VoicedRunMS() > 0 {
		e.pausesInside = append(e.pausesInside, int(e.silentBeforeMS))
		e.log.Debug("listen: the caller paused inside the utterance",
			"pause_ms", int(e.silentBeforeMS))
	}
	e.silentBeforeMS = e.vad.TrailingSilenceMS()
}

// lineSummary reports what measureLine collected, once, at close: one line
// for each measurement that has anything in it.
func (e *Engine) lineSummary() {
	if n := len(e.echoReturns); n > 0 {
		v := append([]float64(nil), e.echoReturns...)
		sort.Float64s(v)
		e.log.Info("listen: echo return time",
			"n", n,
			"p50_ms", int(v[(n-1)/2]),
			"min_ms", int(v[0]),
			"max_ms", int(v[n-1]),
			"not_timed", e.echoMisses)
	} else if e.echoMisses > 0 {
		// Tried and never managed: say so, or it reads as a line with no echo.
		e.log.Info("listen: echo return time", "n", 0, "not_timed", e.echoMisses)
	}
	if n := len(e.pausesInside); n > 0 {
		var under80, under100, under120, longer int
		for _, ms := range e.pausesInside {
			switch {
			case ms < 80:
				under80++
			case ms < 100:
				under100++
			case ms < 120:
				under120++
			default:
				longer++
			}
		}
		e.log.Info("listen: pauses inside utterances",
			"n", n,
			"ms_60_79", under80,
			"ms_80_99", under100,
			"ms_100_119", under120,
			"ms_120_plus", longer)
	}
}

// --- backchannel channel ---

// maybeBackchannel emits a short acknowledgement while the user still holds the
// floor. It is the one place golive speaks and listens at literally the same
// moment, and it is what a real full-duplex model does for free.
func (e *Engine) maybeBackchannel() {
	d := e.opts.Cfg.Duplex
	if !e.opts.Backchannel || !d.Backchannel || len(d.BackchannelPhrases) == 0 {
		return
	}
	if !e.userOpen || e.muted || e.speaking.Load() {
		return
	}
	if time.Since(e.userSince) < time.Duration(d.BackchannelAfterMS)*time.Millisecond {
		return
	}
	if !e.lastBC.IsZero() && time.Since(e.lastBC) < time.Duration(d.BackchannelEveryMS)*time.Millisecond {
		return
	}
	// Never talk over a real answer that is merely paused between segments.
	if e.player.PendingMS() > 0 {
		return
	}
	e.lastBC = time.Now()
	e.bcSeq++
	phrase := e.pickPhrase(d.BackchannelPhrases, &e.lastAck)
	e.log.Debug("backchannel: acknowledging while the user holds the floor",
		"text", phrase, "user_talking_ms", time.Since(e.userSince).Milliseconds())

	turn := &Turn{
		ID:         fmt.Sprintf("bc_%d", e.bcSeq),
		Generation: e.gen.Current(),
		State:      TurnCommitted,
	}
	e.deps.Emit(live.BackchannelEvent{
		Envelope: live.Envelope{Type: live.ExtBackchannel},
		Kind:     live.BackchannelAck,
		Text:     phrase,
	})
	go e.speakBackchannel(turn, phrase)
}

// maybeHoldingFiller says something short while the backend is still working.
//
// A delegating agent that goes silent for two seconds sounds like a dropped
// call. The conversational layer is supposed to hold the floor while the slow
// part runs elsewhere — that separation is the whole point of delegation, and
// it only works if the front half keeps talking.
//
// It fires at most once per delegation, only when nothing else is being said,
// only when the user is not talking (the backchannel above covers that case),
// and only once the turn is running late.
//
// "Late" is measured against this session, not against the configured number,
// and that distinction is the difference between a filler and a verbal tic. A
// real call showed 稍等一下 before literally every answer, because the backend's
// time-to-first-token had settled at about 2.1 s — a long system prompt will do
// that — while holding_filler_after_ms was still the 1.5 s that suited a
// different prompt. A filler that fires every turn is not covering an unusual
// wait; it is just something the agent says now, and it costs a synthesis and
// delays the real answer each time.
//
// So the configured threshold is a floor, and the second condition is that this
// turn is slower than this session's own median. Until there are observations
// to compare against, the floor is all there is.
func (e *Engine) maybeHoldingFiller() {
	d := e.opts.Cfg.Duplex
	if !d.HoldingFiller || len(d.HoldingFillerPhrases) == 0 {
		return
	}
	if e.filled || e.delegatedTurn == nil || e.delegatedAt.IsZero() {
		return
	}
	if e.muted || e.userOpen || e.player.Active() {
		return
	}
	turn := e.delegatedTurn
	if !e.tracker.IsCurrent(turn) {
		e.delegatedTurn = nil
		return
	}
	// Once the turn has produced audio the wait is over, filler or not.
	if !turn.Timings().FirstAudio.IsZero() {
		e.delegatedTurn = nil
		return
	}
	waited := time.Since(e.delegatedAt)
	if waited < time.Duration(d.HoldingFillerAfterMS)*time.Millisecond {
		return
	}
	late, known := e.lateThreshold()
	if !known {
		// The first delegation of the session. Nothing has been measured, so
		// there is no sense in which this wait is unusual — and the configured
		// floor is a guess about a backend and a prompt this session has not
		// yet exercised. Say nothing and find out.
		//
		// Settled for the whole delegation, not re-evaluated each tick: the
		// only thing that adds an observation is an answer, which ends the
		// turn.
		e.filled = true
		e.log.Debug("backchannel: holding filler suppressed; this session has no answer to compare against yet",
			"turn", turn.ID,
			"waited_ms", waited.Milliseconds())
		return
	}
	if waited < late {
		return
	}
	// At most one filler between real answers.
	//
	// The failure this prevents is the worst thing a filler can do. When the
	// backend is slower than the caller's patience, every turn is superseded
	// before it speaks and the only thing the caller ever hears is the filler:
	// a real call went "让我查一下" — question — "让我查一下" — question —
	// "让我查一下", three turns deep, with no answer at any point. Each filler
	// was individually justified; together they were an agent that appeared to
	// have nothing to say but that.
	//
	// One is a reassurance that work is happening. Three in a row is evidence
	// it is not, and saying it again cannot help — the caller has already heard
	// that promise and watched it go unkept.
	if e.fillerSinceAnswer > 0 {
		e.log.Debug("backchannel: holding filler suppressed; the last one was not followed by an answer",
			"turn", turn.ID)
		return
	}

	e.filled = true
	e.fillerSinceAnswer++
	e.bcSeq++
	phrase := e.pickPhrase(d.HoldingFillerPhrases, &e.lastFiller)
	e.log.Debug("backchannel: holding the floor while the backend works",
		"text", phrase,
		"waiting_ms", waited.Milliseconds(),
		"session_p50_ms", e.ttfa.P50(),
		"turn", turn.ID)

	filler := &Turn{
		ID:         fmt.Sprintf("bc_%d", e.bcSeq),
		Generation: e.gen.Current(),
		State:      TurnCommitted,
	}
	e.deps.Emit(live.BackchannelEvent{
		Envelope: live.Envelope{Type: live.ExtBackchannel},
		Kind:     live.BackchannelHoldingFiller,
		Text:     phrase,
	})
	go e.speakBackchannel(filler, phrase)
}

// pickPhrase chooses at random but never twice running.
//
// Independent random choice from a three-item list repeats about a third of the
// time, and a real call duly produced "让我查一下" three turns in a row. A human
// filling a silence varies what they say; the same four syllables repeated is
// how a caller works out they are talking to a machine that is stuck.
func (e *Engine) pickPhrase(phrases []string, last *string) string {
	if len(phrases) == 0 {
		return ""
	}
	if len(phrases) == 1 {
		*last = phrases[0]
		return phrases[0]
	}
	for {
		p := phrases[rand.Intn(len(phrases))]
		if p != *last {
			*last = p
			return p
		}
	}
}

// lateThreshold is how long this turn must run before it counts as unusually
// slow for this session, and whether that is knowable yet.
//
// The margin is generous on purpose. Firing a filler on a turn that is merely
// average is the failure being fixed, and the cost of missing one is that the
// caller hears a slightly longer silence — much cheaper than an agent that says
// "稍等一下" every time it opens its mouth.
//
// One observation is enough to ask against, and waiting for three was the
// defect. A short call never reaches three: with a backend that took about
// three seconds on every turn — a long persona prompt will do that — the gate
// stayed off for turns one, two and three and the filler fired on all of them.
// The tic the gate exists to prevent simply moved to the start of the call,
// which is the worst place for it. One sample of this session's real latency
// says far more than a configured default that was chosen for a different
// prompt.
func (e *Engine) lateThreshold() (time.Duration, bool) {
	if e.ttfa.N() == 0 {
		return 0, false
	}
	return time.Duration(float64(e.ttfa.P50())*1.5) * time.Millisecond, true
}

// observeTTFA records how long the last delegation took to produce audio.
//
// Deliberately measured from delegation rather than from VAD close: it is the
// backend-and-synthesis wait the filler exists to cover, and it excludes the
// silence threshold, which no filler can help with.
func (e *Engine) observeTTFA() {
	turn := e.delegatedTurn
	if turn == nil || e.delegatedAt.IsZero() {
		return
	}
	first := turn.Timings().FirstAudio
	if first.IsZero() {
		return
	}
	e.ttfa.Add(first.Sub(e.delegatedAt).Milliseconds())
	// An answer reached the caller, so the filler's promise was kept and the
	// next slow turn may make it again.
	e.fillerSinceAnswer = 0
	e.delegatedTurn = nil
}

// --- channel state ---

func (e *Engine) publishState() {
	turnID := ""
	if turn := e.tracker.Current(); e.tracker.IsCurrent(turn) {
		turnID = turn.ID
	}
	state := live.ChannelStateEvent{
		Envelope:     live.Envelope{Type: live.ExtChannelState},
		Listening:    !e.muted,
		Transcribing: e.asrRun != nil,
		Thinking:     turnID != "" && e.speech.Load() != nil,
		Speaking:     e.speaking.Load(),
		Muted:        e.muted,
		TurnID:       turnID,
	}
	if state == e.lastState {
		return
	}
	e.lastState = state
	e.deps.Emit(state)
}

// --- helpers ---

func retryable(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, n := range []string{"429", "502", "503", "504", "unavailable", "timeout", "connection reset", "try again"} {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// speakable reports whether text has anything in it to pronounce.
func speakable(text string) bool {
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// sameWords reports whether two accounts of an utterance say the same thing:
// identical once punctuation is set aside, or one the other with nothing after
// it but the sounds people trail off on. "也可以是吧" and "也可以是吧，啊" are one
// turn, and on a real call the answer begun on the first was thrown away and
// begun again when the recognizer reported the second.
func sameWords(a, b string) bool {
	na, nb := normalizeForCompare(a), normalizeForCompare(b)
	if na == nb {
		return true
	}
	if len(na) > len(nb) {
		na, nb = nb, na
	}
	if na == "" || !strings.HasPrefix(nb, na) {
		return false
	}
	for _, r := range nb[len(na):] {
		if !strings.ContainsRune("啊呀哦噢哈嗯呃额唔诶欸哎", r) {
			return false
		}
	}
	return true
}

func normalizeForCompare(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '，', ',', '。', '.', '？', '?', '！', '!', '、':
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

func round1(v float64) float64 {
	return float64(int(v*10)) / 10
}

func rawJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}
