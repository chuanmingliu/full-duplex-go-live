package simulator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("run not found")
	ErrConflict = errors.New("run conflict")
	ErrNotReady = errors.New("run not ready")
)

type Telephony interface {
	Accept(context.Context, string) error
	Reject(context.Context, string, string) error
	Hangup(context.Context, string) error
}
type Voice interface {
	Open(context.Context, Binding, string) error
	Speak(context.Context, Binding, string, string, time.Duration) error
	Close(context.Context, Binding) error
}
type VoiceSilencer interface {
	Silence(context.Context, Binding, string, time.Duration) error
}
type Timer interface{ Stop() bool }
type Clock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) Timer
}
type realClock struct{}

func (realClock) Now() time.Time                            { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

type Options struct {
	Telephony                                                                                               Telephony
	Voice                                                                                                   Voice
	Token, Destination, GatewayIdentity, DataDir, SourceCommit, ProviderModel, VoiceID, DialTargetReference string
	Clock                                                                                                   Clock
	ReservationTTL, SetupTimeout                                                                            time.Duration
}
type Snapshot struct {
	RunID                string    `json:"run_id"`
	Phase                Phase     `json:"phase"`
	Reason               string    `json:"reason,omitempty"`
	Binding              Binding   `json:"binding"`
	ReservationExpiresAt time.Time `json:"reservation_expires_at"`
	DialTargetReference  string    `json:"dial_target_reference"`
}
type Controller interface {
	Prepare(context.Context, Scenario) (Snapshot, error)
	Handle(context.Context, Event) error
	Get(context.Context, string) (Snapshot, error)
	Stop(context.Context, string) error
}
type IncomingCall struct {
	RunID           string `json:"run_id"`
	CallID          string `json:"call_id"`
	Destination     string `json:"destination"`
	GatewayIdentity string `json:"gateway_identity"`
}
type RecordedEvent struct {
	Sequence uint64 `json:"sequence"`
	Kind     string `json:"kind"`
	AtMS     int64  `json:"at_ms"`
	Reason   string `json:"reason,omitempty"`
}
type EventPage struct {
	Events    []RecordedEvent `json:"events"`
	Next      uint64          `json:"next"`
	Truncated bool            `json:"truncated"`
}
type run struct {
	state                                         State
	scenario                                      Scenario
	scenarioHash, promptHash, reason              string
	idempotencyKeyHash                            string
	provenance                                    reportProvenance
	createdAt, expiresAt, connectedAt, terminalAt time.Time
	ctx                                           context.Context
	cancel                                        context.CancelFunc
	timers                                        []Timer
	idle                                          Timer
	customerIdle                                  Timer
	remoteIdle                                    Timer
	remoteAudioGeneration                         uint64
	idleGeneration                                uint64
	customerAudioGeneration                       uint64
	voiceClosed, hangupSent                       bool
	events                                        []RecordedEvent
	seq                                           uint64
	eventsTruncated                               bool
	observer                                      *observer
	actions                                       []ActionOutcome
	actionScheduled                               map[string]bool
	occurrences                                   map[string]int
	remoteTurns                                   int
	failures                                      []string
}
type idempotency struct{ hash, id string }
type RunController struct {
	mu        sync.Mutex
	opts      Options
	clock     Clock
	epoch     time.Time
	runs      map[string]*run
	keys      map[string]idempotency
	seenCalls map[string]string
	current   string
	closed    bool
}

func NewController(opts Options) (*RunController, error) {
	if opts.Telephony == nil || opts.Voice == nil || opts.Token == "" || opts.Destination == "" || opts.GatewayIdentity == "" {
		return nil, errors.New("telephony, voice, token, destination and gateway identity are required")
	}
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}
	if opts.ReservationTTL == 0 {
		opts.ReservationTTL = 120 * time.Second
	}
	if opts.SetupTimeout == 0 {
		opts.SetupTimeout = 5 * time.Second
	}
	if opts.ReservationTTL < time.Millisecond || opts.ReservationTTL > 300*time.Second || opts.SetupTimeout < time.Millisecond || opts.SetupTimeout > 30*time.Second {
		return nil, errors.New("invalid controller deadlines")
	}
	if opts.DialTargetReference == "" {
		opts.DialTargetReference = "dedicated-receiver"
	}
	c := &RunController{opts: opts, clock: opts.Clock, epoch: opts.Clock.Now(), runs: map[string]*run{}, keys: map[string]idempotency{}, seenCalls: map[string]string{}}
	if opts.DataDir != "" {
		if err := c.restore(); err != nil {
			return nil, err
		}
	}
	return c, nil
}
func (c *RunController) RelativeMS() int64 { return c.clock.Now().Sub(c.epoch).Milliseconds() }
func cloneScenario(s Scenario) Scenario {
	raw, _ := json.Marshal(s)
	var copy Scenario
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func hashJSON(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (c *RunController) Prepare(ctx context.Context, s Scenario) (Snapshot, error) {
	return c.prepare(ctx, "", s)
}
func (c *RunController) PrepareIdempotent(ctx context.Context, key string, s Scenario) (Snapshot, error) {
	if !safeID(key) {
		return Snapshot{}, fmt.Errorf("%w: safe idempotency key required", ErrInvalidScenario)
	}
	return c.prepare(ctx, key, s)
}
func (c *RunController) prepare(ctx context.Context, key string, s Scenario) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if err := s.Validate(); err != nil {
		return Snapshot{}, err
	}
	s = cloneScenario(s)
	hash := hashJSON(s)
	for _, a := range s.Actions {
		if a.Kind == "silence" {
			if _, ok := c.opts.Voice.(VoiceSilencer); !ok {
				return Snapshot{}, fmt.Errorf("%w: silence voice control unsupported", ErrInvalidScenario)
			}
		}
	}
	if key != "" {
		key = hashJSON(key)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if key != "" {
		if prev, ok := c.keys[key]; ok {
			if prev.hash != hash {
				return Snapshot{}, ErrConflict
			}
			return c.snapshot(c.runs[prev.id]), nil
		}
	}
	if c.closed {
		return Snapshot{}, ErrNotReady
	}
	if c.current != "" {
		return Snapshot{}, ErrConflict
	}
	if len(c.runs) >= 128 || len(c.keys) >= 128 || len(c.seenCalls) >= 4096 {
		return Snapshot{}, errors.New("bounded run capacity reached")
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return Snapshot{}, err
	}
	id := "sim-" + hex.EncodeToString(random)
	now := c.clock.Now()
	runctx, cancel := context.WithCancel(context.Background())
	r := &run{state: State{Phase: Prepared, Binding: Binding{RunID: id}}, scenario: s, scenarioHash: hash, idempotencyKeyHash: key, provenance: reportProvenance{SourceCommit: redact(c.opts.SourceCommit), ProviderModel: redact(c.opts.ProviderModel), VoiceID: redact(c.opts.VoiceID)}, promptHash: hashJSON(s.Prompt()), createdAt: now, expiresAt: now.Add(c.opts.ReservationTTL), ctx: runctx, cancel: cancel, observer: newObserver(), actionScheduled: map[string]bool{}, occurrences: map[string]int{}}
	r.observer.retainTranscript = s.RetainTranscript
	r.state, _, _ = Advance(r.state, Event{Kind: "reserve", Binding: r.state.Binding})
	c.runs[id] = r
	c.current = id
	if key != "" {
		c.keys[key] = idempotency{hash: hash, id: id}
	}
	c.record(r, "reserve", "")
	if err := c.persistSnapshot(r); err != nil {
		cancel()
		delete(c.runs, id)
		delete(c.keys, key)
		c.current = ""
		return Snapshot{}, err
	}
	c.schedule(r, c.opts.ReservationTTL, func() {
		_ = c.Handle(context.Background(), Event{Kind: "expired", Binding: Binding{RunID: id}, AtMS: c.RelativeMS()})
	})
	return c.snapshot(r), nil
}
func (c *RunController) snapshot(r *run) Snapshot {
	return Snapshot{RunID: r.state.Binding.RunID, Phase: r.state.Phase, Reason: redact(r.reason), Binding: r.state.Binding, ReservationExpiresAt: r.expiresAt, DialTargetReference: redact(c.opts.DialTargetReference)}
}
func (c *RunController) Get(ctx context.Context, id string) (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.runs[id]
	if !ok {
		return Snapshot{}, ErrNotFound
	}
	return c.snapshot(r), ctx.Err()
}
func (c *RunController) ScenarioForBinding(ctx context.Context, b Binding) (Scenario, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.runs[b.RunID]
	if !ok {
		return Scenario{}, ErrNotFound
	}
	if b.SessionID == "" || !exactBinding(r.state.Binding, b) || (r.state.Phase != Answering && r.state.Phase != Connected) {
		return Scenario{}, ErrInvalidTransition
	}
	return cloneScenario(r.scenario), ctx.Err()
}
func exactBinding(expected, b Binding) bool {
	return b.RunID == expected.RunID && b.CallID != "" && b.CallID == expected.CallID && b.SessionID == expected.SessionID
}
func (c *RunController) Associate(ctx context.Context, b Binding) error {
	return c.Handle(ctx, Event{Kind: "associate", Binding: b, AtMS: c.RelativeMS()})
}
func (c *RunController) Incoming(ctx context.Context, in IncomingCall) error {
	if !safeID(in.CallID) {
		return ErrInvalidTransition
	}
	c.mu.Lock()
	r, ok := c.runs[in.RunID]
	reject := ""
	if !ok {
		reject = "unknown reservation"
	} else if in.Destination != c.opts.Destination || in.GatewayIdentity != c.opts.GatewayIdentity {
		reject = "gateway or destination mismatch"
	} else if previous, seen := c.seenCalls[in.CallID]; seen {
		if previous == in.RunID && r.state.Binding.CallID == in.CallID && !r.state.Phase.Terminal() && r.state.Phase != Ending {
			c.mu.Unlock()
			return nil
		}
		reject = "stale call"
	} else if c.current != in.RunID || r.state.Phase != Waiting || !c.clock.Now().Before(r.expiresAt) {
		reject = "reservation unavailable"
	}
	if reject != "" {
		c.mu.Unlock()
		deadline, cancel := context.WithTimeout(ctx, c.opts.SetupTimeout)
		defer cancel()
		_ = c.opts.Telephony.Reject(deadline, in.CallID, reject)
		return ErrInvalidTransition
	}
	b := Binding{RunID: in.RunID, CallID: in.CallID}
	next, _, err := Advance(r.state, Event{Kind: "incoming", Binding: b, AtMS: c.RelativeMS()})
	if err != nil {
		c.mu.Unlock()
		return err
	}
	r.state = next
	c.seenCalls[in.CallID] = in.RunID
	c.record(r, "incoming", "")
	c.schedule(r, time.Duration(r.scenario.AnswerDelayMS)*time.Millisecond, func() {
		_ = c.Handle(context.Background(), Event{Kind: "answer_due", Binding: b, AtMS: c.RelativeMS()})
	})
	err = c.persistSnapshot(r)
	c.mu.Unlock()
	return err
}
func (c *RunController) Stop(ctx context.Context, id string) error {
	c.mu.Lock()
	r, ok := c.runs[id]
	if !ok {
		c.mu.Unlock()
		return ErrNotFound
	}
	b := r.state.Binding
	c.mu.Unlock()
	return c.Handle(ctx, Event{Kind: "stop", Binding: b, AtMS: c.RelativeMS(), Reason: "stopped"})
}
func (c *RunController) Handle(ctx context.Context, e Event) error {
	c.mu.Lock()
	r, ok := c.runs[e.Binding.RunID]
	if !ok {
		c.mu.Unlock()
		return ErrNotFound
	}
	before := r.state
	next, effects, err := Advance(before, e)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	if next == before && len(effects) == 0 {
		c.mu.Unlock()
		return ctx.Err()
	}
	r.state = next
	if next != before || len(effects) > 0 {
		c.record(r, e.Kind, e.Reason)
	}
	if e.Reason != "" {
		r.reason = redact(e.Reason)
	}
	if e.Kind == "answered" && next.AnswerSeen {
		r.observer.answered = 1
	}
	if e.Kind == "ended" && next.Phase == Completed {
		r.observer.ended = 1
	}
	if (next.Phase == Ending || next.Phase.Terminal()) && next != before {
		r.cancel()
		c.cancelTimers(r)
	}
	if before.Phase != Answering && next.Phase == Answering {
		b := next.Binding
		c.schedule(r, c.opts.SetupTimeout, func() { c.failIfSetup(b) })
	}
	if before.Phase != Connected && next.Phase == Connected {
		r.connectedAt = c.clock.Now()
		c.trigger(r, "connected")
		b := next.Binding
		c.schedule(r, time.Duration(r.scenario.MaxDurationMS)*time.Millisecond, func() {
			_ = c.Handle(context.Background(), Event{Kind: "stop", Binding: b, AtMS: c.RelativeMS(), Reason: "duration limit"})
		})
		c.resetIdle(r)
	}
	if next.Phase.Terminal() && !before.Phase.Terminal() {
		r.terminalAt = c.clock.Now()
		if next.Phase == Failed {
			c.addFailure(r, e.Reason)
		}
	}
	if next.Phase == Ending && before.Phase != Ending {
		b := next.Binding
		c.schedule(r, c.opts.SetupTimeout, func() { c.failIfEnding(b) })
	}
	persistErr := c.persistSnapshot(r)
	c.mu.Unlock()
	var effectErrors []error
	for _, effect := range effects {
		if err = c.execute(ctx, r, effect); err != nil {
			effectErrors = append(effectErrors, err)
			c.mu.Lock()
			c.addFailure(r, "control effect failed: "+effect.Kind)
			binding := r.state.Binding
			terminal := r.state.Phase.Terminal()
			c.mu.Unlock()
			if !terminal && effect.Kind != "finalize" {
				_ = c.Handle(context.Background(), Event{Kind: "failed", Binding: binding, AtMS: c.RelativeMS(), Reason: "control effect failed: " + effect.Kind})
			}
		}
	}
	c.mu.Lock()
	if r.state.Phase.Terminal() && c.current == r.state.Binding.RunID {
		c.current = ""
	}
	if r.state.Phase.Terminal() && len(effectErrors) > 0 {
		effectErrors = append(effectErrors, c.persistReport(r))
	}
	c.mu.Unlock()
	return errors.Join(append(effectErrors, persistErr)...)
}

func (c *RunController) execute(ctx context.Context, r *run, e Effect) error {
	switch e.Kind {
	case "accept", "open_greeting":
		c.mu.Lock()
		active := r.state.Binding.RunID == e.Binding.RunID && !r.state.Phase.Terminal() && r.state.Phase != Ending
		callctx := r.ctx
		c.mu.Unlock()
		if !active {
			return nil
		}
		deadline, cancel := context.WithTimeout(callctx, c.opts.SetupTimeout)
		defer cancel()
		if e.Kind == "accept" {
			return c.opts.Telephony.Accept(deadline, e.Binding.CallID)
		}
		if err := c.opts.Voice.Open(deadline, e.Binding, r.scenario.Greeting); err != nil {
			return err
		}
		c.mu.Lock()
		if r.state.Phase == Connected {
			r.observer.greetings++
		}
		c.mu.Unlock()
		return nil
	case "hangup":
		c.mu.Lock()
		if r.hangupSent {
			c.mu.Unlock()
			return nil
		}
		r.hangupSent = true
		c.mu.Unlock()
		deadline, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.opts.SetupTimeout)
		defer cancel()
		return c.opts.Telephony.Hangup(deadline, e.Binding.CallID)
	case "close_voice":
		c.mu.Lock()
		if r.voiceClosed {
			c.mu.Unlock()
			return nil
		}
		r.voiceClosed = true
		c.mu.Unlock()
		deadline, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.opts.SetupTimeout)
		defer cancel()
		return c.opts.Voice.Close(deadline, e.Binding)
	case "finalize":
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.persistReport(r)
	}
	return nil
}
func (c *RunController) failIfSetup(b Binding) {
	c.mu.Lock()
	r, ok := c.runs[b.RunID]
	if !ok || r.state.Phase != Answering {
		c.mu.Unlock()
		return
	}
	b = r.state.Binding
	c.mu.Unlock()
	_ = c.Handle(context.Background(), Event{Kind: "failed", Binding: b, AtMS: c.RelativeMS(), Reason: "setup deadline"})
}
func (c *RunController) failIfEnding(b Binding) {
	c.mu.Lock()
	r, ok := c.runs[b.RunID]
	if !ok || r.state.Phase != Ending {
		c.mu.Unlock()
		return
	}
	b = r.state.Binding
	c.mu.Unlock()
	_ = c.Handle(context.Background(), Event{Kind: "failed", Binding: b, AtMS: c.RelativeMS(), Reason: "termination deadline; missing gateway ended confirmation"})
}
func (c *RunController) record(r *run, kind, reason string) {
	r.seq++
	if len(r.events) == 512 {
		r.events = r.events[1:]
		r.eventsTruncated = true
	}
	r.events = append(r.events, RecordedEvent{Sequence: r.seq, Kind: kind, AtMS: c.RelativeMS(), Reason: redact(reason)})
}
func (c *RunController) addFailure(r *run, reason string) {
	if len(r.failures) < 32 {
		r.failures = append(r.failures, redact(reason))
	}
}
func (c *RunController) Events(ctx context.Context, id string, after uint64) (EventPage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.runs[id]
	if !ok {
		return EventPage{}, ErrNotFound
	}
	p := EventPage{Events: []RecordedEvent{}, Next: r.seq, Truncated: r.eventsTruncated}
	for _, e := range r.events {
		if e.Sequence > after {
			p.Events = append(p.Events, e)
		}
	}
	return p, ctx.Err()
}
func (c *RunController) Observe(ctx context.Context, b Binding, v Observation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.runs[b.RunID]
	if !ok {
		return ErrNotFound
	}
	if !exactBinding(r.state.Binding, b) || b.SessionID == "" || r.state.Phase != Connected {
		return ErrInvalidTransition
	}
	before := r.observer.assistantSpeaking
	duplicate := v.ID != "" && r.observer.seen[v.ID]
	if v.Kind == "transcript" {
		v.Text = redact(v.Text)
	}
	if err := r.observer.observe(v); err != nil {
		c.addFailure(r, "invalid audio observation")
		return err
	}
	if duplicate {
		return nil
	}
	if v.Kind == "customer_audio" && v.NonSilent && !v.End {
		if r.customerIdle != nil {
			r.customerIdle.Stop()
		}
		r.customerAudioGeneration++
		generation := r.customerAudioGeneration
		boundaryEndMS := v.AtMS
		r.customerIdle = c.clock.AfterFunc(200*time.Millisecond, func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if r.state.Phase != Connected || generation != r.customerAudioGeneration || !r.observer.customerSpeaking {
				return
			}
			r.observer.endCustomer(boundaryEndMS)
			c.record(r, "customer_speech_end_debounced", "200 ms without successful outbound audio write")
		})
	} else if v.Kind == "customer_speech_ended" || (v.Kind == "customer_audio" && v.End) {
		if r.customerIdle != nil {
			r.customerIdle.Stop()
			r.customerIdle = nil
		}
		r.customerAudioGeneration++
	}
	if v.Kind == "assistant_audio" && v.NonSilent && !v.End {
		if r.remoteIdle != nil {
			r.remoteIdle.Stop()
		}
		r.remoteAudioGeneration++
		generation := r.remoteAudioGeneration
		boundaryEndMS := v.AtMS
		r.remoteIdle = c.clock.AfterFunc(200*time.Millisecond, func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if r.state.Phase != Connected || generation != r.remoteAudioGeneration || !r.observer.assistantSpeaking {
				return
			}
			r.observer.endRemote(boundaryEndMS)
			c.record(r, "remote_speech_ended", "200 ms without nonsilent received assistant audio")
			c.trigger(r, "remote_speech_ended")
		})
	} else if v.Kind == "remote_speech_ended" || (v.Kind == "assistant_audio" && v.End) {
		if r.remoteIdle != nil {
			r.remoteIdle.Stop()
			r.remoteIdle = nil
		}
		r.remoteAudioGeneration++
	}
	if v.NonSilent || v.Kind == "remote_speech_started" || v.Kind == "customer_speech_started" {
		c.resetIdle(r)
	}
	if !before && r.observer.assistantSpeaking {
		r.remoteTurns++
		c.record(r, "remote_speech_started", "")
		c.trigger(r, "remote_speech_started")
		if r.remoteTurns >= r.scenario.MaxRemoteTurns {
			binding := r.state.Binding
			c.schedule(r, 0, func() {
				_ = c.Handle(context.Background(), Event{Kind: "stop", Binding: binding, AtMS: c.RelativeMS(), Reason: "remote turn limit"})
			})
		}
	}
	if before && !r.observer.assistantSpeaking {
		c.record(r, "remote_speech_ended", "")
		c.trigger(r, "remote_speech_ended")
	}
	return ctx.Err()
}
func (c *RunController) Close() error {
	c.mu.Lock()
	c.closed = true
	ids := []string{}
	for id, r := range c.runs {
		if !r.state.Phase.Terminal() {
			ids = append(ids, id)
		}
	}
	c.mu.Unlock()
	for _, id := range ids {
		if err := c.Stop(context.Background(), id); err != nil {
			return err
		}
	}
	return nil
}

// ActionObservation records engine-reported action outcomes separately from
// requested controls. Audio overlap remains evidenced only by audio boundaries.
func (c *RunController) ActionObservation(ctx context.Context, b Binding, outcome ActionOutcome) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.runs[b.RunID]
	if !ok {
		return ErrNotFound
	}
	if !exactBinding(r.state.Binding, b) || b.SessionID == "" {
		return ErrInvalidTransition
	}
	known := false
	for _, a := range r.scenario.Actions {
		if a.ID == outcome.ID {
			known = true
			break
		}
	}
	if !known {
		return ErrInvalidTransition
	}
	switch outcome.Result {
	case "started", "completed", "cancelled", "timed_out", "failed", "skipped":
	default:
		return ErrInvalidTransition
	}
	if outcome.AtMS < 0 {
		return ErrInvalidTransition
	}
	outcome.Reason = redact(outcome.Reason)
	for _, previous := range r.actions {
		if previous.ID == outcome.ID && previous.Result == outcome.Result {
			return nil
		}
	}
	if len(r.actions) >= 320 {
		return errors.New("action outcome capacity reached")
	}
	r.actions = append(r.actions, outcome)
	if outcome.Result == "failed" {
		c.addFailure(r, "engine action failed: "+outcome.ID)
	}
	c.record(r, "action_"+outcome.Result, outcome.ID)
	if r.state.Phase.Terminal() {
		if err := c.persistReport(r); err != nil {
			return err
		}
	}
	return ctx.Err()
}
