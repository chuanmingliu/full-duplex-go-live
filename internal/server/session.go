package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/duplex"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/metrics"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/store"
)

// EngineFactory builds the duplex engine for a session. The server injects it
// so tests can substitute a stub without a WebSocket.
type EngineFactory func(duplex.Options, duplex.Deps) *duplex.Engine

// Session is one live conversation: the protocol state machine in front of one
// duplex engine.
//
// It has one primary connection, which carries the caller's audio in and the
// assistant's out, and any number of sidebands — further WebSockets attached to
// the same session by a server that wants to watch it or steer it without
// being in the audio path. Every connection sees every session event; commands
// are accepted from all of them, and a command's rejection goes back only to
// the connection that sent it.
//
// Exactly one goroutine writes to each socket. Everything that wants to emit —
// the read loops, the engine loop, the player, each generation goroutine —
// hands the event to those writers through channels, which is what keeps the
// wire ordered without a lock on the hot audio path.
type Session struct {
	cfg config.Config
	log *slog.Logger
	// baseLog has no session attribute. The engine adds its own, and handing
	// it s.log instead would stamp every engine line with session= twice.
	baseLog *slog.Logger

	id      string
	primary *peer
	// seq numbers server events in the order they are queued.
	seq atomic.Int64

	sideMu sync.RWMutex
	sides  map[*peer]struct{}
	// sideCount mirrors len(sides) so the audio path can skip reflecting input
	// to sidebands without taking a lock when, as usual, there are none.
	sideCount atomic.Int32

	// Reading and writing stop at different moments, deliberately. stopRead
	// ends the read loop; only once it has ended does Serve emit
	// session.closed and then close stopWrite, so the closing event is always
	// queued before the writers are allowed to drain and exit. Collapsing these
	// into one signal loses the final event about half the time.
	stopReadOnce sync.Once
	stopRead     chan struct{}
	stopWrite    chan struct{}

	mu      sync.Mutex
	engine  *duplex.Engine
	started bool
	config  live.SessionConfig
	// codec is the wire encoding negotiated at session.start. The engine only
	// ever sees linear PCM16.
	codec audio.Codec
	rate  int

	// store and rec are set for a session started with store:true on a server
	// that has somewhere to keep it. seed is set on a fork.
	store     *store.Store
	rec       *store.Recording
	seed      *store.Meta
	createdAt time.Time

	// rt is set when the primary connection speaks the OpenAI Realtime event
	// protocol rather than gpt-live-1. The session and its engine are the
	// same; only the wire is translated, in realtime.go. Sidebands on such a
	// session still speak gpt-live-1, which is what makes one a tap on a phone
	// call.
	rt *realtimeState
	// tel reports turn boundaries to an external latency console. Nil unless
	// the server has somewhere to send them.
	tel *telSession

	newEngine   EngineFactory
	closeMsg    atomic.Pointer[string]
	audioEvents atomic.Int64
	audioOut    atomic.Int64
	lastActive  atomic.Int64
}

// peer is one WebSocket attached to a session.
type peer struct {
	conn *websocket.Conn
	out  chan []byte
	// side marks a sideband: it receives a copy of the caller's audio, may not
	// send any, and its loss does not end the session.
	side bool
	// rate is this connection's own command budget.
	rate byteRate
	// gone is set, and stop closed, when a sideband is detached. The writer
	// blocks on its queue, not on the socket, so closing the socket alone
	// would leave it there until the next ping.
	gone atomic.Bool
	stop chan struct{}
}

func newPeer(conn *websocket.Conn, side bool) *peer {
	return &peer{conn: conn, out: make(chan []byte, 512), side: side, stop: make(chan struct{})}
}

// SessionOptions configure a new session.
type SessionOptions struct {
	Conn    *websocket.Conn
	Cfg     config.Config
	Log     *slog.Logger
	ID      string
	Factory EngineFactory
	// Store is where a session that asks for store:true is kept. Nil means
	// nowhere, and the request is ignored.
	Store *store.Store
	// Seed is the stored session this one is forked from.
	Seed *store.Meta
	// Realtime makes the primary connection speak the OpenAI Realtime event
	// protocol. Nil for a gpt-live-1 session.
	Realtime *RealtimeOptions
	// Telemetry receives turn-boundary events, if the server posts them.
	Telemetry *telPoster
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
		cfg:       opts.Cfg,
		log:       log.With("session", opts.ID),
		baseLog:   log,
		id:        opts.ID,
		primary:   newPeer(opts.Conn, false),
		sides:     map[*peer]struct{}{},
		stopRead:  make(chan struct{}),
		stopWrite: make(chan struct{}),
		newEngine: factory,
		store:     opts.Store,
		seed:      opts.Seed,
		createdAt: time.Now(),
	}
	s.lastActive.Store(time.Now().UnixNano())
	if opts.Realtime != nil {
		s.rt = newRealtimeState(*opts.Realtime)
	}
	if opts.Telemetry != nil {
		s.tel = newTelSession(opts.Telemetry, s.id, s.CallID(), s.rt != nil)
	}
	return s
}

// ID is the session's identifier, as reported in session.started.
func (s *Session) ID() string { return s.id }

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
	go func() { defer wg.Done(); s.writeLoop(s.primary) }()

	go func() {
		<-ctx.Done()
		_ = s.primary.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	}()

	if idle := s.cfg.Duplex.IdleTimeoutSeconds; idle > 0 {
		go s.watchIdle(idle)
	}

	s.readLoop(ctx, s.primary)
	s.finish()

	s.mu.Lock()
	engine := s.engine
	resolved := s.config
	started := s.started
	rec := s.rec
	s.mu.Unlock()

	var usage *live.UsageBody
	var history []provider.Message
	record := resolved
	if engine != nil {
		if rec != nil {
			history = engine.History()
			// As they stand now, with whatever session.instructions.append
			// added: a fork continues this conversation, under these rules.
			record.Instructions = engine.Instructions()
		}
		u := engine.Usage()
		usage = &u
		engine.Close()
	}
	if rec != nil {
		if err := rec.Close(store.Meta{
			ID:        s.id,
			CreatedAt: s.createdAt,
			Session:   record,
			History:   history,
		}); err != nil {
			s.log.Warn("stored session did not close cleanly", "err", err)
		}
	}

	reason := live.CloseConnectionLost
	if m := s.closeMsg.Load(); m != nil {
		reason = *m
	} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		// The server bounds a session's life with a deadline on this context.
		reason = live.CloseExpired
	}
	closed := live.SessionClosedEvent{
		Envelope: live.Envelope{Type: live.ServerSessionClosed},
		Reason:   reason,
		Usage:    usage,
	}
	if started {
		closed.Session = &resolved
	}
	s.Emit(closed)

	close(s.stopWrite)
	wg.Wait()
	cancel()
	s.writeClose(s.primary, reason)
	_ = s.primary.conn.Close()
}

// --- emitting ---

// Emit queues a server event for every connection. It never blocks the caller
// for long: a client that cannot keep up is disconnected rather than allowed to
// stall the engine, because backpressure on a realtime audio path only makes
// the lag worse.
func (s *Session) Emit(event any) { s.deliver(nil, event) }

// reply queues an event for one connection only: the answer to a command it
// sent, which is nobody else's business.
func (s *Session) reply(to *peer, event any) { s.deliver(to, event) }

func (s *Session) deliver(only *peer, event any) {
	switch ev := event.(type) {
	case live.ErrorEvent:
		metrics.Default().Error(ev.Error.Code)
	case live.OutputAudioDelta:
		// The engine hands over linear PCM; this is where it becomes whatever
		// the client negotiated, and where a stored session keeps its copy.
		s.mu.Lock()
		codec, rec := s.codec, s.rec
		s.mu.Unlock()
		if rec != nil {
			rec.WriteOutput(ev.PCM, ev.StartMS)
		}
		ev.Delta = base64.StdEncoding.EncodeToString(codec.FromPCM16(ev.PCM))
		if s.cfg.Duplex.LegacyAudioField {
			ev.Audio = ev.Delta
		}
		event = ev
	}

	if s.tel != nil && only == nil {
		s.tel.observe(event, s.cfg)
	}

	// A Realtime primary is sent the event translated; everything else below
	// is for connections that speak gpt-live-1, and when the primary is not
	// one of them and nobody is listening on a sideband there is nothing more
	// to do — which, at fifty audio chunks a second, is worth not doing.
	if s.rt != nil {
		if only == nil || only == s.primary {
			s.deliverRealtime(event)
		}
		if only == s.primary || (only == nil && s.sideCount.Load() == 0) {
			return
		}
	}

	data, err := json.Marshal(event)
	if err != nil {
		s.log.Error("marshalling server event", "err", err)
		return
	}
	data = live.StampEventID(data, "event_"+strconv.FormatInt(s.seq.Add(1), 10))
	s.logOutbound(data)

	if only != nil {
		s.enqueue(only, data)
		return
	}
	if s.rt == nil {
		s.enqueue(s.primary, data)
	}
	if s.sideCount.Load() > 0 {
		s.sideMu.RLock()
		for p := range s.sides {
			s.enqueue(p, data)
		}
		s.sideMu.RUnlock()
	}
}

func (s *Session) enqueue(p *peer, data []byte) {
	if p.gone.Load() {
		return
	}
	select {
	case p.out <- data:
	default:
		if p.side {
			// A sideband that cannot keep up loses its seat, not the call.
			s.log.Warn("sideband write queue full; detaching it")
			s.dropSide(p)
			return
		}
		s.log.Warn("client write queue full; closing session")
		s.setCloseReason(live.CloseConnectionLost)
		s.finish()
	}
}

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

// finish ends the read loop. The writers keep running until Serve has queued
// session.closed.
func (s *Session) finish() {
	s.stopReadOnce.Do(func() {
		close(s.stopRead)
		// ReadMessage ignores stopRead while it is blocked. Expiring the
		// deadline is what actually wakes the read loop so idle, drain,
		// flood and session.close all close the socket in bounded time.
		_ = s.primary.conn.SetReadDeadline(time.Now())
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

func (s *Session) writeClose(p *peer, reason string) {
	code := websocket.CloseGoingAway
	switch reason {
	case live.CloseRequested, live.CloseRemoteHangup:
		code = websocket.CloseNormalClosure
	case live.CloseFlooded:
		code = websocket.CloseTryAgainLater
	case live.CloseShutdown:
		code = websocket.CloseGoingAway
	}
	_ = p.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason),
		time.Now().Add(time.Second),
	)
}

func (s *Session) floodClose(p *peer, why string) {
	s.log.Warn("session rate limit exceeded", "why", why, "sideband", p.side)
	s.reply(p, live.NewError("invalid_request_error", "rate_limited",
		"client is sending too fast", ""))
	if p.side {
		s.dropSide(p)
		return
	}
	s.setCloseReason(live.CloseFlooded)
	s.finish()
}

// byteRate is a one-second window counted on one read loop, so it needs no lock.
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

func (s *Session) writeLoop(p *peer) {
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-s.stopWrite:
			// Drain whatever is already queued so session.closed is delivered.
			for {
				select {
				case data := <-p.out:
					_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
					_ = p.conn.WriteMessage(websocket.TextMessage, data)
				default:
					return
				}
			}
		case <-p.stop:
			return
		case data := <-p.out:
			_ = p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := p.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				s.log.Debug("write failed", "err", err, "sideband", p.side)
				s.lost(p)
				return
			}
		case <-ping.C:
			_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := p.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				s.lost(p)
				return
			}
		}
	}
}

// lost handles a connection that can no longer be written to.
func (s *Session) lost(p *peer) {
	if p.side {
		s.dropSide(p)
		return
	}
	s.finish()
}

func (s *Session) readLoop(ctx context.Context, p *peer) {
	// 1 MiB is enough for a session.start carrying the 128-message history
	// cap, and far smaller than the previous 8 MiB which invited a memory
	// spike from a single frame.
	p.conn.SetReadLimit(1 << 20)
	_ = p.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	p.conn.SetPongHandler(func(string) error {
		return p.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	})

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopRead:
			return
		default:
		}
		if p.gone.Load() {
			return
		}

		msgType, data, err := p.conn.ReadMessage()
		if err != nil {
			if !p.side && websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				s.setCloseReason(live.CloseRemoteHangup)
			}
			return
		}
		s.touch()
		_ = p.conn.SetReadDeadline(time.Now().Add(90 * time.Second))

		if msgType == websocket.BinaryMessage {
			// A convenience for raw-audio clients: binary frames are treated as
			// session.input_audio.append with no base64 round trip. A
			// conformant gpt-live-1 client never sends these.
			if p.side {
				s.reply(p, live.NewError("invalid_request_error", "audio_on_sideband",
					"a sideband cannot send audio; use the session's primary connection", ""))
				continue
			}
			if !p.rate.allowAudio(len(data), s.cfg.Duplex.MaxAudioKbps) {
				s.floodClose(p, "audio")
				return
			}
			s.pushAudio(p, data, "")
			continue
		}
		if err := s.handleEvent(ctx, p, data); err != nil {
			s.log.Debug("event rejected", "err", err)
		}
	}
}

// pushAudio takes audio in the session's wire format to the engine, and to
// whatever else is listening.
func (s *Session) pushAudio(p *peer, wire []byte, eventID string) {
	s.mu.Lock()
	engine, codec, rec := s.engine, s.codec, s.rec
	s.mu.Unlock()
	if engine == nil {
		return
	}
	if codec == audio.CodecPCM16 && len(wire)%2 != 0 {
		s.reply(p, live.NewError("invalid_request_error", "bad_audio",
			"audio/pcm chunks must hold whole 16-bit samples; this one has an odd number of bytes", eventID))
		return
	}
	pcm := codec.ToPCM16(wire)
	if rec != nil {
		rec.WriteInput(pcm)
	}
	engine.PushAudio(pcm)

	// A sideband is shown the caller's audio as the event a client sends it
	// in, so it can run its own detectors over it. It carries no event_id: it
	// is a reflection, not something the service said. Done here so that audio
	// sent as binary frames is reflected like any other.
	if s.sideCount.Load() > 0 {
		reflected, err := json.Marshal(live.InputAudioAppendEvent{
			Envelope: live.Envelope{Type: live.ClientInputAudioAppend},
			Audio:    base64.StdEncoding.EncodeToString(wire),
		})
		if err == nil {
			s.sideMu.RLock()
			for side := range s.sides {
				s.enqueue(side, reflected)
			}
			s.sideMu.RUnlock()
		}
	}
}

func (s *Session) handleEvent(ctx context.Context, p *peer, data []byte) error {
	if s.rt != nil && p == s.primary {
		return s.handleRealtimeEvent(ctx, p, data)
	}
	eventType, eventID, err := live.DecodeType(data)
	if err != nil {
		s.reply(p, live.NewError("invalid_request_error", "malformed_event", err.Error(), ""))
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
		s.log.Debug("client event", "type", eventType, "event_id", eventID,
			"bytes", len(data), "sideband", p.side)
	}

	if eventType != live.ClientInputAudioAppend {
		if !p.rate.allowEvent(s.cfg.Duplex.MaxEventsPerSecond) {
			s.floodClose(p, "events")
			return fmt.Errorf("live: rate limited")
		}
	}

	if !started && eventType != live.ClientSessionStart {
		s.reply(p, live.NewError("invalid_request_error", "session_not_started",
			"send session.start before any other event", eventID))
		return fmt.Errorf("live: %s before session.start", eventType)
	}
	if p.side {
		switch eventType {
		case live.ClientSessionStart:
			s.reply(p, live.NewError("invalid_request_error", "already_started",
				"the session is already running; a sideband does not send session.start", eventID))
			return fmt.Errorf("live: session.start on a sideband")
		case live.ClientInputAudioAppend:
			s.reply(p, live.NewError("invalid_request_error", "audio_on_sideband",
				"a sideband cannot send audio; use the session's primary connection", eventID))
			return fmt.Errorf("live: audio on a sideband")
		}
	}

	switch eventType {
	case live.ClientSessionStart:
		return s.onSessionStart(ctx, p, data, eventID)
	case live.ClientSessionUpdate:
		return s.onSessionUpdate(p, data, eventID)
	case live.ClientInputAudioAppend:
		return s.onInputAudio(p, data, eventID)
	case live.ClientInputAudioMute:
		s.withEngine(func(e *duplex.Engine) { e.SetMuted(true, eventID) })
		return nil
	case live.ClientInputAudioUnmute:
		s.withEngine(func(e *duplex.Engine) { e.SetMuted(false, eventID) })
		return nil
	case live.ClientInstructionsAppend:
		return s.onAppend(p, data, eventID, func(e *duplex.Engine, ev live.AppendEvent) {
			e.AppendInstructions(ev.DelegationID, ev.Content, eventID)
		})
	case live.ClientThinkingAppend:
		return s.onAppend(p, data, eventID, func(e *duplex.Engine, ev live.AppendEvent) {
			e.AppendThinking(ev.DelegationID, ev.Content, eventID)
		})
	case live.ClientCommentaryAppend:
		return s.onAppend(p, data, eventID, func(e *duplex.Engine, ev live.AppendEvent) {
			e.AppendCommentary(ev.DelegationID, ev.Content, eventID)
		})
	case live.ClientResponseItemCreate:
		var ev live.ResponseItemCreateEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			s.reply(p, live.NewError("invalid_request_error", "malformed_event", err.Error(), eventID))
			return err
		}
		s.withEngine(func(e *duplex.Engine) { e.SubmitItem(ev.Item, eventID) })
		return nil
	case live.ClientResponseCreate:
		s.withEngine(func(e *duplex.Engine) { e.ContinueResponse(eventID) })
		return nil
	case live.ClientSessionClose:
		s.setCloseReason(live.CloseRequested)
		s.finish()
		return nil
	default:
		s.reply(p, live.NewError("invalid_request_error", "unknown_event_type",
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

// The range a session may ask its end-of-turn silence to be in. Below the
// floor, the gaps inside words close turns; above the ceiling the session is
// simply not answering.
const (
	minSessionSilenceMS = 100
	maxSessionSilenceMS = 3000
)

func (s *Session) onSessionStart(ctx context.Context, p *peer, data []byte, eventID string) error {
	var ev live.SessionStartEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		s.reply(p, live.NewError("invalid_request_error", "malformed_event", err.Error(), eventID))
		return err
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		s.reply(p, live.NewError("invalid_request_error", "already_started",
			"session.start may only be sent once; fork the session instead", eventID))
		return fmt.Errorf("live: duplicate session.start")
	}
	s.mu.Unlock()

	// A fork starts from what the source session was, and may change only what
	// gpt-live-1 lets a fork change.
	request := ev.Session
	var seeded []provider.Message
	if s.seed != nil {
		request = forkConfig(s.seed.Session, ev.Session)
		seeded = s.seed.History
	}

	resolved, codec, err := s.resolve(request)
	if err != nil {
		s.reply(p, live.NewError("invalid_request_error", "invalid_session", err.Error(), eventID))
		return err
	}

	asrName, llmName, ttsName := s.cfg.ASR, s.cfg.LLM, s.cfg.TTS
	if g := request.Golive; g != nil {
		if g.ASR != "" || g.LLM != "" || g.TTS != "" {
			if !s.cfg.AllowProviderOverride {
				s.reply(p, live.NewError("invalid_request_error", "provider_override_disabled",
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
		s.reply(p, live.NewError("server_error", "asr_unavailable", err.Error(), eventID))
		return err
	}
	llm, err := provider.OpenLLM(llmName)
	if err != nil {
		s.reply(p, live.NewError("server_error", "llm_unavailable", err.Error(), eventID))
		return err
	}
	llm = provider.HedgeLLM(llm, s.cfg.LLMHedge, time.Duration(s.cfg.LLMHedgeAfterMS)*time.Millisecond, s.log)
	tts, err := provider.OpenTTS(ttsName)
	if err != nil {
		s.reply(p, live.NewError("server_error", "tts_unavailable", err.Error(), eventID))
		return err
	}
	tts = provider.HedgeTTS(tts, s.cfg.TTSHedge, s.cfg.TTSHedgeRPM, s.log)

	history := seeded
	if history == nil {
		history, err = historyFromInput(request.Input)
		if err != nil {
			s.reply(p, live.NewError("invalid_request_error", "invalid_session", err.Error(), eventID))
			return err
		}
	}

	// A copy, so a per-session override never mutates the server's defaults for
	// every other session.
	cfg := s.cfg
	backchannel := s.cfg.Duplex.Backchannel
	speculative := s.cfg.Duplex.Speculative
	language := s.cfg.Language
	greeting := s.cfg.Greeting
	onNewQuery := ""
	clientDelegation := s.cfg.Duplex.ClientDelegation
	if s.seed != nil {
		// A fork picks a conversation up in the middle. Greeting the caller
		// again would be starting it over.
		greeting = ""
	}
	if g := request.Golive; g != nil {
		if g.UserBackchannelPhrases != nil {
			phrases, err := validPhrases(*g.UserBackchannelPhrases)
			if err != nil {
				s.reply(p, live.NewError("invalid_request_error", "invalid_backchannel_phrases",
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
				s.reply(p, live.NewError("invalid_request_error", "invalid_on_new_query",
					fmt.Sprintf("golive.on_new_query %q must be cut, finish_sentence or queue",
						g.OnNewQuery), eventID))
				return fmt.Errorf("live: bad on_new_query %q", g.OnNewQuery)
			}
		}
		if g.ClientDelegation != "" {
			switch g.ClientDelegation {
			case "always", "auto":
				clientDelegation = g.ClientDelegation
			default:
				s.reply(p, live.NewError("invalid_request_error", "invalid_client_delegation",
					fmt.Sprintf("golive.client_delegation %q must be always or auto",
						g.ClientDelegation), eventID))
				return fmt.Errorf("live: bad client_delegation %q", g.ClientDelegation)
			}
		}
		// A pointer, so "" explicitly suppresses the server's greeting rather
		// than falling back to it.
		if g.Greeting != nil {
			greeting = *g.Greeting
		}
		if g.MinSilenceMS != 0 {
			if g.MinSilenceMS < minSessionSilenceMS || g.MinSilenceMS > maxSessionSilenceMS {
				s.reply(p, live.NewError("invalid_request_error", "invalid_min_silence_ms",
					fmt.Sprintf("golive.min_silence_ms %d must be between %d and %d",
						g.MinSilenceMS, minSessionSilenceMS, maxSessionSilenceMS), eventID))
				return fmt.Errorf("live: bad min_silence_ms %d", g.MinSilenceMS)
			}
			cfg.VAD.MinSilenceMS = g.MinSilenceMS
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

	backend, dropped := s.backendOptions(resolved)

	// Storage is best effort: a call is worth more than its recording, so a
	// disk that cannot be written to costs the recording and nothing else.
	var rec *store.Recording
	if resolved.Store {
		if s.store == nil {
			s.log.Warn("the session asked for store:true but no store_dir is configured; nothing will be kept")
			resolved.Store = false
		} else if r, err := s.store.Create(s.id, resolved.Audio.Format.Rate); err != nil {
			s.log.Warn("could not start storing the session", "err", err)
			resolved.Store = false
		} else {
			rec = r
		}
	}

	engine := s.newEngine(duplex.Options{
		Cfg:              cfg,
		SessionID:        s.id,
		ClientRate:       resolved.Audio.Format.Rate,
		Instructions:     resolved.Instructions,
		Voice:            duplex.ResolveVoice(s.cfg, voiceOf(request)),
		Language:         language,
		Delegation:       resolved.Delegation.Type,
		ClientDelegation: clientDelegation,
		Backchannel:      backchannel,
		Speculative:      speculative,
		Greeting:         greeting,
		OnNewQuery:       onNewQuery,
		History:          history,
		Backend:          backend,
	}, duplex.Deps{
		ASR:  asr,
		LLM:  llm,
		TTS:  tts,
		Emit: s.Emit,
		Log:  s.baseLog,
	})

	s.mu.Lock()
	s.engine = engine
	s.started = true
	s.config = resolved
	s.codec = codec
	s.rate = resolved.Audio.Format.Rate
	s.rec = rec
	s.mu.Unlock()

	engine.Start(ctx)

	s.log.Info("session started",
		"model", resolved.Model,
		"format", resolved.Audio.Format.Type,
		"rate", resolved.Audio.Format.Rate,
		"delegation", resolved.Delegation.Type,
		"client_delegation", clientDelegation,
		"greeting", greeting != "",
		"stored", rec != nil,
		"forked", s.seed != nil,
		"on_new_query", onNewQueryOr(onNewQuery, s.cfg.Duplex.OnNewQuery),
		"realtime", s.rt != nil,
		"call", s.CallID(),
		"asr", asrName, "llm", llmName, "tts", ttsName)

	s.Emit(live.SessionStartedEvent{
		Envelope:      live.Envelope{Type: live.ServerSessionStarted},
		ClientEventID: eventID,
		Session:       resolved,
	})
	if len(dropped) > 0 {
		// Said out loud rather than dropped quietly. A session that believes it
		// has web search and does not will be told things are being looked up
		// that are not.
		s.Emit(live.NewError("invalid_request_error", "unsupported_tool",
			"these delegation.responses.tools cannot be run by this server's backend and were ignored: "+
				strings.Join(dropped, ", ")+". Only function tools are supported.", eventID))
	}

	// Only after session.started is queued: a single writer goroutine drains
	// the outbound channel in order, so greeting audio cannot reach the client
	// before the event that tells it what format that audio is in.
	engine.Greet()
	return nil
}

// historyFromInput turns session.input into the conversation the backend is
// shown.
//
// Roles are checked here because nothing later will: whatever is in this list
// is sent to the backend on every turn, and a role it does not recognise — or a
// "tool" message with no call in front of it — fails every one of them.
func historyFromInput(input []live.InputMessage) ([]provider.Message, error) {
	history := make([]provider.Message, 0, len(input))
	for i, m := range input {
		text := strings.TrimSpace(m.Content.Text())
		if text == "" {
			continue
		}
		role := m.Role
		switch role {
		case "", provider.RoleUser:
			role = provider.RoleUser
		case provider.RoleAssistant, provider.RoleSystem:
		case "developer":
			// gpt-live-1's name for what chat completions calls system.
			role = provider.RoleSystem
		default:
			return nil, fmt.Errorf("session.input[%d].role %q must be user, assistant or developer", i, m.Role)
		}
		history = append(history, provider.Message{Role: role, Content: text})
	}
	return history, nil
}

// forkConfig builds the session a fork starts with: the source's model,
// instructions and delegation mode, with the three things a fork is allowed to
// choose for itself — whether it is stored, its Responses delegation settings,
// and the audio format of its own connection.
func forkConfig(source, request live.SessionConfig) live.SessionConfig {
	out := source
	out.ID = ""
	// The conversation is carried over as history, not replayed as input.
	out.Input = nil
	out.Store = request.Store
	// Never inherited: it described the source's connection, not this one.
	voice := voiceOf(source)
	out.Audio = &live.AudioConfig{Output: &live.OutputAudio{Voice: voice}}
	if request.Audio != nil && request.Audio.Format != nil {
		format := *request.Audio.Format
		out.Audio.Format = &format
	}
	if source.Delegation != nil {
		d := *source.Delegation
		if d.Responses != nil {
			r := *d.Responses
			d.Responses = &r
		}
		if request.Delegation != nil && request.Delegation.Responses != nil {
			if d.Responses == nil {
				d.Responses = &live.ResponsesDelegation{}
			}
			d.Responses.Merge(request.Delegation.Responses)
		}
		out.Delegation = &d
	}
	out.Golive = forkGolive(source.Golive, request.Golive)
	return out
}

// forkGolive carries the source session's golive settings into a fork, with
// whatever the fork's own session.start says taking precedence.
//
// The greeting is the exception. It is something said once, at the start of a
// conversation, and a fork is the middle of one — so it is never inherited,
// only set.
func forkGolive(source, request *live.GoliveConfig) *live.GoliveConfig {
	if source == nil && request == nil {
		return nil
	}
	var out live.GoliveConfig
	if source != nil {
		out = *source
		out.Greeting = nil
	}
	if request == nil {
		return &out
	}
	if request.Backchannel != nil {
		out.Backchannel = request.Backchannel
	}
	if request.Speculative != nil {
		out.Speculative = request.Speculative
	}
	if request.ASR != "" {
		out.ASR = request.ASR
	}
	if request.LLM != "" {
		out.LLM = request.LLM
	}
	if request.TTS != "" {
		out.TTS = request.TTS
	}
	if request.Language != "" {
		out.Language = request.Language
	}
	if request.OnNewQuery != "" {
		out.OnNewQuery = request.OnNewQuery
	}
	if request.UserBackchannelPhrases != nil {
		out.UserBackchannelPhrases = request.UserBackchannelPhrases
	}
	if request.ClientDelegation != "" {
		out.ClientDelegation = request.ClientDelegation
	}
	out.Greeting = request.Greeting
	return &out
}

// backendOptions translates the session's delegation.responses into what the
// configured provider takes, and names any tools it could not carry over.
func (s *Session) backendOptions(c live.SessionConfig) (duplex.BackendOptions, []string) {
	var b duplex.BackendOptions
	if c.Delegation == nil || c.Delegation.Responses == nil {
		return b, nil
	}
	r := c.Delegation.Responses
	var dropped []string
	b.Tools, dropped = live.ChatTools(r.Tools)
	b.ToolChoice = live.ChatToolChoice(r.ToolChoice)
	b.ParallelToolCalls = r.ParallelToolCalls
	b.Instructions = r.Instructions
	if r.MaxOutputTokens != nil {
		b.MaxOutputTokens = *r.MaxOutputTokens
	}
	if r.Model != "" {
		if mapped, ok := s.cfg.BackendModels[r.Model]; ok {
			b.Model = mapped
		} else if s.cfg.AllowBackendModelPassthrough {
			b.Model = r.Model
		}
		// Otherwise the profile's backend_model stands. The name a
		// gpt-live-1 client sends is an OpenAI model, and this server's
		// provider has never heard of it.
	}
	return b, dropped
}

// resolve fills a client's session object with server defaults and rejects the
// combinations gpt-live-1 rejects.
func (s *Session) resolve(in live.SessionConfig) (live.SessionConfig, audio.Codec, error) {
	out := in
	out.ID = s.id
	if out.Model == "" {
		out.Model = s.cfg.Model
	}
	if out.Instructions == "" {
		out.Instructions = s.cfg.Instructions
	}

	// Everything that will be written to is copied first: the session object
	// arrives as pointers, and on a fork they point into a stored record.
	audioCfg := live.AudioConfig{}
	if in.Audio != nil {
		audioCfg = *in.Audio
	}
	format := live.AudioFormat{Type: audio.MIMEPCM, Rate: s.cfg.ClientRate}
	if audioCfg.Format != nil {
		format = *audioCfg.Format
	}
	if format.Type == "" {
		format.Type = audio.MIMEPCM
	}
	codec, err := audio.CodecForMIME(format.Type)
	if err != nil {
		return out, codec, fmt.Errorf("audio.format.type %q is not supported; use %s, %s or %s",
			format.Type, audio.MIMEPCM, audio.MIMEPCMU, audio.MIMEPCMA)
	}
	if codec == audio.CodecPCM16 {
		if format.Rate == 0 {
			format.Rate = s.cfg.ClientRate
		}
		switch format.Rate {
		case 8000, 16000, 24000, 48000:
		default:
			return out, codec, fmt.Errorf("audio.format.rate %d is not supported; use 8000, 16000, 24000 or 48000",
				format.Rate)
		}
	} else {
		// G.711 is 8 kHz by definition. Saying so is allowed; saying anything
		// else is a mistake worth catching before the call sounds wrong.
		if format.Rate != 0 && format.Rate != audio.RateTelephony {
			return out, codec, fmt.Errorf("audio.format.rate %d is not supported with %s, which is always %d Hz",
				format.Rate, format.Type, audio.RateTelephony)
		}
		format.Rate = audio.RateTelephony
	}
	audioCfg.Format = &format

	output := live.OutputAudio{}
	if audioCfg.Output != nil {
		output = *audioCfg.Output
	}
	if output.Voice == "" {
		output.Voice = s.cfg.Voice
	}
	audioCfg.Output = &output
	out.Audio = &audioCfg

	delegation := live.DelegationConfig{}
	if in.Delegation != nil {
		delegation = *in.Delegation
	}
	if delegation.Type == "" {
		mode := s.cfg.Duplex.DelegationMode
		if mode == "auto" {
			mode = live.DelegationResponses
		}
		delegation.Type = mode
	}
	switch delegation.Type {
	case live.DelegationClient, live.DelegationResponses:
	default:
		return out, codec, fmt.Errorf("delegation.type %q must be %q or %q",
			delegation.Type, live.DelegationClient, live.DelegationResponses)
	}
	if r := delegation.Responses; r != nil {
		if r.MaxOutputTokens != nil && *r.MaxOutputTokens < 16 {
			return out, codec, fmt.Errorf("delegation.responses.max_output_tokens must be at least 16, got %d",
				*r.MaxOutputTokens)
		}
	}
	out.Delegation = &delegation

	if len(out.Input) > 128 {
		return out, codec, fmt.Errorf("session.input accepts at most 128 messages, got %d", len(out.Input))
	}
	return out, codec, nil
}

func (s *Session) onSessionUpdate(p *peer, data []byte, eventID string) error {
	var ev live.SessionUpdateEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		s.reply(p, live.NewError("invalid_request_error", "malformed_event", err.Error(), eventID))
		return err
	}

	s.mu.Lock()
	current := s.config
	s.mu.Unlock()

	// gpt-live-1 fixes model, instructions, voice, audio format and delegation
	// type at startup: they are baked into connections and timestamps that
	// already went out, so changing one mid-session would invalidate the
	// transcript. Instructions are extended with session.instructions.append,
	// not replaced.
	immutable := func(field, hint string) error {
		s.reply(p, live.NewError("invalid_request_error", "immutable_field_update",
			field+" cannot change after startup; "+hint, eventID))
		return fmt.Errorf("live: %s is immutable", field)
	}
	if ev.Session.Model != "" && ev.Session.Model != current.Model {
		return immutable("session.model", "start a new session")
	}
	if ev.Session.Instructions != "" && ev.Session.Instructions != current.Instructions {
		return immutable("session.instructions", "use session.instructions.append to add to them")
	}
	if ev.Session.Audio != nil && ev.Session.Audio.Format != nil {
		return immutable("session.audio.format", "start a new session")
	}
	if ev.Session.Audio != nil && ev.Session.Audio.Output != nil &&
		ev.Session.Audio.Output.Voice != "" && ev.Session.Audio.Output.Voice != voiceOf(current) {
		return immutable("session.audio.output.voice", "start a new session")
	}
	if ev.Session.Delegation != nil && ev.Session.Delegation.Type != "" &&
		ev.Session.Delegation.Type != current.Delegation.Type {
		return immutable("delegation.type", "start a new session")
	}

	if ev.Session.Delegation != nil && ev.Session.Delegation.Responses != nil {
		if current.Delegation.Type != live.DelegationResponses {
			return immutable("delegation.responses",
				"this session uses client delegation, which has no Responses settings")
		}
		if n := ev.Session.Delegation.Responses.MaxOutputTokens; n != nil && *n < 16 {
			s.reply(p, live.NewError("invalid_request_error", "invalid_session",
				fmt.Sprintf("delegation.responses.max_output_tokens must be at least 16, got %d", *n), eventID))
			return fmt.Errorf("live: max_output_tokens too small")
		}

		s.mu.Lock()
		// Copied before it is changed: the old object has already been sent to
		// clients, and to a stored session's record.
		delegation := *s.config.Delegation
		merged := live.ResponsesDelegation{}
		if delegation.Responses != nil {
			merged = *delegation.Responses
		}
		merged.Merge(ev.Session.Delegation.Responses)
		delegation.Responses = &merged
		s.config.Delegation = &delegation
		updated := s.config
		s.mu.Unlock()

		backend, dropped := s.backendOptions(updated)
		s.withEngine(func(e *duplex.Engine) { e.SetBackend(backend) })

		s.Emit(live.SessionStartedEvent{
			Envelope:      live.Envelope{Type: live.ServerSessionUpdated},
			ClientEventID: eventID,
			Session:       updated,
		})
		if len(dropped) > 0 {
			s.Emit(live.NewError("invalid_request_error", "unsupported_tool",
				"these delegation.responses.tools cannot be run by this server's backend and were ignored: "+
					strings.Join(dropped, ", ")+". Only function tools are supported.", eventID))
		}
		return nil
	}

	s.Emit(live.SessionStartedEvent{
		Envelope:      live.Envelope{Type: live.ServerSessionUpdated},
		ClientEventID: eventID,
		Session:       current,
	})
	return nil
}

func (s *Session) onInputAudio(p *peer, data []byte, eventID string) error {
	var ev live.InputAudioAppendEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		s.reply(p, live.NewError("invalid_request_error", "malformed_event", err.Error(), eventID))
		return err
	}
	if ev.Audio == "" {
		return nil
	}
	wire, err := base64.StdEncoding.DecodeString(ev.Audio)
	if err != nil {
		s.reply(p, live.NewError("invalid_request_error", "bad_audio",
			"audio must be base64-encoded audio in the session format", eventID))
		return err
	}
	if !p.rate.allowAudio(len(wire), s.cfg.Duplex.MaxAudioKbps) {
		s.floodClose(p, "audio")
		return fmt.Errorf("live: rate limited")
	}
	s.pushAudio(p, wire, eventID)
	return nil
}

func (s *Session) onAppend(p *peer, data []byte, eventID string, apply func(*duplex.Engine, live.AppendEvent)) error {
	var ev live.AppendEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		s.reply(p, live.NewError("invalid_request_error", "malformed_event", err.Error(), eventID))
		return err
	}
	if strings.TrimSpace(ev.Content) == "" {
		s.reply(p, live.NewError("invalid_request_error", "empty_content", "content must not be empty", eventID))
		return fmt.Errorf("live: empty append content")
	}
	// gpt-live-1 caps an instruction append at 500 tokens. Enforcing a bound
	// matters more than matching it exactly: these appends go straight into a
	// context window the conversational layer cannot afford to fill, and an
	// application that pipes a whole document through one silently degrades
	// every later turn instead of failing.
	if max := s.cfg.Duplex.MaxAppendChars; max > 0 && len([]rune(ev.Content)) > max {
		s.reply(p, live.NewError("invalid_request_error", "content_too_long",
			fmt.Sprintf("content is %d characters; the limit is %d. Summarize before appending: "+
				"this goes into the conversational context window, not the backend's.",
				len([]rune(ev.Content)), max), eventID))
		return fmt.Errorf("live: append content too long")
	}
	s.withEngine(func(e *duplex.Engine) { apply(e, ev) })
	return nil
}

// --- sidebands ---

// MaxSidebands is how many sidebands one session accepts. Each is a full copy
// of the session's output, audio included; the uses for one are an application
// server and perhaps a monitor, not a crowd.
const MaxSidebands = 4

// Sidebands reports how many sidebands are attached.
func (s *Session) Sidebands() int { return int(s.sideCount.Load()) }

// Attach serves a sideband connection until it closes or the session ends.
//
// A sideband is how a server joins a call it is not carrying: the audio stays
// on the primary connection, and this one receives every event — transcripts,
// delegations, function calls, the audio itself — and may send any command
// except audio. It is what lets a telephony bridge own the media while an
// application server owns the tools and the guardrails.
func (s *Session) Attach(ctx context.Context, conn *websocket.Conn) {
	p := newPeer(conn, true)

	s.sideMu.Lock()
	select {
	case <-s.stopRead:
		// The session ended between being found and being attached to.
		s.sideMu.Unlock()
		_ = conn.Close()
		return
	default:
	}
	if len(s.sides) >= MaxSidebands {
		// Checked before the upgrade as well; this is the one that holds when
		// several arrive at once.
		s.sideMu.Unlock()
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "too many sidebands"),
			time.Now().Add(time.Second))
		_ = conn.Close()
		return
	}
	s.sides[p] = struct{}{}
	s.sideCount.Store(int32(len(s.sides)))
	s.sideMu.Unlock()
	s.log.Info("sideband attached", "sidebands", s.sideCount.Load())

	done := make(chan struct{})
	go func() { defer close(done); s.writeLoop(p) }()
	go func() {
		select {
		case <-s.stopRead:
		case <-ctx.Done():
		case <-done:
		}
		_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	}()

	s.readLoop(ctx, p)

	select {
	case <-s.stopRead:
		// The session is ending: stay long enough to be told so. The writer
		// drains session.closed and exits when Serve releases it.
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		s.removeSide(p)
		reason := live.CloseConnectionLost
		if m := s.closeMsg.Load(); m != nil {
			reason = *m
		}
		s.writeClose(p, reason)
	default:
		// The sideband left by itself. The call goes on.
		s.dropSide(p)
		<-done
	}
	_ = conn.Close()
	s.log.Info("sideband detached", "sidebands", s.sideCount.Load())
}

// removeSide takes a sideband out of the fan-out.
func (s *Session) removeSide(p *peer) {
	s.sideMu.Lock()
	delete(s.sides, p)
	s.sideCount.Store(int32(len(s.sides)))
	s.sideMu.Unlock()
}

// dropSide detaches a sideband and closes its socket, which is what stops its
// writer. Safe to call more than once and from any goroutine.
func (s *Session) dropSide(p *peer) {
	if p.gone.Swap(true) {
		return
	}
	close(p.stop)
	// Not under sideMu: enqueue calls this while holding the read lock.
	go func() {
		s.removeSide(p)
		_ = p.conn.Close()
	}()
}

// snapshot returns the stored-session record as it stands, for forking a
// session that is still live. ok is false if the session is not being stored.
func (s *Session) snapshot() (store.Meta, bool) {
	s.mu.Lock()
	engine, rec, resolved := s.engine, s.rec, s.config
	s.mu.Unlock()
	if engine == nil || rec == nil {
		return store.Meta{}, false
	}
	record := resolved
	record.Instructions = engine.Instructions()
	return store.Meta{
		ID:        s.id,
		CreatedAt: s.createdAt,
		Rate:      resolved.Audio.Format.Rate,
		Session:   record,
		History:   engine.History(),
	}, true
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
