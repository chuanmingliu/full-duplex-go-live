package simulator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

// ActiveCallCommit is the upstream version whose native wire contract is used.
const ActiveCallCommit = "eaf7ebc8f39193a289348989f6a09b0ace287da7"

// ActiveCallOptions describes an isolated receiving gateway. SIPDomain is the
// called URI's host (including its port, if present); Destination is the logical
// destination configured on the controller. The SIP extension is the run ID.
// Forwarded requests may also name the ControlURL host or one of its resolved
// addresses, with an optional SIP port. Only that configured host is resolved.
type ActiveCallOptions struct {
	ControlURL, RealtimeURL                 string
	WebhookToken, MediaToken                string
	Destination, GatewayIdentity, SIPDomain string
	DialTimeout, CommandTimeout             time.Duration
}

type ActiveCallController interface {
	Incoming(context.Context, IncomingCall) error
	Handle(context.Context, Event) error
	Get(context.Context, string) (Snapshot, error)
}

// ActiveCall owns only inbound control attachments. It has no dialing API.
// SetController breaks the constructor cycle with RunController's Telephony.
type ActiveCall struct {
	mu           sync.Mutex
	opts         ActiveCallOptions
	controlURL   *url.URL
	mediaURL     *url.URL
	controller   ActiveCallController
	calls        map[string]*activeCallSession
	closed       bool
	dialer       websocket.Dialer
	client       *http.Client
	lookupIPAddr func(context.Context, string) ([]net.IPAddr, error)
}

type activeCallSession struct {
	mu          sync.Mutex // also serializes command writes with observed termination
	dialogID    string
	callID      string
	runID       string
	callee      string
	conn        *websocket.Conn
	ready       chan struct{}
	admitted    bool
	accepted    bool // never retry an uncertain Accept
	terminal    bool
	ended       bool
	hangupTimer *time.Timer
}

type activeCallInvite struct {
	DialogID  string `json:"dialogId"`
	SIPCallID string `json:"sipCallId"`
	Event     string `json:"event"`
	Callee    string `json:"callee"`
}

type activeCallEvent struct {
	Event     string `json:"event"`
	TrackID   string `json:"trackId"`
	Reason    string `json:"reason"`
	Initiator string `json:"initiator"`
	Refer     bool   `json:"refer"`
}

var activeCallRunID = regexp.MustCompile(`^sim-[0-9a-f]{24}$`)

func NewActiveCall(opts ActiveCallOptions) (*ActiveCall, error) {
	if opts.WebhookToken == "" || len(opts.MediaToken) < 32 || opts.Destination == "" || opts.GatewayIdentity == "" || opts.SIPDomain == "" {
		return nil, errors.New("webhook token, 32-character media token, destination, gateway identity and SIP domain are required")
	}
	if strings.ContainsAny(opts.SIPDomain, " /@;?#\r\n") {
		return nil, errors.New("invalid receiving SIP domain")
	}
	control, err := activeCallURL(opts.ControlURL, false)
	if err != nil {
		return nil, err
	}
	media, err := activeCallURL(opts.RealtimeURL, true)
	if err != nil {
		return nil, err
	}
	if control.Path != "" && control.Path != "/" {
		return nil, errors.New("Active Call control URL must be an origin")
	}
	if media.Path != "/v1/realtime" || media.RawQuery != "" {
		return nil, errors.New("realtime URL must end at /v1/realtime without query")
	}
	if opts.DialTimeout == 0 {
		opts.DialTimeout = 3 * time.Second
	}
	if opts.CommandTimeout == 0 {
		opts.CommandTimeout = 3 * time.Second
	}
	if opts.DialTimeout <= 0 || opts.DialTimeout > 10*time.Second || opts.CommandTimeout <= 0 || opts.CommandTimeout > 10*time.Second {
		return nil, errors.New("gateway deadlines must be positive and at most 10 seconds")
	}
	return &ActiveCall{opts: opts, controlURL: control, mediaURL: media, calls: make(map[string]*activeCallSession), dialer: websocket.Dialer{HandshakeTimeout: opts.DialTimeout}, client: &http.Client{Timeout: opts.CommandTimeout}, lookupIPAddr: net.DefaultResolver.LookupIPAddr}, nil
}

func activeCallURL(raw string, media bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (!media && u.RawQuery != "") {
		return nil, errors.New("invalid gateway URL")
	}
	if u.Scheme != "ws" && u.Scheme != "wss" && (media || (u.Scheme != "http" && u.Scheme != "https")) {
		return nil, errors.New("invalid gateway URL scheme")
	}
	return u, nil
}

// SetController must be called once before serving any requests.
func (a *ActiveCall) SetController(c ActiveCallController) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.controller == nil && !a.closed {
		a.controller = c
	}
}

// ServeHTTP consumes the pinned native POST invite webhook. Gateway identity is
// derived from the configured authenticated adapter, never from request JSON.
func (a *ActiveCall) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+a.opts.WebhookToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "webhook too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid webhook", http.StatusBadRequest)
		}
		return
	}
	var in activeCallInvite
	if !utf8.Valid(body) || json.Unmarshal(body, &in) != nil || in.Event != "invite" || !validDialogID(in.DialogID) || in.Callee == "" {
		http.Error(w, "invalid native invite", http.StatusBadRequest)
		return
	}
	controller, call, duplicate, err := a.register(r.Context(), in)
	if err != nil {
		http.Error(w, "gateway unavailable", http.StatusServiceUnavailable)
		return
	}
	if duplicate {
		if call.callee != in.Callee {
			http.Error(w, "dialog identity conflict", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.opts.DialTimeout)
	defer cancel()
	attachURL := *a.controlURL
	if attachURL.Scheme == "http" {
		attachURL.Scheme = "ws"
	}
	if attachURL.Scheme == "https" {
		attachURL.Scheme = "wss"
	}
	attachURL.Path = "/call/sip"
	attachURL.RawQuery = url.Values{"id": {call.dialogID}, "dump": {"false"}}.Encode()
	conn, response, err := a.dialer.DialContext(ctx, attachURL.String(), nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		call.mu.Lock()
		call.terminal = true
		call.mu.Unlock()
		close(call.ready)
		http.Error(w, "control attachment failed", http.StatusBadGateway)
		return
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_ = conn.Close()
		call.mu.Lock()
		call.terminal = true
		call.mu.Unlock()
		close(call.ready)
		http.Error(w, "gateway unavailable", http.StatusServiceUnavailable)
		return
	}
	call.mu.Lock()
	call.conn = conn
	call.mu.Unlock()
	a.mu.Unlock()
	conn.SetReadLimit(64 << 10)
	// Start consuming CANCEL/close before the controller schedules answer delay.
	go a.readControl(controller, call)
	err = controller.Incoming(r.Context(), IncomingCall{RunID: call.runID, CallID: call.callID, Destination: a.opts.Destination, GatewayIdentity: a.opts.GatewayIdentity})
	call.mu.Lock()
	call.admitted = err == nil
	call.mu.Unlock()
	close(call.ready)
	if err != nil {
		_ = conn.Close()
		http.Error(w, "reservation rejected", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validDialogID(id string) bool {
	if id == "" || len(id) > 1024 {
		return false
	}
	for _, c := range id {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}

func (a *ActiveCall) register(ctx context.Context, in activeCallInvite) (ActiveCallController, *activeCallSession, bool, error) {
	// DNS may be needed for a PBX-forwarded numeric Request-URI. Keep it off
	// the adapter mutex so call termination/control remain independent of DNS.
	runID := a.runFromCallee(ctx, in.Callee)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.controller == nil {
		return nil, nil, false, ErrNotReady
	}
	hash := sha256.Sum256([]byte(in.DialogID))
	callID := "ac-" + hex.EncodeToString(hash[:])
	if call, ok := a.calls[callID]; ok {
		return a.controller, call, true, nil
	}
	// Tombstones prevent old/repeated dialogs being rebound. Fail closed at the
	// bound instead of forgetting a stale dialog or growing memory indefinitely.
	if len(a.calls) >= 4096 {
		return nil, nil, false, ErrNotReady
	}
	call := &activeCallSession{dialogID: in.DialogID, callID: callID, runID: runID, callee: in.Callee, ready: make(chan struct{})}
	a.calls[callID] = call
	return a.controller, call, false, nil
}

func (a *ActiveCall) readControl(controller ActiveCallController, call *activeCallSession) {
	defer call.conn.Close()
	for {
		kind, body, err := call.conn.ReadMessage()
		if err != nil {
			a.terminal(controller, call, "failed", "Active Call control disconnected", false)
			return
		}
		if kind != websocket.TextMessage {
			continue
		}
		var event activeCallEvent
		if json.Unmarshal(body, &event) != nil {
			a.terminal(controller, call, "failed", "invalid Active Call control event", false)
			return
		}
		if event.Refer || (event.TrackID != "" && event.TrackID != call.dialogID) {
			continue
		}
		switch event.Event {
		case "answer":
			<-call.ready
			call.mu.Lock()
			active := call.admitted && !call.terminal && call.accepted
			call.mu.Unlock()
			if active {
				_ = controller.Handle(context.Background(), Event{Kind: "answered", Binding: Binding{RunID: call.runID, CallID: call.callID}})
			}
		case "hangup":
			reason := "Active Call ended"
			if event.Reason == "Canceled" && event.Initiator == "caller" {
				reason = NativeCancelBeforeAnswerReason
			}
			a.terminal(controller, call, "ended", reason, true, event)
			return
		case "reject", "error":
			a.terminal(controller, call, "failed", "Active Call rejected or failed", event.Event == "reject")
			return
		}
	}
}

func (a *ActiveCall) terminal(controller ActiveCallController, call *activeCallSession, kind, reason string, ended bool, native ...activeCallEvent) {
	call.mu.Lock()
	first := !call.terminal
	call.terminal = true
	call.ended = call.ended || ended
	if call.hangupTimer != nil {
		call.hangupTimer.Stop()
	}
	call.mu.Unlock()
	if !first {
		return
	}
	<-call.ready
	call.mu.Lock()
	admitted := call.admitted
	call.mu.Unlock()
	if admitted {
		event := Event{Kind: kind, Binding: Binding{RunID: call.runID, CallID: call.callID}, Reason: reason}
		if len(native) > 0 {
			termination := nativeTermination(native[0].Initiator, native[0].Reason)
			event.Termination = &termination
		}
		_ = controller.Handle(context.Background(), event)
	}
}

func (a *ActiveCall) session(callID string) (*activeCallSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	call, ok := a.calls[callID]
	if !ok {
		return nil, ErrNotFound
	}
	return call, nil
}

func (a *ActiveCall) Accept(ctx context.Context, callID string) error {
	call, err := a.session(callID)
	if err != nil {
		return err
	}
	call.mu.Lock()
	defer call.mu.Unlock()
	if call.terminal || call.conn == nil || call.runID == "" {
		return ErrInvalidTransition
	}
	if call.accepted {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	provider := *a.mediaURL
	provider.RawQuery = url.Values{"run": {call.runID}, "call": {call.callID}, "token": {a.opts.MediaToken}}.Encode()
	command := map[string]any{"command": "accept", "option": map[string]any{
		"inactivityTimeout": 0, "rtpTimeout": 0,
		"realtime": map[string]any{"provider": provider.String(), "secretKey": "simulator-local-placeholder", "tools": []any{}, "extra": map[string]string{"instructions": "Simulated inbound customer", "voice": "alloy"}},
	}}
	// Mark before writing: a partial write has an unknown result and must not
	// cause a second Accept. The controller's setup deadline resolves uncertainty.
	call.accepted = true
	return a.writeLocked(ctx, call, command)
}

func (a *ActiveCall) Reject(ctx context.Context, callID, _ string) error {
	call, err := a.session(callID)
	if err != nil {
		return err
	}
	call.mu.Lock()
	defer call.mu.Unlock()
	if call.terminal {
		return nil
	}
	defer func() {
		call.terminal = true
		call.ended = true
		if call.conn != nil {
			_ = call.conn.Close()
		}
	}()
	return a.writeLocked(ctx, call, map[string]any{"command": "reject", "code": 486, "reason": "Reservation unavailable"})
}

func (a *ActiveCall) Hangup(ctx context.Context, callID string) error {
	call, err := a.session(callID)
	if err != nil {
		return err
	}
	command := map[string]any{"command": "hangup", "reason": "normal", "initiator": "system", "refer": false}
	call.mu.Lock()
	if call.ended {
		call.mu.Unlock()
		return nil
	}
	wasTerminal := call.terminal
	err = a.writeLocked(ctx, call, command)
	if err == nil && !wasTerminal && call.hangupTimer == nil {
		call.hangupTimer = time.AfterFunc(a.opts.CommandTimeout, func() {
			call.mu.Lock()
			ended := call.ended
			conn := call.conn
			call.mu.Unlock()
			if ended {
				return
			}
			deadline, cancel := context.WithTimeout(context.Background(), a.opts.CommandTimeout)
			defer cancel()
			_ = a.hangupREST(deadline, call.dialogID, command)
			// A missing hangup confirmation must not retain a reader indefinitely.
			// Its close event fails the exact binding instead of reporting success.
			if conn != nil {
				_ = conn.Close()
			}
		})
	}
	call.mu.Unlock()
	if err == nil && !wasTerminal {
		return nil
	}
	// A disconnected WS does not prove the SIP dialog ended. The exact native
	// ID REST control route is the sole fallback, never a new attachment/dial.
	return a.hangupREST(ctx, call.dialogID, command)
}

func (a *ActiveCall) hangupREST(ctx context.Context, dialogID string, command any) error {
	endpoint := *a.controlURL
	if endpoint.Scheme == "ws" {
		endpoint.Scheme = "http"
	}
	if endpoint.Scheme == "wss" {
		endpoint.Scheme = "https"
	}
	endpoint.Path = "/command/" + dialogID
	endpoint.RawPath = "/command/" + url.PathEscape(dialogID)
	body, _ := json.Marshal(command)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return errors.New("Active Call exact hangup failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Active Call hangup status %d", resp.StatusCode)
	}
	return nil
}

func (a *ActiveCall) writeLocked(ctx context.Context, call *activeCallSession, command any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if call.conn == nil {
		return ErrNotReady
	}
	deadline := time.Now().Add(a.opts.CommandTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := call.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	if err := call.conn.WriteJSON(command); err != nil {
		return errors.New("Active Call command write failed")
	}
	return nil
}

// Ready checks the dedicated gateway's call-list endpoint without attaching or
// creating any call. Responses and URLs are deliberately omitted from errors.
func (a *ActiveCall) Ready(ctx context.Context) error {
	a.mu.Lock()
	ready := !a.closed && a.controller != nil
	a.mu.Unlock()
	if !ready {
		return ErrNotReady
	}
	endpoint := *a.controlURL
	if endpoint.Scheme == "ws" {
		endpoint.Scheme = "http"
	}
	if endpoint.Scheme == "wss" {
		endpoint.Scheme = "https"
	}
	endpoint.Path = "/list"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return ErrNotReady
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ErrNotReady
	}
	var body json.RawMessage
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body) != nil {
		return ErrNotReady
	}
	return nil
}

func (a *ActiveCall) Close() error {
	a.mu.Lock()
	a.closed = true
	calls := make([]*activeCallSession, 0, len(a.calls))
	for _, c := range a.calls {
		calls = append(calls, c)
	}
	a.mu.Unlock()
	for _, call := range calls {
		call.mu.Lock()
		if call.hangupTimer != nil {
			call.hangupTimer.Stop()
		}
		if call.conn != nil {
			_ = call.conn.Close()
		}
		call.mu.Unlock()
	}
	return nil
}

var _ Telephony = (*ActiveCall)(nil)
