package simulator

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeTimer struct {
	at      time.Time
	fn      func()
	stopped bool
	clock   *fakeClock
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

func (f *fakeClock) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
func (f *fakeClock) AfterFunc(d time.Duration, fn func()) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{at: f.now.Add(d), fn: fn, clock: f}
	f.timers = append(f.timers, t)
	return t
}
func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	target := f.now.Add(d)
	f.mu.Unlock()
	for {
		f.mu.Lock()
		var next *fakeTimer
		for _, t := range f.timers {
			if !t.stopped && !t.at.After(target) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		if next == nil {
			f.now = target
			f.mu.Unlock()
			return
		}
		f.now = next.at
		next.stopped = true
		f.mu.Unlock()
		next.fn()
	}
}

type fakePorts struct {
	mu                                               sync.Mutex
	accepts, rejects, hangups, opens, speaks, closes int
	acceptErr, hangupErr, closeErr                   error
	openHook                                         func(Binding)
	speakHook                                        func(context.Context)
}

func (p *fakePorts) Accept(context.Context, string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accepts++
	return p.acceptErr
}
func (p *fakePorts) Reject(context.Context, string, string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rejects++
	return nil
}
func (p *fakePorts) Hangup(context.Context, string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hangups++
	return p.hangupErr
}
func (p *fakePorts) Open(_ context.Context, b Binding, _ string) error {
	p.mu.Lock()
	p.opens++
	hook := p.openHook
	p.mu.Unlock()
	if hook != nil {
		hook(b)
	}
	return nil
}
func (p *fakePorts) Speak(ctx context.Context, _ Binding, _, _ string, _ time.Duration) error {
	p.mu.Lock()
	p.speaks++
	hook := p.speakHook
	p.mu.Unlock()
	if hook != nil {
		hook(ctx)
	}
	return nil
}
func (p *fakePorts) Close(context.Context, Binding) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closes++
	return p.closeErr
}
func testController(t *testing.T) (*RunController, *fakeClock, *fakePorts) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	p := &fakePorts{}
	c, err := NewController(Options{Clock: clock, Telephony: p, Voice: p, Token: "test-token", Destination: "receiver", GatewayIdentity: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return c, clock, p
}
func incomingFor(s Snapshot, call string) IncomingCall {
	return IncomingCall{RunID: s.RunID, CallID: call, Destination: "receiver", GatewayIdentity: "fixture"}
}
func connect(t *testing.T, c *RunController, f *fakeClock, s Snapshot) Binding {
	t.Helper()
	ctx := context.Background()
	if err := c.Incoming(ctx, incomingFor(s, "call-1")); err != nil {
		t.Fatal(err)
	}
	f.advance(1500 * time.Millisecond)
	b := Binding{RunID: s.RunID, CallID: "call-1", SessionID: "session-1"}
	if err := c.Associate(ctx, b); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"voice_ready", "answered"} {
		if err := c.Handle(ctx, Event{Kind: kind, Binding: b, AtMS: c.RelativeMS()}); err != nil {
			t.Fatal(err)
		}
	}
	return b
}
func TestControllerDuplicateCancelAndTimeout(t *testing.T) {
	ctx := context.Background()
	c, f, p := testController(t)
	s, _ := c.Prepare(ctx, sampleScenario())
	in := incomingFor(s, "c1")
	if err := c.Incoming(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := c.Incoming(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := c.Incoming(ctx, incomingFor(s, "c2")); err == nil {
		t.Fatal("second admitted")
	}
	f.advance(1499 * time.Millisecond)
	if err := c.Stop(ctx, s.RunID); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Millisecond)
	if p.accepts != 0 || p.rejects != 1 || p.hangups != 1 {
		t.Fatalf("%+v", p)
	}
	_ = c.Handle(ctx, Event{Kind: "ended", Binding: Binding{RunID: s.RunID, CallID: "c1"}, AtMS: c.RelativeMS()})
	next, _ := c.Prepare(ctx, sampleScenario())
	if err := c.Incoming(ctx, in); err == nil {
		t.Fatal("stale admitted")
	}
	if err := c.Incoming(ctx, incomingFor(next, "c3")); err != nil {
		t.Fatal(err)
	}
	f.advance(1500 * time.Millisecond)
	if p.accepts != 1 {
		t.Fatal("missing accept")
	}
	f.advance(5 * time.Second)
	snap, _ := c.Get(ctx, next.RunID)
	if snap.Phase != Failed || p.hangups != 2 {
		t.Fatalf("%+v %+v", snap, p)
	}
}
func TestControllerBindingActionsAndLimits(t *testing.T) {
	ctx := context.Background()
	c, f, p := testController(t)
	scenario := sampleScenario()
	scenario.MaxDurationMS = 5000
	scenario.Actions = []Action{{ID: "interrupt", Trigger: "remote_speech_started", Occurrence: 1, AfterMS: 700, RequireRemoteSpeaking: true, Kind: "speak", Text: "Wait", MaxHoldMS: 3000}}
	s, _ := c.Prepare(ctx, scenario)
	b := connect(t, c, f, s)
	if p.opens != 1 {
		t.Fatal("greeting count")
	}
	if err := c.Observe(ctx, Binding{RunID: b.RunID, CallID: b.CallID, SessionID: "wrong"}, Observation{Kind: "assistant_audio", AtMS: c.RelativeMS(), NonSilent: true}); err == nil {
		t.Fatal("wrong session observed")
	}
	_ = c.Observe(ctx, b, Observation{ID: "start", Kind: "assistant_audio", AtMS: c.RelativeMS(), NonSilent: true})
	for i := 0; i < 6; i++ {
		f.advance(100 * time.Millisecond)
		_ = c.Observe(ctx, b, Observation{Kind: "assistant_audio", AtMS: c.RelativeMS(), NonSilent: true})
	}
	f.advance(100 * time.Millisecond)
	if p.speaks != 1 {
		t.Fatal("action did not interrupt")
	}
	f.advance(4300 * time.Millisecond)
	snap, _ := c.Get(ctx, b.RunID)
	if snap.Phase != Ending || p.hangups != 1 {
		t.Fatalf("%+v", snap)
	}
	_ = c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS()})
	rep, err := c.Report(ctx, b.RunID)
	if err != nil || len(rep.Actions) != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
}
func TestControllerExpiredAndUnauthenticatedIdentity(t *testing.T) {
	ctx := context.Background()
	c, f, p := testController(t)
	s, _ := c.Prepare(ctx, sampleScenario())
	bad := incomingFor(s, "bad")
	bad.GatewayIdentity = "wrong"
	if c.Incoming(ctx, bad) == nil {
		t.Fatal("source mismatch admitted")
	}
	f.advance(120 * time.Second)
	if c.Incoming(ctx, incomingFor(s, "late")) == nil {
		t.Fatal("expired admitted")
	}
	snap, _ := c.Get(ctx, s.RunID)
	if snap.Phase != Expired || p.accepts != 0 {
		t.Fatal(snap)
	}
}
func TestControllerStopCancelsInFlightAction(t *testing.T) {
	ctx := context.Background()
	c, f, p := testController(t)
	s := sampleScenario()
	s.Actions = []Action{{ID: "interrupt", Trigger: "connected", Occurrence: 1, Kind: "speak", Text: "Wait", MaxHoldMS: 3000}}
	snap, _ := c.Prepare(ctx, s)
	b := connect(t, c, f, snap)
	started := make(chan struct{})
	done := make(chan struct{})
	p.speakHook = func(ctx context.Context) { close(started); <-ctx.Done(); close(done) }
	go f.advance(0)
	<-started
	if err := c.Stop(ctx, b.RunID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel action")
	}
}

func TestControllerEndingDeadlineAndCleanupFailure(t *testing.T) {
	ctx := context.Background()
	c, f, p := testController(t)
	snap, _ := c.Prepare(ctx, sampleScenario())
	b := connect(t, c, f, snap)
	if err := c.Stop(ctx, b.RunID); err != nil {
		t.Fatal(err)
	}
	f.advance(5 * time.Second)
	state, _ := c.Get(ctx, b.RunID)
	if state.Phase != Failed || p.hangups != 1 || p.closes != 1 {
		t.Fatalf("deadline failed: %+v %+v", state, p)
	}
	if _, err := c.Prepare(ctx, sampleScenario()); err != nil {
		t.Fatal("slot not released", err)
	}
	c, f, p = testController(t)
	snap, _ = c.Prepare(ctx, sampleScenario())
	b = connect(t, c, f, snap)
	p.hangupErr = ErrNotReady
	if err := c.Stop(ctx, b.RunID); err == nil {
		t.Fatal("hangup failure lost")
	}
	state, _ = c.Get(ctx, b.RunID)
	if state.Phase != Failed || p.closes != 1 || p.hangups != 1 {
		t.Fatalf("cleanup skipped: %+v %+v", state, p)
	}
	if rep, err := c.Report(ctx, b.RunID); err != nil || len(rep.Failures) == 0 {
		t.Fatalf("missing failure report: %+v %v", rep, err)
	}
}
func TestControllerConcurrentCallbacks(t *testing.T) {
	ctx := context.Background()
	c, f, p := testController(t)
	snap, _ := c.Prepare(ctx, sampleScenario())
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = c.Incoming(ctx, incomingFor(snap, "same-call")) }()
	}
	wg.Wait()
	f.advance(1500 * time.Millisecond)
	if p.accepts != 1 {
		t.Fatal("duplicate Accept", p.accepts)
	}
}
func TestControllerDebouncePreservesActualWriteBoundary(t *testing.T) {
	ctx := context.Background()
	c, f, _ := testController(t)
	snap, _ := c.Prepare(ctx, sampleScenario())
	b := connect(t, c, f, snap)
	_ = c.Observe(ctx, b, Observation{Kind: "customer_audio", NonSilent: true, AtMS: c.RelativeMS()})
	f.advance(50 * time.Millisecond)
	lastWrite := c.RelativeMS()
	_ = c.Observe(ctx, b, Observation{Kind: "customer_audio", NonSilent: true, AtMS: lastWrite})
	f.advance(100 * time.Millisecond)
	_ = c.Observe(ctx, b, Observation{Kind: "assistant_audio", NonSilent: true, AtMS: c.RelativeMS()})
	f.advance(100 * time.Millisecond)
	_ = c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS()})
	rep, err := c.Report(ctx, b.RunID)
	if err != nil || len(rep.AssistantGaps) != 1 || !rep.AssistantGaps[0].Valid || rep.AssistantGaps[0].ValueMS != 100 {
		t.Fatalf("debounce contaminated boundary: %+v %v", rep.AssistantGaps, err)
	}
}
func TestControllerRemoteDebounceAndSilence(t *testing.T) {
	ctx := context.Background()
	c, f, p := testController(t)
	s := sampleScenario()
	s.Actions = []Action{{ID: "after-remote", Trigger: "remote_speech_ended", Occurrence: 1, Kind: "speak", Text: "Synthetic reply", MaxHoldMS: 3000}}
	snap, _ := c.Prepare(ctx, s)
	b := connect(t, c, f, snap)
	_ = c.Observe(ctx, b, Observation{Kind: "assistant_audio", NonSilent: true, AtMS: c.RelativeMS()})
	f.advance(50 * time.Millisecond)
	lastRemote := c.RelativeMS()
	_ = c.Observe(ctx, b, Observation{Kind: "assistant_audio", NonSilent: true, AtMS: lastRemote})
	f.advance(50 * time.Millisecond)
	_ = c.Observe(ctx, b, Observation{Kind: "assistant_audio", AtMS: c.RelativeMS()})
	if p.speaks != 0 {
		t.Fatal("silence instantly ended speech")
	}
	f.advance(50 * time.Millisecond)
	_ = c.Observe(ctx, b, Observation{Kind: "customer_audio", NonSilent: true, AtMS: c.RelativeMS()})
	f.advance(100 * time.Millisecond)
	if p.speaks != 1 {
		t.Fatal("missing ended action")
	}
	_ = c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS()})
	rep, err := c.Report(ctx, b.RunID)
	if err != nil || len(rep.SimulatorGaps) != 1 || !rep.SimulatorGaps[0].Valid || rep.SimulatorGaps[0].ValueMS != 100 {
		t.Fatalf("wrong remote boundary: %+v %v", rep.SimulatorGaps, err)
	}
}
