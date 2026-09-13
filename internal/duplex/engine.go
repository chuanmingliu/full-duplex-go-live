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
	history     []provider.Message
	instrMu     sync.RWMutex
	instr       string
	lastBC      time.Time
	bcSeq       int
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
}

func (e *Engine) handleDecision(d audio.Decision) {
	switch d.Kind {
	case audio.DecisionStarted:
		e.userOpen = true
		e.userSince = time.Now()
		e.watch.Reset()
		e.specTurn = nil
		e.log.Debug("listen: utterance opened",
			"start_ms", d.StartMS,
			"active_ms", int(d.ActiveMS),
			"barge_in", d.BargeIn,
			"noise_floor_db", round1(e.vad.NoiseFloorDB()),
			"assistant_speaking", e.speaking.Load())
		e.deps.Emit(live.SpeechEvent{
			Envelope: live.Envelope{Type: live.ExtSpeechStarted},
			StartMS:  d.StartMS,
			BargeIn:  d.BargeIn,
		})
		if d.BargeIn && e.opts.Cfg.Duplex.AllowBargeIn {
			e.interrupt()
		}
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
		// The origin for every latency this turn will report. A speculative
		// turn is already running by now, so it is stamped here rather than at
		// creation.
		e.speechEndAt = time.Now()
		e.speechEndMS = d.EndMS
		if turn := e.tracker.Current(); turn != nil {
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
	e.maybeBackchannel()

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
	e.emitInputTranscript(run, text, ev.res.Final)

	if ev.res.Final {
		run.lastText = text
		e.onFinalTranscript(run, text)
		return
	}
	run.lastText = text
	if e.opts.Speculative && e.watch.Observe(text, time.Now()) {
		e.startSpeculativeTurn(text)
	}
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
	e.beginGeneration(turn, "final_transcript")
}

// beginGeneration announces the delegation and, in responses mode, runs the
// backend itself.
func (e *Engine) beginGeneration(turn *Turn, reason string) {
	if len([]rune(turn.Transcript)) < e.opts.Cfg.Duplex.DelegateMinChars {
		return
	}
	if !e.speechEndAt.IsZero() {
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

func (p *speechPipe) Push(text string) {
	select {
	case p.in <- text:
	case <-p.ctx.Done():
	}
}

// Close signals end of text; the pipe flushes and closes the turn.
func (p *speechPipe) Close() {
	p.once.Do(func() { close(p.in) })
}

// Abort stops synthesis immediately and discards buffered text.
func (p *speechPipe) Abort() {
	p.cancel()
	p.once.Do(func() { close(p.in) })
}

func (p *speechPipe) run() {
	defer close(p.done)
	seg := segment.New(segment.Config{
		FirstChunkChars: p.e.opts.Cfg.Duplex.StreamFirstChunkChars,
		MinChunkChars:   p.e.opts.Cfg.Duplex.StreamMinChunkChars,
		MaxChunkChars:   p.e.opts.Cfg.Duplex.StreamMaxChunkChars,
	})

	for text := range p.in {
		for _, ready := range seg.Push(text) {
			if !p.speak(ready) {
				return
			}
		}
	}
	if rest := seg.Flush(); rest != "" {
		if !p.speak(rest) {
			return
		}
	}
	if p.ctx.Err() == nil && p.e.tracker.IsCurrent(p.turn) {
		p.e.player.Enqueue(Segment{
			Kind:     SegEnd,
			Gen:      p.turn.Generation,
			TurnID:   p.turn.ID,
			Revision: p.turn.Revision,
		})
	}
}

// speak synthesizes one segment and streams it to the player. It returns false
// when the turn has been superseded and the pipe should stop.
func (p *speechPipe) speak(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return true
	}
	if p.ctx.Err() != nil || !p.e.tracker.IsCurrent(p.turn) {
		return false
	}

	stream, err := p.e.ttsSession()
	if err != nil {
		p.e.log.Error("tts open failed", "err", err)
		p.e.deps.Emit(live.NewError("server_error", "tts_unavailable", err.Error(), ""))
		return false
	}

	segStarted := time.Now()
	chunks, err := stream.Synthesize(p.ctx, text)
	if err != nil {
		p.e.log.Error("tts synthesize failed", "err", err)
		p.e.deps.Emit(live.NewError("server_error", "tts_error", err.Error(), ""))
		p.e.dropTTSSession(stream)
		return false
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
		if p.ctx.Err() != nil || !p.e.tracker.IsCurrent(p.turn) {
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

// interrupt is barge-in: stop generating, stop speaking, and let the
// truncation callback fix history.
func (e *Engine) interrupt() {
	gen := e.gen.Bump()
	e.abortSpeech()
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
		turn.MarkCompleted()
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
		turn.MarkCompleted()
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

func (e *Engine) trimHistory() {
	max := e.opts.Cfg.HistoryTurns * 2
	if max > 0 && len(e.history) > max {
		e.history = append([]provider.Message(nil), e.history[len(e.history)-max:]...)
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
	phrase := d.BackchannelPhrases[rand.Intn(len(d.BackchannelPhrases))]
	e.log.Debug("backchannel: acknowledging while the user holds the floor",
		"text", phrase, "user_talking_ms", time.Since(e.userSince).Milliseconds())

	turn := &Turn{
		ID:         fmt.Sprintf("bc_%d", e.bcSeq),
		Generation: e.gen.Current(),
		State:      TurnCommitted,
	}
	e.deps.Emit(live.BackchannelEvent{
		Envelope: live.Envelope{Type: live.ExtBackchannel},
		Text:     phrase,
	})
	go e.speakBackchannel(turn, phrase)
}

// speakBackchannel bypasses the speech pipe: a backchannel must not disturb the
// pipe that a real answer may already be using.
func (e *Engine) speakBackchannel(turn *Turn, phrase string) {
	stream, err := e.ttsSession()
	if err != nil {
		return
	}
	chunks, err := stream.Synthesize(e.ctx, phrase)
	if err != nil {
		return
	}
	e.player.Enqueue(Segment{Kind: SegBegin, Gen: turn.Generation, TurnID: turn.ID, Text: phrase})
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
