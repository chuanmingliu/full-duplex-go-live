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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/live"
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
	Delegation  string
	Backchannel bool
	Speculative bool
	History     []provider.Message
	// Greeting is spoken once the session is live, unprompted.
	Greeting string
	// OnNewQuery overrides the profile's interruption policy for this session.
	OnNewQuery string
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
	ctrl    chan func()

	// --- loop-goroutine state (no locking) ---
	muted       bool
	userOpen    bool
	userSince   time.Time
	speechEndAt time.Time
	speechEndMS int64
	asrRun      *asrRun
	asrSeq      uint64
	watch       *StabilityWatch
	specTurn    *Turn
	bcSet       *PhraseSet
	echoWarned  bool
	hold        *floorHold
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
	specWhy     string
	lastState   live.ChannelStateEvent
	pendingTool map[string]pendingCall
	waitingTool bool

	// --- shared state ---
	speech   atomic.Pointer[speechPipe]
	speaking atomic.Bool

	ttsMu     sync.Mutex
	ttsStream provider.TTSStream

	latencies []int64

	inputMS  atomic.Int64
	outputMS atomic.Int64
	inTok    atomic.Int64
	outTok   atomic.Int64
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
	openedAt time.Time
	sendDone bool
}

type pendingCall struct {
	turn *Turn
	call provider.ToolCall
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
	}
	vad := audio.NewVAD(vcfg)

	e := &Engine{
		opts:    opts,
		deps:    deps,
		log:     deps.Log.With("session", opts.SessionID),
		vad:     vad,
		framer:  audio.NewFramer(vad.FrameBytes()),
		audioCh: make(chan []byte, 64),
		asrCh:   make(chan asrEvent, 64),
		ctrl:    make(chan func(), 32),
		history: append([]provider.Message(nil), opts.History...),
		instr:   opts.Instructions,
		watch: NewStabilityWatch(
			time.Duration(opts.Cfg.Duplex.SpeculativeStableMS)*time.Millisecond,
			opts.Cfg.Duplex.SpeculativeMinChars,
		),
		bcSet:       NewPhraseSet(opts.Cfg.Duplex.UserBackchannelPhrases),
		pendingTool: map[string]pendingCall{},
		startAt:     time.Now(),
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
		// and it has exited, so reading the slice here needs no lock.
		e.latencySummary()
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
		e.log.Debug("speak: greeting", "turn", turn.ID, "text", text)
		e.emitOutputTranscript(turn, text)
		pipe := e.newSpeechPipe(turn)
		pipe.Push(text)
		pipe.Close()
		turn.AppendResponse(text)
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
	}
}

// SetMuted stops or resumes consumption of client audio. Muting does not tear
// down the listen channel, so unmuting is instantaneous.
func (e *Engine) SetMuted(muted bool) {
	e.post(func() {
		e.muted = muted
		if muted {
			e.closeASR(true)
			e.vad.Reset()
			e.userOpen = false
		}
		e.publishState()
	})
}

// AppendInstructions adds trusted system-level guidance mid-session.
func (e *Engine) AppendInstructions(content string) {
	if strings.TrimSpace(content) == "" {
		return
	}
	e.instrMu.Lock()
	if e.instr == "" {
		e.instr = content
	} else {
		e.instr += "\n" + content
	}
	e.instrMu.Unlock()
	e.deps.Emit(live.AckEvent{Envelope: live.Envelope{Type: live.ServerInstructionsAppended}})
}

// AppendThinking records backend context that must inform the conversation but
// must not be spoken. This is the quiet half of client delegation: progress an
// application knows about, injected without taking the floor.
func (e *Engine) AppendThinking(delegationID, content string) {
	if strings.TrimSpace(content) == "" {
		return
	}
	e.post(func() {
		e.history = append(e.history, provider.Message{
			Role:    provider.RoleSystem,
			Content: "[backend] " + content,
		})
		e.trimHistory()
	})
	e.deps.Emit(live.AckEvent{
		Envelope:     live.Envelope{Type: live.ServerThinkingAppended},
		DelegationID: delegationID,
	})
}

// AppendCommentary hands the engine text to speak. In client-delegation mode
// this is how an application's answer reaches the user.
func (e *Engine) AppendCommentary(delegationID, content string) {
	if strings.TrimSpace(content) == "" {
		return
	}
	e.post(func() {
		turn := e.tracker.Current()
		if turn == nil || !e.tracker.IsCurrent(turn) {
			// Unsolicited commentary (a push notification, say) still deserves
			// a turn of its own so truncation accounting works.
			turn = e.tracker.Begin("", false)
		}
		e.emitOutputTranscript(turn, content)
		pipe := e.newSpeechPipe(turn)
		pipe.Push(content)
		pipe.Close()
		turn.AppendResponse(content)
	})
	e.deps.Emit(live.AckEvent{
		Envelope:     live.Envelope{Type: live.ServerCommentaryAppended},
		DelegationID: delegationID,
	})
}

// SubmitToolOutput accepts a function_call_output from the client.
func (e *Engine) SubmitToolOutput(item live.ResponseItem) {
	e.post(func() {
		pending, ok := e.pendingTool[item.CallID]
		if !ok {
			e.deps.Emit(live.NewError("invalid_request_error", "unknown_call_id",
				fmt.Sprintf("no pending tool call with call_id %q", item.CallID), ""))
			return
		}
		delete(e.pendingTool, item.CallID)
		e.history = append(e.history, provider.Message{
			Role:       provider.RoleTool,
			Name:       pending.call.Name,
			ToolCallID: item.CallID,
			Content:    item.Output,
		})
		e.trimHistory()
	})
}

// ContinueResponse resumes delegated backend work after tool results landed.
func (e *Engine) ContinueResponse(delegationID string) {
	e.post(func() {
		turn := e.tracker.Current()
		if turn == nil || !e.tracker.IsCurrent(turn) {
			return
		}
		if !e.waitingTool {
			return
		}
		e.waitingTool = false
		go e.runBackend(turn, e.snapshotMessages(turn))
	})
}

// Usage reports cumulative session cost drivers.
func (e *Engine) Usage() live.UsageBody {
	in := float64(e.inputMS.Load()) / 1000
	out := float64(e.outputMS.Load()) / 1000
	return live.UsageBody{
		VoiceDurationSeconds: time.Since(e.startAt).Seconds(),
		InputAudioSeconds:    in,
		OutputAudioSeconds:   out,
		BackendInputTokens:   int(e.inTok.Load()),
		BackendOutputTokens:  int(e.outTok.Load()),
	}
}

// post runs f on the engine loop goroutine.
func (e *Engine) post(f func()) {
	select {
	case e.ctrl <- f:
	case <-e.ctx.Done():
	}
}

// --- main loop ---

func (e *Engine) loop() {
	tick := time.NewTicker(120 * time.Millisecond)
	defer tick.Stop()
	defer e.closeASR(true)

	for {
		select {
		case <-e.ctx.Done():
			return
		case pcm := <-e.audioCh:
			e.onAudio(pcm)
		case ev := <-e.asrCh:
			e.onASREvent(ev)
		case f := <-e.ctrl:
			f()
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
	}
	// Per frame, not per tick: the window between "gone quiet" and "turn over"
	// is a couple of hundred milliseconds, and a 120 ms tick would spend most
	// of the head start waiting to notice it was available. The same argument
	// applies to a hold that has run out — every millisecond late is a
	// millisecond spent talking over someone.
	e.expireHold()
	e.maybeSpeculate()
}

func (e *Engine) handleDecision(d audio.Decision) {
	switch d.Kind {
	case audio.DecisionStarted:
		e.userOpen = true
		e.userSince = time.Now()
		e.watch.Reset()
		e.specTurn = nil
		e.specWhy = ""
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
		e.openASR(d.StartMS)
		e.writeASR(d.Snapshot)
		e.publishState()

	case audio.DecisionSpeaking:
		e.writeASR(d.Snapshot)
		if e.asrRun != nil {
			e.asrRun.endMS = d.EndMS
		}
		e.log.Debug("listen: snapshot",
			"samples", len(d.Snapshot),
			"active_ms", int(d.ActiveMS),
			"end_ms", d.EndMS)

	case audio.DecisionStopped:
		e.userOpen = false
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
		// ...but only for a turn this utterance actually produced. A turn that
		// was already live when the caller started talking belongs to an
		// earlier utterance, or to none at all — the greeting is the clearest
		// case, and it is how item_1 came to report first_segment_ms: -742.
		// Stamping it dated audio that had already been spoken to a moment
		// still in the future, so every figure came out negative. It is not
		// waiting for anything the caller said, and it has no origin.
		if turn := e.tracker.Current(); turn != nil && turn != e.turnBeforeUtterance {
			turn.MarkSpeechEnd(e.speechEndAt, e.speechEndMS)
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
			Envelope: live.Envelope{Type: live.ExtSpeechStopped},
			StartMS:  d.StartMS,
			EndMS:    d.EndMS,
			BargeIn:  d.BargeIn,
		})
		e.closeASR(false)
		e.publishState()
	}
}

func (e *Engine) onTick() {
	e.publishState()
	e.expireHold()
	e.observeTTFA()
	e.maybeBackchannel()
	e.maybeHoldingFiller()

	if max := e.opts.Cfg.Duplex.SessionMaxSeconds; max > 0 && time.Since(e.startAt) > time.Duration(max)*time.Second {
		e.deps.Emit(live.SessionClosedEvent{
			Envelope: live.Envelope{Type: live.ServerSessionClosed},
			Reason:   live.CloseExpired,
		})
		e.cancel()
	}
}

// --- listen channel: ASR ---

func (e *Engine) openASR(startMS int64) {
	e.closeASR(true)
	if e.deps.ASR == nil {
		return
	}
	stream, err := e.deps.ASR.Open(e.ctx, provider.ASROptions{
		SampleRate: provider.PipelineRate,
		Language:   e.opts.Language,
		Interim:    true,
	})
	if err != nil {
		e.log.Error("asr open failed", "err", err)
		e.deps.Emit(live.NewError("server_error", "asr_unavailable", err.Error(), ""))
		return
	}
	e.asrSeq++
	run := &asrRun{
		id:       e.asrSeq,
		stream:   stream,
		itemID:   fmt.Sprintf("user_%d", e.asrSeq),
		startMS:  startMS,
		endMS:    startMS,
		openedAt: time.Now(),
	}
	e.asrRun = run
	e.log.Debug("asr: stream opened",
		"provider", e.deps.ASR.Name(),
		"run", run.id,
		"item", run.itemID,
		"start_ms", startMS)

	go func(id uint64, s provider.ASRStream) {
		for res := range s.Results() {
			select {
			case e.asrCh <- asrEvent{run: id, res: res}:
			case <-e.ctx.Done():
				return
			}
		}
		select {
		case e.asrCh <- asrEvent{run: id, done: true}:
		case <-e.ctx.Done():
		}
	}(run.id, stream)
}

func (e *Engine) writeASR(samples []float32) {
	if e.asrRun == nil || len(samples) == 0 || e.asrRun.sendDone {
		return
	}
	if err := e.asrRun.stream.Write(audio.EncodePCM16(samples)); err != nil {
		e.log.Warn("asr write failed", "err", err)
	}
}

// closeASR ends the current recognition. hard tears the stream down without
// waiting for a final result; the soft form asks for one.
func (e *Engine) closeASR(hard bool) {
	run := e.asrRun
	if run == nil {
		return
	}
	if hard {
		e.asrRun = nil
		_ = run.stream.Close()
		return
	}
	if !run.sendDone {
		run.sendDone = true
		if err := run.stream.CloseSend(); err != nil {
			e.log.Warn("asr close-send failed", "err", err)
		}
	}
}

func (e *Engine) onASREvent(ev asrEvent) {
	run := e.asrRun
	if run == nil || run.id != ev.run {
		return // a stale stream's result; the utterance it belongs to is gone
	}
	if ev.done {
		// The recognizer finished the utterance without ever producing a word.
		// Same conclusion as an empty final, reached by a provider that closes
		// rather than sending one: there was no speech, so the assistant keeps
		// the floor instead of waiting for the hold to time out and yielding to
		// a noise.
		if e.hold != nil && strings.TrimSpace(run.lastText) == "" {
			e.log.Debug("listen: the recognizer ended with no words; keeping the floor",
				"held_ms", time.Since(e.hold.started).Milliseconds())
			e.hold = nil
		}
		if run.sendDone {
			e.asrRun = nil
		}
		return
	}
	if ev.res.Err != nil {
		e.log.Warn("asr error", "err", ev.res.Err)
		e.deps.Emit(live.NewError("server_error", "asr_error", ev.res.Err.Error(), ""))
		return
	}

	text := strings.TrimSpace(ev.res.Text)
	if text == "" && !ev.res.Final {
		return
	}
	if ev.res.Final {
		e.log.Debug("asr: final",
			"run", run.id,
			"text", text,
			"elapsed_ms", time.Since(run.openedAt).Milliseconds())
	} else if text != run.lastText {
		e.log.Debug("asr: partial", "run", run.id, "text", text)
	}
	// Before anything else: if an interruption is being held pending the
	// transcript, this is the evidence it was waiting for.
	if e.hold != nil && e.judgeHold(text, ev.res.Final) {
		run.lastText = text
		return
	}

	e.emitInputTranscript(run, text, ev.res.Final)

	if ev.res.Final {
		run.lastText = text
		e.onFinalTranscript(run, text)
		return
	}
	run.lastText = text
	// Deliberately no speculation check here. Whether to guess depends on
	// whether the speaker has paused, which is an acoustic question, so it is
	// answered on the audio path in maybeSpeculate.
}

func (e *Engine) emitInputTranscript(run *asrRun, text string, final bool) {
	if text == run.lastText && !final {
		return
	}
	delta := live.TranscriptDelta{
		Envelope: live.Envelope{Type: live.ServerInputTranscriptDelta},
		ItemID:   run.itemID,
		StartMS:  run.startMS,
		EndMS:    run.endMS,
		Final:    final,
	}
	// Extending hypotheses stream as fragments; corrections and the final
	// result carry the whole row.
	if !final && strings.HasPrefix(text, run.lastText) && run.lastText != "" {
		delta.Content = strings.TrimPrefix(text, run.lastText)
	} else {
		delta.Content = text
		delta.Replace = true
	}
	if delta.Content == "" && !final {
		return
	}
	e.deps.Emit(delta)
}

// maybeSpeculate decides whether to start the backend before the VAD has
// finished waiting out the end of the turn.
//
// It runs on the audio path, once per frame, because the trigger is acoustic:
// the speaker has gone quiet for a while but not yet long enough for
// vad.min_silence_ms to close the utterance. That window — the difference
// between speculative_stable_ms and min_silence_ms, plus however long the final
// transcript takes to arrive — is the head start speculation buys, and it is
// free whenever the guess holds.
//
// The earlier version triggered on a transcript that had stopped changing,
// which sounds equivalent and is not. A recognizer running a few hundred
// milliseconds behind the speaker also stops changing, so the watch fired
// mid-utterance on a prefix, and almost every speculative turn was immediately
// revised: the guess was not merely wrong, it was wrong by construction. Tying
// the clock to a pause the microphone can actually hear fixes that, and
// Unsettle restarts it the instant the speaker resumes.
func (e *Engine) maybeSpeculate() {
	if !e.opts.Speculative || e.waitingTool || e.muted {
		return
	}
	run := e.asrRun
	if run == nil || run.lastText == "" {
		e.specWhy = "no partial transcript yet"
		return
	}
	if e.watch.Fired() {
		return
	}
	// Tested against the partial itself, not against whether a floor hold is
	// pending.
	//
	// The two are different questions — the hold decides whether to yield the
	// floor, speculation decides whether to start the backend early — and
	// keying this on the hold conflated them. In practice the hold releases on
	// the first partial that cannot become an acknowledgement, so by the time
	// the text is a question the hold is already gone and the old condition
	// rarely bit; the case it did cover is this one, stated directly. It also
	// catches an acknowledgement long enough to clear speculative_min_chars
	// ("好的好的"), which the old form missed whenever no hold existed.
	if e.bcSet.Matches(run.lastText) {
		e.specWhy = "the partial so far is an acknowledgement, not a question"
		return
	}
	if n := len([]rune(run.lastText)); n < e.opts.Cfg.Duplex.SpeculativeMinChars {
		e.specWhy = fmt.Sprintf("partial is %d characters; speculative_min_chars is %d",
			n, e.opts.Cfg.Duplex.SpeculativeMinChars)
		return
	}
	if e.vad.TrailingSilenceMS() <= 0 {
		// Still talking. Anything the recognizer has emitted so far is a
		// prefix, however settled it looks.
		e.watch.Unsettle()
		e.specWhy = "the speaker never paused long enough before the turn closed"
		return
	}
	// One clock, started at the first frame of the pause: speculative_stable_ms
	// is now "quiet for this long, with the transcript unchanged throughout".
	// A hypothesis landing mid-pause restarts it, which is what we want — the
	// recognizer catching up is exactly when the guess would have been wrong.
	e.specWhy = fmt.Sprintf(
		"the pause never stayed quiet and unchanged for speculative_stable_ms (%d ms) before vad.min_silence_ms (%d ms) closed the turn",
		e.opts.Cfg.Duplex.SpeculativeStableMS, e.opts.Cfg.VAD.MinSilenceMS)
	if e.watch.Observe(run.lastText, time.Now()) {
		e.startSpeculativeTurn(run.lastText)
	}
}

// --- think channel ---

func (e *Engine) startSpeculativeTurn(text string) {
	if e.waitingTool {
		return
	}
	turn := e.tracker.Begin(text, true)
	e.specTurn = turn
	e.log.Debug("think: speculating on a stable partial", "turn", turn.ID, "text", text)
	e.beginGeneration(turn, "speculative")
}

func (e *Engine) onFinalTranscript(run *asrRun, text string) {
	if text == "" {
		// Nothing was said after all. Drop any speculation so its audio never
		// reaches the user.
		if e.specTurn != nil {
			e.abortSpeech()
			e.tracker.Abandon()
			e.specTurn = nil
		}
		return
	}

	if spec := e.specTurn; spec != nil && e.tracker.IsCurrent(spec) {
		if normalizeForCompare(spec.Transcript) == normalizeForCompare(text) {
			// The guess held: the audio already flowing is correct, and the
			// final transcript costs nothing. This is the whole point of
			// speculating.
			e.tracker.Commit(spec.ID, spec.Revision)
			e.log.Debug("think: speculation held; keeping the audio already in flight",
				"turn", spec.ID, "text", text)
			e.specTurn = nil
			return
		}
		e.log.Debug("think: speculation missed; regenerating",
			"turn", spec.ID, "guess", spec.Transcript, "final", text)
		e.abortSpeech()
		revised := e.tracker.Revise(spec.ID, text, true)
		e.specTurn = nil
		if revised != nil {
			e.beginGeneration(revised, "revision")
			return
		}
	}

	turn := e.tracker.Begin(text, false)
	turn.MarkTranscriptFinal()
	e.specTurn = nil
	// A turn that waited for the final transcript pays the backend's whole
	// time-to-first-token where a speculative one would have spent it during
	// the pause. When speculation is switched on and still never fires, that is
	// worth one line rather than a silent loss — a real call showed every
	// delegation arriving as final_transcript with nothing to say why.
	if e.opts.Speculative && e.specWhy != "" {
		e.log.Info("think: answered without speculating",
			"turn", turn.ID, "reason", e.specWhy, "transcript", text)
	}
	e.specWhy = ""
	e.beginGeneration(turn, "final_transcript")
}

// beginGeneration announces the delegation and, in responses mode, runs the
// backend itself.
func (e *Engine) beginGeneration(turn *Turn, reason string) {
	if len([]rune(turn.Transcript)) < e.opts.Cfg.Duplex.DelegateMinChars {
		return
	}
	// Only stamp the origin when it belongs to this turn. While the utterance
	// is still open — which is the case for every speculative turn — the
	// engine's speechEndAt is the *previous* utterance's, and stamping it here
	// dated the turn to the last thing the caller said. That is where
	// llm_first_token_ms=16279 came from: a real number, measured from the
	// wrong zero. A speculative turn is stamped when speech actually ends.
	if !e.userOpen && !e.speechEndAt.IsZero() {
		turn.MarkSpeechEnd(e.speechEndAt, e.speechEndMS)
	}
	target := e.opts.Delegation
	e.log.Debug("think: delegating",
		"turn", turn.ID,
		"revision", turn.Revision,
		"target", target,
		"reason", reason,
		"transcript", turn.Transcript,
		"history_messages", len(e.history))
	e.deps.Emit(live.DelegationCreatedEvent{
		Envelope: live.Envelope{Type: live.ServerDelegationCreated},
		Delegation: live.Delegation{
			ID:          fmt.Sprintf("%s.%d", turn.ID, turn.Revision),
			Target:      target,
			Reason:      reason,
			Transcript:  turn.Transcript,
			CreatedAtMS: time.Since(e.startAt).Milliseconds(),
		},
	})
	e.publishState()

	e.delegatedAt = time.Now()
	e.delegatedTurn = turn
	e.filled = false

	if target == live.DelegationClient {
		// The application owns the work. It will answer with
		// session.commentary.append or session.thinking.append.
		return
	}
	go e.runBackend(turn, e.snapshotMessages(turn))
}

// snapshotMessages builds the backend request on the loop goroutine so the
// generation goroutine never touches mutable history.
func (e *Engine) snapshotMessages(turn *Turn) []provider.Message {
	e.instrMu.RLock()
	instructions := e.instr
	e.instrMu.RUnlock()

	msgs := make([]provider.Message, 0, len(e.history)+2)
	if instructions != "" {
		msgs = append(msgs, provider.Message{Role: provider.RoleSystem, Content: instructions})
	}
	msgs = append(msgs, e.history...)
	if turn.Transcript != "" {
		msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: turn.Transcript})
	}
	return msgs
}

func (e *Engine) runBackend(turn *Turn, msgs []provider.Message) {
	if e.deps.LLM == nil {
		return
	}
	delegationID := fmt.Sprintf("%s.%d", turn.ID, turn.Revision)

	deltas, err := e.deps.LLM.Stream(e.ctx, provider.LLMRequest{
		Model:           e.opts.Cfg.BackendModel,
		Messages:        msgs,
		Temperature:     e.opts.Cfg.Temperature,
		MaxTokens:       e.opts.Cfg.MaxOutputTokens,
		DisableThinking: e.opts.Cfg.DisableThinking,
	})
	if err != nil {
		e.log.Error("llm stream failed", "err", err)
		e.deps.Emit(live.NewError("server_error", "backend_unavailable", err.Error(), ""))
		return
	}

	pipe := e.newSpeechPipe(turn)
	var full strings.Builder
	started := time.Now()
	firstToken := true

	for d := range deltas {
		if !e.tracker.IsCurrent(turn) {
			pipe.Abort()
			return
		}
		if d.Err != nil {
			e.log.Error("llm delta error", "err", d.Err)
			e.deps.Emit(live.NewError("server_error", "backend_error", d.Err.Error(), ""))
			break
		}
		if d.Usage != nil {
			e.inTok.Add(int64(d.Usage.InputTokens))
			e.outTok.Add(int64(d.Usage.OutputTokens))
		}
		if d.ToolCall != nil {
			e.onToolCall(turn, delegationID, *d.ToolCall)
			continue
		}
		if d.Text == "" {
			continue
		}
		turn.MarkFirstToken()
		if firstToken {
			firstToken = false
			e.log.Debug("think: first token",
				"turn", turn.ID,
				"provider", e.deps.LLM.Name(),
				"model", e.opts.Cfg.BackendModel,
				"ms", time.Since(started).Milliseconds())
		}
		full.WriteString(d.Text)
		e.emitOutputTranscript(turn, d.Text)
		e.deps.Emit(live.ResponseEventEnvelope{
			Envelope:     live.Envelope{Type: live.ServerResponseEvent},
			DelegationID: delegationID,
			Event:        rawJSON(map[string]any{"type": "response.output_text.delta", "delta": d.Text}),
		})
		pipe.Push(d.Text)
	}

	turn.AppendResponse(full.String())
	e.log.Debug("think: backend complete",
		"turn", turn.ID,
		"chars", len([]rune(full.String())),
		"ms", time.Since(started).Milliseconds())
	pipe.Close()
}

func (e *Engine) onToolCall(turn *Turn, delegationID string, call provider.ToolCall) {
	e.post(func() {
		e.pendingTool[call.ID] = pendingCall{turn: turn, call: call}
		e.waitingTool = true
	})
	// gpt-live-1 wraps backend function calls in response.event rather than
	// surfacing them inline, so the application handles them the same way in
	// either delegation mode.
	e.deps.Emit(live.ResponseEventEnvelope{
		Envelope:     live.Envelope{Type: live.ServerResponseEvent},
		DelegationID: delegationID,
		Event: rawJSON(map[string]any{
			"type":      "response.output_item.done",
			"call_id":   call.ID,
			"name":      call.Name,
			"arguments": call.Arguments,
		}),
	})
}

func (e *Engine) emitOutputTranscript(turn *Turn, text string) {
	e.deps.Emit(live.TranscriptDelta{
		Envelope: live.Envelope{Type: live.ServerOutputTranscriptDelta},
		ItemID:   turn.ID,
		Content:  text,
		StartMS:  time.Since(e.startAt).Milliseconds(),
		EndMS:    time.Since(e.startAt).Milliseconds(),
	})
}

// --- speak channel ---

// speechPipe converts a stream of text deltas into paced audio. One pipe is
// alive at a time; starting a new one aborts the old.
type speechPipe struct {
	e      *Engine
	turn   *Turn
	in     chan string
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	// soft asks the pipe to stop after the sentence it is currently
	// synthesizing, rather than abandoning it mid-word.
	soft atomic.Bool
}

func (e *Engine) newSpeechPipe(turn *Turn) *speechPipe {
	if old := e.speech.Load(); old != nil {
		old.Abort()
	}
	ctx, cancel := context.WithCancel(e.ctx)
	p := &speechPipe{
		e:      e,
		turn:   turn,
		in:     make(chan string, 64),
		done:   make(chan struct{}),
		ctx:    ctx,
		cancel: cancel,
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

func (p *speechPipe) run() {
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
	spoke := false

consume:
	for {
		if p.soft.Load() {
			break
		}
		select {
		case <-p.ctx.Done():
			return
		case <-firstDeadline:
			firstDeadline = nil
			if spoke {
				continue
			}
			forced := seg.FlushFirst()
			if forced == "" {
				continue
			}
			p.e.log.Debug("speak: first segment flushed on deadline",
				"turn", p.turn.ID, "chars", len([]rune(forced)), "ms", deadline.Milliseconds())
			spoke = true
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
				if !p.speak(ready) {
					return
				}
				if p.soft.Load() {
					break consume
				}
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
	if p.ctx.Err() != nil || !p.current() {
		return false
	}

	stream, err := p.e.ttsSession()
	if err != nil {
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
	chunks, err := stream.Synthesize(segCtx, text)
	if err != nil {
		p.e.log.Error("tts synthesize failed", "err", err)
		p.e.deps.Emit(live.NewError("server_error", "tts_error", err.Error(), ""))
		p.e.dropTTSSession(stream)
		return false
	}

	p.turn.MarkFirstSegment()
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
	p.e.log.Debug("speak: segment synthesized",
		"turn", p.turn.ID,
		"text", text,
		"audio_ms", int(float64(pcmBytes)/float64(audio.BytesPerSample)/float64(p.e.opts.ClientRate)*1000),
		"ms", time.Since(segStarted).Milliseconds())
	return true
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
	stream, err := e.deps.TTS.Open(e.ctx, provider.TTSOptions{
		SampleRate: provider.PipelineRate,
		Voice:      e.opts.Voice,
		Speed:      e.opts.Cfg.Speed,
		Language:   e.opts.Language,
	})
	if err != nil {
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
func (e *Engine) assistantHasFloor() bool {
	if e.player.Active() {
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
	gen := e.gen.Bump()
	e.abortSpeech()
	if e.opts.Cfg.Duplex.ResetTTSOnInterrupt {
		e.resetTTS()
	}
	e.log.Debug("barge-in: generation invalidated", "generation", gen)
}

func (e *Engine) emitAudio(itemID string, pcm []byte) {
	e.outputMS.Add(int64(audio.PCM16(e.opts.ClientRate).DurationMS(pcm)))
	// The first byte on the wire is the moment the caller stops waiting, so it
	// is stamped here rather than when synthesis produced it — the paced player
	// sits between the two.
	if turn := e.tracker.Current(); turn != nil && turn.ID == itemID {
		turn.MarkFirstAudioOut()
	}
	e.deps.Emit(live.OutputAudioDelta{
		Envelope: live.Envelope{Type: live.ServerOutputAudioDelta},
		ItemID:   itemID,
		Audio:    encodeBase64(pcm),
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
	e.deps.Emit(live.Usage{
		Envelope: live.Envelope{Type: live.ServerUsageUpdated},
		Usage:    e.Usage(),
	})
}

// recordTurn commits what was actually heard to conversation history. Turns
// that were cut off contribute only their audible prefix; anything else would
// have the model reasoning from sentences the user never received.
func (e *Engine) recordTurn(turnID, spoken string) {
	spoken = strings.TrimSpace(spoken)
	e.post(func() {
		if strings.HasPrefix(turnID, "bc_") {
			return // backchannels are not conversation
		}
		turn := e.tracker.Current()
		if turn != nil && turn.ID == turnID {
			turn.SetSpoken(spoken)
			if turn.Transcript != "" {
				e.history = append(e.history, provider.Message{
					Role:    provider.RoleUser,
					Content: turn.Transcript,
				})
			}
		}
		if spoken != "" {
			e.history = append(e.history, provider.Message{
				Role:    provider.RoleAssistant,
				Content: spoken,
			})
		}
		e.trimHistory()
	})
}

// trimHistory bounds what is sent to the backend, by turns and by size.
//
// The turn cap alone is not a bound on cost. history_turns: 16 is sixteen
// exchanges whatever their length, and a sales agent's answers run to fifty
// characters each while a caller's questions run to four — so the same setting
// describes wildly different prompts, and the one it produces on a real call is
// the expensive end. Since time-to-first-token tracks prompt size and nothing
// else in this cascade comes close to it as a cost, the size needs its own
// limit.
//
// Oldest first, and always in whole messages: half an exchange is worse than
// none, because the model then reads an answer with no question or a question
// with no answer and infers a conversation that did not happen.
func (e *Engine) trimHistory() {
	if max := e.opts.Cfg.HistoryTurns * 2; max > 0 && len(e.history) > max {
		e.history = append([]provider.Message(nil), e.history[len(e.history)-max:]...)
	}

	maxChars := e.opts.Cfg.HistoryMaxChars
	if maxChars <= 0 {
		return
	}
	total := 0
	for _, m := range e.history {
		total += len([]rune(m.Content))
	}
	dropped := 0
	for total > maxChars && len(e.history) > 1 {
		total -= len([]rune(e.history[0].Content))
		e.history = e.history[1:]
		dropped++
	}
	if dropped > 0 {
		e.history = append([]provider.Message(nil), e.history...)
		e.log.Debug("think: history trimmed to fit history_max_chars",
			"dropped_messages", dropped, "chars", total, "limit", maxChars)
	}
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
		ASRFinalMS:      ms(tm.TranscriptFinal),
		LLMFirstTokenMS: ms(tm.FirstToken),
		FirstSegmentMS:  ms(tm.FirstSegment),
		TTSFirstAudioMS: ms(tm.FirstAudio),
		FirstAudioOutMS: ms(tm.FirstAudioOut),
		TurnCompleteMS:  ms(tm.CompletedAt),
		OutputAudioMS:   totalMS,
		Truncated:       truncated,
	}
	e.deps.Emit(ev)

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
		"asr_final_ms", ev.ASRFinalMS,
		"llm_first_token_ms", ev.LLMFirstTokenMS,
		"first_segment_ms", ev.FirstSegmentMS,
		"tts_first_audio_ms", ev.TTSFirstAudioMS,
		"speculative", turn.Speculative,
		"truncated", truncated)
}

// latencySummary reports the session's response latencies once, at close.
// Median and p95 rather than a mean: one slow cold-start turn drags a mean
// enough to hide what every other turn actually did.
func (e *Engine) latencySummary() {
	if len(e.latencies) == 0 {
		return
	}
	v := append([]int64(nil), e.latencies...)
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	at := func(q float64) int64 {
		i := int(q * float64(len(v)-1))
		return v[i]
	}
	e.log.Info("session: response latency summary",
		"turns", len(v),
		"min_ms", v[0],
		"p50_ms", at(0.5),
		"p95_ms", at(0.95),
		"max_ms", v[len(v)-1])
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

// speakBackchannel bypasses the speech pipe: a backchannel must not disturb the
// pipe that a real answer may already be using.
func (e *Engine) speakBackchannel(turn *Turn, phrase string) {
	stream, err := e.ttsSession()
	if err != nil {
		return
	}
	// Own context, for the same reason as speak: the loop below returns the
	// moment the generation moves, and abandoning the channel without
	// cancelling wedges the connection every other turn shares. Backchannels
	// are abandoned often — a filler is superseded whenever the caller speaks
	// again — so this is the call site that did the most damage.
	bcCtx, cancelBC := context.WithCancel(e.ctx)
	defer cancelBC()
	chunks, err := stream.Synthesize(bcCtx, phrase)
	if err != nil {
		return
	}
	// The floor may have been taken while this was synthesizing, in which case
	// the player refuses it and there is nothing to say or to transcribe.
	if !e.player.Enqueue(Segment{Kind: SegBegin, Gen: turn.Generation, TurnID: turn.ID, Text: phrase}) {
		e.log.Debug("backchannel: dropped; the answer took the floor while it was synthesizing",
			"turn", turn.ID, "text", phrase)
		return
	}
	// The caller hears this, so it belongs in the transcript.
	//
	// It was missing, and the gap was not cosmetic: a real call spoke four
	// holding fillers — 稍等一下, 让我查一下, 我看一下, 让我查一下 — and the
	// transcript showed none of them, so the written record of the call did not
	// match the call. Worse, two of those fillers belonged to turns the caller
	// interrupted before any answer arrived, which read in the transcript as
	// two user questions in a row that the agent simply ignored, when what
	// actually happened was the agent saying "one moment" and then being cut
	// off.
	//
	// Conversation history is the separate question and keeps the opposite
	// answer: recordTurn still drops bc_ turns, because a filler is not
	// something the model should reason from. What was said aloud and what the
	// model is told are two different records, and conflating them is what hid
	// this.
	e.emitOutputTranscript(turn, phrase)
	for chunk := range chunks {
		if chunk.Err != nil || !e.gen.Valid(turn.Generation) {
			return
		}
		pcm, err := audio.ResamplePCM16(chunk.PCM, stream.SampleRate(), e.opts.ClientRate)
		if err != nil {
			continue
		}
		e.player.Enqueue(Segment{Kind: SegAudio, Gen: turn.Generation, TurnID: turn.ID, PCM: pcm})
	}
	e.player.Enqueue(Segment{Kind: SegMark, Gen: turn.Generation, TurnID: turn.ID, Text: phrase})
	e.player.Enqueue(Segment{Kind: SegEnd, Gen: turn.Generation, TurnID: turn.ID})
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

func encodeBase64(pcm []byte) string {
	return base64.StdEncoding.EncodeToString(pcm)
}

func rawJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}
