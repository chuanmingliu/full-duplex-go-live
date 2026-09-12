// Package tencent implements streaming speech recognition against Tencent
// Cloud's realtime ASR WebSocket service (asr.cloud.tencent.com/asr/v2).
//
// The protocol is: sign a query string with HMAC-SHA1, connect, wait for a
// handshake frame, push raw PCM16 frames as binary messages, and read JSON
// results where slice_type 2 marks a stabilised sentence. Sending
// {"type":"end"} asks for the final result.
package tencent

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/provider"
)

const hostPath = "asr.cloud.tencent.com/asr/v2"

// voiceFormatPCM is Tencent's code for raw PCM16.
const voiceFormatPCM = 1

func init() {
	provider.RegisterASR("tencent", func() (provider.ASR, error) { return New() })
}

// ASR is a Tencent realtime recognizer.
type ASR struct {
	AppID     string
	SecretID  string
	SecretKey string
	Engine    string
	// OpenTimeout bounds the handshake. Tencent is usually well under a
	// second; a slow open here delays the first partial for the whole turn, so
	// failing fast and retrying next utterance beats waiting.
	OpenTimeout time.Duration
}

// New builds the recognizer from the environment.
func New() (*ASR, error) {
	appID, err := config.EnvRequired("TENCENT_ASR_APP_ID")
	if err != nil {
		return nil, err
	}
	secretID, err := config.EnvRequired("TENCENT_ASR_SECRET_ID")
	if err != nil {
		return nil, err
	}
	secretKey, err := config.EnvRequired("TENCENT_ASR_SECRET_KEY")
	if err != nil {
		return nil, err
	}
	return &ASR{
		AppID:       appID,
		SecretID:    secretID,
		SecretKey:   secretKey,
		Engine:      config.Env("TENCENT_ASR_ENGINE", "16k_zh"),
		OpenTimeout: time.Duration(config.EnvFloat("TENCENT_ASR_OPEN_TIMEOUT_S", 1.5) * float64(time.Second)),
	}, nil
}

// Name implements provider.ASR.
func (a *ASR) Name() string { return "tencent" }

// SignedURL builds the signed realtime endpoint for one recognition. It is
// exported so the signature can be unit-tested against fixed inputs without a
// network call.
func (a *ASR) SignedURL(voiceID string, now time.Time) string {
	ts := now.Unix()
	params := map[string]string{
		"convert_num_mode":  "0",
		"engine_model_type": a.Engine,
		"expired":           strconv.FormatInt(ts+24*60*60, 10),
		"filter_dirty":      "0",
		"filter_modal":      "0",
		"filter_punc":       "0",
		// needvad=0: golive has already decided where the utterance starts and
		// ends. Letting the provider segment as well produces two disagreeing
		// notions of a turn.
		"needvad":          "0",
		"nonce":            strconv.FormatInt(ts, 10),
		"secretid":         a.SecretID,
		"sub_service_type": "1",
		"timestamp":        strconv.FormatInt(ts, 10),
		"voice_format":     strconv.Itoa(voiceFormatPCM),
		"voice_id":         voiceID,
		"word_info":        "0",
	}

	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var signPairs, urlPairs []string
	for _, k := range keys {
		// The signature is computed over the *unescaped* values; only the
		// request URL percent-encodes them. Getting this backwards is the
		// classic Tencent ASR 4xx.
		signPairs = append(signPairs, k+"="+params[k])
		urlPairs = append(urlPairs, k+"="+url.QueryEscape(params[k]))
	}

	signSource := fmt.Sprintf("%s/%s?%s", hostPath, a.AppID, strings.Join(signPairs, "&"))
	mac := hmac.New(sha1.New, []byte(a.SecretKey))
	mac.Write([]byte(signSource))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return fmt.Sprintf("wss://%s/%s?%s&signature=%s",
		hostPath, a.AppID, strings.Join(urlPairs, "&"), url.QueryEscape(signature))
}

// Open implements provider.ASR.
func (a *ASR) Open(ctx context.Context, opts provider.ASROptions) (provider.ASRStream, error) {
	voiceID := newVoiceID()
	endpoint := a.SignedURL(voiceID, time.Now())

	dialer := websocket.Dialer{
		HandshakeTimeout: a.OpenTimeout,
		ReadBufferSize:   16 << 10,
		WriteBufferSize:  16 << 10,
	}
	conn, resp, err := dialer.DialContext(ctx, endpoint, http.Header{})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("tencent asr: dial failed with HTTP %d: %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("tencent asr: dial failed: %w", err)
	}

	slog.Debug("tencent asr: connected",
		"engine", a.Engine,
		"voice_id", voiceID,
		"rate", opts.SampleRate)

	s := &stream{
		conn:    conn,
		results: make(chan provider.ASRResult, 32),
		writes:  make(chan []byte, 64),
		ctx:     ctx,
	}
	go s.readLoop()
	go s.writeLoop()
	return s, nil
}

type stream struct {
	conn    *websocket.Conn
	results chan provider.ASRResult
	writes  chan []byte
	ctx     context.Context

	mu       sync.Mutex
	stable   []string
	partial  string
	sendDone bool
	closed   bool
}

type asrFrame struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Final   int    `json:"final"`
	Result  struct {
		SliceType    int    `json:"slice_type"`
		VoiceTextStr string `json:"voice_text_str"`
	} `json:"result"`
}

func (s *stream) Write(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	buf := make([]byte, len(pcm))
	copy(buf, pcm)
	select {
	case s.writes <- buf:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	default:
		// Audio is worth dropping rather than blocking the engine loop: a
		// backed-up recognizer socket means the hypothesis is already stale.
		return fmt.Errorf("tencent asr: write queue full")
	}
}

func (s *stream) CloseSend() error {
	s.mu.Lock()
	if s.sendDone || s.closed {
		s.mu.Unlock()
		return nil
	}
	s.sendDone = true
	s.mu.Unlock()

	select {
	case s.writes <- nil: // nil is the end-of-utterance marker
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func (s *stream) Results() <-chan provider.ASRResult { return s.results }

func (s *stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	return s.conn.Close()
}

func (s *stream) writeLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case buf, ok := <-s.writes:
			if !ok {
				return
			}
			_ = s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if buf == nil {
				_ = s.conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"end"}`))
				return
			}
			if err := s.conn.WriteMessage(websocket.BinaryMessage, buf); err != nil {
				s.fail(fmt.Errorf("tencent asr: write: %w", err))
				return
			}
		}
	}
}

func (s *stream) readLoop() {
	defer s.finish()
	for {
		_ = s.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			s.mu.Lock()
			done := s.sendDone || s.closed
			s.mu.Unlock()
			if !done {
				s.emit(provider.ASRResult{Err: fmt.Errorf("tencent asr: read: %w", err)})
			}
			return
		}

		var frame asrFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			s.emit(provider.ASRResult{Err: fmt.Errorf("tencent asr: malformed frame: %w", err)})
			return
		}
		if frame.Code != 0 {
			s.emit(provider.ASRResult{Err: fmt.Errorf("tencent asr: code %d: %s", frame.Code, frame.Message)})
			return
		}

		text := strings.TrimSpace(frame.Result.VoiceTextStr)
		s.mu.Lock()
		if text != "" {
			// slice_type 2 means the recognizer has committed to that sentence
			// and will not revise it; anything else is a live hypothesis.
			if frame.Result.SliceType == 2 {
				s.stable = append(s.stable, text)
				s.partial = ""
			} else {
				s.partial = text
			}
		}
		current := s.currentLocked()
		s.mu.Unlock()

		if frame.Final == 1 {
			s.emit(provider.ASRResult{Text: current, Final: true})
			return
		}
		if current != "" {
			s.emit(provider.ASRResult{Text: current})
		}
	}
}

func (s *stream) currentLocked() string {
	return strings.TrimSpace(strings.Join(s.stable, "") + s.partial)
}

func (s *stream) emit(r provider.ASRResult) {
	select {
	case s.results <- r:
	case <-s.ctx.Done():
	}
}

func (s *stream) fail(err error) {
	s.emit(provider.ASRResult{Err: err})
}

// finish closes the results channel exactly once.
func (s *stream) finish() {
	s.mu.Lock()
	already := s.closed
	s.closed = true
	s.mu.Unlock()
	if !already {
		_ = s.conn.Close()
	}
	close(s.results)
}

func newVoiceID() string {
	return fmt.Sprintf("golive-%d", time.Now().UnixNano())
}
