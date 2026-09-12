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
	return &Session{
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
}

// Serve runs the session until the client disconnects or the context ends.
func (s *Session) Serve(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); s.writeLoop() }()

	go func() {
		<-ctx.Done()
		_ = s.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	}()

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
	_ = s.conn.Close()
}

// Emit queues a server event. It never blocks the caller for long: a client
// that cannot keep up is disconnected rather than allowed to stall the engine,
// because backpressure on a realtime audio path only makes the lag worse.
func (s *Session) Emit(event any) {
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
	s.stopReadOnce.Do(func() { close(s.stopRead) })
}

func (s *Session) setCloseReason(reason string) {
	s.closeMsg.CompareAndSwap(nil, &reason)
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
	s.conn.SetReadLimit(8 << 20)
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
		_ = s.conn.SetReadDeadline(time.Now().Add(90 * time.Second))

		if msgType == websocket.BinaryMessage {
			// A convenience for raw-PCM clients: binary frames are treated as
			// session.input_audio.append with no base64 round trip. A
			// conformant gpt-live-1 client never sends these.
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

	backchannel := s.cfg.Duplex.Backchannel
	speculative := s.cfg.Duplex.Speculative
	language := s.cfg.Language
	if g := ev.Session.Golive; g != nil {
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
		Cfg:          s.cfg,
		SessionID:    s.id,
		ClientRate:   resolved.Audio.Format.Rate,
		Instructions: resolved.Instructions,
		Voice:        voiceOf(resolved),
		Language:     language,
		Delegation:   resolved.Delegation.Type,
		Backchannel:  backchannel,
		Speculative:  speculative,
		History:      history,
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
		"asr", asrName, "llm", llmName, "tts", ttsName)

	s.Emit(live.SessionStartedEvent{
		Envelope: live.Envelope{Type: live.ServerSessionStarted},
		Session:  resolved,
	})
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
	s.withEngine(func(e *duplex.Engine) { apply(e, ev) })
	return nil
}

func voiceOf(c live.SessionConfig) string {
	if c.Audio != nil && c.Audio.Output != nil {
		return c.Audio.Output.Voice
	}
	return ""
}
