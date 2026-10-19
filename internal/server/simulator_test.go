package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/duplex"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/simulator"
	"github.com/gorilla/websocket"
)

const simulatorMediaToken = "local-test-media-token-32-characters"

type simulatorPhone struct{ accepted chan string }

func (p *simulatorPhone) Accept(_ context.Context, id string) error  { p.accepted <- id; return nil }
func (*simulatorPhone) Reject(context.Context, string, string) error { return nil }
func (*simulatorPhone) Hangup(context.Context, string) error         { return nil }
func simulatorHarness(t *testing.T, actions ...simulator.Action) (*httptest.Server, *SimulatorReceiver, *simulator.RunController, simulator.Snapshot) {
	t.Helper()
	scenario := simulator.Scenario{SchemaVersion: 1, ID: "test-customer", Language: "en", Persona: "You are Sam, a brief customer.", Facts: map[string]string{"appointment": "Tuesday"}, Greeting: "Hello?", MaxDurationMS: 30000, MaxRemoteTurns: 10}
	scenario.Actions = actions
	return simulatorScenarioHarness(t, scenario)
}

func simulatorScenarioHarness(t *testing.T, scenario simulator.Scenario) (*httptest.Server, *SimulatorReceiver, *simulator.RunController, simulator.Snapshot) {
	t.Helper()
	cfg := config.Default()
	cfg.ASR = "mock"
	cfg.LLM = "mock"
	cfg.TTS = "mock"
	cfg.Greeting = "BAD DEFAULT GREETING"
	cfg.Duplex.Backchannel = false
	cfg.Duplex.Speculative = false
	r, err := NewSimulatorReceiver(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), simulatorMediaToken)
	if err != nil {
		t.Fatal(err)
	}
	phone := &simulatorPhone{accepted: make(chan string, 1)}
	c, err := simulator.NewController(simulator.Options{Telephony: phone, Voice: r, Token: "control-test", Destination: "receiver", GatewayIdentity: "gateway", SetupTimeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	r.SetController(c)
	snap, err := c.Prepare(context.Background(), scenario)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Incoming(context.Background(), simulator.IncomingCall{RunID: snap.RunID, CallID: "test-call", Destination: "receiver", GatewayIdentity: "gateway"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-phone.accepted:
	case <-time.After(time.Second):
		t.Fatal("not accepted")
	}
	snap, _ = c.Get(context.Background(), snap.RunID)
	ts := httptest.NewServer(r)
	t.Cleanup(func() { _ = c.Close(); ts.Close() })
	return ts, r, c, snap
}
func simulatorPath(snap simulator.Snapshot) string {
	return "/v1/realtime?run=" + snap.RunID + "&call=" + snap.Binding.CallID + "&token=" + simulatorMediaToken
}
func TestSimulatorAdmissionRejectsMissingWrongAndStaleBindings(t *testing.T) {
	ts, _, _, snap := simulatorHarness(t)
	for _, path := range []string{"/v1/realtime", simulatorPath(snap) + "&hold=false", strings.Replace(simulatorPath(snap), "test-call", "wrong", 1)} {
		conn, resp, err := websocket.DefaultDialer.Dial(wsURL(ts, path), nil)
		if conn != nil {
			conn.Close()
		}
		if err == nil || resp == nil || resp.StatusCode < 400 {
			t.Fatalf("admitted %s", path)
		}
	}
	for _, path := range []string{"/v1/opening", "/v1/live", "/v1/live/sessions/x/attach"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("legacy route %s available", path)
		}
	}
	conn := dialPath(t, ts, simulatorPath(snap))
	defer conn.Close()
	_, resp, err := websocket.DefaultDialer.Dial(wsURL(ts, simulatorPath(snap)), nil)
	if err == nil || resp.StatusCode != 409 {
		t.Fatal("duplicate socket admitted")
	}
}
func TestSimulatorGatesGreetingAndPinsPersona(t *testing.T) {
	ts, r, c, snap := simulatorHarness(t)
	opts := make(chan duplex.Options, 1)
	r.Factory = func(o duplex.Options, d duplex.Deps) *duplex.Engine { opts <- o; return duplex.New(o, d) }
	client := dialRealtime(t, ts, simulatorPath(snap))
	client.first("session.created", time.Second)
	client.send(map[string]any{"type": "session.update", "session": map[string]any{"instructions": "INJECTED", "input_audio_format": "g711_ulaw", "voice": "INJECTED", "tools": []any{map[string]any{"type": "function", "name": "bad"}}}})
	client.first("session.updated", time.Second)
	option := <-opts
	if !option.SimulatorEnabled || !strings.Contains(option.Instructions, "Sam") || strings.Contains(option.Instructions, "INJECTED") || option.ClientRate != 16000 || len(option.Backend.Tools) != 0 {
		t.Fatalf("untrusted scenario overrides: %+v", option)
	}
	time.Sleep(100 * time.Millisecond)
	if client.audioMS(16000) > 0 {
		t.Fatal("greeting before answer")
	}
	bound, _ := c.Get(context.Background(), snap.RunID)
	if err := c.Handle(context.Background(), simulator.Event{Kind: "answered", Binding: bound.Binding, AtMS: c.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	client.first("response.audio.delta", 2*time.Second)
	if err := c.Handle(context.Background(), simulator.Event{Kind: "answered", Binding: bound.Binding, AtMS: c.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	if err := c.Handle(context.Background(), simulator.Event{Kind: "ended", Binding: bound.Binding, AtMS: c.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.done:
	case <-time.After(time.Second):
		t.Fatal("media remained open after end")
	}
	rep, err := c.Report(context.Background(), snap.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Evidence != "application_audio_boundary" {
		t.Fatalf("wrong evidence %s", rep.Evidence)
	}
}
func TestSimulatorAudioBeforeUpdateCannotStartDefaultSession(t *testing.T) {
	ts, r, _, snap := simulatorHarness(t)
	started := make(chan bool, 1)
	r.Factory = func(o duplex.Options, d duplex.Deps) *duplex.Engine { started <- true; return duplex.New(o, d) }
	client := dialRealtime(t, ts, simulatorPath(snap))
	client.send(map[string]string{"type": "input_audio_buffer.append", "audio": "AAAA"})
	select {
	case <-client.done:
	case <-time.After(time.Second):
		t.Fatal("early audio left connection open")
	}
	select {
	case <-started:
		t.Fatal("default engine started")
	default:
	}
}
func TestSimulatorMediaLossEndsRunAndHangup(t *testing.T) {
	ts, _, c, snap := simulatorHarness(t)
	conn := dialPath(t, ts, simulatorPath(snap))
	conn.Close()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := c.Get(context.Background(), snap.RunID)
		if got.Phase == simulator.Failed {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("media loss not terminal")
}

func TestSimulatorScriptedInterruptionTraversesAudioBoundary(t *testing.T) {
	ts, _, controller, snap := simulatorHarness(t, simulator.Action{ID: "interrupt", Trigger: "remote_speech_started", Occurrence: 1, AfterMS: 180, RequireRemoteSpeaking: true, Kind: "speak", Text: "Wait, one question.", MaxHoldMS: 1000})
	client := dialRealtime(t, ts, simulatorPath(snap))
	client.send(map[string]any{"type": "session.update", "session": map[string]any{"input_audio_format": "pcm16"}})
	client.first("session.updated", time.Second)
	bound, _ := controller.Get(context.Background(), snap.RunID)
	if err := controller.Handle(context.Background(), simulator.Event{Kind: "answered", Binding: bound.Binding, AtMS: controller.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	client.first("response.audio.done", 3*time.Second)
	time.Sleep(250 * time.Millisecond)
	client.say(voicedPCM(16000, 1600), 16000)
	time.Sleep(300 * time.Millisecond)
	if err := controller.Handle(context.Background(), simulator.Event{Kind: "ended", Binding: bound.Binding, AtMS: controller.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	report, err := controller.Report(context.Background(), snap.RunID)
	if err != nil {
		t.Fatal(err)
	}
	started := false
	for _, a := range report.Actions {
		if a.ID == "interrupt" && a.Result == "started" {
			started = true
		}
	}
	if !started {
		t.Fatalf("script did not reach engine: %+v", report.Actions)
	}
	overlap := false
	for _, check := range report.Checks {
		if check.Name == "interruption_overlap" && check.Result == "pass" {
			overlap = true
		}
	}
	if !overlap || report.OverlapDurationMS <= 0 {
		t.Fatalf("no observed bidirectional overlap: %+v", report)
	}
}

func TestSimulatorMediaCloseBeforeGatewayHangupReconciles(t *testing.T) {
	ts, _, controller, snap := simulatorHarness(t)
	conn := dialPath(t, ts, simulatorPath(snap))
	bound, _ := controller.Get(context.Background(), snap.RunID)
	conn.Close()
	time.Sleep(600 * time.Millisecond)
	if err := controller.Handle(context.Background(), simulator.Event{Kind: "ended", Binding: bound.Binding, AtMS: controller.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(550 * time.Millisecond)
	ended, _ := controller.Get(context.Background(), snap.RunID)
	if ended.Phase != simulator.Completed {
		t.Fatalf("clean hangup became %s", ended.Phase)
	}
}

// This clock pauses a producer after sampling its time, before Observe can
// ingest it. Other reads remain available so an unserialized producer can
// demonstrably overtake that observation at a later timestamp.
type simulatorObservationClock struct {
	mu      sync.Mutex
	now     time.Time
	pause   bool
	paused  bool
	sampled chan struct{}
	later   chan struct{}
	resume  chan struct{}
}

func (c *simulatorObservationClock) Now() time.Time {
	c.mu.Lock()
	now := c.now
	pause := c.pause
	if pause {
		c.pause = false
		c.paused = true
		close(c.sampled)
	} else if c.paused {
		select {
		case c.later <- struct{}{}:
		default:
		}
	}
	c.mu.Unlock()
	if pause {
		<-c.resume
	}
	return now
}

type simulatorObservationTimer struct{}

func (simulatorObservationTimer) Stop() bool { return true }
func (*simulatorObservationClock) AfterFunc(time.Duration, func()) simulator.Timer {
	return simulatorObservationTimer{}
}

type simulatorObservationVoice struct{}

func (simulatorObservationVoice) Open(context.Context, simulator.Binding, string) error { return nil }
func (simulatorObservationVoice) Speak(context.Context, simulator.Binding, string, string, time.Duration) error {
	return nil
}
func (simulatorObservationVoice) Close(context.Context, simulator.Binding) error { return nil }

func TestSimulatorSerializesObservationTimestampAndIngestion(t *testing.T) {
	for _, tc := range []struct {
		name, first, second string
		assistant, customer int
	}{
		{"inbound_then_outbound", "inbound", "outbound", 1, 1},
		{"outbound_then_inbound", "outbound", "inbound", 1, 2},
		{"inbound_then_transcript", "inbound", "transcript", 0, 1},
		{"transcript_then_outbound", "transcript", "outbound", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			clock := &simulatorObservationClock{now: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), sampled: make(chan struct{}), later: make(chan struct{}, 1), resume: make(chan struct{})}
			phone := &simulatorPhone{accepted: make(chan string, 1)}
			controller, err := simulator.NewController(simulator.Options{Clock: clock, Telephony: phone, Voice: simulatorObservationVoice{}, Token: "test", Destination: "receiver", GatewayIdentity: "gateway"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = controller.Close() })
			scenario := simulator.Scenario{SchemaVersion: 1, ID: "ordered-observations", Language: "en", Persona: "A brief customer.", Greeting: "Hello?", MaxDurationMS: 30000, MaxRemoteTurns: 10, RetainTranscript: true}
			snap, err := controller.Prepare(ctx, scenario)
			if err != nil {
				t.Fatal(err)
			}
			if err = controller.Incoming(ctx, simulator.IncomingCall{RunID: snap.RunID, CallID: "test-call", Destination: "receiver", GatewayIdentity: "gateway"}); err != nil {
				t.Fatal(err)
			}
			binding := simulator.Binding{RunID: snap.RunID, CallID: "test-call", SessionID: "test-session"}
			if err = controller.Handle(ctx, simulator.Event{Kind: "answer_due", Binding: simulator.Binding{RunID: snap.RunID, CallID: "test-call"}, AtMS: 0}); err != nil {
				t.Fatal(err)
			}
			if err = controller.Associate(ctx, binding); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"voice_ready", "answered"} {
				if err = controller.Handle(ctx, simulator.Event{Kind: kind, Binding: binding, AtMS: 0}); err != nil {
					t.Fatal(err)
				}
			}
			session := &simulatorSession{receiver: &SimulatorReceiver{controller: controller}, binding: binding, scenario: scenario, ctx: ctx}
			observe := func(kind string) {
				if kind == "transcript" {
					session.observeEvent(live.TranscriptDelta{Envelope: live.Envelope{Type: live.ServerInputTranscriptDelta}, Delta: "Hello"})
				} else {
					session.audio(kind == "outbound", []byte{0, 4})
				}
			}
			clock.mu.Lock()
			clock.now = clock.now.Add(100 * time.Millisecond)
			clock.pause = true
			clock.mu.Unlock()
			firstDone := make(chan struct{})
			go func() { observe(tc.first); close(firstDone) }()
			select {
			case <-clock.sampled:
			case <-time.After(time.Second):
				t.Fatal("first producer did not sample its clock")
			}
			clock.mu.Lock()
			clock.now = clock.now.Add(time.Millisecond)
			clock.mu.Unlock()
			secondDone := make(chan struct{})
			go func() { observe(tc.second); close(secondDone) }()
			select {
			case <-clock.later:
				t.Error("later producer sampled a timestamp before the paused observation was ingested")
				// Ensure the old implementation ingests 101 ms before releasing
				// the sampled 100 ms observation, making the regression repeatable.
				<-secondDone
			case <-time.After(100 * time.Millisecond):
			}
			clock.mu.Lock()
			clock.paused = false
			clock.mu.Unlock()
			close(clock.resume)
			for _, done := range []chan struct{}{firstDone, secondDone} {
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("observation did not finish")
				}
			}
			if err = controller.Handle(ctx, simulator.Event{Kind: "ended", Binding: binding, AtMS: 101}); err != nil {
				t.Fatal(err)
			}
			report, err := controller.Report(ctx, snap.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if report.InvalidObservations != 0 || len(report.Failures) != 0 || report.AssistantSamples != tc.assistant || report.SimulatorSamples != tc.customer {
				t.Fatalf("concurrent observations lost evidence or poisoned report: %+v", report)
			}
			for _, check := range report.Checks {
				if check.Name == "clean_termination" && check.Result != "pass" {
					t.Fatalf("healthy media became unclean: %+v", check)
				}
			}
		})
	}
}
