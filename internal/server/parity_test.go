package server

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/duplex"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
)

// These tests speak to the server the way a gpt-live-1 client does: raw JSON
// in, raw JSON out, with the field names taken from OpenAI's guides rather than
// from golive's Go structs. A test that marshals a Go struct and unmarshals the
// reply into the same struct proves the two ends agree with each other, which
// is not the claim being made.

// --- harness ---

func newServerWith(t *testing.T, mutate func(*config.Config)) (*httptest.Server, *Server, config.Config) {
	t.Helper()
	cfg := config.Default()
	cfg.WebRoot = ""
	cfg.Duplex.Backchannel = false
	cfg.ClientRate = 16000
	cfg.Greeting = ""
	if mutate != nil {
		mutate(&cfg)
	}
	srv := NewServer(cfg, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv, cfg
}

func wsURL(ts *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(ts.URL, "http") + path
}

func dialPath(t *testing.T, ts *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(ts, path), nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dialing %s: %v (status %d)", path, err, status)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func sendRaw(t *testing.T, conn *websocket.Conn, raw string) {
	t.Helper()
	if !json.Valid([]byte(raw)) {
		t.Fatalf("test bug: not JSON: %s", raw)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
		t.Fatalf("writing %s: %v", raw, err)
	}
}

func object(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("not a JSON object: %v: %s", err, raw)
	}
	return obj
}

func str(t *testing.T, obj map[string]json.RawMessage, name string) string {
	t.Helper()
	raw, ok := obj[name]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("%s is not a string: %s", name, raw)
	}
	return s
}

func num(t *testing.T, obj map[string]json.RawMessage, name string) (float64, bool) {
	t.Helper()
	raw, ok := obj[name]
	if !ok {
		return 0, false
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("%s is not a number: %s", name, raw)
	}
	return n, true
}

// first waits for the first event of a type and returns it as an object.
func (r *reader) first1(t *testing.T, kind string, timeout time.Duration) map[string]json.RawMessage {
	t.Helper()
	await(t, kind, timeout, func() bool { return r.count(kind) > 0 })
	return object(t, r.events(kind)[0])
}

func (r *reader) errorCodes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.errs))
	for _, e := range r.errs {
		out = append(out, e.Error.Code)
	}
	return out
}

func (r *reader) sawError(code string) bool {
	for _, c := range r.errorCodes() {
		if c == code {
			return true
		}
	}
	return false
}

// said concatenates the assistant's transcript the way a client would.
func (r *reader) said(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, raw := range r.events(live.ServerOutputTranscriptDelta) {
		b.WriteString(str(t, object(t, raw), "delta"))
	}
	return b.String()
}

// functionCall finds the first function call announced in a response.event.
func (r *reader) functionCall(t *testing.T) (callID, name, delegationID string, ok bool) {
	t.Helper()
	for _, raw := range r.events(live.ServerResponseEvent) {
		obj := object(t, raw)
		inner := object(t, obj["event"])
		if str(t, inner, "type") != "response.output_item.done" {
			continue
		}
		item := object(t, inner["item"])
		if str(t, item, "type") != "function_call" {
			continue
		}
		return str(t, item, "call_id"), str(t, item, "name"), str(t, obj, "delegation_id"), true
	}
	return "", "", "", false
}

func speakTurn(t *testing.T, conn *websocket.Conn, rate, voicedMS int) {
	t.Helper()
	streamPCM(t, conn, voicedPCM(rate, voicedMS), rate)
	streamPCM(t, conn, make([]byte, audio.PCM16(rate).BytesForMS(700)), rate)
}

const startResponses = `{"type":"session.start","event_id":"evt_start","session":{
	"model":"gpt-live-1",
	"audio":{"format":{"type":"audio/pcm","rate":16000},"output":{"voice":"marin"}},
	"delegation":{"type":"responses"}}}`

// --- the shapes a gpt-live-1 client reads ---

func TestEventsCarryGptLive1FieldNames(t *testing.T) {
	ts, _, cfg := newServerWith(t, nil)
	conn := dialPath(t, ts, "/v1/live/sessions")
	r := startReader(conn)

	sendRaw(t, conn, startResponses)
	started := r.first1(t, live.ServerSessionStarted, 2*time.Second)
	if got := str(t, started, "client_event_id"); got != "evt_start" {
		t.Errorf("session.started.client_event_id = %q, want the id of the command it answers", got)
	}
	if got := str(t, started, "event_id"); !strings.HasPrefix(got, "event_") {
		t.Errorf("session.started.event_id = %q; every server event carries its own id", got)
	}
	session := object(t, started["session"])
	sessionID := str(t, session, "id")
	if !strings.HasPrefix(sessionID, "sess_") {
		t.Errorf("session.id = %q", sessionID)
	}
	if got := str(t, session, "model"); got != "gpt-live-1" {
		t.Errorf("session.model = %q; the model a client asked for is the one it is told it has", got)
	}

	speakTurn(t, conn, cfg.ClientRate, 1400)
	await(t, "assistant audio", 6*time.Second, func() bool {
		return r.audioBytes() > audio.PCM16(cfg.ClientRate).BytesForMS(200)
	})

	in := r.first1(t, live.ServerInputTranscriptDelta, time.Second)
	if str(t, in, "delta") == "" {
		t.Errorf("input transcript has no delta: %s", r.events(live.ServerInputTranscriptDelta)[0])
	}
	if _, ok := num(t, in, "start_ms"); !ok {
		t.Error("input transcript has no start_ms")
	}
	if _, ok := num(t, in, "end_ms"); !ok {
		t.Error("input transcript has no end_ms")
	}
	for _, old := range []string{"content", "replace"} {
		if _, ok := in[old]; ok {
			t.Errorf("input transcript still carries %q, which gpt-live-1 does not have", old)
		}
	}

	out := r.first1(t, live.ServerOutputAudioDelta, time.Second)
	pcm, err := base64.StdEncoding.DecodeString(str(t, out, "delta"))
	if err != nil || len(pcm) == 0 {
		t.Fatalf("output audio is not base64 in delta: %s", r.events(live.ServerOutputAudioDelta)[0])
	}
	if _, ok := out["audio"]; ok {
		t.Error("output audio is duplicated into the old audio field with duplex.legacy_audio_field off")
	}
	startMS, ok1 := num(t, out, "start_ms")
	endMS, ok2 := num(t, out, "end_ms")
	if !ok1 || !ok2 || endMS <= startMS {
		t.Errorf("output audio spans start_ms=%v end_ms=%v", startMS, endMS)
	} else if want := float64(len(pcm)) / 2 / float64(cfg.ClientRate) * 1000; endMS-startMS < want-1 || endMS-startMS > want+1 {
		t.Errorf("output audio claims %v ms and carries %v ms of samples", endMS-startMS, want)
	}

	if got := str(t, r.first1(t, live.ServerOutputTranscriptDelta, 2*time.Second), "delta"); got == "" {
		t.Error("output transcript has no delta")
	}

	created := r.first1(t, live.ServerDelegationCreated, time.Second)
	if _, ok := num(t, created, "offset_ms"); !ok {
		t.Error("session.delegation.created has no offset_ms")
	}
	delegation := object(t, created["delegation"])
	delegationID := str(t, delegation, "id")
	if delegationID == "" || str(t, delegation, "type") != "delegation" ||
		str(t, delegation, "target") != "responses" || str(t, delegation, "response_id") == "" {
		t.Errorf("delegation object is malformed: %s", created["delegation"])
	}
	resp := r.first1(t, live.ServerResponseEvent, time.Second)
	if got := str(t, resp, "delegation_id"); got != delegationID {
		t.Errorf("response.event.delegation_id = %q, want %q", got, delegationID)
	}
	if got := str(t, object(t, resp["event"]), "type"); got != "response.created" {
		t.Errorf("the first nested Responses event is %q, want response.created", got)
	}

	// Commands are acknowledged with the id they were sent under.
	sendRaw(t, conn, `{"type":"session.input_audio.mute","event_id":"evt_mute"}`)
	if got := str(t, r.first1(t, live.ServerInputAudioMuted, time.Second), "client_event_id"); got != "evt_mute" {
		t.Errorf("session.input_audio.muted.client_event_id = %q", got)
	}
	sendRaw(t, conn, `{"type":"session.input_audio.unmute","event_id":"evt_unmute"}`)
	if got := str(t, r.first1(t, live.ServerInputAudioUnmuted, time.Second), "client_event_id"); got != "evt_unmute" {
		t.Errorf("session.input_audio.unmuted.client_event_id = %q", got)
	}

	// An error names the command that caused it, inside the error object.
	sendRaw(t, conn, `{"type":"session.commentary.append","event_id":"evt_bad",
		"delegation_id":"dlg_nope","content":"没有这个委托。"}`)
	errEv := r.first1(t, live.ServerError, 2*time.Second)
	body := object(t, errEv["error"])
	if str(t, body, "code") != "unknown_delegation_id" || str(t, body, "client_event_id") != "evt_bad" ||
		str(t, body, "type") == "" || str(t, body, "message") == "" {
		t.Errorf("error object is malformed: %s", errEv["error"])
	}

	sendRaw(t, conn, `{"type":"session.close","event_id":"evt_close"}`)
	closed := r.first1(t, live.ServerSessionClosed, 3*time.Second)
	if got := str(t, closed, "reason"); got != "close_requested" {
		t.Errorf("session.closed.reason = %q", got)
	}
	if secs, ok := num(t, object(t, closed["usage"]), "seconds"); !ok || secs <= 0 {
		t.Errorf("session.closed.usage.seconds = %v", secs)
	}
	if got := str(t, object(t, closed["session"]), "id"); got != sessionID {
		t.Errorf("session.closed.session.id = %q, want %q", got, sessionID)
	}

	// gpt-live-1 has no turns to finish, and so no event that says one has.
	for _, gone := range []string{"response.done", "session.response.done", "input_audio_buffer.committed"} {
		if r.count(gone) != 0 {
			t.Errorf("the server sent %s, which belongs to the turn-based protocol", gone)
		}
	}
}

func TestLegacyAudioFieldIsOptIn(t *testing.T) {
	ts, _, cfg := newServerWith(t, func(c *config.Config) { c.Duplex.LegacyAudioField = true })
	conn := dialPath(t, ts, "/v1/live")
	r := startReader(conn)
	sendRaw(t, conn, startResponses)
	speakTurn(t, conn, cfg.ClientRate, 1400)

	out := r.first1(t, live.ServerOutputAudioDelta, 6*time.Second)
	if str(t, out, "audio") == "" || str(t, out, "audio") != str(t, out, "delta") {
		t.Errorf("with duplex.legacy_audio_field on, audio should mirror delta for clients written before it")
	}
}

func TestPlainRequestsToTheSessionEndpointAreToldWhy(t *testing.T) {
	ts, _, _ := newServerWith(t, nil)

	// gpt-live-1 takes a WebRTC offer here. This server does not, and says so.
	resp, err := http.Post(ts.URL+"/v1/live/sessions", "application/sdp", strings.NewReader("v=0"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented || !strings.Contains(string(body), "WebRTC") {
		t.Errorf("POST answered %d %s; want 501 and a reason", resp.StatusCode, body)
	}

	resp, err = http.Get(ts.URL + "/v1/live/sessions")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("GET without an upgrade answered %d, want 426", resp.StatusCode)
	}
}

// --- telephony audio ---

func TestG711SessionOverTheWire(t *testing.T) {
	ts, _, _ := newServerWith(t, nil)
	conn := dialPath(t, ts, "/v1/live/sessions")
	r := startReader(conn)

	sendRaw(t, conn, `{"type":"session.start","session":{
		"audio":{"format":{"type":"audio/pcmu"}},"delegation":{"type":"responses"}}}`)
	started := r.first1(t, live.ServerSessionStarted, 2*time.Second)
	format := object(t, object(t, object(t, started["session"])["audio"])["format"])
	if rate, _ := num(t, format, "rate"); rate != 8000 || str(t, format, "type") != "audio/pcmu" {
		t.Fatalf("negotiated format is %s; audio/pcmu is 8000 Hz by definition", object(t, object(t, started["session"])["audio"])["format"])
	}

	// One byte a sample: 160 bytes is 20 ms.
	speech := audio.CodecMuLaw.FromPCM16(voicedPCM(8000, 1400))
	silence := audio.CodecMuLaw.FromPCM16(make([]byte, audio.PCM16(8000).BytesForMS(700)))
	for _, chunk := range [][]byte{speech, silence} {
		for off := 0; off < len(chunk); off += 160 {
			end := off + 160
			if end > len(chunk) {
				end = len(chunk)
			}
			send(t, conn, map[string]string{
				"type":  live.ClientInputAudioAppend,
				"audio": base64.StdEncoding.EncodeToString(chunk[off:end]),
			})
			time.Sleep(20 * time.Millisecond)
		}
	}

	await(t, "a transcript of μ-law speech", 4*time.Second, func() bool { return r.finalTranscript() != "" })
	await(t, "μ-law audio back", 6*time.Second, func() bool { return r.audioBytes() > 8000/5 })
	if codes := r.errorCodes(); len(codes) != 0 {
		t.Fatalf("errors: %v", codes)
	}

	out := r.first1(t, live.ServerOutputAudioDelta, time.Second)
	wire, _ := base64.StdEncoding.DecodeString(str(t, out, "delta"))
	startMS, _ := num(t, out, "start_ms")
	endMS, _ := num(t, out, "end_ms")
	if got, want := float64(len(wire)), (endMS-startMS)*8; got < want-8 || got > want+8 {
		t.Errorf("a %v ms chunk is %v bytes on the wire; G.711 is one byte a sample, so %v", endMS-startMS, got, want)
	}
	r.mu.Lock()
	all := append([]byte(nil), r.wire...)
	r.mu.Unlock()
	samples, err := audio.DecodePCM16(audio.CodecMuLaw.ToPCM16(all))
	if err != nil {
		t.Fatal(err)
	}
	if level := audio.DBFS(audio.RMS(samples)); level < -40 {
		t.Errorf("the reply decodes from μ-law to %.1f dBFS; it should be audible speech", level)
	}
}

func TestG711RefusesAnyOtherRate(t *testing.T) {
	ts, _, _ := newServerWith(t, nil)
	conn := dialPath(t, ts, "/v1/live")
	r := startReader(conn)
	sendRaw(t, conn, `{"type":"session.start","event_id":"s","session":{
		"audio":{"format":{"type":"audio/pcma","rate":16000}}}}`)
	await(t, "an error", 2*time.Second, func() bool { return r.sawError("invalid_session") })
	if r.count(live.ServerSessionStarted) != 0 {
		t.Error("a session started on a format that does not exist")
	}
}

// --- sidebands ---

func TestSidebandObservesAndSteersTheCall(t *testing.T) {
	ts, _, cfg := newServerWith(t, nil)
	primary := dialPath(t, ts, "/v1/live/sessions")
	pr := startReader(primary)
	sendRaw(t, primary, `{"type":"session.start","session":{
		"audio":{"format":{"type":"audio/pcm","rate":16000}},"delegation":{"type":"client"}}}`)
	started := pr.first1(t, live.ServerSessionStarted, 2*time.Second)
	id := str(t, object(t, started["session"]), "id")

	side := dialPath(t, ts, "/v1/live/sessions/"+id+"/attach")
	sr := startReader(side)
	time.Sleep(100 * time.Millisecond)

	// It joins a session that is already running, and cannot carry its audio.
	sendRaw(t, side, startResponses)
	await(t, "session.start to be refused on a sideband", 2*time.Second, func() bool {
		return sr.sawError("already_started")
	})
	sendRaw(t, side, `{"type":"session.input_audio.append","audio":"AAAA"}`)
	await(t, "audio to be refused on a sideband", 2*time.Second, func() bool {
		return sr.sawError("audio_on_sideband")
	})
	if pr.count(live.ServerError) != 0 {
		t.Errorf("the sideband's mistakes were reported to the caller's connection: %v", pr.errorCodes())
	}

	speakTurn(t, primary, cfg.ClientRate, 1400)

	// The sideband hears the caller, as the event the caller's connection sent.
	reflected := sr.first1(t, live.ClientInputAudioAppend, 2*time.Second)
	if str(t, reflected, "audio") == "" {
		t.Error("the reflected audio event carries no audio")
	}
	if _, ok := reflected["event_id"]; ok {
		t.Error("reflected audio carries an event_id; it is not something the service said")
	}
	if pr.count(live.ClientInputAudioAppend) != 0 {
		t.Error("the caller's own audio was echoed back to them")
	}

	// And it is the one that does the work.
	created := sr.first1(t, live.ServerDelegationCreated, 4*time.Second)
	delegationID := str(t, object(t, created["delegation"]), "id")
	send(t, side, map[string]string{
		"type": live.ClientCommentaryAppend, "event_id": "evt_result",
		"delegation_id": delegationID, "content": "明天上海晴，二十度。",
	})
	await(t, "the sideband's answer to reach the caller", 4*time.Second, func() bool {
		return pr.audioBytes() > 0 && strings.Contains(pr.said(t), "明天上海晴")
	})
	out := sr.first1(t, live.ServerOutputAudioDelta, 2*time.Second)
	if str(t, out, "delta") == "" {
		t.Error("the sideband was not shown the assistant's audio")
	}
	if got := str(t, sr.first1(t, live.ServerCommentaryAppended, time.Second), "client_event_id"); got != "evt_result" {
		t.Errorf("acknowledgement carries client_event_id %q", got)
	}

	// A sideband leaving is not the call ending.
	side.Close()
	time.Sleep(200 * time.Millisecond)
	sendRaw(t, primary, `{"type":"session.input_audio.mute","event_id":"after"}`)
	pr.first1(t, live.ServerInputAudioMuted, 2*time.Second)
	if pr.count(live.ServerSessionClosed) != 0 {
		t.Error("the session closed when its sideband left")
	}

	// A session that ends takes a sideband with it, and tells it why.
	side2 := dialPath(t, ts, "/v1/live/sessions/"+id+"/attach")
	sr2 := startReader(side2)
	time.Sleep(100 * time.Millisecond)
	sendRaw(t, primary, `{"type":"session.close"}`)
	closed := sr2.first1(t, live.ServerSessionClosed, 3*time.Second)
	if got := str(t, closed, "reason"); got != "close_requested" {
		t.Errorf("the sideband was told the session closed for %q", got)
	}
}

func TestAttachToAnUnknownSessionIsNotFound(t *testing.T) {
	ts, _, _ := newServerWith(t, nil)
	_, resp, err := websocket.DefaultDialer.Dial(wsURL(ts, "/v1/live/sessions/sess_0123456789abcdef/attach"), nil)
	if err == nil {
		t.Fatal("attached to a session that does not exist")
	}
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %v, want 404", resp)
	}
}

func TestSidebandsRequireTheSameCredentials(t *testing.T) {
	ts, _, _ := newServerWith(t, func(c *config.Config) { c.AuthToken = "s3cret" })
	header := http.Header{"Authorization": []string{"Bearer s3cret"}}
	primary, _, err := websocket.DefaultDialer.Dial(wsURL(ts, "/v1/live/sessions"), header)
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	pr := startReader(primary)
	sendRaw(t, primary, startResponses)
	id := str(t, object(t, pr.first1(t, live.ServerSessionStarted, 2*time.Second)["session"]), "id")

	for _, path := range []string{"/attach", "/fork"} {
		_, resp, err := websocket.DefaultDialer.Dial(wsURL(ts, "/v1/live/sessions/"+id+path), nil)
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without credentials: err=%v status=%v, want 401", path, err, resp)
		}
	}
	resp, err := http.Get(ts.URL + "/v1/live/sessions/" + id + "/content")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("content without credentials answered %d, want 401", resp.StatusCode)
	}
}

// --- stored sessions ---

// engineSpy records the options every engine on a server is built with.
type engineSpy struct {
	mu   sync.Mutex
	opts []duplex.Options
}

func (c *engineSpy) factory(o duplex.Options, d duplex.Deps) *duplex.Engine {
	c.mu.Lock()
	c.opts = append(c.opts, o)
	c.mu.Unlock()
	return duplex.New(o, d)
}

func (c *engineSpy) last(t *testing.T) duplex.Options {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.opts) == 0 {
		t.Fatal("no engine was built")
	}
	return c.opts[len(c.opts)-1]
}

func TestStoredSessionCanBeDownloadedAndForked(t *testing.T) {
	ts, srv, cfg := newServerWith(t, func(c *config.Config) { c.StoreDir = t.TempDir() })
	seen := &engineSpy{}
	srv.Factory = seen.factory

	conn := dialPath(t, ts, "/v1/live/sessions")
	r := startReader(conn)
	sendRaw(t, conn, `{"type":"session.start","session":{
		"store":true,
		"instructions":"你是保险公司的客服。",
		"audio":{"format":{"type":"audio/pcm","rate":16000},"output":{"voice":"cedar"}},
		"delegation":{"type":"responses","responses":{"instructions":"只回答保单问题。"}}}}`)
	started := object(t, r.first1(t, live.ServerSessionStarted, 2*time.Second)["session"])
	id := str(t, started, "id")
	if string(started["store"]) != "true" {
		t.Fatalf("session.started says store is %s", started["store"])
	}

	speakTurn(t, conn, cfg.ClientRate, 1400)
	await(t, "a second of the reply", 8*time.Second, func() bool {
		return r.audioBytes() > audio.PCM16(cfg.ClientRate).BytesForMS(1000)
	})

	// Still running: there is no finished recording to hand over yet.
	resp, err := http.Get(ts.URL + "/v1/live/sessions/" + id + "/content")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("content of a running session answered %d, want 409", resp.StatusCode)
	}

	// A running session can be forked from where it has got to.
	forkLive := dialPath(t, ts, "/v1/live/sessions/"+id+"/fork")
	fl := startReader(forkLive)
	sendRaw(t, forkLive, `{"type":"session.start","session":{"audio":{"format":{"type":"audio/pcm","rate":16000}}}}`)
	fl.first1(t, live.ServerSessionStarted, 2*time.Second)
	if h := seen.last(t).History; len(h) == 0 || h[0].Role != provider.RoleUser {
		t.Errorf("a fork of a running session did not inherit what had been said so far: %+v", h)
	}
	forkLive.Close()

	sendRaw(t, conn, `{"type":"session.close"}`)
	r.first1(t, live.ServerSessionClosed, 3*time.Second)
	<-r.done

	// The recording: one stereo WAV, caller left, assistant right.
	resp, err = http.Get(ts.URL + "/v1/live/sessions/" + id + "/content")
	if err != nil {
		t.Fatal(err)
	}
	wav, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "audio/wav" {
		t.Fatalf("content answered %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if len(wav) < 44 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatalf("content is not a WAV file (%d bytes)", len(wav))
	}
	if ch := binary.LittleEndian.Uint16(wav[22:24]); ch != 2 {
		t.Errorf("recording has %d channel(s), want 2", ch)
	}
	if rate := binary.LittleEndian.Uint32(wav[24:28]); int(rate) != cfg.ClientRate {
		t.Errorf("recording is at %d Hz, the session was at %d", rate, cfg.ClientRate)
	}
	if got := int(binary.LittleEndian.Uint32(wav[40:44])); got != len(wav)-44 || got == 0 {
		t.Errorf("WAV header claims %d bytes of audio; the body is %d", got, len(wav)-44)
	}
	var left, right []float32
	for off := 44; off+4 <= len(wav); off += 4 {
		left = append(left, float32(int16(binary.LittleEndian.Uint16(wav[off:])))/32768)
		right = append(right, float32(int16(binary.LittleEndian.Uint16(wav[off+2:])))/32768)
	}
	if audio.DBFS(audio.RMS(left)) < -50 {
		t.Error("the left channel, the caller's, is silent")
	}
	if audio.DBFS(audio.RMS(right)) < -50 {
		t.Error("the right channel, the assistant's, is silent")
	}
	// The caller spoke first, so the assistant's channel begins with silence.
	if lead := len(right) / 10; lead > 0 && audio.DBFS(audio.RMS(right[:cfg.ClientRate/2])) > -50 {
		t.Error("the assistant's audio is not placed at the time it was spoken")
	}

	// The fork: a new session that picks up where the stored one stopped.
	fork := dialPath(t, ts, "/v1/live/sessions/"+id+"/fork")
	fr := startReader(fork)
	sendRaw(t, fork, `{"type":"session.start","event_id":"evt_fork","session":{
		"instructions":"忘掉之前的一切。",
		"audio":{"format":{"type":"audio/pcm","rate":24000}},
		"delegation":{"responses":{"instructions":"现在也回答理赔问题。"}}}}`)
	forked := object(t, fr.first1(t, live.ServerSessionStarted, 2*time.Second)["session"])
	if got := str(t, forked, "id"); got == id || !strings.HasPrefix(got, "sess_") {
		t.Errorf("the fork's id is %q; it is a session of its own", got)
	}
	if got := str(t, forked, "instructions"); got != "你是保险公司的客服。" {
		t.Errorf("the fork's instructions are %q; they are inherited, not replaced", got)
	}
	audioCfg := object(t, forked["audio"])
	if rate, _ := num(t, object(t, audioCfg["format"]), "rate"); rate != 24000 {
		t.Errorf("the fork's audio format is %s; its connection chooses its own", audioCfg["format"])
	}
	if got := str(t, object(t, audioCfg["output"]), "voice"); got != "cedar" {
		t.Errorf("the fork speaks as %q, the source as cedar", got)
	}
	opts := seen.last(t)
	if opts.Backend.Instructions != "现在也回答理赔问题。" {
		t.Errorf("the fork's Responses settings were not applied: %q", opts.Backend.Instructions)
	}
	if opts.Greeting != "" {
		t.Error("a fork greets the caller again, in the middle of their conversation")
	}
	var user, assistant bool
	for _, m := range opts.History {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "你好") {
			user = true
		}
		if m.Role == provider.RoleAssistant && strings.HasPrefix(m.Content, "好的") {
			assistant = true
		}
	}
	if !user || !assistant {
		t.Errorf("the fork did not inherit the conversation: %+v", opts.History)
	}
}

func TestOnlyStoredSessionsCanBeFetchedOrForked(t *testing.T) {
	ts, _, _ := newServerWith(t, func(c *config.Config) { c.StoreDir = t.TempDir() })
	conn := dialPath(t, ts, "/v1/live/sessions")
	r := startReader(conn)
	sendRaw(t, conn, startResponses) // store defaults to false
	id := str(t, object(t, r.first1(t, live.ServerSessionStarted, 2*time.Second)["session"]), "id")

	for _, target := range []string{id, "sess_0123456789abcdef", "..%2F..%2Fetc"} {
		resp, err := http.Get(ts.URL + "/v1/live/sessions/" + target + "/content")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("content of %q answered %d, want 404", target, resp.StatusCode)
		}
		_, wsResp, err := websocket.DefaultDialer.Dial(wsURL(ts, "/v1/live/sessions/"+target+"/fork"), nil)
		if err == nil || wsResp == nil || wsResp.StatusCode != http.StatusNotFound {
			t.Errorf("fork of %q: err=%v status=%v, want 404", target, err, wsResp)
		}
	}
}

// --- function calls ---

func TestFunctionCallOverTheWire(t *testing.T) {
	ts, srv, cfg := newServerWith(t, func(c *config.Config) {
		c.BackendModels = map[string]string{"gpt-5.5": "deepseek-chat"}
	})
	seen := &engineSpy{}
	srv.Factory = seen.factory

	conn := dialPath(t, ts, "/v1/live/sessions")
	r := startReader(conn)
	// Tools as a gpt-live-1 client writes them: the Responses form, flat.
	sendRaw(t, conn, `{"type":"session.start","event_id":"evt_start","session":{
		"audio":{"format":{"type":"audio/pcm","rate":16000}},
		"delegation":{"type":"responses","responses":{
			"model":"gpt-5.5",
			"tools":[
				{"type":"function","name":"lookup_weather","description":"查天气",
				 "parameters":{"type":"object","properties":{"query":{"type":"string"}}}},
				{"type":"web_search"}
			],
			"tool_choice":"auto"}}}}`)
	r.first1(t, live.ServerSessionStarted, 2*time.Second)

	// A hosted tool needs a backend that runs it. Saying so beats pretending.
	await(t, "the unsupported tool to be reported", 2*time.Second, func() bool {
		return r.sawError("unsupported_tool")
	})
	opts := seen.last(t)
	if opts.Backend.Model != "deepseek-chat" {
		t.Errorf("gpt-5.5 was sent to the backend as %q; backend_models maps it to deepseek-chat", opts.Backend.Model)
	}
	if len(opts.Backend.Tools) != 1 || !strings.Contains(string(opts.Backend.Tools[0]), `"function":{`) ||
		!strings.Contains(string(opts.Backend.Tools[0]), `"lookup_weather"`) {
		t.Fatalf("the backend was offered %s; want the one function, in chat-completions form", opts.Backend.Tools)
	}

	speakTurn(t, conn, cfg.ClientRate, 2000) // "你好，帮我查一下明天"
	await(t, "a function call", 5*time.Second, func() bool {
		_, _, _, ok := r.functionCall(t)
		return ok
	})
	callID, name, delegationID, _ := r.functionCall(t)
	if name != "lookup_weather" || callID == "" || delegationID == "" {
		t.Fatalf("function call is malformed: call_id=%q name=%q delegation_id=%q", callID, name, delegationID)
	}

	// The turn has ended as far as audio goes, and usage says where things are.
	usage := r.first1(t, live.ServerUsageUpdated, 5*time.Second)
	if secs, ok := num(t, object(t, usage["usage"]), "seconds"); !ok || secs <= 0 {
		t.Errorf("session.usage.updated.usage.seconds = %v", secs)
	}
	if _, ok := num(t, object(t, usage["context_window"]), "usage_ratio"); !ok {
		t.Errorf("session.usage.updated has no context_window.usage_ratio: %s", r.events(live.ServerUsageUpdated)[0])
	}

	send(t, conn, map[string]any{
		"type": "response.item.create", "event_id": "evt_item", "delegation_id": delegationID,
		"item": map[string]any{"type": "function_call_output", "call_id": callID, "output": "明天上海晴，二十度"},
	})
	sendRaw(t, conn, `{"type":"response.create","event_id":"evt_create"}`)
	await(t, "the result to be spoken", 6*time.Second, func() bool {
		return strings.Contains(r.said(t), "查到了，明天上海晴，二十度")
	})
	if codes := r.errorCodes(); len(codes) != 1 {
		t.Errorf("errors beyond the unsupported tool: %v", codes)
	}
}

func TestBackendModelNamesAreNotPassedThroughUnasked(t *testing.T) {
	for _, tc := range []struct {
		name        string
		passthrough bool
		want        string
	}{
		{"an OpenAI model name means nothing to this server's provider", false, ""},
		{"unless the operator says the provider knows it", true, "gpt-5.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, srv, _ := newServerWith(t, func(c *config.Config) {
				c.AllowBackendModelPassthrough = tc.passthrough
			})
			seen := &engineSpy{}
			srv.Factory = seen.factory
			conn := dialPath(t, ts, "/v1/live")
			r := startReader(conn)
			sendRaw(t, conn, `{"type":"session.start","session":{
				"delegation":{"type":"responses","responses":{"model":"gpt-5.5"}}}}`)
			r.first1(t, live.ServerSessionStarted, 2*time.Second)
			if got := seen.last(t).Backend.Model; got != tc.want {
				t.Errorf("backend model = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSessionUpdateMergesResponsesSettings(t *testing.T) {
	ts, _, _ := newServerWith(t, nil)
	conn := dialPath(t, ts, "/v1/live/sessions")
	r := startReader(conn)
	sendRaw(t, conn, `{"type":"session.start","session":{
		"delegation":{"type":"responses","responses":{
			"instructions":"只回答保单问题。",
			"tools":[{"type":"function","name":"lookup"}]}}}}`)
	r.first1(t, live.ServerSessionStarted, 2*time.Second)

	// Only the instructions are named; the tools stay as they were.
	sendRaw(t, conn, `{"type":"session.update","event_id":"evt_update","session":{
		"delegation":{"responses":{"instructions":"现在也回答理赔问题。"}}}}`)
	updated := r.first1(t, live.ServerSessionUpdated, 2*time.Second)
	if got := str(t, updated, "client_event_id"); got != "evt_update" {
		t.Errorf("session.updated.client_event_id = %q", got)
	}
	responses := object(t, object(t, object(t, updated["session"])["delegation"])["responses"])
	if got := str(t, responses, "instructions"); got != "现在也回答理赔问题。" {
		t.Errorf("instructions after the update: %q", got)
	}
	if !strings.Contains(string(responses["tools"]), `"lookup"`) {
		t.Errorf("a setting the update did not mention was lost: tools = %s", responses["tools"])
	}
	if codes := r.errorCodes(); len(codes) != 0 {
		t.Errorf("errors: %v", codes)
	}
}

// --- session lifetime ---

func TestSessionThatOutlivesItsLimitClosesOnceAsExpired(t *testing.T) {
	ts, _, _ := newServerWith(t, func(c *config.Config) { c.Duplex.SessionMaxSeconds = 1 })
	conn := dialPath(t, ts, "/v1/live/sessions")
	r := startReader(conn)
	sendRaw(t, conn, startResponses)
	r.first1(t, live.ServerSessionStarted, 2*time.Second)

	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the session outlived session_max_seconds")
	}
	if n := r.count(live.ServerSessionClosed); n != 1 {
		t.Errorf("session.closed was sent %d times", n)
	}
	if got := r.closeReason(); got != live.CloseExpired {
		t.Errorf("session.closed.reason = %q, want %q", got, live.CloseExpired)
	}
}

// --- limits and inheritance ---

func TestSidebandsAreLimitedPerSession(t *testing.T) {
	ts, _, _ := newServerWith(t, nil)
	primary := dialPath(t, ts, "/v1/live/sessions")
	pr := startReader(primary)
	sendRaw(t, primary, startResponses)
	id := str(t, object(t, pr.first1(t, live.ServerSessionStarted, 2*time.Second)["session"]), "id")

	for i := 0; i < MaxSidebands; i++ {
		dialPath(t, ts, "/v1/live/sessions/"+id+"/attach")
	}
	await(t, "the sidebands to be attached", 2*time.Second, func() bool {
		resp, err := http.Get(ts.URL + "/v1/live/sessions/" + id + "/attach")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusTooManyRequests
	})
	_, resp, err := websocket.DefaultDialer.Dial(wsURL(ts, "/v1/live/sessions/"+id+"/attach"), nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("one sideband too many: err=%v status=%v, want 429", err, resp)
	}
}

func TestBinaryAudioIsReflectedToSidebandsToo(t *testing.T) {
	ts, _, _ := newServerWith(t, nil)
	primary := dialPath(t, ts, "/v1/live/sessions")
	pr := startReader(primary)
	sendRaw(t, primary, startResponses)
	id := str(t, object(t, pr.first1(t, live.ServerSessionStarted, 2*time.Second)["session"]), "id")
	side := dialPath(t, ts, "/v1/live/sessions/"+id+"/attach")
	sr := startReader(side)
	time.Sleep(100 * time.Millisecond)

	frame := voicedPCM(16000, 20)
	if err := primary.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	reflected := sr.first1(t, live.ClientInputAudioAppend, 2*time.Second)
	got, err := base64.StdEncoding.DecodeString(str(t, reflected, "audio"))
	if err != nil || len(got) != len(frame) {
		t.Fatalf("a binary frame of %d bytes was reflected as %d (err %v)", len(frame), len(got), err)
	}
}

// TestForkInheritsWhatTheSessionBecame: a fork continues the conversation under
// the rules it was running under when it was forked — the golive settings it
// started with, and the instructions as they stood, appends included.
func TestForkInheritsWhatTheSessionBecame(t *testing.T) {
	ts, srv, _ := newServerWith(t, func(c *config.Config) { c.StoreDir = t.TempDir() })
	seen := &engineSpy{}
	srv.Factory = seen.factory

	conn := dialPath(t, ts, "/v1/live/sessions")
	r := startReader(conn)
	sendRaw(t, conn, `{"type":"session.start","session":{
		"store":true,
		"instructions":"你是保险公司的客服。",
		"delegation":{"type":"client"},
		"golive":{"language":"en","client_delegation":"auto","greeting":"欢迎致电。","on_new_query":"queue"}}}`)
	id := str(t, object(t, r.first1(t, live.ServerSessionStarted, 2*time.Second)["session"]), "id")
	sendRaw(t, conn, `{"type":"session.instructions.append","event_id":"evt_rule","content":"称呼客户为王先生。"}`)
	r.first1(t, live.ServerInstructionsAppended, 2*time.Second)
	sendRaw(t, conn, `{"type":"session.close"}`)
	<-r.done

	fork := dialPath(t, ts, "/v1/live/sessions/"+id+"/fork")
	fr := startReader(fork)
	sendRaw(t, fork, `{"type":"session.start","session":{"golive":{"on_new_query":"cut"}}}`)
	forked := object(t, fr.first1(t, live.ServerSessionStarted, 2*time.Second)["session"])

	if got := str(t, forked, "instructions"); !strings.Contains(got, "你是保险公司的客服。") || !strings.Contains(got, "称呼客户为王先生。") {
		t.Errorf("the fork's instructions are %q; an appended rule still applies", got)
	}
	opts := seen.last(t)
	if opts.Language != "en" || opts.ClientDelegation != "auto" {
		t.Errorf("the fork did not inherit golive settings: language=%q client_delegation=%q",
			opts.Language, opts.ClientDelegation)
	}
	if opts.OnNewQuery != "cut" {
		t.Errorf("the fork's own setting should win: on_new_query=%q", opts.OnNewQuery)
	}
	if opts.Greeting != "" {
		t.Errorf("the fork inherited the greeting %q; it is the middle of a conversation", opts.Greeting)
	}
}

func TestSessionInputRolesAreChecked(t *testing.T) {
	ts, _, _ := newServerWith(t, nil)
	conn := dialPath(t, ts, "/v1/live/sessions")
	r := startReader(conn)
	sendRaw(t, conn, `{"type":"session.start","session":{"input":[
		{"role":"developer","content":[{"type":"input_text","text":"只说中文。"}]},
		{"role":"tool","content":[{"type":"input_text","text":"{}"}]}]}}`)
	await(t, "the bad role to be refused", 2*time.Second, func() bool { return r.sawError("invalid_session") })
	if r.count(live.ServerSessionStarted) != 0 {
		t.Error("a session started with a message every backend request would be refused for")
	}
}

// TestStoredSessionKeepsItsConversationWhenItExpires: the deadline that ends
// the call also stops the engine, and the conversation was read after that.
func TestStoredSessionKeepsItsConversationWhenItExpires(t *testing.T) {
	ts, srv, _ := newServerWith(t, func(c *config.Config) {
		c.StoreDir = t.TempDir()
		c.Duplex.SessionMaxSeconds = 2
	})
	seen := &engineSpy{}
	srv.Factory = seen.factory

	conn := dialPath(t, ts, "/v1/live/sessions")
	r := startReader(conn)
	sendRaw(t, conn, `{"type":"session.start","session":{"store":true,
		"input":[{"role":"user","content":"我的保单号是A17。"},{"role":"assistant","content":"好的，记下了。"}],
		"delegation":{"type":"responses"}}}`)
	id := str(t, object(t, r.first1(t, live.ServerSessionStarted, 2*time.Second)["session"]), "id")

	select {
	case <-r.done:
	case <-time.After(6 * time.Second):
		t.Fatal("the session outlived session_max_seconds")
	}
	if got := r.closeReason(); got != live.CloseExpired {
		t.Fatalf("closed for %q", got)
	}

	fork := dialPath(t, ts, "/v1/live/sessions/"+id+"/fork")
	fr := startReader(fork)
	sendRaw(t, fork, `{"type":"session.start","session":{}}`)
	fr.first1(t, live.ServerSessionStarted, 2*time.Second)
	history := seen.last(t).History
	if len(history) != 2 || !strings.Contains(history[0].Content, "A17") {
		t.Fatalf("the expired session was stored without its conversation: %+v", history)
	}
}
