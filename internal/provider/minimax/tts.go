// Package minimax implements streaming text-to-speech against MiniMax's T2A
// WebSocket API (wss://.../ws/v1/t2a_v2).
//
// The protocol is a long-lived task: connect, wait for connected_success, send
// task_start, then one task_continue per text segment, reading frames whose
// data.audio is hex-encoded PCM until is_final. Keeping the task open across
// segments is what keeps time-to-first-audio in the tens of milliseconds — a
// fresh connection per sentence would dominate the latency budget.
package minimax

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/provider"
)

func init() {
	provider.RegisterTTS("minimax", func() (provider.TTS, error) { return New() })
}

// TTS is a MiniMax T2A client.
type TTS struct {
	Endpoint       string
	APIKey         string
	Model          string
	VoiceID        string
	Speed          float64
	LanguageBoost  string
	OpenTimeout    time.Duration
	ReceiveTimeout time.Duration
	MaxIdle        time.Duration
}

// New builds the client from the environment.
func New() (*TTS, error) {
	key, err := config.EnvRequired("MINIMAX_TTS_API_KEY")
	if err != nil {
		return nil, err
	}
	return &TTS{
		// api.minimaxi.com for mainland-platform keys, api.minimax.io for
		// global ones. Using the wrong host authenticates and then fails at
		// task_start, which is a confusing way to find out.
		Endpoint:       config.Env("MINIMAX_TTS_WEBSOCKET_ENDPOINT", "wss://api.minimaxi.com/ws/v1/t2a_v2"),
		APIKey:         key,
		Model:          config.Env("MINIMAX_TTS_MODEL", "speech-2.8-turbo"),
		VoiceID:        config.Env("MINIMAX_TTS_VOICE_ID", ""),
		Speed:          config.EnvFloat("MINIMAX_TTS_SPEED", 1.2),
		LanguageBoost:  config.Env("MINIMAX_TTS_LANGUAGE_BOOST", "auto"),
		OpenTimeout:    time.Duration(config.EnvFloat("MINIMAX_TTS_WEBSOCKET_OPEN_TIMEOUT_S", 5) * float64(time.Second)),
		ReceiveTimeout: time.Duration(config.EnvFloat("MINIMAX_TTS_WEBSOCKET_RECEIVE_TIMEOUT_S", 30) * float64(time.Second)),
		MaxIdle:        time.Duration(config.EnvFloat("MINIMAX_TTS_WEBSOCKET_MAX_IDLE_S", 90) * float64(time.Second)),
	}, nil
}

// Name implements provider.TTS.
func (t *TTS) Name() string { return "minimax" }

// Open implements provider.TTS.
func (t *TTS) Open(ctx context.Context, opts provider.TTSOptions) (provider.TTSStream, error) {
	rate := opts.SampleRate
	if rate <= 0 {
		rate = provider.PipelineRate
	}
	voice := opts.Voice
	if voice == "" {
		voice = t.VoiceID
	}
	if voice == "" {
		return nil, fmt.Errorf("minimax tts: no voice configured; set MINIMAX_TTS_VOICE_ID or session.audio.output.voice")
	}
	speed := opts.Speed
	if speed <= 0 {
		speed = t.Speed
	}
	s := &stream{
		tts:   t,
		rate:  rate,
		voice: voice,
		speed: speed,
	}
	if err := s.connect(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

type stream struct {
	tts   *TTS
	rate  int
	voice string
	speed float64

	// busy serializes Synthesize calls; the provider contract says callers do
	// not overlap them, and the MiniMax task is a single ordered stream.
	busy sync.Mutex

	// connMu guards the connection itself. It is deliberately separate from
	// busy so Close can tear down a socket that a synthesis is still blocked
	// reading from, instead of waiting out the 30-second receive timeout.
	connMu  sync.Mutex
	conn    *websocket.Conn
	lastUse time.Time
	closed  bool
}

type wsFrame struct {
	Event   string `json:"event"`
	IsFinal bool   `json:"is_final"`
	Data    struct {
		Audio  string `json:"audio"`
		Status int    `json:"status"`
	} `json:"data"`
	BaseResp *struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

func (s *stream) SampleRate() int { return s.rate }

func (s *stream) taskStart() map[string]any {
	return map[string]any{
		"event":          "task_start",
		"model":          s.tts.Model,
		"language_boost": s.tts.LanguageBoost,
		"voice_setting": map[string]any{
			"voice_id":              s.voice,
			"speed":                 s.speed,
			"vol":                   1.0,
			"pitch":                 0,
			"english_normalization": false,
		},
		"audio_setting": map[string]any{
			"sample_rate": s.rate,
			"format":      "pcm",
			"channel":     1,
		},
		// MiniMax documents false as the lower-latency segmentation mode, which
		// is the right trade for a conversational turn.
		"continuous_sound": false,
	}
}

func (s *stream) connect(ctx context.Context) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: s.tts.OpenTimeout,
		ReadBufferSize:   64 << 10,
		WriteBufferSize:  16 << 10,
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+s.tts.APIKey)

	conn, resp, err := dialer.DialContext(ctx, s.tts.Endpoint, header)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("minimax tts: dial failed with HTTP %d: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("minimax tts: dial failed: %w", err)
	}

	s.setConn(conn)
	for _, step := range []func() error{
		func() error { return s.expect(ctx, "connected_success", s.tts.OpenTimeout) },
		func() error { return s.send(s.taskStart()) },
		func() error { return s.expect(ctx, "task_started", s.tts.OpenTimeout) },
	} {
		if err := step(); err != nil {
			s.dropConn()
			return err
		}
	}
	s.touch()
	return nil
}

// Synthesize implements provider.TTSStream. Calls are serialized; the engine
// never overlaps them on one stream.
func (s *stream) Synthesize(ctx context.Context, text string) (<-chan provider.TTSChunk, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		ch := make(chan provider.TTSChunk)
		close(ch)
		return ch, nil
	}

	s.busy.Lock()
	if s.isClosed() {
		s.busy.Unlock()
		return nil, fmt.Errorf("minimax tts: stream is closed")
	}
	if s.needsReconnect() {
		s.dropConn()
		if err := s.connect(ctx); err != nil {
			s.busy.Unlock()
			return nil, err
		}
	}
	if err := s.send(map[string]any{"event": "task_continue", "text": text}); err != nil {
		s.busy.Unlock()
		return nil, err
	}

	out := make(chan provider.TTSChunk, 16)
	go func() {
		defer s.busy.Unlock()
		defer close(out)
		s.readAudio(ctx, out)
	}()
	return out, nil
}

func (s *stream) readAudio(ctx context.Context, out chan<- provider.TTSChunk) {
	// MiniMax repeats the whole utterance in a trailing status=2 frame. Once
	// incremental status=1 audio has been delivered, replaying it would double
	// every sentence.
	sawIncremental := false

	for {
		if ctx.Err() != nil {
			return
		}
		frame, err := s.recv(s.tts.ReceiveTimeout)
		if err != nil {
			s.emit(ctx, out, provider.TTSChunk{Err: err})
			s.dropConn()
			return
		}
		if frame.BaseResp != nil && frame.BaseResp.StatusCode != 0 {
			s.emit(ctx, out, provider.TTSChunk{Err: fmt.Errorf("minimax tts: status %d: %s",
				frame.BaseResp.StatusCode, frame.BaseResp.StatusMsg)})
			s.dropConn()
			return
		}
		if frame.Event == "task_failed" {
			s.emit(ctx, out, provider.TTSChunk{Err: fmt.Errorf("minimax tts: task failed")})
			s.dropConn()
			return
		}

		if frame.Data.Audio != "" {
			if frame.Data.Status == 2 && sawIncremental {
				// Aggregate duplicate of audio already sent.
			} else {
				pcm, err := hex.DecodeString(frame.Data.Audio)
				if err != nil {
					s.emit(ctx, out, provider.TTSChunk{Err: fmt.Errorf("minimax tts: audio is not hex: %w", err)})
					s.dropConn()
					return
				}
				if frame.Data.Status != 2 {
					sawIncremental = true
				}
				if !s.emit(ctx, out, provider.TTSChunk{PCM: pcm}) {
					return
				}
			}
		}

		if frame.IsFinal {
			s.touch()
			return
		}
	}
}

func (s *stream) emit(ctx context.Context, out chan<- provider.TTSChunk, chunk provider.TTSChunk) bool {
	select {
	case out <- chunk:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *stream) send(payload map[string]any) error {
	conn := s.currentConn()
	if conn == nil {
		return fmt.Errorf("minimax tts: not connected")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("minimax tts: write: %w", err)
	}
	return nil
}

func (s *stream) recv(timeout time.Duration) (*wsFrame, error) {
	conn := s.currentConn()
	if conn == nil {
		return nil, fmt.Errorf("minimax tts: not connected")
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	_, data, err := conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("minimax tts: read: %w", err)
	}
	var frame wsFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return nil, fmt.Errorf("minimax tts: malformed frame: %w", err)
	}
	return &frame, nil
}

func (s *stream) expect(ctx context.Context, event string, timeout time.Duration) error {
	frame, err := s.recv(timeout)
	if err != nil {
		return err
	}
	if frame.BaseResp != nil && frame.BaseResp.StatusCode != 0 {
		return fmt.Errorf("minimax tts: %s failed with status %d: %s",
			event, frame.BaseResp.StatusCode, frame.BaseResp.StatusMsg)
	}
	if frame.Event != event {
		return fmt.Errorf("minimax tts: expected %q but got %q", event, frame.Event)
	}
	return nil
}

func (s *stream) dropConn() {
	s.connMu.Lock()
	conn := s.conn
	s.conn = nil
	s.connMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (s *stream) currentConn() *websocket.Conn {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	return s.conn
}

func (s *stream) setConn(conn *websocket.Conn) {
	s.connMu.Lock()
	s.conn = conn
	s.lastUse = time.Now()
	s.connMu.Unlock()
}

func (s *stream) touch() {
	s.connMu.Lock()
	s.lastUse = time.Now()
	s.connMu.Unlock()
}

func (s *stream) isClosed() bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	return s.closed
}

// needsReconnect reports a missing socket, or one idle past the server's
// window. A task left idle that long is dead but still looks open, and
// discovering that mid-sentence costs a turn.
func (s *stream) needsReconnect() bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if s.conn == nil {
		return true
	}
	return s.tts.MaxIdle > 0 && time.Since(s.lastUse) > s.tts.MaxIdle
}

// Close ends the task and the connection. It never waits on an in-flight
// synthesis: closing the socket is what unblocks one.
func (s *stream) Close() error {
	s.connMu.Lock()
	if s.closed {
		s.connMu.Unlock()
		return nil
	}
	s.closed = true
	conn := s.conn
	s.conn = nil
	s.connMu.Unlock()

	if conn == nil {
		return nil
	}
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	if data, err := json.Marshal(map[string]any{"event": "task_finish"}); err == nil {
		_ = conn.WriteMessage(websocket.TextMessage, data)
	}
	return conn.Close()
}
