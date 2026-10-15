package edge

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/focus"
	"github.com/gorilla/websocket"
)

// fakeDiarizer speaks the wire: ready, then one frames message per audio
// message, labelling everything as speaker 0 at 80 ms.
func fakeDiarizer() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) })
	mux.HandleFunc("GET /diarize", func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		c.WriteJSON(map[string]any{"type": "ready", "lag_ms": 345, "frame_ms": 80, "model": "fake"})
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if mt != websocket.BinaryMessage {
				continue
			}
			pcm, at, ok := focus.DecodeAudio(data)
			if !ok {
				continue
			}
			n := len(pcm) / 32 / 80
			p := make([][]float32, n)
			for i := range p {
				p[i] = []float32{0.9}
			}
			c.WriteJSON(focus.FramesMsg{Type: "frames", StartMS: at, FrameMS: 80, P: p})
		}
	})
	return mux
}

func TestDiarizeIsProxiedWithTheTokenAndOneSessionAtATime(t *testing.T) {
	up := httptest.NewServer(fakeDiarizer())
	defer up.Close()
	s := NewServer()
	s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Key = "secret"
	s.QueueWait = 100 * time.Millisecond
	s.Add(NewDiarizeUpstream(up.URL).Model("nemotron-diar"))
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()
	wsURL := "ws" + strings.TrimPrefix(gw.URL, "http") + "/v1/audio/diarize?model=nemotron-diar"

	// The model is listed with its kind.
	req, _ := http.NewRequest("GET", gw.URL+"/v1/models/nemotron-diar", nil)
	resp, body := do(t, req)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"kind":"diarization"`) || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}

	// Without the token: refused before any upgrade.
	if _, resp, err := websocket.DefaultDialer.Dial(wsURL, nil); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("no token: err %v resp %v", err, resp)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := focus.Dial(ctx, wsURL, "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Lag() != 345*time.Millisecond {
		t.Fatalf("lag %v", c.Lag())
	}
	// 320 ms of audio at 1000 ms → four 80 ms frames from 1000.
	if err := c.Push(make([]byte, 320*32), 1000); err != nil {
		t.Fatal(err)
	}
	var got []focus.Frame
	timeout := time.After(3 * time.Second)
	for len(got) < 4 {
		select {
		case f := <-c.Frames():
			got = append(got, f)
		case <-timeout:
			t.Fatalf("got %d frames", len(got))
		}
	}
	if got[0].StartMS != 1000 || got[3].EndMS != 1320 || got[0].P[0] != 0.9 {
		t.Fatalf("frames %+v", got)
	}

	// A second session while the first is open: 429.
	_, resp2, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": {"Bearer secret"}})
	if err == nil || resp2 == nil || resp2.StatusCode != 429 {
		t.Fatalf("second session: err %v resp %v", err, resp2)
	}
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	b, _ := io.ReadAll(resp2.Body)
	json.Unmarshal(b, &e)
	if e.Error.Code != "rate_limit_exceeded" {
		t.Fatalf("second session body %s", b)
	}
}
