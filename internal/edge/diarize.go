package edge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Diarization is a model that says who is speaking, frame by frame.
const Diarization Kind = "diarization"

// DiarizeUpstream is a streaming diarization service speaking the wire
// protocol in internal/focus/wire.go over a WebSocket: audio in as binary
// messages with an 8-byte time prefix, labelled frames out as JSON. The
// gateway passes messages through unchanged, adds the token and the one-
// session-at-a-time slot, and reports the service's health.
type DiarizeUpstream struct {
	// WS is the service's WebSocket URL (ws://127.0.0.1:8031/diarize).
	WS string
	// Health is its health URL (http://127.0.0.1:8031/health).
	Health string
	HTTP   *http.Client
}

// NewDiarizeUpstream serves the diarizer at base (http://127.0.0.1:8031).
func NewDiarizeUpstream(base string) *DiarizeUpstream {
	base = strings.TrimRight(base, "/")
	ws := "ws" + strings.TrimPrefix(base, "http") + "/diarize"
	return &DiarizeUpstream{WS: ws, Health: base + "/health", HTTP: &http.Client{Timeout: 5 * time.Second}}
}

// Model is the model entry.
func (d *DiarizeUpstream) Model(id string) *Model {
	return &Model{ID: id, Kind: Diarization, Family: "nemotron-diarization", Upstream: d.WS, Rate: 16000, Streaming: true,
		Licence:  "OpenMDW 1.1",
		Note:     "streaming speaker activity: a WebSocket, 16 kHz PCM16 in with an 8-byte ms time prefix, JSON frames out; labels run ~320 ms behind the audio plus compute; speakers numbered in order of first arrival",
		diarizer: d, health: d.health}
}

func (d *DiarizeUpstream) health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.Health, nil)
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	return nil
}

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }, ReadBufferSize: 1 << 16, WriteBufferSize: 1 << 16}

// diarize is GET /v1/audio/diarize?model=…: one streaming session, proxied.
func (s *Server) diarize(w http.ResponseWriter, r *http.Request) {
	m, ok := s.model(w, r, r.URL.Query().Get("model"), Diarization)
	if !ok {
		return
	}
	release, err := s.acquire(r.Context(), m.ID)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	defer release()
	// Connect upstream first, so a service that is down is a 502 and not a
	// WebSocket that closes at once.
	dctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	up, resp, err := websocket.DefaultDialer.DialContext(dctx, m.diarizer.WS, nil)
	cancel()
	if err != nil {
		if resp != nil {
			s.fail(w, r, http.StatusBadGateway, "server_error", "", fmt.Sprintf("the model's service refused the connection: HTTP %d", resp.StatusCode))
		} else {
			s.fail(w, r, http.StatusBadGateway, "server_error", "", fmt.Sprintf("the model's service is not answering: %v", err))
		}
		return
	}
	defer up.Close()
	down, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer down.Close()
	began := time.Now()
	var audioMS int64
	var frames int
	var once sync.Once
	done := make(chan struct{})
	finish := func() { once.Do(func() { close(done) }) }
	// client → service
	go func() {
		defer finish()
		for {
			mt, data, err := down.ReadMessage()
			if err != nil {
				up.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
				return
			}
			if mt == websocket.BinaryMessage && len(data) > 8 {
				audioMS += int64(len(data)-8) / 32 // 16 kHz PCM16: 32 bytes a ms
			}
			if err := up.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}()
	// service → client
	go func() {
		defer finish()
		for {
			mt, data, err := up.ReadMessage()
			if err != nil {
				down.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
				return
			}
			if mt == websocket.TextMessage {
				frames++
			}
			if err := down.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}()
	<-done
	s.Log.Info("edge: diarized", "id", requestIDOf(r), "model", m.ID, "audio_ms", audioMS, "messages", frames,
		"ms", time.Since(began).Milliseconds())
}
