package simulator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestReviewRejectUnboundGatewayEvents(t *testing.T) {
	for _, kind := range []string{"ended", "answered", "voice_ready", "failed"} {
		s := State{Phase: Waiting, Binding: Binding{RunID: "reserved"}}
		_, _, err := Advance(s, Event{Kind: kind, Binding: Binding{RunID: "reserved", CallID: "foreign"}})
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("%s consumed waiting reservation", kind)
		}
	}
}
func TestReviewScenarioRequiresAssociatedSession(t *testing.T) {
	ctx := context.Background()
	c, f, _ := testController(t)
	snap, _ := c.Prepare(ctx, sampleScenario())
	_ = c.Incoming(ctx, incomingFor(snap, "call"))
	b := Binding{RunID: snap.RunID, CallID: "call"}
	if _, err := c.ScenarioForBinding(ctx, b); err == nil {
		t.Fatal("ringing unassociated scenario lookup")
	}
	f.advance(1500 * time.Millisecond)
	if _, err := c.ScenarioForBinding(ctx, b); err == nil {
		t.Fatal("answering empty-session lookup")
	}
	b.SessionID = "session"
	if err := c.Associate(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ScenarioForBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
}

type actionReturnPorts struct {
	*fakePorts
	started    chan struct{}
	released   chan struct{}
	returnErr  error
	ctxError   bool
	hangupHook func()
}

func (p *actionReturnPorts) Speak(ctx context.Context, _ Binding, _, _ string, _ time.Duration) error {
	if p.started != nil {
		close(p.started)
	}
	if p.ctxError {
		<-ctx.Done()
		if p.released != nil {
			<-p.released
		}
		return ctx.Err()
	}
	if p.released != nil {
		<-p.released
	}
	return p.returnErr
}
func (p *actionReturnPorts) Hangup(ctx context.Context, id string) error {
	if p.hangupHook != nil {
		p.hangupHook()
	}
	return p.fakePorts.Hangup(ctx, id)
}
func assertArtifactMatches(t *testing.T, c *RunController, id string) {
	t.Helper()
	live, err := c.Report(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(c.opts.DataDir, id, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved Report
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live, saved) {
		t.Fatalf("saved actions/failures differ: live=%+v/%v saved=%+v/%v", live.Actions, live.Failures, saved.Actions, saved.Failures)
	}
	restored, err := NewController(c.opts)
	if err != nil {
		t.Fatal(err)
	}
	after, err := restored.Report(context.Background(), id)
	if err != nil || !reflect.DeepEqual(after, saved) {
		t.Fatalf("restored report differs: %v", err)
	}
}
func TestReviewStopCancellationAndLateArtifact(t *testing.T) {
	ctx := context.Background()
	c, f, base := testController(t)
	p := &actionReturnPorts{fakePorts: base, started: make(chan struct{}), released: make(chan struct{}), ctxError: true}
	c.opts.Voice = p
	c.opts.DataDir = t.TempDir()
	s := sampleScenario()
	s.Actions = []Action{{ID: "speech", Trigger: "connected", Occurrence: 1, Kind: "speak", Text: "Synthetic", MaxHoldMS: 3000}}
	snap, _ := c.Prepare(ctx, s)
	b := connect(t, c, f, snap)
	finished := make(chan struct{})
	go func() { c.performAction(b.RunID, s.Actions[0]); close(finished) }()
	<-p.started
	_ = c.Stop(ctx, b.RunID)
	_ = c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS()})
	close(p.released)
	<-finished
	rep, err := c.Report(ctx, b.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Failures) != 0 {
		t.Fatal("normal stop became failure", rep.Failures)
	}
	found := false
	for _, a := range rep.Actions {
		if a.Result == "cancelled" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing cancelled action")
	}
	assertArtifactMatches(t, c, b.RunID)
}
func TestReviewSynchronousHangupArtifact(t *testing.T) {
	ctx := context.Background()
	c, f, base := testController(t)
	p := &actionReturnPorts{fakePorts: base}
	c.opts.Telephony = p
	c.opts.DataDir = t.TempDir()
	s := sampleScenario()
	s.Actions = []Action{{ID: "leave", Trigger: "connected", Occurrence: 1, Kind: "hangup"}}
	snap, _ := c.Prepare(ctx, s)
	b := connect(t, c, f, snap)
	p.hangupHook = func() { _ = c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS()}) }
	c.performAction(b.RunID, s.Actions[0])
	rep, _ := c.Report(ctx, b.RunID)
	if len(rep.Actions) == 0 || rep.Actions[0].Result == "not_triggered" {
		t.Fatal("dispatched hangup lost")
	}
	assertArtifactMatches(t, c, b.RunID)
}
func TestReviewSimulatorGapPendingAndConsumed(t *testing.T) {
	o := newObserver()
	_ = o.observe(Observation{Kind: "remote_speech_started", AtMS: 10})
	_ = o.observe(Observation{Kind: "remote_speech_ended", AtMS: 100})
	gaps := o.customerGaps()
	if len(gaps) != 1 || gaps[0].Valid || gaps[0].Reason != "missing customer response" {
		t.Fatalf("unanswered assistant omitted: %+v", gaps)
	}
	_ = o.observe(Observation{Kind: "customer_speech_started", AtMS: 200})
	_ = o.observe(Observation{Kind: "customer_speech_ended", AtMS: 250})
	_ = o.observe(Observation{Kind: "customer_speech_started", AtMS: 300})
	gaps = o.customerGaps()
	if len(gaps) != 2 || !gaps[0].Valid || gaps[0].ValueMS != 100 || gaps[1].Valid {
		t.Fatalf("assistant boundary reused: %+v", gaps)
	}
}
func TestReviewDefaultMemoryTranscriptRetention(t *testing.T) {
	ctx := context.Background()
	c, f, _ := testController(t)
	snap, _ := c.Prepare(ctx, sampleScenario())
	b := connect(t, c, f, snap)
	_ = c.Observe(ctx, b, Observation{Kind: "transcript", Role: "assistant", Text: "synthetic retained text", AtMS: c.RelativeMS()})
	_ = c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS()})
	c.mu.Lock()
	retained := len(c.runs[b.RunID].observer.transcript)
	c.mu.Unlock()
	if retained != 0 {
		t.Fatal("default retained text in terminal history")
	}
}

func TestReviewActionOwnHoldDeadline(t *testing.T) {
	ctx := context.Background()
	c, f, base := testController(t)
	p := &actionReturnPorts{fakePorts: base, ctxError: true}
	c.opts.Voice = p
	s := sampleScenario()
	s.Actions = []Action{{ID: "bounded", Trigger: "connected", Occurrence: 1, Kind: "speak", Text: "Synthetic", MaxHoldMS: 1}}
	snap, _ := c.Prepare(ctx, s)
	b := connect(t, c, f, snap)
	c.performAction(b.RunID, s.Actions[0])
	c.mu.Lock()
	outcomes := append([]ActionOutcome{}, c.runs[b.RunID].actions...)
	failures := len(c.runs[b.RunID].failures)
	c.mu.Unlock()
	if len(outcomes) != 2 || outcomes[1].Result != "timed_out" || failures != 0 {
		t.Fatalf("hold deadline misclassified: %+v failures=%d", outcomes, failures)
	}
}
func TestReviewLateEngineOutcomePersists(t *testing.T) {
	ctx := context.Background()
	c, f, _ := testController(t)
	c.opts.DataDir = t.TempDir()
	s := sampleScenario()
	s.Actions = []Action{{ID: "scripted", Trigger: "connected", Occurrence: 1, Kind: "speak", Text: "Synthetic", MaxHoldMS: 3000}}
	snap, _ := c.Prepare(ctx, s)
	b := connect(t, c, f, snap)
	_ = c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS()})
	if err := c.ActionObservation(ctx, b, ActionOutcome{ID: "scripted", Result: "cancelled", AtMS: c.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	assertArtifactMatches(t, c, b.RunID)
	if err := c.ActionObservation(ctx, Binding{RunID: b.RunID, CallID: b.CallID, SessionID: "other"}, ActionOutcome{ID: "scripted", Result: "completed", AtMS: c.RelativeMS()}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("late wrong session accepted", err)
	}
}

func TestReviewDelayedRemoteEndKeepsOriginalResponseRow(t *testing.T) {
	for _, inferred := range []bool{false, true} {
		name := "explicit"
		if inferred {
			name = "inferred"
		}
		t.Run(name, func(t *testing.T) {
			o := newObserver()
			_ = o.observe(Observation{Kind: "remote_speech_started", AtMS: 10})
			_ = o.observe(Observation{Kind: "assistant_audio", AtMS: 50, NonSilent: true})
			if !inferred {
				_ = o.observe(Observation{Kind: "remote_speech_ended", AtMS: 50})
			}
			for _, v := range []Observation{{Kind: "customer_speech_started", AtMS: 100}, {Kind: "customer_speech_ended", AtMS: 110}, {Kind: "customer_speech_started", AtMS: 150}, {Kind: "customer_speech_ended", AtMS: 160}} {
				if err := o.observe(v); err != nil {
					t.Fatal(err)
				}
			}
			if inferred {
				o.endRemote(50)
			}
			gaps := o.customerGaps()
			if len(gaps) != 2 || !gaps[0].Valid || gaps[0].ValueMS != 50 || gaps[1].Valid || gaps[1].Reason != "missing assistant speech end" {
				t.Fatalf("remote end reassigned: %+v", gaps)
			}
			if o.lastMS != 160 {
				t.Fatal("delayed inference rewound observation clock")
			}
		})
	}
}
func TestReviewControllerDelayedRemoteEndKeepsOriginalResponseRow(t *testing.T) {
	ctx := context.Background()
	c, f, _ := testController(t)
	snap, _ := c.Prepare(ctx, sampleScenario())
	b := connect(t, c, f, snap)
	f.advance(10 * time.Millisecond)
	_ = c.Observe(ctx, b, Observation{Kind: "assistant_audio", AtMS: c.RelativeMS(), NonSilent: true})
	f.advance(40 * time.Millisecond)
	_ = c.Observe(ctx, b, Observation{Kind: "assistant_audio", AtMS: c.RelativeMS(), NonSilent: true})
	for _, step := range []struct {
		after time.Duration
		kind  string
	}{{50 * time.Millisecond, "customer_speech_started"}, {10 * time.Millisecond, "customer_speech_ended"}, {40 * time.Millisecond, "customer_speech_started"}, {10 * time.Millisecond, "customer_speech_ended"}} {
		f.advance(step.after)
		if err := c.Observe(ctx, b, Observation{Kind: step.kind, AtMS: c.RelativeMS()}); err != nil {
			t.Fatal(err)
		}
	}
	f.advance(90 * time.Millisecond)
	_ = c.Handle(ctx, Event{Kind: "ended", Binding: b, AtMS: c.RelativeMS()})
	rep, err := c.Report(ctx, b.RunID)
	if err != nil || len(rep.SimulatorGaps) != 2 || !rep.SimulatorGaps[0].Valid || rep.SimulatorGaps[0].ValueMS != 50 || rep.SimulatorGaps[1].Valid || rep.InvalidObservations != 0 {
		t.Fatalf("wrong delayed controller gap: %+v %v", rep.SimulatorGaps, err)
	}
}
