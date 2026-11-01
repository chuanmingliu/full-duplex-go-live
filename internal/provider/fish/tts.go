// Package fish is a synthesizer on Fish Audio's HTTP endpoint.
//
// One request per segment, on a connection the HTTP client keeps open: the
// vendor streams raw PCM back as it is made, and the first bytes arrive
// without any protocol greeting on the critical path. There is no persistent
// session to resynchronize: a cancelled segment is a closed response body,
// and the next segment is a new request that knows nothing of it.
//
//	FISH_TTS_API_KEY      the key (FISH_TTS_API_KEY_2…9 for more accounts)
//	FISH_TTS_VOICE_ID     reference_id of the voice
//	FISH_TTS_MODEL        s2.1-pro (default), drama-3, s2-pro, s1, s2.1-pro-free
//	FISH_TTS_LATENCY      normal (default), balanced, low
//	FISH_TTS_ENDPOINT     https://api.fish.audio/v1/tts
package fish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/provider"
)

func init() {
	provider.RegisterTTS("fish", func() (provider.TTS, error) { return New() })
}

// TTS is a Fish Audio client.
type TTS struct {
	Endpoint string
	APIKeys  []string
	VoiceID  string
	Model    string
	Latency  string
	Speed    float64
	HTTP     *http.Client
	Log      *slog.Logger
}

// New reads the environment.
func New() (*TTS, error) {
	keys, err := config.EnvKeys("FISH_TTS_API_KEY")
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, errors.New("fish tts: FISH_TTS_API_KEY is not set")
	}
	return &TTS{
		Endpoint: config.Env("FISH_TTS_ENDPOINT", "https://api.fish.audio/v1/tts"),
		APIKeys:  keys,
		VoiceID:  config.Env("FISH_TTS_VOICE_ID", ""),
		Model:    config.Env("FISH_TTS_MODEL", "s2.1-pro"),
		Latency:  config.Env("FISH_TTS_LATENCY", "normal"),
		Speed:    config.EnvFloat("FISH_TTS_SPEED", 1.0),
		HTTP: &http.Client{Transport: &http.Transport{
			MaxIdleConnsPerHost: 8, IdleConnTimeout: 5 * time.Minute, ForceAttemptHTTP2: true,
			TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second}},
		Log: slog.Default(),
	}, nil
}

func (t *TTS) Name() string { return "fish" }

// Accounts is how many keys the client holds.
func (t *TTS) Accounts() int { return len(t.APIKeys) }

// Open starts a session. The lane the context names (provider.WithLane)
// picks the account, as in the MiniMax pool: lane n is key n mod the keys.
func (t *TTS) Open(ctx context.Context, opts provider.TTSOptions) (provider.TTSStream, error) {
	rate := opts.SampleRate
	if rate == 0 {
		rate = provider.PipelineRate
	}
	switch rate {
	case 8000, 16000, 24000, 32000, 44100:
	default:
		return nil, fmt.Errorf("fish tts: sample rate %d is not one the vendor makes (8000, 16000, 24000, 32000, 44100)", rate)
	}
	account := 0
	if lane, ok := provider.Lane(ctx); ok {
		if lane < 0 {
			lane = -lane
		}
		account = lane % len(t.APIKeys)
	}
	s := &stream{tts: t, key: t.APIKeys[account], account: account, rate: rate,
		voice: opts.Voice, model: opts.Model, speed: opts.Speed}
	if s.voice == "" {
		s.voice = t.VoiceID
	}
	if s.model == "" {
		s.model = t.Model
	}
	if s.speed == 0 {
		s.speed = t.Speed
	}
	return s, nil
}

// Prewarm makes the connection before anything waits on it.
func (t *TTS) Prewarm(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, t.Endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

type stream struct {
	tts     *TTS
	key     string
	account int
	rate    int
	voice   string
	model   string
	speed   float64
}

// Account is which key the session is on, counted from zero, for the pool.
func (s *stream) Account() int { return s.account }

func (s *stream) SampleRate() int { return s.rate }

func (s *stream) Close() error { return nil }

func (s *stream) Prewarm(ctx context.Context) error { return s.tts.Prewarm(ctx) }

type request struct {
	Text        string   `json:"text"`
	ReferenceID string   `json:"reference_id,omitempty"`
	Format      string   `json:"format"`
	SampleRate  int      `json:"sample_rate"`
	Latency     string   `json:"latency,omitempty"`
	Prosody     *prosody `json:"prosody,omitempty"`
}

type prosody struct {
	Speed float64 `json:"speed"`
}

// Synthesize speaks one segment: one request, PCM streamed back.
func (s *stream) Synthesize(ctx context.Context, text string) (<-chan provider.TTSChunk, error) {
	text = strings.TrimSpace(text)
	out := make(chan provider.TTSChunk, 64)
	if text == "" {
		close(out)
		return out, nil
	}
	body := request{Text: text, ReferenceID: s.voice, Format: "pcm", SampleRate: s.rate, Latency: s.tts.Latency}
	if s.speed != 0 && s.speed != 1 {
		body.Prosody = &prosody{Speed: s.speed}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tts.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("model", s.model)
	started := time.Now()
	resp, err := s.tts.HTTP.Do(req)
	if err != nil {
		return nil, &refusal{err: fmt.Errorf("fish tts: request failed: %w", err)}
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		return nil, &refusal{status: resp.StatusCode, err: fmt.Errorf("fish tts: HTTP %d on account %d: %s", resp.StatusCode, s.account+1, summarize(snippet))}
	}
	go func() {
		defer close(out)
		defer resp.Body.Close()
		buf := make([]byte, 4096)
		first := true
		// A stray odd byte between reads would split a sample; carry it.
		var carry []byte
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				chunk := append(carry, buf[:n]...)
				carry = nil
				if len(chunk)%2 == 1 {
					carry = []byte{chunk[len(chunk)-1]}
					chunk = chunk[:len(chunk)-1]
				}
				if len(chunk) > 0 {
					if first {
						first = false
						s.tts.Log.Debug("fish tts: first audio", "model", s.model, "account", s.account+1,
							"ms", time.Since(started).Milliseconds(), "chars", len([]rune(text)))
					}
					select {
					case out <- provider.TTSChunk{PCM: append([]byte(nil), chunk...)}:
					case <-ctx.Done():
						return
					}
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) && ctx.Err() == nil {
					out <- provider.TTSChunk{Err: fmt.Errorf("fish tts: %w", err)}
				}
				return
			}
		}
	}()
	return out, nil
}

// refusal is a request the vendor did not take, with its status when it
// answered at all. The pool reads it to rest the account.
type refusal struct {
	status int
	err    error
}

func (r *refusal) Error() string { return r.err.Error() }
func (r *refusal) Unwrap() error { return r.err }

// Status is the HTTP status of the refusal, or 0 when the request never got
// an answer.
func (r *refusal) Status() int { return r.status }

func summarize(snippet []byte) string {
	var e struct {
		Message string `json:"message"`
		Reason  string `json:"reason"`
	}
	if json.Unmarshal(snippet, &e) == nil && e.Message != "" {
		if e.Reason != "" {
			return e.Message + " (" + e.Reason + ")"
		}
		return e.Message
	}
	s := strings.TrimSpace(string(snippet))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
