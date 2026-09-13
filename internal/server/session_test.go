package server

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/live"

	_ "github.com/chuanmingliu/golive/internal/provider/mock"
)

// newTestServer starts the real HTTP + WebSocket stack with mock providers, so
// these tests exercise the wire format rather than the engine's Go API.
func newTestServer(t *testing.T) (*httptest.Server, config.Config) {
	t.Helper()
	cfg := config.Default()
	cfg.WebRoot = ""
	cfg.Duplex.Backchannel = false
	cfg.ClientRate = 16000
	// Most tests assert on audio that a turn produced, so the unprompted
	// greeting is off unless a test asks for one.
	cfg.Greeting = ""

	srv := NewServer(cfg, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, cfg
}

func dial(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/live"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dialing %s: %v", url, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// reader drains the socket into a typed record of what arrived.
type reader struct {
	mu     sync.Mutex
	counts map[string]int
	errs   []live.ErrorEvent
	audio  []byte
	final  string
	first  string
	done   chan struct{}
}

func startReader(conn *websocket.Conn) *reader {
	r := &reader{counts: map[string]int{}, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env live.Envelope
			if json.Unmarshal(data, &env) != nil {
				continue
			}
			r.mu.Lock()
			if r.first == "" {
				r.first = env.Type
			}
			r.counts[env.Type]++
			switch env.Type {
			case live.ServerError:
				var ev live.ErrorEvent
				if json.Unmarshal(data, &ev) == nil {
					r.errs = append(r.errs, ev)
				}
			case live.ServerOutputAudioDelta:
				var ev live.OutputAudioDelta
				if json.Unmarshal(data, &ev) == nil {
					if pcm, err := base64.StdEncoding.DecodeString(ev.Audio); err == nil {
						r.audio = append(r.audio, pcm...)
					}
				}
			case live.ServerInputTranscriptDelta:
				var ev live.TranscriptDelta
				if json.Unmarshal(data, &ev) == nil && ev.Final {
					r.final = ev.Content
				}
			}
			r.mu.Unlock()
		}
	}()
	return r
}

func (r *reader) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[name]
}

func (r *reader) audioBytes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.audio)
}

func (r *reader) lastError() (live.ErrorEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.errs) == 0 {
		return live.ErrorEvent{}, false
	}
	return r.errs[len(r.errs)-1], true
}

// firstEventType is the very first event the server sent, which is how the
// ordering guarantee around session.started is checked.
func (r *reader) firstEventType() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.first
}

func (r *reader) finalTranscript() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.final
}

func send(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("writing %T: %v", v, err)
	}
}

func await(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func voicedPCM(rate, ms int) []byte {
	n := rate * ms / 1000
	out := make([]float32, n)
	for i := range out {
		t := float64(i) / float64(rate)
		env := 0.62 + 0.38*math.Sin(2*math.Pi*4.5*t)
		v := 0.45 * env * (math.Sin(2*math.Pi*140*t) +
			0.5*math.Sin(2*math.Pi*280*t) +
			0.25*math.Sin(2*math.Pi*560*t)) / 1.75
		out[i] = float32(v)
	}
	return audio.EncodePCM16(out)
}

func streamPCM(t *testing.T, conn *websocket.Conn, pcm []byte, rate int) {
	t.Helper()
	frame := audio.PCM16(rate).BytesForMS(20)
	for off := 0; off < len(pcm); off += frame {
		end := off + frame
		if end > len(pcm) {
			end = len(pcm)
		}
		send(t, conn, live.InputAudioAppendEvent{
			Envelope: live.Envelope{Type: live.ClientInputAudioAppend},
			Audio:    base64.StdEncoding.EncodeToString(pcm[off:end]),
		})
		time.Sleep(20 * time.Millisecond)
	}
}

func startSession(t *testing.T, conn *websocket.Conn, rate int) {
	t.Helper()
	send(t, conn, live.SessionStartEvent{
		Envelope: live.Envelope{Type: live.ClientSessionStart, EventID: "start"},
		Session: live.SessionConfig{
			Audio:      &live.AudioConfig{Format: &live.AudioFormat{Type: "audio/pcm", Rate: rate}},
			Delegation: &live.DelegationConfig{Type: live.DelegationResponses},
		},
	})
}

func TestSessionRunsAFullTurnOverTheWire(t *testing.T) {
	ts, cfg := newTestServer(t)
	conn := dial(t, ts)
	r := startReader(conn)

	startSession(t, conn, cfg.ClientRate)
	await(t, "session.started", 2*time.Second, func() bool {
		return r.count(live.ServerSessionStarted) > 0
	})

	streamPCM(t, conn, voicedPCM(cfg.ClientRate, 1400), cfg.ClientRate)
	streamPCM(t, conn, make([]byte, audio.PCM16(cfg.ClientRate).BytesForMS(700)), cfg.ClientRate)

	await(t, "a final transcript", 4*time.Second, func() bool {
		return r.finalTranscript() != ""
	})
	await(t, "assistant audio", 6*time.Second, func() bool {
		return r.audioBytes() > audio.PCM16(cfg.ClientRate).BytesForMS(200)
	})

	if got := r.count(live.ServerError); got != 0 {
		ev, _ := r.lastError()
		t.Errorf("session produced %d errors; last: %+v", got, ev.Error)
	}
	if r.count(live.ServerDelegationCreated) == 0 {
		t.Error("no delegation was announced")
	}
}

func TestSessionRejectsEventsBeforeStart(t *testing.T) {
	ts, _ := newTestServer(t)
	conn := dial(t, ts)
	r := startReader(conn)

	send(t, conn, live.InputAudioAppendEvent{
		Envelope: live.Envelope{Type: live.ClientInputAudioAppend, EventID: "early"},
		Audio:    base64.StdEncoding.EncodeToString(make([]byte, 640)),
	})

	await(t, "an error", 2*time.Second, func() bool { return r.count(live.ServerError) > 0 })
	ev, _ := r.lastError()
	if ev.Error.Code != "session_not_started" {
		t.Errorf("error code is %q, want session_not_started", ev.Error.Code)
	}
	if ev.ClientEventID != "early" {
		t.Errorf("error should name the rejected event; got client_event_id %q", ev.ClientEventID)
	}
}

func TestSessionRejectsAnUnsupportedRate(t *testing.T) {
	ts, _ := newTestServer(t)
	conn := dial(t, ts)
	r := startReader(conn)

	send(t, conn, live.SessionStartEvent{
		Envelope: live.Envelope{Type: live.ClientSessionStart, EventID: "start"},
		Session: live.SessionConfig{
			Audio: &live.AudioConfig{Format: &live.AudioFormat{Type: "audio/pcm", Rate: 44100}},
		},
	})

	await(t, "an error", 2*time.Second, func() bool { return r.count(live.ServerError) > 0 })
	ev, _ := r.lastError()
	if !strings.Contains(ev.Error.Message, "44100") {
		t.Errorf("error should name the rejected rate; got %q", ev.Error.Message)
	}
	if r.count(live.ServerSessionStarted) != 0 {
		t.Error("session started despite an invalid audio format")
	}
}

func TestSessionRefusesImmutableUpdates(t *testing.T) {
	ts, cfg := newTestServer(t)
	conn := dial(t, ts)
	r := startReader(conn)

	startSession(t, conn, cfg.ClientRate)
	await(t, "session.started", 2*time.Second, func() bool {
		return r.count(live.ServerSessionStarted) > 0
	})

	// gpt-live-1 fixes the delegation mode at startup.
	send(t, conn, live.SessionUpdateEvent{
		Envelope: live.Envelope{Type: live.ClientSessionUpdate, EventID: "upd"},
		Session:  live.SessionConfig{Delegation: &live.DelegationConfig{Type: live.DelegationClient}},
	})

	await(t, "an error", 2*time.Second, func() bool { return r.count(live.ServerError) > 0 })
	ev, _ := r.lastError()
	if ev.Error.Code != "immutable_field" {
		t.Errorf("error code is %q, want immutable_field", ev.Error.Code)
	}

	// A legal update must still be accepted.
	send(t, conn, live.SessionUpdateEvent{
		Envelope: live.Envelope{Type: live.ClientSessionUpdate, EventID: "upd2"},
		Session: live.SessionConfig{
			Delegation: &live.DelegationConfig{
				Responses: &live.ResponsesDelegation{Model: "some-other-model"},
			},
		},
	})
	await(t, "session.updated", 2*time.Second, func() bool {
		return r.count(live.ServerSessionUpdated) > 0
	})
}

func TestSessionCloseIsAcknowledged(t *testing.T) {
	ts, cfg := newTestServer(t)
	conn := dial(t, ts)
	r := startReader(conn)

	startSession(t, conn, cfg.ClientRate)
	await(t, "session.started", 2*time.Second, func() bool {
		return r.count(live.ServerSessionStarted) > 0
	})

	send(t, conn, live.Envelope{Type: live.ClientSessionClose, EventID: "bye"})
	await(t, "session.closed", 3*time.Second, func() bool {
		return r.count(live.ServerSessionClosed) > 0
	})
}

func TestHealthAndProviderEndpoints(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz returned %d", resp.StatusCode)
	}

	resp2, err := http.Get(ts.URL + "/v1/live/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	var body struct {
		ASR []string `json:"asr"`
		LLM []string `json:"llm"`
		TTS []string `json:"tts"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, list := range [][]string{body.ASR, body.LLM, body.TTS} {
		if len(list) == 0 {
			t.Error("a provider category is empty; the mock adapters should always register")
		}
	}
}

func TestSessionGreetingSpeaksFirst(t *testing.T) {
	ts, cfg := newTestServer(t)
	conn := dial(t, ts)
	r := startReader(conn)

	send(t, conn, live.SessionStartEvent{
		Envelope: live.Envelope{Type: live.ClientSessionStart, EventID: "start"},
		Session: live.SessionConfig{
			Audio:  &live.AudioConfig{Format: &live.AudioFormat{Type: "audio/pcm", Rate: cfg.ClientRate}},
			Golive: &live.GoliveConfig{Greeting: strptr("早上好，这里是测试。")},
		},
	})

	// No audio is sent at all: the assistant must speak unprompted.
	await(t, "greeting audio", 5*time.Second, func() bool {
		return r.audioBytes() > audio.PCM16(cfg.ClientRate).BytesForMS(200)
	})
	if r.count(live.ServerSessionStarted) == 0 {
		t.Error("greeting arrived but session.started did not")
	}
	if got := r.firstEventType(); got != live.ServerSessionStarted {
		t.Errorf("first event was %q; session.started must precede greeting audio, or the "+
			"client does not yet know the audio format", got)
	}
	if r.count(live.ServerError) != 0 {
		ev, _ := r.lastError()
		t.Errorf("greeting produced an error: %+v", ev.Error)
	}
}

func TestSessionEmptyGreetingSuppressesTheServerDefault(t *testing.T) {
	ts, cfg := newTestServer(t)
	conn := dial(t, ts)
	r := startReader(conn)

	send(t, conn, live.SessionStartEvent{
		Envelope: live.Envelope{Type: live.ClientSessionStart, EventID: "start"},
		Session: live.SessionConfig{
			Audio: &live.AudioConfig{Format: &live.AudioFormat{Type: "audio/pcm", Rate: cfg.ClientRate}},
			// An explicit empty string, which must beat the server default
			// rather than fall back to it.
			Golive: &live.GoliveConfig{Greeting: strptr("")},
		},
	})
	await(t, "session.started", 2*time.Second, func() bool {
		return r.count(live.ServerSessionStarted) > 0
	})

	time.Sleep(900 * time.Millisecond)
	if r.audioBytes() != 0 {
		t.Errorf("an explicitly empty greeting still produced %d bytes of audio", r.audioBytes())
	}
}

func strptr(s string) *string { return &s }
