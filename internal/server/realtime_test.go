package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/duplex"
	"github.com/chuanmingliu/golive/internal/live"
)

// These tests are a media gateway's view of golive: the OpenAI Realtime events
// Active Call's realtime client sends and reads, written out as it writes them.

// --- a Realtime client ---

type rtEvent struct {
	Type string
	At   time.Time
	Raw  map[string]json.RawMessage
}

type rtClient struct {
	t    *testing.T
	conn *websocket.Conn
	// wmu serializes writes: a test talks over the assistant from one
	// goroutine while sending commands from another, and a WebSocket takes one
	// writer at a time.
	wmu  sync.Mutex
	mu   sync.Mutex
	evs  []rtEvent
	pcm  []byte
	done chan struct{}
}

func dialRealtime(t *testing.T, ts *httptest.Server, path string) *rtClient {
	t.Helper()
	c := &rtClient{t: t, conn: dialPath(t, ts, path), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		for {
			_, data, err := c.conn.ReadMessage()
			if err != nil {
				return
			}
			var raw map[string]json.RawMessage
			if json.Unmarshal(data, &raw) != nil {
				continue
			}
			var typ string
			_ = json.Unmarshal(raw["type"], &typ)
			c.mu.Lock()
			if typ == "response.audio.delta" || typ == "response.output_audio.delta" {
				var b64 string
				_ = json.Unmarshal(raw["delta"], &b64)
				if pcm, err := base64.StdEncoding.DecodeString(b64); err == nil {
					c.pcm = append(c.pcm, pcm...)
				}
			}
			c.evs = append(c.evs, rtEvent{Type: typ, At: time.Now(), Raw: raw})
			c.mu.Unlock()
		}
	}()
	return c
}

func (c *rtClient) count(typ string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// index of the nth (0-based) event of a type, or -1.
func (c *rtClient) index(typ string, n int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, e := range c.evs {
		if e.Type == typ {
			if n == 0 {
				return i
			}
			n--
		}
	}
	return -1
}

func (c *rtClient) first(typ string, timeout time.Duration) map[string]json.RawMessage {
	c.t.Helper()
	await(c.t, typ, timeout, func() bool { return c.count(typ) > 0 })
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.evs {
		if e.Type == typ {
			return e.Raw
		}
	}
	return nil
}

func (c *rtClient) types() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, e := range c.evs {
		if !seen[e.Type] {
			seen[e.Type] = true
			out = append(out, e.Type)
		}
	}
	return out
}

func (c *rtClient) audioMS(rate int) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return audio.PCM16(rate).DurationMS(c.pcm)
}

// text concatenates the delta fields of every event of a type.
func (c *rtClient) text(typ string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, e := range c.evs {
		if e.Type == typ {
			var d string
			_ = json.Unmarshal(e.Raw["delta"], &d)
			b.WriteString(d)
		}
	}
	return b.String()
}

func (c *rtClient) send(v any) {
	c.t.Helper()
	c.wmu.Lock()
	defer c.wmu.Unlock()
	send(c.t, c.conn, v)
}

// say streams PCM16 at real time the way the gateway does: one
// input_audio_buffer.append per 20 ms frame.
func (c *rtClient) say(pcm []byte, rate int) {
	c.t.Helper()
	frame := audio.PCM16(rate).BytesForMS(20)
	for off := 0; off < len(pcm); off += frame {
		end := off + frame
		if end > len(pcm) {
			end = len(pcm)
		}
		c.send(map[string]string{
			"type":  "input_audio_buffer.append",
			"audio": base64.StdEncoding.EncodeToString(pcm[off:end]),
		})
		time.Sleep(20 * time.Millisecond)
	}
}

func (c *rtClient) turn(rate, voicedMS int) {
	c.t.Helper()
	c.say(voicedPCM(rate, voicedMS), rate)
	c.say(make([]byte, audio.PCM16(rate).BytesForMS(700)), rate)
}

// activeCallSessionUpdate is the session.update Active Call's realtime client
// sends on connecting, field for field, with the "type" its GA patch adds.
const activeCallSessionUpdate = `{"type":"session.update","session":{
	"type":"realtime",
	"modalities":["text","audio"],
	"instructions":"你是电话语音助手，每次只说一两句话。",
	"voice":"alloy",
	"input_audio_format":"pcm16",
	"output_audio_format":"pcm16",
	"turn_detection":{"type":"server_vad","threshold":0.5,"prefix_padding_ms":300,"silence_duration_ms":500},
	"tools":[]}}`

func gatewayServer(t *testing.T, mutate func(*config.Config)) (*httptest.Server, *engineSpy, config.Config) {
	t.Helper()
	ts, srv, cfg := newServerWith(t, func(c *config.Config) {
		// Active Call's internal rate: it says "pcm16" and means this.
		c.Realtime.SampleRate = 16000
		c.Voice = "the-configured-voice"
		if mutate != nil {
			mutate(c)
		}
	})
	seen := &engineSpy{}
	srv.Factory = seen.factory
	return ts, seen, cfg
}

// --- a call, as the gateway sees it ---

func TestRealtimeGatewayRunsATurn(t *testing.T) {
	ts, seen, _ := gatewayServer(t, nil)
	c := dialRealtime(t, ts, "/v1/realtime")

	// A Realtime server speaks first.
	created := c.first("session.created", 2*time.Second)
	if i := c.index("session.created", 0); i != 0 {
		t.Errorf("session.created was event %d; it is the first thing a Realtime server says", i)
	}
	if !strings.Contains(string(created["session"]), `"sess_`) {
		t.Errorf("session.created carries no session id: %s", created["session"])
	}

	sendRaw(t, c.conn, activeCallSessionUpdate)
	c.first("session.updated", 2*time.Second)
	opts := seen.last(t)
	if opts.ClientRate != 16000 {
		t.Errorf("the engine runs at %d Hz; the gateway's pcm16 is 16000", opts.ClientRate)
	}
	if opts.Instructions != "你是电话语音助手，每次只说一两句话。" {
		t.Errorf("instructions did not arrive: %q", opts.Instructions)
	}
	if opts.Voice != "the-configured-voice" {
		t.Errorf("the synthesizer would be asked for the voice %q; alloy is OpenAI's name, not a vendor's", opts.Voice)
	}
	if opts.Greeting != "" {
		t.Errorf("the session would greet with %q; on this endpoint the opening is released, not volunteered", opts.Greeting)
	}

	c.turn(16000, 1400)

	c.first("input_audio_buffer.speech_started", 3*time.Second)
	c.first("input_audio_buffer.speech_stopped", 3*time.Second)
	heard := c.first("conversation.item.input_audio_transcription.completed", 4*time.Second)
	if got := string(heard["transcript"]); !strings.Contains(got, "你好") {
		t.Errorf("transcript = %s", got)
	}
	c.first("response.created", 5*time.Second)
	await(t, "the assistant's audio", 5*time.Second, func() bool { return c.audioMS(16000) > 300 })
	if c.index("response.created", 0) > c.index("response.audio.delta", 0) {
		t.Error("audio arrived before the response it belongs to was announced")
	}
	c.mu.Lock()
	pcm := append([]byte(nil), c.pcm...)
	c.mu.Unlock()
	samples, err := audio.DecodePCM16(pcm)
	if err != nil {
		t.Fatalf("the audio is not PCM16: %v", err)
	}
	if level := audio.DBFS(audio.RMS(samples)); level < -40 {
		t.Errorf("the audio decodes to %.1f dBFS", level)
	}
	if said := c.text("response.audio_transcript.delta"); !strings.HasPrefix(said, "好的") {
		t.Errorf("assistant transcript = %q", said)
	}

	// Nothing of gpt-live-1 reaches a client that does not speak it.
	for _, typ := range c.types() {
		if strings.HasPrefix(typ, "session.") && typ != "session.created" && typ != "session.updated" ||
			strings.HasPrefix(typ, "golive.") {
			t.Errorf("a gpt-live-1 event reached the Realtime client: %s", typ)
		}
	}
	if n := c.count("error"); n != 0 {
		t.Errorf("%d error(s): %s", n, c.first("error", time.Second)["error"])
	}
}

// TestRealtimeBargeInIsTheEnginesDecision: a gateway flushes its playback and
// cancels the response the moment it is told the caller is speaking. So it is
// told when the engine has given up the floor, and not before — and the cancel
// it sends straight back does not cost the caller their answer.
func TestRealtimeBargeInIsTheEnginesDecision(t *testing.T) {
	ts, _, _ := gatewayServer(t, func(c *config.Config) {
		c.Duplex.OnNewQuery = "cut"
		// Long enough to tell "held, then yielded" from "told at once".
		c.Duplex.UserBackchannelHoldMS = 400
	})
	c := dialRealtime(t, ts, "/v1/realtime")
	sendRaw(t, c.conn, activeCallSessionUpdate)
	c.first("session.updated", 2*time.Second)

	c.turn(16000, 1400)
	await(t, "the assistant to be mid-answer", 6*time.Second, func() bool { return c.audioMS(16000) > 600 })
	if n := c.count("input_audio_buffer.speech_started"); n != 1 {
		t.Fatalf("%d speech_started events before the caller interrupted", n)
	}

	// The caller talks over the answer.
	started := time.Now()
	go c.say(voicedPCM(16000, 1500), 16000)
	await(t, "the gateway to be told to stop playing", 4*time.Second, func() bool {
		return c.count("input_audio_buffer.speech_started") == 2
	})
	told := c.index("input_audio_buffer.speech_started", 1)
	done := c.index("response.done", 0)
	if done < 0 || done > told {
		t.Errorf("the gateway was told the caller was speaking (event %d) before the engine gave up the floor (event %d)", told, done)
	}
	c.mu.Lock()
	var status struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(c.evs[done].Raw["response"], &status)
	waited := c.evs[told].At.Sub(started)
	c.mu.Unlock()
	if status.Status != "cancelled" {
		t.Errorf("the interrupted response ended as %q", status.Status)
	}
	if waited < 250*time.Millisecond {
		t.Errorf("the gateway was told %v after the first sound; the engine holds the floor until it knows what the sound was", waited)
	}

	// What Active Call does next, every time.
	c.send(map[string]string{"type": "response.cancel"})

	time.Sleep(1300 * time.Millisecond) // the rest of what the caller is saying
	c.say(make([]byte, audio.PCM16(16000).BytesForMS(700)), 16000)
	await(t, "an answer to what the caller said", 6*time.Second, func() bool {
		return c.count("response.created") >= 2
	})
	if n := c.count("error"); n != 0 {
		t.Errorf("%d error(s)", n)
	}
}

func TestRealtimeCancelStopsAnAnswer(t *testing.T) {
	ts, _, _ := gatewayServer(t, nil)
	c := dialRealtime(t, ts, "/v1/realtime")
	sendRaw(t, c.conn, activeCallSessionUpdate)
	c.first("session.updated", 2*time.Second)
	c.turn(16000, 1400)
	await(t, "the assistant to be mid-answer", 6*time.Second, func() bool { return c.audioMS(16000) > 400 })

	// Long after any speech_started: this one is a request, not an echo.
	time.Sleep(900 * time.Millisecond)
	c.send(map[string]string{"type": "response.cancel"})
	await(t, "the answer to stop", 3*time.Second, func() bool { return c.count("response.done") == 1 })
	before := c.audioMS(16000)
	time.Sleep(600 * time.Millisecond)
	if after := c.audioMS(16000); after-before > 200 {
		t.Errorf("%.0f ms more audio arrived after the answer was cancelled", after-before)
	}
}

// --- an outbound call: connected before it is answered ---

func postJSON(t *testing.T, url, body string) map[string]json.RawMessage {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("POST %s answered %d %s", url, resp.StatusCode, raw)
	}
	return out
}

func TestRealtimeOpeningIsHeldUntilTheCallIsAnswered(t *testing.T) {
	duplex.Phrases().Reset()
	t.Cleanup(duplex.Phrases().Reset)
	ts, _, _ := gatewayServer(t, func(c *config.Config) { c.Realtime.HoldForOpening = true })
	const greeting = "您好，我是小翠儿，请问现在方便吗？"

	// Dial: the line is synthesized while the phone rings. The originator
	// polls until it is ready.
	await(t, "the opening line to be ready", 3*time.Second, func() bool {
		got := postJSON(t, ts.URL+"/v1/opening", `{"greeting":"`+greeting+`","warm":true}`)
		var cached []struct {
			Ready bool `json:"ready"`
			Bytes int  `json:"bytes"`
		}
		_ = json.Unmarshal(got["cached"], &cached)
		return string(got["ok"]) == "true" && len(cached) == 1 && cached[0].Ready && cached[0].Bytes > 0
	})

	// Answered too early: no session has attached yet, and the reply says so.
	if got := postJSON(t, ts.URL+"/v1/opening", `{"greeting":"`+greeting+`","warm":false}`); string(got["kicked"]) != "[]" {
		t.Fatalf("with no session attached, kicked = %s", got["kicked"])
	}

	// The gateway connects its agent at INVITE. Until pickup, what it sends is
	// ringback — which is loud, periodic, and not the caller.
	c := dialRealtime(t, ts, "/v1/realtime")
	sendRaw(t, c.conn, activeCallSessionUpdate)
	c.first("session.updated", 2*time.Second)
	c.turn(16000, 1200)
	time.Sleep(300 * time.Millisecond)
	if c.count("input_audio_buffer.speech_started") != 0 || c.count("response.created") != 0 || c.audioMS(16000) != 0 {
		t.Fatalf("the agent reacted to ringback before the call was answered: %v", c.types())
	}

	// Pickup.
	answered := time.Now()
	got := postJSON(t, ts.URL+"/v1/opening", `{"greeting":"`+greeting+`","warm":false}`)
	var kicked []string
	_ = json.Unmarshal(got["kicked"], &kicked)
	if len(kicked) != 1 || !strings.HasPrefix(kicked[0], "sess_") {
		t.Fatalf("kicked = %s; want the one session on the line", got["kicked"])
	}
	await(t, "the opening line", 2*time.Second, func() bool { return c.audioMS(16000) > 0 })
	c.mu.Lock()
	first := c.evs[len(c.evs)-1].At
	for _, e := range c.evs {
		if e.Type == "response.audio.delta" {
			first = e.At
			break
		}
	}
	c.mu.Unlock()
	if took := first.Sub(answered); took > 250*time.Millisecond {
		t.Errorf("the opening began %v after pickup; it was synthesized in advance and should start at once", took)
	}
	await(t, "the whole line", 6*time.Second, func() bool {
		return strings.Contains(c.text("response.audio_transcript.delta"), "请问现在方便吗")
	})

	// The same pickup is not answered twice.
	if got := postJSON(t, ts.URL+"/v1/opening", `{"greeting":"`+greeting+`","warm":false}`); string(got["kicked"]) != "[]" {
		t.Errorf("a second release opened the session again: %s", got["kicked"])
	}

	// And now the agent listens.
	await(t, "the opening to finish", 8*time.Second, func() bool { return c.count("response.done") >= 1 })
	c.turn(16000, 1400)
	c.first("input_audio_buffer.speech_started", 3*time.Second)
	c.first("conversation.item.input_audio_transcription.completed", 4*time.Second)
}

// --- the latency console ---

type telSink struct {
	mu  sync.Mutex
	evs []map[string]any
}

func (s *telSink) handler(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var batch []map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &batch); err != nil {
		http.Error(w, "bad", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.evs = append(s.evs, batch...)
	s.mu.Unlock()
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// find returns the first event with a field equal to a value.
func (s *telSink) find(field, value string) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.evs {
		if got, _ := e[field].(string); got == value {
			return e, true
		}
	}
	return nil, false
}

func (s *telSink) count(field, value string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.evs {
		if got, _ := e[field].(string); got == value {
			n++
		}
	}
	return n
}

func TestRealtimeTurnBoundariesReachTheLatencyConsole(t *testing.T) {
	sink := &telSink{}
	console := httptest.NewServer(http.HandlerFunc(sink.handler))
	defer console.Close()

	ts, _, _ := gatewayServer(t, func(c *config.Config) { c.TelemetryURL = console.URL + "/ingest" })
	// The originator names the call on the URL it hands the gateway.
	const call = "out-mv2fd0qg-kbyy"
	c := dialRealtime(t, ts, "/v1/realtime?call="+call)
	sendRaw(t, c.conn, activeCallSessionUpdate)
	c.first("session.updated", 2*time.Second)
	c.turn(16000, 1400)
	await(t, "the assistant's audio", 5*time.Second, func() bool { return c.audioMS(16000) > 200 })

	// The chain the console derives a turn's latency from, in the order it
	// reads them.
	chain := []string{"vad.speech_started", "vad.speech_stopped", "asr.final", "llm.first_token", "tts.first_audio", "phone.audio_sent"}
	await(t, "the turn's boundaries at the console", 3*time.Second, func() bool {
		for _, point := range chain {
			if _, ok := sink.find("point", point); !ok {
				return false
			}
		}
		return true
	})
	var stopNs, sentNs int64
	for _, point := range chain {
		ev, _ := sink.find("point", point)
		if ev["schemaVersion"] != float64(1) || ev["callId"] != call || ev["backend"] != "local" || ev["chain"] != "turn" {
			t.Errorf("%s is not filed under the call: %v", point, ev)
		}
		if ev["turnId"] != float64(1) {
			t.Errorf("%s has turnId %v; this is the caller's first turn", point, ev["turnId"])
		}
		if id, _ := ev["eventId"].(string); id == "" {
			t.Errorf("%s has no eventId", point)
		}
		clock, _ := ev["clockId"].(string)
		mono, _ := ev["monotonicNs"].(string)
		if !strings.HasPrefix(clock, "boot:") || mono == "" {
			t.Errorf("%s carries clockId=%q monotonicNs=%q; spans across processes need both", point, clock, mono)
		}
		ns, _ := strconv.ParseInt(mono, 10, 64)
		switch point {
		case "vad.speech_stopped":
			stopNs = ns
		case "phone.audio_sent":
			sentNs = ns
		}
	}
	// The console refuses a whole batch for one key in extra it does not know.
	// These are the ones it was found to take; "agent", "protocol", "barge_in"
	// and "silence_ms" each cost golive every event it sent.
	accepted := map[string]bool{"coverage": true, "endpoint": true, "port": true, "revision": true}
	sink.mu.Lock()
	for _, ev := range sink.evs {
		extra, _ := ev["extra"].(map[string]any)
		for k := range extra {
			if !accepted[k] {
				t.Errorf("%v carries extra[%q]; the console turns the batch away for it", ev["point"], k)
			}
		}
		if extra["coverage"] != "application" {
			t.Errorf("%v does not say where on the path it was observed: extra=%v", ev["point"], extra)
		}
	}
	sink.mu.Unlock()
	if e2e := (sentNs - stopNs) / 1e6; e2e < 0 || e2e > 3000 {
		t.Errorf("speech stopped to audio sent comes to %d ms on the monotonic clock", e2e)
	}
	// One per turn, not one per chunk.
	if n := sink.count("point", "phone.audio_sent"); n != 1 {
		t.Errorf("phone.audio_sent was reported %d times for one turn", n)
	}

	c.conn.Close()
	await(t, "the session's end at the console", 3*time.Second, func() bool {
		_, ok := sink.find("point", "agent.disconnected")
		return ok
	})
}

// TestTelemetryLegacyEnvelope: the looser shape an older console reads.
func TestTelemetryLegacyEnvelope(t *testing.T) {
	sink := &telSink{}
	console := httptest.NewServer(http.HandlerFunc(sink.handler))
	defer console.Close()

	ts, _, _ := gatewayServer(t, func(c *config.Config) {
		c.TelemetryURL = console.URL + "/ingest"
		c.TelemetryFormat = "legacy"
	})
	c := dialRealtime(t, ts, "/v1/realtime") // no call id: the old contract has none
	sendRaw(t, c.conn, activeCallSessionUpdate)
	c.first("session.updated", 2*time.Second)
	c.turn(16000, 1400)
	await(t, "the turn at the console", 6*time.Second, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		want := map[string]bool{"vad/speech_start": false, "vad/speech_stop": false, "asr/final": false,
			"llm/first_token": false, "tts/first_audio": false, "s2s/first_audio": false}
		for _, e := range sink.evs {
			key, _ := e["component"].(string)
			ev, _ := e["event"].(string)
			if _, ok := want[key+"/"+ev]; ok {
				want[key+"/"+ev] = true
			}
		}
		for _, seen := range want {
			if !seen {
				return false
			}
		}
		return true
	})
	ev, _ := sink.find("event", "speech_stop")
	if _, ok := ev["at"].(float64); !ok {
		t.Errorf("legacy event has no at: %v", ev)
	}
	if extra, _ := ev["extra"].(map[string]any); extra["brain"] != "golive" {
		t.Errorf("legacy event is not labelled: %v", ev)
	}
}

// TestOpeningIsReleasedToTheNamedCall: with two calls waiting, pickup of one
// does not start the other talking.
func TestOpeningIsReleasedToTheNamedCall(t *testing.T) {
	ts, _, _ := gatewayServer(t, func(c *config.Config) { c.Realtime.HoldForOpening = true })
	a := dialRealtime(t, ts, "/v1/realtime?call=out-aaaa-0001")
	b := dialRealtime(t, ts, "/v1/realtime?call=out-bbbb-0002")
	for _, c := range []*rtClient{a, b} {
		sendRaw(t, c.conn, activeCallSessionUpdate)
		c.first("session.updated", 2*time.Second)
	}

	got := postJSON(t, ts.URL+"/v1/opening", `{"greeting":"您好。","warm":false,"callId":"out-bbbb-0002"}`)
	if string(got["kicked"]) != `["out-bbbb-0002"]` {
		t.Fatalf("kicked = %s; want the call that was answered, by the name it was given", got["kicked"])
	}
	await(t, "the answered call's opening", 3*time.Second, func() bool { return b.audioMS(16000) > 0 })
	time.Sleep(300 * time.Millisecond)
	if a.audioMS(16000) != 0 || a.count("response.created") != 0 {
		t.Error("the call that is still ringing was opened too")
	}

	// A pickup that names no call, or one no session claims, opens whoever is
	// waiting: an originator that cannot say which is not left with silence.
	got = postJSON(t, ts.URL+"/v1/opening", `{"greeting":"您好。","warm":false,"callId":"out-unknown-9"}`)
	if string(got["kicked"]) != `["out-aaaa-0001"]` {
		t.Errorf("kicked = %s; want the one session still waiting", got["kicked"])
	}
}

// --- watching a call from outside it ---

// TestRealtimeSessionCanBeTappedOverGptLive1: the phone call's own socket
// speaks Realtime, and a sideband on the same session speaks gpt-live-1 — so a
// call can be watched, measured or steered without being in its audio path.
func TestRealtimeSessionCanBeTappedOverGptLive1(t *testing.T) {
	ts, _, _ := gatewayServer(t, nil)
	c := dialRealtime(t, ts, "/v1/realtime")
	sendRaw(t, c.conn, activeCallSessionUpdate)
	c.first("session.updated", 2*time.Second)

	// The call's originator never learns the session id. The pool lists it.
	resp, err := http.Get(ts.URL + "/v1/pool")
	if err != nil {
		t.Fatal(err)
	}
	var pool struct {
		Sessions []struct {
			ID       string `json:"id"`
			Protocol string `json:"protocol"`
		} `json:"sessions"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&pool)
	resp.Body.Close()
	if len(pool.Sessions) != 1 || pool.Sessions[0].Protocol != "openai-realtime" {
		t.Fatalf("pool = %+v", pool)
	}

	side := dialPath(t, ts, "/v1/live/sessions/"+pool.Sessions[0].ID+"/attach")
	sr := startReader(side)
	time.Sleep(100 * time.Millisecond)

	c.turn(16000, 1400)
	await(t, "the tap to hear the caller", 3*time.Second, func() bool { return sr.count(live.ClientInputAudioAppend) > 0 })
	await(t, "the tap to see the transcript", 4*time.Second, func() bool { return sr.finalTranscript() != "" })
	await(t, "the tap to hear the assistant", 5*time.Second, func() bool { return sr.audioBytes() > 0 })
	if sr.count("response.audio.delta") != 0 {
		t.Error("the sideband was sent Realtime events; it speaks gpt-live-1")
	}
	if c.count(live.ServerOutputAudioDelta) != 0 || c.count(live.ClientInputAudioAppend) != 0 {
		t.Error("the gateway was sent gpt-live-1 events")
	}
}

// --- other clients of the same endpoint ---

func TestRealtimeTypedInputAndTheGAEventNames(t *testing.T) {
	ts, seen, _ := gatewayServer(t, nil)
	c := dialRealtime(t, ts, "/v1/realtime?dialect=ga")
	// The newer session shape, which states its own rate.
	sendRaw(t, c.conn, `{"type":"session.update","session":{"type":"realtime",
		"audio":{"input":{"format":{"type":"audio/pcm","rate":24000}},
		         "output":{"format":{"type":"audio/pcm","rate":24000},"voice":"marin"}}}}`)
	c.first("session.updated", 2*time.Second)
	if got := seen.last(t).ClientRate; got != 24000 {
		t.Errorf("the session runs at %d Hz; the client asked for 24000", got)
	}

	sendRaw(t, c.conn, `{"type":"conversation.item.create","item":{"type":"message","role":"user",
		"content":[{"type":"input_text","text":"明天上海的天气"}]}}`)
	sendRaw(t, c.conn, `{"type":"response.create"}`)
	c.first("response.output_audio.delta", 5*time.Second)
	if c.count("response.audio.delta") != 0 {
		t.Error("the beta event name was used on a connection that asked for the GA ones")
	}
	if said := c.text("response.output_audio_transcript.delta"); said == "" {
		await(t, "the assistant's transcript", 2*time.Second, func() bool {
			return c.text("response.output_audio_transcript.delta") != ""
		})
	}
}

func TestRealtimeG711(t *testing.T) {
	ts, seen, _ := gatewayServer(t, nil)
	c := dialRealtime(t, ts, "/v1/realtime")
	sendRaw(t, c.conn, `{"type":"session.update","session":{"input_audio_format":"g711_alaw","output_audio_format":"g711_alaw"}}`)
	updated := c.first("session.updated", 2*time.Second)
	if !strings.Contains(string(updated["session"]), `"input_audio_format":"g711_alaw"`) ||
		!strings.Contains(string(updated["session"]), `"rate":8000`) {
		t.Errorf("session = %s", updated["session"])
	}
	if got := seen.last(t).ClientRate; got != 8000 {
		t.Errorf("the session runs at %d Hz; G.711 is 8000", got)
	}
}

func TestRealtimeUnknownEventIsAnErrorNotAHangup(t *testing.T) {
	ts, _, _ := gatewayServer(t, nil)
	c := dialRealtime(t, ts, "/v1/realtime")
	sendRaw(t, c.conn, `{"type":"transcription_session.update","event_id":"evt_1"}`)
	errEv := c.first("error", 2*time.Second)
	if !strings.Contains(string(errEv["error"]), `"unknown_event_type"`) || !strings.Contains(string(errEv["error"]), `"evt_1"`) {
		t.Errorf("error = %s", errEv["error"])
	}
	// The connection is still good.
	sendRaw(t, c.conn, activeCallSessionUpdate)
	c.first("session.updated", 2*time.Second)
}

func TestRealtimeCanBeSwitchedOff(t *testing.T) {
	ts, _, _ := newServerWith(t, func(c *config.Config) { c.Realtime.Enabled = false })
	for _, path := range []string{"/v1/realtime", "/v1/opening", "/v1/pool"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s answered %d with realtime.enabled false", path, resp.StatusCode)
		}
	}
}

// A Realtime client states how long a silence ends a turn, and a call
// originator tuning for its line sets it on purpose. The server used to read
// past it and wait its own 380 ms, which on a call asking for 150 was the
// larger part of a quarter of a second added to every answer.
func TestRealtimeTurnDetectionSetsTheEndOfTurnSilence(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*config.Config)
		update string
		want   int // the engine's vad.min_silence_ms
		echo   string
	}{
		{
			name:   "asked for more than the floor",
			update: `{"type":"session.update","session":{"turn_detection":{"type":"server_vad","silence_duration_ms":260}}}`,
			want:   260,
			echo:   `"silence_duration_ms":260`,
		},
		{
			name:   "asked for less than this detector can be trusted with",
			update: `{"type":"session.update","session":{"turn_detection":{"type":"server_vad","threshold":0.5,"prefix_padding_ms":80,"silence_duration_ms":150}}}`,
			want:   200,
			echo:   `"silence_duration_ms":200`,
		},
		{
			name:   "spelled the way the phone gateway spells it",
			update: `{"type":"session.update","session":{"turn_detection":{"createResponse":true,"interruptResponse":true,"prefixPaddingMs":80,"silenceDurationMs":260,"threshold":0.5,"type":"server_vad"}}}`,
			want:   260,
			echo:   `"silence_duration_ms":260`,
		},
		{
			name:   "the newer session shape",
			update: `{"type":"session.update","session":{"type":"realtime","audio":{"input":{"turn_detection":{"type":"server_vad","silence_duration_ms":500}}}}}`,
			want:   500,
			echo:   `"silence_duration_ms":500`,
		},
		{
			name:   "a server told to keep its own",
			mutate: func(c *config.Config) { c.Realtime.HonorTurnDetection = false },
			update: `{"type":"session.update","session":{"turn_detection":{"type":"server_vad","silence_duration_ms":150}}}`,
			want:   380,
		},
		{
			name:   "a client that does not say",
			update: `{"type":"session.update","session":{"voice":"alloy"}}`,
			want:   380,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, seen, _ := gatewayServer(t, tc.mutate)
			c := dialRealtime(t, ts, "/v1/realtime")
			c.first("session.created", 2*time.Second)
			sendRaw(t, c.conn, tc.update)
			updated := c.first("session.updated", 2*time.Second)
			if got := seen.last(t).Cfg.VAD.MinSilenceMS; got != tc.want {
				t.Errorf("the engine closes a turn after %d ms of silence, want %d", got, tc.want)
			}
			session := string(updated["session"])
			if tc.echo != "" && !strings.Contains(session, tc.echo) {
				t.Errorf("session.updated does not report the silence in force (%s): %s", tc.echo, session)
			}
			if tc.echo == "" && strings.Contains(session, "silence_duration_ms") {
				t.Errorf("session.updated reports a silence nobody set: %s", session)
			}
		})
	}
}

// A restarted service must not reissue the event ids of the one before it: the
// console refuses an id it has seen, and the boot id that prefixes them is the
// same for every process since the machine came up. Nor may the count in the id
// grow long: the console refuses those too.
func TestTelemetryEventIDsAreUniqueAcrossRestartsAndShort(t *testing.T) {
	start := time.Now()
	if runToken(start) == runToken(start.Add(time.Second)) {
		t.Error("two processes started a second apart would issue the same ids")
	}
	sink := &telSink{}
	console := httptest.NewServer(http.HandlerFunc(sink.handler))
	defer console.Close()
	ts, _, _ := gatewayServer(t, func(c *config.Config) { c.TelemetryURL = console.URL + "/ingest" })
	c := dialRealtime(t, ts, "/v1/realtime?call=out-ids-test")
	sendRaw(t, c.conn, activeCallSessionUpdate)
	c.first("session.updated", 2*time.Second)
	await(t, "the session's first event at the console", 3*time.Second, func() bool {
		_, ok := sink.find("point", "agent.connected")
		return ok
	})
	ev, _ := sink.find("point", "agent.connected")
	id, _ := ev["eventId"].(string)
	parts := strings.Split(id, ":")
	if len(parts) < 4 {
		t.Fatalf("eventId %q; want boot:<id>:<producer>:<count>", id)
	}
	producer, count := parts[len(parts)-2], parts[len(parts)-1]
	if !strings.HasPrefix(producer, "golive-s2s-r") || len(producer) <= len("golive-s2s-r") {
		t.Errorf("producer %q does not say which run of the process this is", producer)
	}
	if n, err := strconv.Atoi(count); err != nil || n < 1 || n > 1000 {
		t.Errorf("the first event of a process is numbered %q; the console takes only a short count", count)
	}
	if seq, _ := ev["sequence"].(float64); strconv.Itoa(int(seq)) != count {
		t.Errorf("sequence %v and the count in the id %q differ", ev["sequence"], count)
	}
}
