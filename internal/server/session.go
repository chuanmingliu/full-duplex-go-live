package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/duplex"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/metrics"
	"github.com/chuanmingliu/golive/internal/provider"
)

// EngineFactory builds the duplex engine for a session. The server injects it
// so tests can substitute a stub without a WebSocket.
type EngineFactory func(duplex.Options, duplex.Deps) *duplex.Engine

// Session is one client connection: the protocol state machine in front of one
// duplex engine.
//
// Exactly one goroutine writes to the socket. Everything that wants to emit —
// the read loop, the engine loop, the player, each generation goroutine — hands
// the event to that writer through a channel, which is what keeps the wire
// ordered without a lock on the hot audio path.
type Session struct {
	conn *websocket.Conn
	cfg  config.Config
	log  *slog.Logger
	// baseLog has no session attribute. The engine adds its own, and handing
	// it s.log instead would stamp every engine line with session= twice.
	baseLog *slog.Logger

	id  string
	out chan []byte

	// Reading and writing stop at different moments, deliberately. stopRead
	// ends the read loop; only once it has ended does Serve emit
	// session.closed and then close stopWrite, so the closing event is always
	// queued before the writer is allowed to drain and exit. Collapsing these
	// into one signal loses the final event about half the time.
	stopReadOnce sync.Once
	stopRead     chan struct{}
	stopWrite    chan struct{}

	mu      sync.Mutex
	engine  *duplex.Engine
	started bool
	config  live.SessionConfig

	newEngine   EngineFactory
	closeMsg    atomic.Pointer[string]
	audioEvents atomic.Int64
	audioOut    atomic.Int64
	lastActive  atomic.Int64
	rate        byteRate
}

// SessionOptions configure a new session.
type SessionOptions struct {
	Conn    *websocket.Conn
	Cfg     config.Config
	Log     *slog.Logger
	ID      string
	Factory EngineFactory
}

// NewSession wires a connection to the protocol handler.
func NewSession(opts SessionOptions) *Session {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	factory := opts.Factory
	if factory == nil {
		factory = duplex.New
	}
	s := &Session{
		conn:      opts.Conn,
		cfg:       opts.Cfg,
		log:       log.With("session", opts.ID),
		baseLog:   log,
		id:        opts.ID,
		out:       make(chan []byte, 512),
		stopRead:  make(chan struct{}),
		stopWrite: make(chan struct{}),
		newEngine: factory,
	}
	s.lastActive.Store(time.Now().UnixNano())
	return s
}

// Serve runs the session until the client disconnects or the context ends.
func (s *Session) Serve(ctx context.Context) {
	defer func() {
		if v := recover(); v != nil {
			s.log.Error("session panic", "err", v)
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); s.writeLoop() }()

	go func() {
		<-ctx.Done()
		_ = s.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	}()

	if idle := s.cfg.Duplex.IdleTimeoutSeconds; idle > 0 {
		go s.watchIdle(idle)
	}

	s.readLoop(ctx)

	s.mu.Lock()
	engine := s.engine
	s.mu.Unlock()
	if engine != nil {
		engine.Close()
	}

	reason := live.CloseConnectionLost
	if m := s.closeMsg.Load(); m != nil {
		reason = *m
	}
	s.Emit(live.SessionClosedEvent{Envelope: live.Envelope{Type: live.ServerSessionClosed}, Reason: reason})

	close(s.stopWrite)
	wg.Wait()
	cancel()
	s.writeClose(reason)
	_ = s.conn.Close()
}

// Emit queues a server event. It never blocks the caller for long: a client
// that cannot keep up is disconnected rather than allowed to stall the engine,
// because backpressure on a realtime audio path only makes the lag worse.
func (s *Session) Emit(event any) {
	if errEv, ok := event.(live.ErrorEvent); ok {
		metrics.Default().Error(errEv.Error.Code)
	}
	data, err := json.Marshal(event)
	if err != nil {
		s.log.Error("marshalling server event", "err", err)
		return
	}
	s.logOutbound(data)
	select {
	case s.out <- data:
	default:
		s.log.Warn("client write queue full; closing session")
		s.setCloseReason(live.CloseConnectionLost)
		s.finish()
	}
}

// finish ends the read loop. The writer keeps running until Serve has queued
// session.closed.
// logOutbound records what went out. Audio deltas are counted rather than
// printed — at 40 ms a chunk they would be 25 lines a second and would bury the
// events you actually want to read — and the full JSON of everything else is
// logged at debug so a session transcript can be replayed from the log alone.
func (s *Session) logOutbound(data []byte) {
	if !s.log.Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	var env live.Envelope
	if json.Unmarshal(data, &env) != nil {
		return
	}
	if env.Type == live.ServerOutputAudioDelta {
		n := s.audioOut.Add(1)
		if n%50 == 1 {
			s.log.Debug("server audio", "chunks", n)
		}
		return
	}
	body := string(data)
	if len(body) > 600 {
		body = body[:600] + "…"
	}
	s.log.Debug("server event", "type", env.Type, "json", body)
}

func (s *Session) finish() {
	s.stopReadOnce.Do(func() {
		close(s.stopRead)
		// ReadMessage ignores stopRead while it is blocked. Expiring the
		// deadline is what actually wakes the read loop so idle, drain,
		// flood and session.close all close the socket in bounded time.
		_ = s.conn.SetReadDeadline(time.Now())
	})
}

func (s *Session) touch() {
	s.lastActive.Store(time.Now().UnixNano())
}

func (s *Session) watchIdle(seconds int) {
	d := time.Duration(seconds) * time.Second
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.stopRead:
			return
		case <-tick.C:
			last := time.Unix(0, s.lastActive.Load())
			if time.Since(last) >= d {
				s.log.Info("session idle timeout", "idle_s", seconds)
				s.setCloseReason(live.CloseExpired)
				s.finish()
				return
			}
		}
	}
}

func (s *Session) setCloseReason(reason string) {
	s.closeMsg.CompareAndSwap(nil, &reason)
}

func (s *Session) writeClose(reason string) {
	code := websocket.CloseGoingAway
	switch reason {
	case live.CloseRequested, live.CloseRemoteHangup:
		code = websocket.CloseNormalClosure
	case live.CloseFlooded:
		code = websocket.CloseTryAgainLater
	case live.CloseShutdown:
		code = websocket.CloseGoingAway
	}
	_ = s.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason),
		time.Now().Add(time.Second),
	)
}

func (s *Session) floodClose(why string) {
	s.log.Warn("session rate limit exceeded", "why", why)
	s.setCloseReason(live.CloseFlooded)
	s.Emit(live.NewError("invalid_request_error", "rate_limited",
		"client is sending too fast", ""))
	s.finish()
}

// byteRate is a one-second window counted on the read loop, so it needs no lock.
type byteRate struct {
	start  time.Time
	events int
	audio  int
}

func (b *byteRate) roll() {
	if b.start.IsZero() || time.Since(b.start) >= time.Second {
		b.start = time.Now()
		b.events = 0
		b.audio = 0
	}
}

func (b *byteRate) allowEvent(max int) bool {
	if max <= 0 {
		return true
	}
	b.roll()
	b.events++
	return b.events <= max
}

func (b *byteRate) allowAudio(n, maxKbps int) bool {
	if maxKbps <= 0 {
		return true
	}
	b.roll()
	b.audio += n
	return b.audio <= maxKbps*1000/8
}

func (s *Session) writeLoop() {
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-s.stopWrite:
			// Drain whatever is already queued so session.closed is delivered.
			for {
				select {
				case data := <-s.out:
					_ = s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
					_ = s.conn.WriteMessage(websocket.TextMessage, data)
				default:
					return
				}
			}
		case data := <-s.out:
			_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := s.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				s.log.Debug("write failed", "err", err)
				s.finish()
				return
			}
		case <-ping.C:
			_ = s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := s.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				s.finish()
				return
			}
		}
	}
}

func (s *Session) readLoop(ctx context.Context) {
	// 1 MiB is enough for a session.start carrying the 128-message history
	// cap, and far smaller than the previous 8 MiB which invited a memory
	// spike from a single frame.
	s.conn.SetReadLimit(1 << 20)
	_ = s.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	s.conn.SetPongHandler(func(string) error {
		return s.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	})

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopRead:
			return
		default:
		}

		msgType, data, err := s.conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				s.setCloseReason(live.CloseRemoteHangup)
			}
			return
		}
		s.touch()
		_ = s.conn.SetReadDeadline(time.Now().Add(90 * time.Second))

		if msgType == websocket.BinaryMessage {
			// A convenience for raw-PCM clients: binary frames are treated as
			// session.input_audio.append with no base64 round trip. A
			// conformant gpt-live-1 client never sends these.
			if !s.rate.allowAudio(len(data), s.cfg.Duplex.MaxAudioKbps) {
				s.floodClose("audio")
				return
			}
			s.handleBinaryAudio(data)
			continue
		}
		if err := s.handleEvent(ctx, data); err != nil {
			s.log.Debug("event rejected", "err", err)
		}
	}
}

func (s *Session) handleBinaryAudio(data []byte) {
	s.mu.Lock()
	engine := s.engine
	s.mu.Unlock()
	if engine == nil {
		return
	}
	engine.PushAudio(data)
}

func (s *Session) handleEvent(ctx context.Context, data []byte) error {
	eventType, eventID, err := live.DecodeType(data)
	if err != nil {
		s.Emit(live.NewError("invalid_request_error", "malformed_event", err.Error(), ""))
		return err
	}

	s.mu.Lock()
	started := s.started
	s.mu.Unlock()

	// Audio append is logged at its own level: one line per 20 ms frame would
	// bury everything else, so it is counted rather than printed.
	if eventType == live.ClientInputAudioAppend {
		n := s.audioEvents.Add(1)
		if n%250 == 1 {
			s.log.Debug("client audio", "frames", n, "bytes", len(data))
		}
	} else {
		s.log.Debug("client event", "type", eventType, "event_id", eventID, "bytes", len(data))
	}

	if eventType != live.ClientInputAudioAppend {
		if !s.rate.allowEvent(s.cfg.Duplex.MaxEventsPerSecond) {
			s.floodClose("events")
			return fmt.Errorf("live: rate limited")
		}
	}

	if !started && eventType != live.ClientSessionStart {
		s.Emit(live.NewError("invalid_request_error", "session_not_started",
			"send session.start before any other event", eventID))
		return fmt.Errorf("live: %s before session.start", eventType)
	}

	switch eventType {
	case live.ClientSessionStart:
		return s.onSessionStart(ctx, data, eventID)
	case live.ClientSessionUpdate:
		return s.onSessionUpdate(data, eventID)
	case live.ClientInputAudioAppend:
		return s.onInputAudio(data, eventID)
	case live.ClientInputAudioMute:
		s.withEngine(func(e *duplex.Engine) { e.SetMuted(true) })
		return nil
	case live.ClientInputAudioUnmute:
		s.withEngine(func(e *duplex.Engine) { e.SetMuted(false) })
		return nil
	case live.ClientInstructionsAppend:
		return s.onAppend(data, eventID, func(e *duplex.Engine, ev live.AppendEvent) {
			e.AppendInstructions(ev.Content)
		})
	case live.ClientThinkingAppend:
		return s.onAppend(data, eventID, func(e *duplex.Engine, ev live.AppendEvent) {
			e.AppendThinking(ev.DelegationID, ev.Content)
		})
	case live.ClientCommentaryAppend:
		return s.onAppend(data, eventID, func(e *duplex.Engine, ev live.AppendEvent) {
			e.AppendCommentary(ev.DelegationID, ev.Content)
		})
	case live.ClientResponseItemCreate:
		var ev live.ResponseItemCreateEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			s.Emit(live.NewError("invalid_request_error", "malformed_event", err.Error(), eventID))
			return err
		}
		s.withEngine(func(e *duplex.Engine) { e.SubmitToolOutput(ev.Item) })
		return nil
	case live.ClientResponseCreate:
		var ev live.ResponseCreateEvent
		_ = json.Unmarshal(data, &ev)
		s.withEngine(func(e *duplex.Engine) { e.ContinueResponse(ev.DelegationID) })
		return nil
	case live.ClientSessionClose:
		s.setCloseReason(live.CloseRequested)
		s.finish()
		return nil
	default:
		s.Emit(live.NewError("invalid_request_error", "unknown_event_type",
			fmt.Sprintf("unsupported client event %q", eventType), eventID))
		return fmt.Errorf("live: unsupported client event %q", eventType)
	}
}

func (s *Session) withEngine(f func(*duplex.Engine)) {
	s.mu.Lock()
	engine := s.engine
	s.mu.Unlock()
	if engine != nil {
		f(engine)
	}
}

func (s *Session) onSessionStart(ctx context.Context, data []byte, eventID string) error {
	var ev live.SessionStartEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		s.Emit(live.NewError("invalid_request_error", "malformed_event", err.Error(), eventID))
		return err
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		s.Emit(live.NewError("invalid_request_error", "already_started",
			"session.start may only be sent once; fork the session instead", eventID))
		return fmt.Errorf("live: duplicate session.start")
	}
	s.mu.Unlock()

	resolved, err := s.resolve(ev.Session)
	if err != nil {
		s.Emit(live.NewError("invalid_request_error", "invalid_session", err.Error(), eventID))
		return err
	}

	asrName, llmName, ttsName := s.cfg.ASR, s.cfg.LLM, s.cfg.TTS
	if g := ev.Session.Golive; g != nil {
		if g.ASR != "" || g.LLM != "" || g.TTS != "" {
			if !s.cfg.AllowProviderOverride {
				s.Emit(live.NewError("invalid_request_error", "provider_override_disabled",
					"golive.asr/llm/tts cannot be set on this server", eventID))
				return fmt.Errorf("live: provider override disabled")
			}
			if g.ASR != "" {
				asrName = g.ASR
			}
			if g.LLM != "" {
				llmName = g.LLM
			}
			if g.TTS != "" {
				ttsName = g.TTS
			}
		}
	}

	asr, err := provider.OpenASR(asrName)
	if err != nil {
		s.Emit(live.NewError("server_error", "asr_unavailable", err.Error(), eventID))
		return err
	}
	llm, err := provider.OpenLLM(llmName)
	if err != nil {
		s.Emit(live.NewError("server_error", "llm_unavailable", err.Error(), eventID))
		return err
	}
	tts, err := provider.OpenTTS(ttsName)
	if err != nil {
		s.Emit(live.NewError("server_error", "tts_unavailable", err.Error(), eventID))
		return err
	}

	history := make([]provider.Message, 0, len(ev.Session.Input))
	for _, m := range ev.Session.Input {
		role := m.Role
		if role == "" {
			role = provider.RoleUser
		}
		history = append(history, provider.Message{Role: role, Content: m.Content})
	}

	// A copy, so a per-session override never mutates the server's defaults for
	// every other session.
	cfg := s.cfg
	backchannel := s.cfg.Duplex.Backchannel
	speculative := s.cfg.Duplex.Speculative
	language := s.cfg.Language
	greeting := s.cfg.Greeting
	onNewQuery := ""
	if g := ev.Session.Golive; g != nil {
		if g.UserBackchannelPhrases != nil {
			phrases, err := validPhrases(*g.UserBackchannelPhrases)
			if err != nil {
				s.Emit(live.NewError("invalid_request_error", "invalid_backchannel_phrases",
					err.Error(), eventID))
				return err
			}
			cfg.Duplex.UserBackchannelPhrases = phrases
		}
		if g.OnNewQuery != "" {
			switch g.OnNewQuery {
			case "cut", "finish_sentence", "queue":
				onNewQuery = g.OnNewQuery
			default:
				s.Emit(live.NewError("invalid_request_error", "invalid_on_new_query",
					fmt.Sprintf("golive.on_new_query %q must be cut, finish_sentence or queue",
						g.OnNewQuery), eventID))
				return fmt.Errorf("live: bad on_new_query %q", g.OnNewQuery)
			}
		}
		// A pointer, so "" explicitly suppresses the server's greeting rather
		// than falling back to it.
		if g.Greeting != nil {
			greeting = *g.Greeting
		}
		if g.Backchannel != nil {
			backchannel = *g.Backchannel
		}
		if g.Speculative != nil {
			speculative = *g.Speculative
		}
		if g.Language != "" {
			language = g.Language
		}
	}

	engine := s.newEngine(duplex.Options{
		Cfg:          cfg,
		SessionID:    s.id,
		ClientRate:   resolved.Audio.Format.Rate,
		Instructions: resolved.Instructions,
		Voice:        voiceOf(resolved),
		Language:     language,
		Delegation:   resolved.Delegation.Type,
		Backchannel:  backchannel,
		Speculative:  speculative,
		Greeting:     greeting,
		OnNewQuery:   onNewQuery,
		History:      history,
		Tools:        toolsOf(resolved),
	}, duplex.Deps{
		ASR:  asr,
		LLM:  llm,
		TTS:  tts,
		Emit: s.Emit,
		Log:  s.baseLog,
	})
	engine.Start(ctx)

	s.mu.Lock()
	s.engine = engine
	s.started = true
	s.config = resolved
	s.mu.Unlock()

	s.log.Info("session started",
		"model", resolved.Model,
		"rate", resolved.Audio.Format.Rate,
		"delegation", resolved.Delegation.Type,
		"greeting", greeting != "",
		"on_new_query", onNewQueryOr(onNewQuery, s.cfg.Duplex.OnNewQuery),
		"asr", asrName, "llm", llmName, "tts", ttsName)

	s.Emit(live.SessionStartedEvent{
		Envelope: live.Envelope{Type: live.ServerSessionStarted},
		Session:  resolved,
	})

	// Only after session.started is queued: a single writer goroutine drains
	// the outbound channel in order, so greeting audio cannot reach the client
	// before the event that tells it what format that audio is in.
	engine.Greet()
	return nil
}

// resolve fills a client's session object with server defaults and rejects the
// combinations gpt-live-1 rejects.
func (s *Session) resolve(in live.SessionConfig) (live.SessionConfig, error) {
	out := in
	out.ID = s.id
	if out.Model == "" {
		out.Model = s.cfg.Model
	}
	if out.Instructions == "" {
		out.Instructions = s.cfg.Instructions
	}

	if out.Audio == nil {
		out.Audio = &live.AudioConfig{}
	}
	if out.Audio.Format == nil {
		out.Audio.Format = &live.AudioFormat{Type: "audio/pcm", Rate: s.cfg.ClientRate}
	}
	if out.Audio.Format.Type == "" {
		out.Audio.Format.Type = "audio/pcm"
	}
	if out.Audio.Format.Type != "audio/pcm" {
		return out, fmt.Errorf("audio.format.type %q is not supported; use audio/pcm", out.Audio.Format.Type)
	}
	if out.Audio.Format.Rate == 0 {
		out.Audio.Format.Rate = s.cfg.ClientRate
	}
	switch out.Audio.Format.Rate {
	case 8000, 16000, 24000, 48000:
	default:
		return out, fmt.Errorf("audio.format.rate %d is not supported; use 8000, 16000, 24000 or 48000",
			out.Audio.Format.Rate)
	}
	if out.Audio.Output == nil {
		out.Audio.Output = &live.OutputAudio{}
	}
	if out.Audio.Output.Voice == "" {
		out.Audio.Output.Voice = s.cfg.Voice
	}

	if out.Delegation == nil {
		out.Delegation = &live.DelegationConfig{}
	}
	if out.Delegation.Type == "" {
		mode := s.cfg.Duplex.DelegationMode
		if mode == "auto" {
			mode = live.DelegationResponses
		}
		out.Delegation.Type = mode
	}
	switch out.Delegation.Type {
	case live.DelegationClient, live.DelegationResponses:
	default:
		return out, fmt.Errorf("delegation.type %q must be %q or %q",
			out.Delegation.Type, live.DelegationClient, live.DelegationResponses)
	}
	if len(out.Input) > 128 {
		return out, fmt.Errorf("session.input accepts at most 128 messages, got %d", len(out.Input))
	}
	return out, nil
}

func (s *Session) onSessionUpdate(data []byte, eventID string) error {
	var ev live.SessionUpdateEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		s.Emit(live.NewError("invalid_request_error", "malformed_event", err.Error(), eventID))
		return err
	}

	s.mu.Lock()
	current := s.config
	s.mu.Unlock()

	// gpt-live-1 fixes model, voice, audio format and delegation type at
	// startup: all four are baked into connections and timestamps that already
	// went out, so changing one mid-session would invalidate the transcript.
	if ev.Session.Model != "" && ev.Session.Model != current.Model {
		s.Emit(live.NewError("invalid_request_error", "immutable_field",
			"session.model cannot change after startup; start a new session", eventID))
		return fmt.Errorf("live: model is immutable")
	}
	if ev.Session.Audio != nil && ev.Session.Audio.Format != nil {
		s.Emit(live.NewError("invalid_request_error", "immutable_field",
			"session.audio.format cannot change after startup", eventID))
		return fmt.Errorf("live: audio format is immutable")
	}
	if ev.Session.Delegation != nil && ev.Session.Delegation.Type != "" &&
		ev.Session.Delegation.Type != current.Delegation.Type {
		s.Emit(live.NewError("invalid_request_error", "immutable_field",
			"delegation.type cannot change after startup; start a new session", eventID))
		return fmt.Errorf("live: delegation type is immutable")
	}

	if ev.Session.Delegation != nil && ev.Session.Delegation.Responses != nil {
		s.mu.Lock()
		if s.config.Delegation == nil {
			s.config.Delegation = &live.DelegationConfig{Type: current.Delegation.Type}
		}
		s.config.Delegation.Responses = ev.Session.Delegation.Responses
		updated := s.config
		s.mu.Unlock()

		s.withEngine(func(e *duplex.Engine) { e.SetTools(toolsOf(updated)) })

		s.Emit(live.SessionStartedEvent{
			Envelope: live.Envelope{Type: live.ServerSessionUpdated},
			Session:  updated,
		})
		return nil
	}

	s.Emit(live.SessionStartedEvent{
		Envelope: live.Envelope{Type: live.ServerSessionUpdated},
		Session:  current,
	})
	return nil
}

func (s *Session) onInputAudio(data []byte, eventID string) error {
	var ev live.InputAudioAppendEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		s.Emit(live.NewError("invalid_request_error", "malformed_event", err.Error(), eventID))
		return err
	}
	if ev.Audio == "" {
		return nil
	}
	pcm, err := base64.StdEncoding.DecodeString(ev.Audio)
	if err != nil {
		s.Emit(live.NewError("invalid_request_error", "bad_audio",
			"audio must be base64-encoded PCM in the session format", eventID))
		return err
	}
	if !s.rate.allowAudio(len(pcm), s.cfg.Duplex.MaxAudioKbps) {
		s.floodClose("audio")
		return fmt.Errorf("live: rate limited")
	}
	s.withEngine(func(e *duplex.Engine) { e.PushAudio(pcm) })
	return nil
}

func (s *Session) onAppend(data []byte, eventID string, apply func(*duplex.Engine, live.AppendEvent)) error {
	var ev live.AppendEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		s.Emit(live.NewError("invalid_request_error", "malformed_event", err.Error(), eventID))
		return err
	}
	if strings.TrimSpace(ev.Content) == "" {
		s.Emit(live.NewError("invalid_request_error", "empty_content", "content must not be empty", eventID))
		return fmt.Errorf("live: empty append content")
	}
	// gpt-live-1 caps an instruction append at 500 tokens. Enforcing a bound
	// matters more than matching it exactly: these appends go straight into a
	// context window the conversational layer cannot afford to fill, and an
	// application that pipes a whole document through one silently degrades
	// every later turn instead of failing.
	if max := s.cfg.Duplex.MaxAppendChars; max > 0 && len([]rune(ev.Content)) > max {
		s.Emit(live.NewError("invalid_request_error", "content_too_long",
			fmt.Sprintf("content is %d characters; the limit is %d. Summarize before appending: "+
				"this goes into the conversational context window, not the backend's.",
				len([]rune(ev.Content)), max), eventID))
		return fmt.Errorf("live: append content too long")
	}
	s.withEngine(func(e *duplex.Engine) { apply(e, ev) })
	return nil
}

// maxBackchannelPhrases and maxBackchannelPhraseRunes bound what a session may
// install. The list is a floor-control policy, not free-text: every entry is a
// phrase the caller permanently loses the ability to interrupt with, so a long
// list or a long entry is far more likely to be a mistake than an intention.
const (
	maxBackchannelPhrases     = 64
	maxBackchannelPhraseRunes = 24
)

// validPhrases checks a client-supplied backchannel list. Blank entries are
// dropped rather than rejected — they are what a textarea produces from a
// trailing newline, and failing a session over one would be needlessly strict.
func validPhrases(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if n := len([]rune(p)); n > maxBackchannelPhraseRunes {
			return nil, fmt.Errorf(
				"golive.user_backchannel_phrases: %q is %d characters; the limit is %d, because an "+
					"entry this long is a sentence the caller can no longer interrupt with",
				p, n, maxBackchannelPhraseRunes)
		}
		out = append(out, p)
	}
	if len(out) > maxBackchannelPhrases {
		return nil, fmt.Errorf(
			"golive.user_backchannel_phrases: %d entries; the limit is %d",
			len(out), maxBackchannelPhrases)
	}
	return out, nil
}

func onNewQueryOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func voiceOf(c live.SessionConfig) string {
	if c.Audio != nil && c.Audio.Output != nil {
		return c.Audio.Output.Voice
	}
	return ""
}

func toolsOf(c live.SessionConfig) []json.RawMessage {
	if c.Delegation != nil && c.Delegation.Responses != nil {
		return c.Delegation.Responses.Tools
	}
	return nil
}
