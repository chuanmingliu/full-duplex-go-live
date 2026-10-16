package server

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/duplex"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/simulator"
	"github.com/gorilla/websocket"
)

// SimulatorReceiver is a separate, receiving-only surface. The ordinary Server
// never enables its capabilities. SetController must run before serving HTTP.
type SimulatorReceiver struct {
	cfg        config.Config
	log        *slog.Logger
	token      string
	controller *simulator.RunController
	mu         sync.Mutex
	sessions   map[string]*Session
	Factory    EngineFactory
}

func NewSimulatorReceiver(cfg config.Config, log *slog.Logger, mediaToken string) (*SimulatorReceiver, error) {
	if len(mediaToken) < 24 {
		return nil, errors.New("media token must contain at least 24 characters")
	}
	if log == nil {
		log = slog.Default()
	}
	cfg.Greeting = ""
	cfg.StoreDir = ""
	cfg.Realtime.StoreCalls = false
	cfg.Duplex.Backchannel = false
	cfg.Duplex.Speculative = false
	cfg.Duplex.HoldingFiller = false
	cfg.Realtime.HonorTurnDetection = false
	return &SimulatorReceiver{cfg: cfg, log: log, token: mediaToken, sessions: map[string]*Session{}}, nil
}
func (r *SimulatorReceiver) SetController(c *simulator.RunController) { r.controller = c }
func (r *SimulatorReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/v1/realtime" {
		http.NotFound(w, req)
		return
	}
	if req.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.URL.Query().Get("token")), []byte(r.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := req.URL.Query()
	for key, values := range q {
		if (key != "run" && key != "call" && key != "token") || len(values) != 1 {
			http.Error(w, "invalid media parameters", http.StatusBadRequest)
			return
		}
	}
	if r.controller == nil || q.Get("run") == "" || q.Get("call") == "" {
		http.Error(w, "binding required", http.StatusConflict)
		return
	}
	if !websocket.IsWebSocketUpgrade(req) {
		w.WriteHeader(http.StatusUpgradeRequired)
		return
	}
	// Serialize admission through association so two sockets cannot claim a call.
	r.mu.Lock()
	snap, err := r.controller.Get(req.Context(), q.Get("run"))
	if err != nil || snap.Binding.CallID != q.Get("call") || snap.Binding.SessionID != "" || snap.Phase != simulator.Answering || len(r.sessions) != 0 {
		r.mu.Unlock()
		http.Error(w, "call unavailable", http.StatusConflict)
		return
	}
	binding := snap.Binding

	upgrader := websocket.Upgrader{HandshakeTimeout: 5 * time.Second, CheckOrigin: func(req *http.Request) bool { return req.Header.Get("Origin") == "" }}
	conn, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		r.mu.Unlock()
		return
	}
	binding.SessionID = "sess_" + randomID()
	if err = r.controller.Associate(req.Context(), binding); err != nil {
		r.mu.Unlock()
		conn.Close()
		return
	}
	scenario, err := r.controller.ScenarioForBinding(req.Context(), binding)
	if err != nil {
		r.mu.Unlock()
		conn.Close()
		_ = r.controller.Handle(context.Background(), simulator.Event{Kind: "failed", Binding: binding, AtMS: r.controller.RelativeMS(), Reason: "scenario binding failed"})
		return
	}
	ctx, cancel := context.WithCancel(req.Context())
	sim := &simulatorSession{receiver: r, binding: binding, scenario: scenario, ctx: ctx, cancel: cancel}
	sess := NewSession(SessionOptions{Conn: conn, Cfg: r.cfg, Log: r.log, ID: binding.SessionID, Factory: r.Factory, Realtime: &RealtimeOptions{Rate: 16000, Dialect: "beta", Hold: true}})
	sess.sim = sim
	r.sessions[binding.SessionID] = sess
	r.mu.Unlock()
	defer func() {
		sim.stop()
		r.mu.Lock()
		delete(r.sessions, binding.SessionID)
		r.mu.Unlock()
		// BYE can close media before its control hangup event is delivered.
		// Output is already closed; allow only a bounded signaling grace. A
		// missing confirmation still fails and actively hangs up this exact leg.
		time.AfterFunc(500*time.Millisecond, func() {
			_ = r.controller.Handle(context.Background(), simulator.Event{Kind: "failed", Binding: binding, AtMS: r.controller.RelativeMS(), Reason: "media connection closed without hangup confirmation"})
		})
	}()
	timer := time.AfterFunc(20*time.Second, func() {
		sess.mu.Lock()
		started := sess.started
		sess.mu.Unlock()
		if !started {
			sim.fail("session setup deadline")
			sess.finish()
		}
	})
	defer timer.Stop()
	sess.queueRealtime(map[string]any{"type": "session.created", "session": map[string]any{"id": binding.SessionID, "object": "realtime.session", "input_audio_format": "pcm16", "output_audio_format": "pcm16"}})
	sess.Serve(ctx)
}
func (r *SimulatorReceiver) session(b simulator.Binding) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[b.SessionID]
	if s == nil || s.sim.binding != b {
		return nil, simulator.ErrNotFound
	}
	return s, nil
}
func (r *SimulatorReceiver) Open(ctx context.Context, b simulator.Binding, greeting string) error {
	s, err := r.session(b)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	s.sim.outputMu.Lock()
	defer s.sim.outputMu.Unlock()
	if s.sim.closed {
		return simulator.ErrNotReady
	}
	s.mu.Lock()
	engine := s.engine
	s.mu.Unlock()
	if engine == nil || !s.sim.input.open(s.sim.ctx, func() bool { return s.Open(greeting) }, engine.PushAudio) {
		return simulator.ErrNotReady
	}
	return nil
}
func (r *SimulatorReceiver) Speak(ctx context.Context, b simulator.Binding, id, text string, hold time.Duration) error {
	s, err := r.session(b)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	engine := s.engine
	s.mu.Unlock()
	if engine == nil {
		return simulator.ErrNotReady
	}
	now := time.Now()
	// The controller gates this action on observed inbound audio. Engine VAD
	// may lag that boundary, so it must not veto the scheduled interruption.
	return engine.ScheduleSimulatorSpeech(s.sim.ctx, duplex.SimulatorSpeechRequest{ActionID: id, Text: text, DueAt: now, ExpiresAt: now.Add(hold), MaxHold: hold})
}
func (r *SimulatorReceiver) Close(ctx context.Context, b simulator.Binding) error {
	s, err := r.session(b)
	if err != nil {
		return nil
	}
	s.sim.stop()
	s.finish()
	return nil
}

type simulatorSession struct {
	receiver  *SimulatorReceiver
	binding   simulator.Binding
	scenario  simulator.Scenario
	ctx       context.Context
	cancel    context.CancelFunc
	fatalOnce sync.Once
	observeMu sync.Mutex
	outputMu  sync.Mutex
	closed    bool
	input     simulatorInputLine
}

func (s *simulatorSession) stop() {
	// Stop and join ingress before a socket writer or engine shutdown can
	// block; no synthetic or real input can be queued after this returns.
	s.input.stop()
	s.outputMu.Lock()
	s.closed = true
	s.cancel()
	s.outputMu.Unlock()
}
func (s *simulatorSession) fail(reason string) {
	_ = s.receiver.controller.Handle(context.Background(), simulator.Event{Kind: "failed", Binding: s.binding, AtMS: s.receiver.controller.RelativeMS(), Reason: reason})
}

// observe serializes the receiver clock sample with ingestion across all live
// producers. Sampling before this lock would let a later boundary overtake an
// earlier timestamp and be mistaken for malformed evidence by the controller.
// Socket I/O and audio classification happen outside this critical section.
func (s *simulatorSession) observe(v simulator.Observation) {
	s.observeMu.Lock()
	defer s.observeMu.Unlock()
	v.AtMS = s.receiver.controller.RelativeMS()
	_ = s.receiver.controller.Observe(s.ctx, s.binding, v)
}

// audio records the receive or successful socket-write boundary using one
// receiver-local clock. A 200 ms quiet gap closes a speech span; raw PCM is
// never retained. These are application timings, not remote handset playout.
func (s *simulatorSession) audio(out bool, pcm []byte) {
	if len(pcm) < 2 {
		return
	}
	var sum float64
	for i := 0; i+1 < len(pcm); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[i:])))
		sum += v * v
	}
	loud := sum/float64(len(pcm)/2) >= 200*200
	if !loud {
		return
	}
	kind := "assistant_audio"
	if out {
		kind = "customer_audio"
	}
	s.observe(simulator.Observation{Kind: kind, NonSilent: true})
}
func (s *Session) simulatorWrite(p *peer, data []byte) error {
	sim := s.sim
	sim.outputMu.Lock()
	defer sim.outputMu.Unlock()
	if sim.closed {
		return nil
	}
	_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := p.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return err
	}
	var ev struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	}
	if json.Unmarshal(data, &ev) == nil && (ev.Type == "response.audio.delta" || ev.Type == "response.output_audio.delta") {
		if pcm, err := base64.StdEncoding.DecodeString(ev.Delta); err == nil {
			sim.audio(true, pcm)
		}
	}
	return nil
}
func (s *Session) startSimulator(ctx context.Context, p *peer, eventID string) error {
	sim := s.sim
	// The gateway transports audio only. Persona, facts, language and speech
	// settings belong to the prepared scenario and cannot be overwritten here.
	prompt := sim.scenario.Prompt()
	quiet := ""
	off := false
	request := live.SessionStartEvent{Envelope: live.Envelope{Type: live.ClientSessionStart}, Session: live.SessionConfig{
		Instructions: prompt,
		Audio:        &live.AudioConfig{Format: &live.AudioFormat{Type: "audio/pcm", Rate: 16000}},
		Golive:       &live.GoliveConfig{Greeting: &quiet, Language: sim.scenario.Language, Backchannel: &off, Speculative: &off},
	}}
	data, _ := json.Marshal(request)
	if err := s.onSessionStart(ctx, p, data, eventID); err != nil {
		sim.fail("voice setup failed")
		s.finish()
		return err
	}
	s.rt.held.Store(true)
	s.withEngine(func(e *duplex.Engine) { e.SetMuted(true, "") })
	// Preparation is cancellable and happens before the greeting gate opens.
	prepCtx, cancel := context.WithTimeout(sim.ctx, 20*time.Second)
	defer cancel()
	var prepErr error
	s.withEngine(func(e *duplex.Engine) {
		for _, a := range sim.scenario.Actions {
			if a.Kind == "speak" {
				if prepErr = e.PrepareSimulatorSpeech(prepCtx, a.ID, a.Text); prepErr != nil {
					return
				}
			}
		}
	})
	if prepErr != nil {
		sim.fail("scripted speech preparation failed")
		s.finish()
		return prepErr
	}
	if err := sim.receiver.controller.Handle(sim.ctx, simulator.Event{Kind: "voice_ready", Binding: sim.binding, AtMS: sim.receiver.controller.RelativeMS()}); err != nil {
		s.finish()
		return err
	}
	return nil
}

// observeEvent keeps script outcomes and optional transcripts private to the
// controller. The audio observer itself runs only at transport boundaries.
func (s *simulatorSession) observeEvent(event any) {
	switch ev := event.(type) {
	case live.ErrorEvent:
		if ev.Error.Code == "simulator_overflow" || ev.Error.Code == "asr_error" {
			s.fatalOnce.Do(func() { go s.fail("voice engine failed") })
		}
	case duplex.SimulatorSpeechEvent:
		status := ev.Status
		if status == "expired" {
			status = "timed_out"
		}
		_ = s.receiver.controller.ActionObservation(s.ctx, s.binding, simulator.ActionOutcome{ID: ev.ActionID, Result: status, AtMS: s.receiver.controller.RelativeMS(), Reason: ev.Reason})
	case live.TranscriptDelta:
		if s.scenario.RetainTranscript && ev.Delta != "" {
			role := "customer"
			if ev.Type == live.ServerInputTranscriptDelta {
				role = "assistant"
			}
			s.observe(simulator.Observation{Kind: "transcript", Role: role, Text: ev.Delta})
		}
	}
}
