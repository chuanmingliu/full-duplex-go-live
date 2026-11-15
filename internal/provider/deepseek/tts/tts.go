// Package tts implements streaming text-to-speech against DeepSeek's
// /v1/tools/tts WebSocket tool.
//
// The protocol is a session, not a long-lived task: connect, send the text as
// one or more {"event":"text"} messages, send {"event":"finish_text"}, and
// read raw PCM16 mono audio at 24 kHz as binary frames until a text frame
// {"event":"finish","code":"success"}. A session ends with that finish frame
// (or after 60 s without text), so unlike MiniMax there is no task to keep
// open across segments.
//
// golive's TTSStream contract is one segment per Synthesize call — the engine
// cuts a tiny first segment to get a syllable out early, then speaks clause by
// clause — so each segment is its own session here. That is the cost of this
// vendor's protocol: a WebSocket handshake before a segment can make a sound.
// Two things keep it off the critical path:
//
//   - Prewarm dials a session on the engine's prewarm path (a turn is coming,
//     so the handshake happens while the caller is still talking), and a
//     segment that arrives with a live session uses it.
//   - After each segment the next session is dialled in the background, so a
//     multi-segment answer pays the handshake once rather than once per clause.
//
// A session left idle is dropped and redialled after DEEPSEEK_TTS_MAX_IDLE_S:
// the vendor ends a session that receives no text, and a dead socket discovered
// mid-sentence costs a turn.
package tts

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/provider"
)

const (
	// defaultEndpoint is the documented streaming synthesis tool.
	defaultEndpoint = "wss://api.deepseek.com/v1/tools/tts"
	// Rate is the sample rate of the audio the tool streams: raw 16-bit mono
	// PCM at 24 kHz. The engine resamples to whatever the session's client
	// wants, so this is fixed rather than negotiated.
	Rate = 24000
	// defaultVoice is the tool's documented default. The other one is "tide".
	defaultVoice = "stella"
	// minAudioChunk drops empty binary frames rather than handing the engine
	// zero-length audio.
	minAudioChunk = 2
)

func init() {
	provider.RegisterTTS("deepseek", func() (provider.TTS, error) { return New() })
}

// TTS is a DeepSeek streaming synthesizer.
type TTS struct {
	Endpoint string
	APIKey   string
	// Voice is the voice to speak with when a session asks for none:
	// "stella" (female, the default) or "tide" (male).
	Voice string
	// FilterMarkdown asks the tool to strip Markdown before reading. On by
	// default, as the vendor documents: an answer's formatting is for the eye,
	// not the ear.
	FilterMarkdown bool
	OpenTimeout    time.Duration
	// ReceiveTimeout bounds the wait for the next frame of a segment that is
	// being synthesized.
	ReceiveTimeout time.Duration
	// SendTimeout bounds writing a text event on the socket.
	SendTimeout time.Duration
	// MaxIdle is how long a session dialled ahead of need may sit before it is
	// dialled again. The vendor ends a session that sees no text for 60 s.
	MaxIdle time.Duration
}

// New builds the synthesizer from the environment.
func New() (*TTS, error) {
	key := config.Env("DEEPSEEK_TTS_API_KEY", "")
	if key == "" {
		// The speech tools and the chat completion endpoint share one platform
		// key; a deployment that already talks to the model has this set.
		key = config.Env("DEEPSEEK_API_KEY", "")
	}
	if key == "" {
		return nil, fmt.Errorf("deepseek tts: set DEEPSEEK_TTS_API_KEY or DEEPSEEK_API_KEY")
	}
	return &TTS{
		Endpoint:       config.Env("DEEPSEEK_TTS_URL", defaultEndpoint),
		APIKey:         key,
		Voice:          config.Env("DEEPSEEK_TTS_VOICE", defaultVoice),
		FilterMarkdown: config.EnvBool("DEEPSEEK_TTS_FILTER_MARKDOWN", true),
		OpenTimeout:    secondsEnv("DEEPSEEK_TTS_OPEN_TIMEOUT_S", 5),
		ReceiveTimeout: secondsEnv("DEEPSEEK_TTS_RECEIVE_TIMEOUT_S", 30),
		SendTimeout:    secondsEnv("DEEPSEEK_TTS_SEND_TIMEOUT_S", 10),
		MaxIdle:        secondsEnv("DEEPSEEK_TTS_MAX_IDLE_S", 20),
	}, nil
}

func secondsEnv(key string, def float64) time.Duration {
	return time.Duration(config.EnvFloat(key, def) * float64(time.Second))
}

// Name implements provider.TTS.
func (t *TTS) Name() string { return "deepseek" }

// Open implements provider.TTS. The connection is made when audio is first
// wanted (or earlier, on the engine's Prewarm path), because a segment's
// session is the unit of connection here and opening one for a session that
// never speaks is a handshake spent twice.
func (t *TTS) Open(ctx context.Context, opts provider.TTSOptions) (provider.TTSStream, error) {
	voice := strings.TrimSpace(opts.Voice)
	if voice == "" {
		voice = strings.TrimSpace(t.Voice)
	}
	if voice == "" {
		voice = defaultVoice
	}
	return &stream{tts: t, voice: voice, rate: Rate}, nil
}

type stream struct {
	tts   *TTS
	voice string
	rate  int

	// busy serializes Synthesize calls; the contract says callers do not
	// overlap them, and a session carries one segment's text. It is also what
	// Prewarm tries, so it never queues behind real work.
	busy sync.Mutex

	// connMu guards the socket, separately from busy, so Close can tear down a
	// socket a synthesis is blocked reading.
	connMu   sync.Mutex
	conn     *websocket.Conn
	dialedAt time.Time
	closed   bool
}

// SampleRate implements provider.TTSStream.
func (s *stream) SampleRate() int { return s.rate }

// Synthesize implements provider.TTSStream.
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
		return nil, fmt.Errorf("deepseek tts: stream is closed")
	}
	if err := ctx.Err(); err != nil {
		s.busy.Unlock()
		return nil, err
	}

	conn := s.currentConn()
	if conn == nil || s.idleTooLong() {
		s.dropConn()
		var err error
		if conn, err = s.dial(ctx); err != nil {
			s.busy.Unlock()
			return nil, err
		}
	}
	if err := s.sendText(conn, text); err != nil {
		s.dropConn()
		s.busy.Unlock()
		return nil, err
	}

	out := make(chan provider.TTSChunk, 16)
	go func() {
		defer close(out)
		s.readAudio(ctx, conn, out)
		// The vendor ends the session on finish_text, so the socket is spent
		// either way. Dial the next one now, while this segment's audio is
		// still playing, so the next clause does not wait for a handshake.
		s.dropConn()
		s.busy.Unlock()
		go s.prefetch()
	}()
	return out, nil
}

// Prewarm implements provider.Prewarmer: a session is dialled before the first
// segment needs it, so the handshake overlaps the caller still talking.
func (s *stream) Prewarm(ctx context.Context) error {
	if !s.busy.TryLock() {
		return nil // a synthesis in progress is proof the path is already warm
	}
	defer s.busy.Unlock()
	if s.isClosed() {
		return fmt.Errorf("deepseek tts: stream is closed")
	}
	if s.currentConn() != nil && !s.idleTooLong() {
		return nil
	}
	s.dropConn()
	_, err := s.dial(ctx)
	return err
}

// prefetch dials the next segment's session without blocking anything.
func (s *stream) prefetch() {
	if !s.busy.TryLock() {
		return
	}
	defer s.busy.Unlock()
	if s.isClosed() || s.currentConn() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.tts.OpenTimeout)
	defer cancel()
	if _, err := s.dial(ctx); err != nil {
		slog.Debug("deepseek tts: could not dial the next segment ahead of time", "err", err)
	}
}

// readAudio consumes one segment: binary frames are PCM16 to be played, and
// the text frame ends the session. Everything it hands to out is the caller's
// to play; the channel closes when the segment is over or has failed.
func (s *stream) readAudio(ctx context.Context, conn *websocket.Conn, out chan<- provider.TTSChunk) {
	for {
		if ctx.Err() != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(s.tts.ReceiveTimeout))
		kind, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				s.emit(ctx, out, provider.TTSChunk{Err: fmt.Errorf("deepseek tts: read: %w", err)})
			}
			return
		}
		switch kind {
		case websocket.BinaryMessage:
			if len(data) < minAudioChunk {
				continue
			}
			if !s.emit(ctx, out, provider.TTSChunk{PCM: data}) {
				return
			}
		case websocket.TextMessage:
			var frame struct {
				Event   string `json:"event"`
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(data, &frame); err != nil {
				s.emit(ctx, out, provider.TTSChunk{Err: fmt.Errorf("deepseek tts: malformed frame: %w", err)})
				return
			}
			if frame.Event != "finish" {
				// The tool documents only the finish event; anything else is
				// informational and not a reason to fail the segment.
				continue
			}
			if frame.Code != "" && frame.Code != "success" {
				s.emit(ctx, out, provider.TTSChunk{Err: fmt.Errorf("deepseek tts: %s: %s", frame.Code, frame.Message)})
			}
			return
		}
	}
}

// emit hands one chunk to the caller, bounded so a caller that stopped reading
// without cancelling cannot wedge the stream forever. It reports whether the
// chunk was taken.
func (s *stream) emit(ctx context.Context, out chan<- provider.TTSChunk, chunk provider.TTSChunk) bool {
	stall := s.tts.ReceiveTimeout
	if stall <= 0 {
		stall = 30 * time.Second
	}
	timer := time.NewTimer(stall)
	defer timer.Stop()
	select {
	case out <- chunk:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		slog.Warn("deepseek tts: the caller stopped reading without cancelling; abandoning the segment",
			"stall", stall)
		return false
	}
}

// ConsumerTimeout lets PCM proxies preserve the read's abandonment bound.
func (s *stream) ConsumerTimeout() time.Duration {
	if s.tts.ReceiveTimeout > 0 {
		return s.tts.ReceiveTimeout
	}
	return 30 * time.Second
}

// sendText sends the segment and tells the tool the text is complete. Both
// messages are required: without finish_text the tool waits for more text
// instead of synthesizing what it has.
func (s *stream) sendText(conn *websocket.Conn, text string) error {
	for _, payload := range []map[string]string{
		{"event": "text", "text": text},
		{"event": "finish_text"},
	} {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		_ = conn.SetWriteDeadline(time.Now().Add(s.tts.SendTimeout))
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return fmt.Errorf("deepseek tts: write: %w", err)
		}
	}
	return nil
}

// dial opens one session, with the voice and format this stream was configured
// for.
func (s *stream) dial(ctx context.Context) (*websocket.Conn, error) {
	endpoint, err := s.endpoint()
	if err != nil {
		return nil, err
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: s.tts.OpenTimeout,
		ReadBufferSize:   64 << 10,
		WriteBufferSize:  16 << 10,
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+s.tts.APIKey)

	conn, resp, err := dialer.DialContext(ctx, endpoint, header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("deepseek tts: dial failed with HTTP %d: %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("deepseek tts: dial failed: %w", err)
	}
	if !s.setConn(conn) {
		_ = conn.Close()
		return nil, fmt.Errorf("deepseek tts: stream closed while connecting")
	}
	slog.Debug("deepseek tts: session opened", "voice", s.voice, "rate", s.rate)
	return conn, nil
}

// endpoint adds the session's options to the tool URL. The query parameters
// are the documented per-session settings; the text itself is sent later.
func (s *stream) endpoint() (string, error) {
	u, err := url.Parse(s.tts.Endpoint)
	if err != nil {
		return "", fmt.Errorf("deepseek tts: bad endpoint %q: %w", s.tts.Endpoint, err)
	}
	q := u.Query()
	q.Set("voice_id", s.voice)
	q.Set("format", "pcm")
	q.Set("filter_markdown", strconv.FormatBool(s.tts.FilterMarkdown))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *stream) currentConn() *websocket.Conn {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	return s.conn
}

// idleTooLong reports a socket the vendor has probably ended. A session with no
// text is ended by the vendor after 60 s; MaxIdle stays inside that.
func (s *stream) idleTooLong() bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	return s.conn != nil && s.tts.MaxIdle > 0 && time.Since(s.dialedAt) > s.tts.MaxIdle
}

func (s *stream) setConn(conn *websocket.Conn) bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if s.closed {
		return false
	}
	s.conn = conn
	s.dialedAt = time.Now()
	return true
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

func (s *stream) isClosed() bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	return s.closed
}

// Close implements provider.TTSStream. Closing the socket is what unblocks a
// synthesis still reading from it, so this never waits for one.
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
	if conn != nil {
		return conn.Close()
	}
	return nil
}
