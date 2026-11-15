package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/provider"
)

// wsTestServer upgrades every request and hands the connection to handle.
func wsTestServer(t *testing.T, handle func(*websocket.Conn, *http.Request)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		handle(conn, r)
	}))
}

func wsEndpoint(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

type sessionObservation struct {
	voice  string
	format string
	mark   string
	auth   string
}

// TestSynthesizeStreamsOneSegment pins the documented session: two text
// messages in (the segment, then finish_text), raw PCM16 frames out, a
// success finish frame at the end.
func TestSynthesizeStreamsOneSegment(t *testing.T) {
	obs := make(chan sessionObservation, 1)
	texts := make(chan []string, 1)
	srv := wsTestServer(t, func(conn *websocket.Conn, r *http.Request) {
		select {
		case obs <- sessionObservation{
			voice:  r.URL.Query().Get("voice_id"),
			format: r.URL.Query().Get("format"),
			mark:   r.URL.Query().Get("filter_markdown"),
			auth:   r.Header.Get("Authorization"),
		}:
		default:
		}
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var got []string
		for range 2 {
			_, data, err := conn.ReadMessage()
			if err != nil {
				// A prefetched session that was never used ends here when the
				// test closes the server; it is not a failure.
				return
			}
			got = append(got, string(data))
		}
		select {
		case texts <- got:
		default:
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte{1, 2})
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte{3, 4})
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"event":"finish","code":"success","message":"ok"}`))
	})
	defer srv.Close()

	tts := &TTS{
		Endpoint:       wsEndpoint(srv),
		APIKey:         "test-key",
		Voice:          "stella",
		FilterMarkdown: true,
		OpenTimeout:    time.Second,
		ReceiveTimeout: 3 * time.Second,
		SendTimeout:    time.Second,
		MaxIdle:        time.Minute,
	}
	st, err := tts.Open(context.Background(), provider.TTSOptions{SampleRate: 16000, Voice: "stella"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if got := st.SampleRate(); got != Rate {
		t.Fatalf("SampleRate = %d, want the tool's fixed %d", got, Rate)
	}

	ch, err := st.Synthesize(context.Background(), "你好")
	if err != nil {
		t.Fatal(err)
	}
	var audio []byte
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("synthesis failed: %v", chunk.Err)
		}
		audio = append(audio, chunk.PCM...)
	}
	if !bytes.Equal(audio, []byte{1, 2, 3, 4}) {
		t.Fatalf("audio = %v, want every binary frame in order", audio)
	}

	select {
	case got := <-texts:
		var first, second map[string]string
		if err := json.Unmarshal([]byte(got[0]), &first); err != nil {
			t.Fatalf("first message is not JSON: %v", err)
		}
		if first["event"] != "text" || first["text"] != "你好" {
			t.Fatalf("first message = %v, want the segment as a text event", first)
		}
		if err := json.Unmarshal([]byte(got[1]), &second); err != nil {
			t.Fatalf("second message is not JSON: %v", err)
		}
		if second["event"] != "finish_text" {
			t.Fatalf("second message = %v, want finish_text", second)
		}
	default:
		t.Fatal("the server never saw the text events")
	}

	select {
	case o := <-obs:
		if o.voice != "stella" || o.format != "pcm" || o.mark != "true" {
			t.Fatalf("session query = %+v, want voice_id=stella format=pcm filter_markdown=true", o)
		}
		if o.auth != "Bearer test-key" {
			t.Fatalf("Authorization = %q, want the bearer key", o.auth)
		}
	default:
		t.Fatal("the session's options were never observed")
	}
}

// TestFinishCodeIsAnError: a session can end with a content-filter refusal
// instead of audio, and the engine must not treat that as a finished segment.
func TestFinishCodeIsAnError(t *testing.T) {
	srv := wsTestServer(t, func(conn *websocket.Conn, r *http.Request) {
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for range 2 {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
		_ = conn.WriteMessage(websocket.TextMessage,
			[]byte(`{"event":"finish","code":"content_filter","message":"text did not pass review"}`))
	})
	defer srv.Close()

	tts := &TTS{Endpoint: wsEndpoint(srv), APIKey: "k", Voice: "stella", OpenTimeout: time.Second,
		ReceiveTimeout: 3 * time.Second, SendTimeout: time.Second, MaxIdle: time.Minute}
	st, err := tts.Open(context.Background(), provider.TTSOptions{SampleRate: 16000})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ch, err := st.Synthesize(context.Background(), "你好")
	if err != nil {
		t.Fatal(err)
	}
	var sawErr bool
	for chunk := range ch {
		if chunk.Err != nil {
			sawErr = true
			if !strings.Contains(chunk.Err.Error(), "content_filter") {
				t.Fatalf("error = %v, want the vendor's code named", chunk.Err)
			}
		}
	}
	if !sawErr {
		t.Fatal("a content_filter finish was reported as success")
	}
}

// TestPrewarmDialsTheSessionAheadOfNeed: the engine prewarms on the path into a
// turn, and the first segment must reuse that session rather than dial again.
func TestPrewarmDialsTheSessionAheadOfNeed(t *testing.T) {
	var conns atomic.Int32
	used := make(chan int32, 1)
	srv := wsTestServer(t, func(conn *websocket.Conn, r *http.Request) {
		n := conns.Add(1)
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for range 2 {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
		select {
		case used <- n:
		default:
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte{9, 8})
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"event":"finish","code":"success"}`))
	})
	defer srv.Close()

	tts := &TTS{Endpoint: wsEndpoint(srv), APIKey: "k", Voice: "stella", OpenTimeout: time.Second,
		ReceiveTimeout: 3 * time.Second, SendTimeout: time.Second, MaxIdle: time.Minute}
	st, err := tts.Open(context.Background(), provider.TTSOptions{SampleRate: 16000})
	if err != nil {
		t.Fatal(err)
	}
	stream := st.(*stream)
	defer st.Close()

	if err := stream.Prewarm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stream.currentConn() == nil {
		t.Fatal("Prewarm returned without dialling a session")
	}

	ch, err := stream.Synthesize(context.Background(), "你好")
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("synthesis failed: %v", chunk.Err)
		}
	}
	select {
	case n := <-used:
		if n != 1 {
			t.Fatalf("the segment was spoken on connection %d, want the prewarmed one", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the prewarmed session was never used")
	}
}

// TestEmptySegmentMakesNoSession: the engine asks for nothing to be said when
// the model's own opener was stripped, and that must not cost a handshake.
func TestEmptySegmentMakesNoSession(t *testing.T) {
	var conns atomic.Int32
	srv := wsTestServer(t, func(conn *websocket.Conn, r *http.Request) {
		conns.Add(1)
		_ = conn.Close()
	})
	defer srv.Close()

	tts := &TTS{Endpoint: wsEndpoint(srv), APIKey: "k", Voice: "stella", OpenTimeout: time.Second,
		ReceiveTimeout: time.Second, SendTimeout: time.Second}
	st, err := tts.Open(context.Background(), provider.TTSOptions{SampleRate: 16000})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ch, err := st.Synthesize(context.Background(), "   ")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := <-ch; ok {
		t.Fatal("an empty segment produced audio")
	}
	if got := conns.Load(); got != 0 {
		t.Fatalf("%d sessions opened for an empty segment, want none", got)
	}
}
