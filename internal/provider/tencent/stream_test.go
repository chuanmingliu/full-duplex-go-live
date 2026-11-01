package tencent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A recognition the service refuses part-way — here an account error, as
// Tencent sends when a request arrives from outside the region the account is
// enabled for — ends the read side while the caller is still speaking. The
// audio still being written then fails, and that failure used to be sent on a
// results channel the reader had already closed: a panic, in a goroutine, so
// one refused utterance ended every call on the server.
func TestRefusedRecognitionDoesNotOutliveItsResults(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Hear a little, then refuse and hang up.
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"code":6001,"message":"not enabled for this region"}`))
		_ = conn.Close()
	}))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newStream(ctx, conn)

	// The caller keeps talking for a while after the refusal.
	frame := make([]byte, 640)
	stop := time.After(400 * time.Millisecond)
	var sawErr, closed bool
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	results := s.Results()
	for {
		select {
		case <-stop:
			if !sawErr {
				t.Fatal("the refusal was never reported")
			}
			if !closed {
				t.Fatal("results were never closed")
			}
			return
		case <-tick.C:
			_ = s.Write(frame)
		case r, ok := <-results:
			if !ok {
				closed = true
				results = nil
				continue
			}
			if r.Err != nil && strings.Contains(r.Err.Error(), "6001") {
				sawErr = true
			}
		}
	}
}
