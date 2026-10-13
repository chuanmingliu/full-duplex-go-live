package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/duplex"
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
	scenario := simulator.Scenario{SchemaVersion: 1, ID: "test-customer", Language: "en", Persona: "You are Sam, a brief customer.", Facts: map[string]string{"appointment": "Tuesday"}, Greeting: "Hello?", MaxDurationMS: 30000, MaxRemoteTurns: 10}
	scenario.Actions = actions
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
	deadline := time.Now().Add(time.Second)
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
	time.Sleep(30 * time.Millisecond)
	if err := controller.Handle(context.Background(), simulator.Event{Kind: "ended", Binding: bound.Binding, AtMS: controller.RelativeMS()}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(550 * time.Millisecond)
	ended, _ := controller.Get(context.Background(), snap.RunID)
	if ended.Phase != simulator.Completed {
		t.Fatalf("clean hangup became %s", ended.Phase)
	}
}
