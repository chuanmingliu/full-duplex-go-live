package simulator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const activeCallTestRun = "sim-0123456789abcdef01234567"

type activeCallTestController struct {
	mu         sync.Mutex
	incoming   []IncomingCall
	events     chan Event
	onIncoming func(IncomingCall) error
	onEvent    func(Event) error
}

func (c *activeCallTestController) Incoming(_ context.Context, in IncomingCall) error {
	c.mu.Lock()
	c.incoming = append(c.incoming, in)
	c.mu.Unlock()
	if c.onIncoming != nil {
		return c.onIncoming(in)
	}
	return nil
}
func (c *activeCallTestController) Handle(_ context.Context, e Event) error {
	if c.onEvent != nil {
		if err := c.onEvent(e); err != nil {
			return err
		}
	}
	c.events <- e
	return nil
}
func (c *activeCallTestController) Get(_ context.Context, id string) (Snapshot, error) {
	return Snapshot{RunID: id}, nil
}
func (c *activeCallTestController) calls() []IncomingCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]IncomingCall(nil), c.incoming...)
}

type activeCallHarness struct {
	adapter     *ActiveCall
	controller  *activeCallTestController
	server      *httptest.Server
	attached    chan *websocket.Conn
	ids         chan string
	commands    chan map[string]any
	connections []*websocket.Conn
	mu          sync.Mutex
}

func newActiveCallHarness(t *testing.T) *activeCallHarness {
	t.Helper()
	h := &activeCallHarness{controller: &activeCallTestController{events: make(chan Event, 16)}, attached: make(chan *websocket.Conn, 16), ids: make(chan string, 16), commands: make(chan map[string]any, 16)}
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "[]") })
	mux.HandleFunc("/call/sip", func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		h.mu.Lock()
		h.connections = append(h.connections, conn)
		h.mu.Unlock()
		h.ids <- r.URL.Query().Get("id")
		h.attached <- conn
		for {
			var command map[string]any
			if conn.ReadJSON(&command) != nil {
				return
			}
			h.commands <- command
		}
	})
	mux.HandleFunc("/command/", func(w http.ResponseWriter, r *http.Request) {
		var command map[string]any
		_ = json.NewDecoder(r.Body).Decode(&command)
		command["nativeID"] = strings.TrimPrefix(r.URL.Path, "/command/")
		h.commands <- command
		w.WriteHeader(http.StatusOK)
	})
	h.server = httptest.NewServer(mux)
	var err error
	h.adapter, err = NewActiveCall(ActiveCallOptions{ControlURL: h.server.URL, RealtimeURL: "ws://simulator:18891/v1/realtime", WebhookToken: "webhook-secret", MediaToken: strings.Repeat("x", 48), Destination: "simulator-receiver", GatewayIdentity: "isolated-active-call", SIPDomain: "simulator.invalid", CommandTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	h.adapter.SetController(h.controller)
	t.Cleanup(func() {
		_ = h.adapter.Close()
		h.mu.Lock()
		for _, conn := range h.connections {
			_ = conn.Close()
		}
		h.mu.Unlock()
		h.server.Close()
	})
	return h
}

func (h *activeCallHarness) webhook(dialogID, callee string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"event": "invite", "dialogId": dialogID, "sipCallId": "unrelated-sip-call@example.invalid", "callee": callee, "headers": []string{"From: <sip:test@example.invalid>"}})
	req := httptest.NewRequest(http.MethodPost, "/active-call/incoming", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer webhook-secret")
	w := httptest.NewRecorder()
	h.adapter.ServeHTTP(w, req)
	return w
}

func activeCallReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for gateway event")
		var zero T
		return zero
	}
}

func TestActiveCallNativeInviteAndExactAccept(t *testing.T) {
	h := newActiveCallHarness(t)
	native := "dialog+with:reserved; chars/123"
	response := h.webhook(native, "sip:"+activeCallTestRun+"@simulator.invalid")
	if response.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	if got := activeCallReceive(t, h.ids); got != native {
		t.Fatalf("native dialog ID not encoded intact: %q", got)
	}
	in := h.controller.calls()[0]
	if in.RunID != activeCallTestRun || in.Destination != "simulator-receiver" || in.GatewayIdentity != "isolated-active-call" || !safeID(in.CallID) || in.CallID == native {
		t.Fatalf("binding: %+v", in)
	}
	if err := h.adapter.Accept(context.Background(), in.CallID); err != nil {
		t.Fatal(err)
	}
	command := activeCallReceive(t, h.commands)
	if command["command"] != "accept" {
		t.Fatalf("wrong command: %+v", command)
	}
	option := command["option"].(map[string]any)
	realtime := option["realtime"].(map[string]any)
	provider, err := url.Parse(realtime["provider"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if provider.Path != "/v1/realtime" || provider.Query().Get("call") != in.CallID || provider.Query().Get("run") != in.RunID || provider.Query().Get("token") != strings.Repeat("x", 48) {
		t.Fatal("media URL lost exact binding or authentication")
	}
	if realtime["secretKey"] != "simulator-local-placeholder" || realtime["endpoint"] != nil || realtime["apiKey"] != nil || realtime["turnDetection"] != nil {
		t.Fatal("wrong custom realtime fields")
	}
	if option["inactivityTimeout"] != float64(0) || option["rtpTimeout"] != float64(0) {
		t.Fatalf("unbounded upstream timeouts: %+v", option)
	}
	conn := activeCallReceive(t, h.attached)
	if err = conn.WriteJSON(map[string]any{"event": "answer", "trackId": native}); err != nil {
		t.Fatal(err)
	}
	event := activeCallReceive(t, h.controller.events)
	if event.Kind != "answered" || event.Binding.RunID != in.RunID || event.Binding.CallID != in.CallID {
		t.Fatalf("wrong answer binding: %+v", event)
	}
	if err = h.adapter.Accept(context.Background(), in.CallID); err != nil {
		t.Fatal(err)
	}
	select {
	case duplicate := <-h.commands:
		t.Fatalf("duplicate accept: %+v", duplicate)
	default:
	}
	if err = h.adapter.Hangup(context.Background(), in.CallID); err != nil {
		t.Fatal(err)
	}
	command = activeCallReceive(t, h.commands)
	if command["command"] != "hangup" || command["initiator"] != "system" || command["refer"] != false {
		t.Fatalf("wrong hangup: %+v", command)
	}
}

func TestActiveCallCancelBeforeAccept(t *testing.T) {
	h := newActiveCallHarness(t)
	if h.webhook("cancel-dialog", "sip:"+activeCallTestRun+"@simulator.invalid").Code != http.StatusNoContent {
		t.Fatal("invite failed")
	}
	conn := activeCallReceive(t, h.attached)
	fixture, err := os.ReadFile("../../deploy/simulator/activecall/fixtures/cancel.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture = []byte(strings.ReplaceAll(string(fixture), "FIXTURE_DIALOG", "cancel-dialog"))
	if err = conn.WriteMessage(websocket.TextMessage, fixture); err != nil {
		t.Fatal(err)
	}
	event := activeCallReceive(t, h.controller.events)
	if event.Kind != "ended" || !strings.Contains(event.Reason, "cancelled") {
		t.Fatalf("CANCEL was not terminal: %+v", event)
	}
	if err = h.adapter.Accept(context.Background(), h.controller.calls()[0].CallID); err == nil {
		t.Fatal("accepted cancelled call")
	}
	select {
	case command := <-h.commands:
		t.Fatalf("command after CANCEL: %+v", command)
	default:
	}
}

func TestActiveCallCancelWhileIncomingBlocked(t *testing.T) {
	h := newActiveCallHarness(t)
	entered, release := make(chan struct{}), make(chan struct{})
	h.controller.onIncoming = func(in IncomingCall) error { close(entered); <-release; return nil }
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() { response <- h.webhook("early-cancel", "sip:"+activeCallTestRun+"@simulator.invalid") }()
	<-entered
	conn := activeCallReceive(t, h.attached)
	if err := conn.WriteJSON(map[string]any{"event": "hangup", "trackId": "early-cancel", "reason": "Canceled", "initiator": "caller"}); err != nil {
		t.Fatal(err)
	}
	// Incoming is intentionally blocked: the reader must still mark cancellation
	// immediately instead of waiting for answer-delay scheduling/admission.
	callID := h.controller.calls()[0].CallID
	call, _ := h.adapter.session(callID)
	deadline := time.Now().Add(time.Second)
	for {
		call.mu.Lock()
		terminal := call.terminal
		call.mu.Unlock()
		if terminal {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("reader did not consume early cancellation")
		}
		time.Sleep(time.Millisecond)
	}
	if err := h.adapter.Accept(context.Background(), callID); err == nil {
		close(release)
		t.Fatal("accepted after early CANCEL")
	}
	close(release)
	if activeCallReceive(t, response).Code != http.StatusNoContent {
		t.Fatal("invite failed")
	}
	if activeCallReceive(t, h.controller.events).Kind != "ended" {
		t.Fatal("missing early cancellation")
	}
}

func TestActiveCallDuplicateAndStaleNativeDialog(t *testing.T) {
	h := newActiveCallHarness(t)
	callee := "sip:" + activeCallTestRun + "@simulator.invalid"
	if h.webhook("duplicate", callee).Code != http.StatusNoContent || h.webhook("duplicate", callee).Code != http.StatusNoContent {
		t.Fatal("duplicate webhook failed")
	}
	if len(h.controller.calls()) != 1 {
		t.Fatal("duplicate webhook rebound run")
	}
	conn := activeCallReceive(t, h.attached)
	_ = conn.WriteJSON(map[string]any{"event": "hangup", "trackId": "duplicate"})
	_ = activeCallReceive(t, h.controller.events)
	if h.webhook("duplicate", callee).Code != http.StatusNoContent || len(h.controller.calls()) != 1 {
		t.Fatal("terminal webhook resurrected run")
	}
	if h.webhook("duplicate", "sip:sim-ffffffffffffffffffffffff@simulator.invalid").Code != http.StatusConflict {
		t.Fatal("old dialog claimed new reservation")
	}
}

func TestActiveCallUnknownDestinationRejected(t *testing.T) {
	for _, callee := range []string{"sip:fixed-number@simulator.invalid", "sip:" + activeCallTestRun + "@wrong.invalid", "sip:" + activeCallTestRun + "@simulator.invalid?run=latest"} {
		t.Run(callee, func(t *testing.T) {
			h := newActiveCallHarness(t)
			h.controller.onIncoming = func(in IncomingCall) error {
				if in.RunID != "" {
					t.Errorf("bad destination produced run %q", in.RunID)
				}
				_ = h.adapter.Reject(context.Background(), in.CallID, "unreserved")
				return ErrInvalidTransition
			}
			if h.webhook("bad-destination", callee).Code != http.StatusConflict {
				t.Fatal("unreserved destination admitted")
			}
			command := activeCallReceive(t, h.commands)
			if command["command"] != "reject" || command["code"] != float64(486) {
				t.Fatalf("wrong reject: %+v", command)
			}
		})
	}
}

func TestActiveCallControlCloseFailsExactBinding(t *testing.T) {
	h := newActiveCallHarness(t)
	native := "lost+control/id"
	if h.webhook(native, "sip:"+activeCallTestRun+"@simulator.invalid").Code != http.StatusNoContent {
		t.Fatal("invite failed")
	}
	conn := activeCallReceive(t, h.attached)
	_ = conn.Close()
	event := activeCallReceive(t, h.controller.events)
	in := h.controller.calls()[0]
	if event.Kind != "failed" || event.Binding.RunID != in.RunID || event.Binding.CallID != in.CallID {
		t.Fatalf("control close binding mismatch: %+v", event)
	}
	if err := h.adapter.Hangup(context.Background(), in.CallID); err != nil {
		t.Fatal(err)
	}
	command := activeCallReceive(t, h.commands)
	if command["nativeID"] != native || command["command"] != "hangup" {
		t.Fatalf("fallback did not target exact native call: %+v", command)
	}
}

func TestActiveCallHangupDeadlineClosesControl(t *testing.T) {
	h := newActiveCallHarness(t)
	_ = h.webhook("no-hangup-confirmation", "sip:"+activeCallTestRun+"@simulator.invalid")
	callID := h.controller.calls()[0].CallID
	if err := h.adapter.Hangup(context.Background(), callID); err != nil {
		t.Fatal(err)
	}
	first := activeCallReceive(t, h.commands)
	if first["command"] != "hangup" || first["nativeID"] != nil {
		t.Fatalf("wrong initial command: %+v", first)
	}
	fallback := activeCallReceive(t, h.commands)
	if fallback["command"] != "hangup" || fallback["nativeID"] != "no-hangup-confirmation" {
		t.Fatalf("wrong deadline fallback: %+v", fallback)
	}
	event := activeCallReceive(t, h.controller.events)
	if event.Kind != "failed" || event.Binding.CallID != callID {
		t.Fatalf("unconfirmed hangup reported success: %+v", event)
	}
	if h.adapter.Accept(context.Background(), callID) == nil {
		t.Fatal("expired control attachment remained usable")
	}
}

func TestActiveCallRejectWrongTrackAndTransferEvents(t *testing.T) {
	h := newActiveCallHarness(t)
	_ = h.webhook("main-dialog", "sip:"+activeCallTestRun+"@simulator.invalid")
	conn := activeCallReceive(t, h.attached)
	_ = conn.WriteJSON(map[string]any{"event": "hangup", "trackId": "other-dialog"})
	_ = conn.WriteJSON(map[string]any{"event": "hangup", "trackId": "main-dialog", "refer": true})
	_ = conn.WriteJSON(map[string]any{"event": "hangup", "trackId": "main-dialog"})
	event := activeCallReceive(t, h.controller.events)
	if event.Kind != "ended" {
		t.Fatalf("wrong terminal: %+v", event)
	}
	select {
	case duplicate := <-h.controller.events:
		t.Fatalf("unexpected event %+v", duplicate)
	default:
	}
}

func TestActiveCallWebhookValidationAndReadiness(t *testing.T) {
	h := newActiveCallHarness(t)
	for _, tc := range []struct {
		method, body, token string
		status              int
	}{
		{"POST", `{}`, "wrong", 401}, {"GET", `{}`, "webhook-secret", 405},
		{"POST", `{"event":"incoming","dialogId":"x","callee":"x"}`, "webhook-secret", 400},
		{"POST", strings.Repeat("x", 65537), "webhook-secret", 413},
		{"POST", `{"event":"invite","dialogId":"","callee":"x"}`, "webhook-secret", 400},
	} {
		r := httptest.NewRequest(tc.method, "/active-call/incoming", strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		h.adapter.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("got %d want %d", w.Code, tc.status)
		}
	}
	if len(h.controller.calls()) != 0 {
		t.Fatal("invalid webhook reached controller")
	}
	if err := h.adapter.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = h.adapter.Close()
	if err := h.adapter.Ready(context.Background()); err == nil {
		t.Fatal("closed adapter ready")
	}
}

func TestActiveCallUnknownCallCannotSendCommands(t *testing.T) {
	h := newActiveCallHarness(t)
	if h.adapter.Accept(context.Background(), "unknown") == nil || h.adapter.Hangup(context.Background(), "unknown") == nil || h.adapter.Reject(context.Background(), "unknown", "") == nil {
		t.Fatal("unknown call accepted")
	}
	select {
	case command := <-h.commands:
		t.Fatalf("unknown call emitted command: %+v", command)
	default:
	}
}

func TestActiveCallControllerCancellationAndStaleRun(t *testing.T) {
	h := newActiveCallHarness(t)
	clock := &fakeClock{now: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)}
	controller, err := NewController(Options{Telephony: h.adapter, Voice: &fakePorts{}, Token: "controller-only", Destination: "simulator-receiver", GatewayIdentity: "isolated-active-call", Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	h.controller.onIncoming = func(in IncomingCall) error { return controller.Incoming(ctx, in) }
	h.controller.onEvent = func(event Event) error { return controller.Handle(ctx, event) }
	cancelScenario := sampleScenario()
	cancelScenario.ExpectedOutcome = OutcomeCancelBeforeAnswer
	first, err := controller.Prepare(ctx, cancelScenario)
	if err != nil {
		t.Fatal(err)
	}
	// A PBX rewrites the Request-URI host to the trunk address. It must still
	// bind to this exact reservation, including when an old run arrives later.
	callee := "sip:" + first.RunID + "@" + h.adapter.controlURL.Hostname() + ":25060"
	if h.webhook("first-native", callee).Code != http.StatusNoContent {
		t.Fatal("invite failed")
	}
	if h.webhook("first-native", callee).Code != http.StatusNoContent {
		t.Fatal("duplicate failed")
	}
	clock.advance(1499 * time.Millisecond)
	conn := activeCallReceive(t, h.attached)
	_ = conn.WriteJSON(map[string]any{"event": "hangup", "trackId": "first-native", "reason": "Canceled", "initiator": "caller"})
	_ = activeCallReceive(t, h.controller.events)
	clock.advance(time.Millisecond)
	select {
	case command := <-h.commands:
		t.Fatalf("1499ms CANCEL allowed 1500ms Accept: %+v", command)
	default:
	}
	snapshot, _ := controller.Get(ctx, first.RunID)
	if !snapshot.Phase.Terminal() {
		t.Fatalf("cancel left phase %s", snapshot.Phase)
	}
	report, err := controller.Report(ctx, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range report.Checks {
		if check.Result != "pass" {
			t.Fatalf("actual native CANCEL did not pass expected cancellation: %+v", check)
		}
	}
	next, err := controller.Prepare(ctx, sampleScenario())
	if err != nil {
		t.Fatal(err)
	}
	if h.webhook("late-old-reservation", callee).Code != http.StatusConflict {
		t.Fatal("stale run claimed next reservation")
	}
	command := activeCallReceive(t, h.commands)
	if command["command"] != "reject" {
		t.Fatalf("old run was not rejected: %+v", command)
	}
	if h.webhook("next-native", "sip:"+next.RunID+"@simulator.invalid").Code != http.StatusNoContent {
		t.Fatal("next run not admitted")
	}
	clock.advance(1500 * time.Millisecond)
	command = activeCallReceive(t, h.commands)
	if command["command"] != "accept" {
		t.Fatalf("next run failed to answer: %+v", command)
	}
	provider := command["option"].(map[string]any)["realtime"].(map[string]any)["provider"].(string)
	u, _ := url.Parse(provider)
	if u.Query().Get("run") != next.RunID {
		t.Fatal("accept bound stale run")
	}
}
