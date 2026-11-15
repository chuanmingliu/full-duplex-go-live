// Package asr implements speech recognition against DeepSeek's batch
// recognition tool (POST /v1/tools/asr).
//
// The DeepSeek API does not stream recognition. One request carries a whole
// audio file — 16-bit PCM WAV or Ogg Opus, at most 120 s and 8 MiB — and
// returns the finished transcript; the guide says so outright: "Streaming
// recognition is not supported."
//
// golive's engine is built around a recognizer that emits hypotheses while the
// caller is still speaking, and it leans on them: the transcript, the
// interruption hold, semantic endpointing, the decision to guess during a
// pause, and the early final that begins the answer inside the pause all read
// partials. So this adapter does not simply wait for the end of the utterance.
// It buffers the audio the engine writes and re-transcribes the buffer at
// DEEPSEEK_ASR_PARTIAL_MS while the caller speaks, delivering each answer as a
// partial hypothesis in the format the engine expects; CloseSend then sends
// the whole buffer once more and delivers the transcript as the final.
//
// Pseudo-streaming is not streaming, and the difference is latency, not
// correctness:
//
//   - A partial is only as fresh as the request that produced it. Each one
//     costs a round trip plus the vendor's decode of the whole utterance so
//     far, so the last partial at the turn's close trails the speaker by more
//     than a realtime recognizer's does.
//   - Every partial is billed as audio: re-transcribing a growing utterance
//     multiplies the vendor's per-hour price by the number of requests.
//   - The final is asked for when the engine closes the utterance and takes a
//     full HTTP round trip, where a realtime recognizer's final is a tenth of
//     a second on a connection that is already open.
//
// The engine survives all of that — its documented fallbacks for a recognizer
// that is slow or silent are what it does anyway — but a deployment that
// measured its turn latency against Tencent's realtime recognizer will measure
// it worse here. The adapter is for the case where one DeepSeek account (and
// one bill) for both speech tools matters more than a few hundred milliseconds
// at the start of a turn.
package asr

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/provider"
)

const (
	// defaultEndpoint is the documented recognition tool.
	defaultEndpoint = "https://api.deepseek.com/v1/tools/asr"
	// maxAudio is the longest audio one request may carry. The vendor rejects
	// anything longer; the engine's max_speech_ms keeps ordinary utterances far
	// below it, so this is a guard rather than a limit the engine plans around.
	maxAudio = 120 * time.Second
	// minPartialAudio is how much new audio makes a partial worth asking for.
	// Less than a syllable or two produces hypotheses that only cost time.
	minPartialAudio = 200 * time.Millisecond
)

func init() {
	provider.RegisterASR("deepseek", func() (provider.ASR, error) { return New() })
}

// ASR is a DeepSeek batch recognizer presented through the streaming contract.
type ASR struct {
	// Endpoint is the recognition tool URL.
	Endpoint string
	APIKey   string
	// PartialInterval is how often a partial hypothesis is requested while the
	// caller speaks. Zero (or an engine that asked for no interim results)
	// disables partials: the adapter then behaves as a plain batch recognizer,
	// which works but leaves the engine's pause and endpointing logic without
	// words until the final.
	PartialInterval time.Duration
	// PartialTimeout bounds one partial request. A partial that has not
	// answered by then is stale anyway, and must not hold up the final.
	PartialTimeout time.Duration
	// FinalTimeout bounds the request that answers an utterance. The engine
	// stops waiting after a few seconds regardless; this only bounds a request
	// that has already lost its reason to finish.
	FinalTimeout time.Duration
	// HTTP is the client used for requests. Nil means http.DefaultClient.
	HTTP *http.Client
}

// New builds the recognizer from the environment.
func New() (*ASR, error) {
	key := config.Env("DEEPSEEK_ASR_API_KEY", "")
	if key == "" {
		// The speech tools and the chat completion endpoint share one platform
		// key; a deployment that already talks to the model has this set.
		key = config.Env("DEEPSEEK_API_KEY", "")
	}
	if key == "" {
		return nil, fmt.Errorf("deepseek asr: set DEEPSEEK_ASR_API_KEY or DEEPSEEK_API_KEY")
	}
	return &ASR{
		Endpoint:        config.Env("DEEPSEEK_ASR_URL", defaultEndpoint),
		APIKey:          key,
		PartialInterval: durationEnv("DEEPSEEK_ASR_PARTIAL_MS", 400),
		PartialTimeout:  durationEnv("DEEPSEEK_ASR_PARTIAL_TIMEOUT_S", 3),
		FinalTimeout:    durationEnv("DEEPSEEK_ASR_FINAL_TIMEOUT_S", 15),
	}, nil
}

func durationEnv(key string, defMS int) time.Duration {
	return time.Duration(config.EnvFloat(key, float64(defMS)) * float64(time.Millisecond))
}

// Name implements provider.ASR.
func (a *ASR) Name() string { return "deepseek" }

func (a *ASR) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return http.DefaultClient
}

// Open implements provider.ASR. There is no connection to make — the request
// is created when there is audio to send — so this returns at once, which is
// what the engine's predial path expects of a recognizer.
func (a *ASR) Open(ctx context.Context, opts provider.ASROptions) (provider.ASRStream, error) {
	s := &stream{
		asr:       a,
		opts:      opts,
		rate:      wavRate(opts.SampleRate),
		results:   make(chan provider.ASRResult, 32),
		finalWake: make(chan struct{}, 1),
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	go s.run()
	return s, nil
}

type stream struct {
	asr  *ASR
	opts provider.ASROptions
	rate int

	ctx    context.Context
	cancel context.CancelFunc

	results chan provider.ASRResult
	// finalWake tells the worker that CloseSend has happened and the final is
	// due. Buffered, so CloseSend never blocks on the worker.
	finalWake chan struct{}

	mu       sync.Mutex
	buf      []byte
	sendDone bool
	// lastPartial is how much of buf the last partial request covered, and
	// partialCancel stops a partial that CloseSend has made pointless.
	lastPartial   int
	lastText      string
	partialCancel context.CancelFunc
	truncated     bool

	closeOnce sync.Once
}

// Write implements provider.ASRStream. It never blocks on the network: the
// audio is buffered, and the worker transcribes snapshots of it.
func (s *stream) Write(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendDone {
		return nil
	}
	s.buf = append(s.buf, pcm...)
	if limit := int(maxAudio.Seconds()) * s.rate * 2; len(s.buf) > limit {
		// The vendor rejects audio longer than this. Keep the most recent
		// speech rather than failing every request; the engine should have
		// closed the utterance long before.
		s.buf = append(s.buf[:0], s.buf[len(s.buf)-limit:]...)
		if !s.truncated {
			s.truncated = true
			slog.Warn("deepseek asr: utterance exceeded the recognizer's audio limit; keeping the most recent audio",
				"limit_s", int(maxAudio.Seconds()))
		}
	}
	return nil
}

// Results implements provider.ASRStream.
func (s *stream) Results() <-chan provider.ASRResult { return s.results }

// CloseSend implements provider.ASRStream: the utterance is over, so stop
// asking for stale partials and transcribe everything once for the final.
func (s *stream) CloseSend() error {
	s.mu.Lock()
	if s.sendDone || s.ctx.Err() != nil {
		s.mu.Unlock()
		return nil
	}
	s.sendDone = true
	cancel := s.partialCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case s.finalWake <- struct{}{}:
	default:
	}
	return nil
}

// Close implements provider.ASRStream.
func (s *stream) Close() error {
	s.closeOnce.Do(s.cancel)
	return nil
}

// run is the only goroutine that touches the results channel after Open, so
// closing it here is race-free.
func (s *stream) run() {
	defer close(s.results)

	var tick <-chan time.Time
	if s.opts.Interim && s.asr.PartialInterval > 0 {
		t := time.NewTicker(s.asr.PartialInterval)
		defer t.Stop()
		tick = t.C
	} else if s.asr.PartialInterval > 0 {
		// No interim asked for, but the final still needs the buffered audio.
		slog.Debug("deepseek asr: partial hypotheses disabled; the final is the only result")
	}

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.finalWake:
			s.final()
			return
		case <-tick:
			s.partial()
		}
	}
}

// minPartialBytes is minPartialAudio as PCM16 bytes at rate.
func minPartialBytes(rate int) int {
	return int(minPartialAudio.Milliseconds()) * rate * 2 / 1000
}

// partial transcribes what has been heard since the last partial and publishes
// it as a hypothesis. Failures are logged, not reported: a partial is an
// optimist's guess, and the engine must not tell a caller their recognizer is
// broken because one optional request timed out.
func (s *stream) partial() {
	s.mu.Lock()
	n, last, done := len(s.buf), s.lastPartial, s.sendDone
	s.mu.Unlock()
	if done || n-last < minPartialBytes(s.rate) {
		return
	}

	pcm := s.snapshot()
	ctx, cancel := context.WithTimeout(s.ctx, s.asr.PartialTimeout)
	s.mu.Lock()
	s.partialCancel = cancel
	s.mu.Unlock()

	text, err := s.asr.transcribe(ctx, pcm, s.rate)
	cancel()

	s.mu.Lock()
	s.partialCancel = nil
	s.lastPartial = len(pcm)
	if err != nil {
		s.mu.Unlock()
		if s.ctx.Err() == nil {
			slog.Debug("deepseek asr: partial request failed; the final will still be asked for", "err", err)
		}
		return
	}
	text = strings.TrimSpace(text)
	if text == "" || text == s.lastText {
		s.mu.Unlock()
		return
	}
	s.lastText = text
	s.mu.Unlock()
	s.emit(provider.ASRResult{Text: text})
}

// final transcribes the whole utterance and publishes it as the final result.
func (s *stream) final() {
	pcm := s.snapshot()
	ctx, cancel := context.WithTimeout(s.ctx, s.asr.FinalTimeout)
	defer cancel()

	text, err := s.asr.transcribe(ctx, pcm, s.rate)
	if err != nil {
		if s.ctx.Err() != nil {
			return // the session went away; nothing to report to
		}
		// One more try on the vendor's busy and rate-limit answers: the
		// engine gives a final a few seconds, and a refusal is exactly the
		// failure worth retrying inside that window.
		if retryable(err) {
			select {
			case <-time.After(250 * time.Millisecond):
			case <-s.ctx.Done():
				return
			}
			text, err = s.asr.transcribe(ctx, pcm, s.rate)
		}
		if err != nil {
			slog.Warn("deepseek asr: final request failed", "err", err)
			s.emit(provider.ASRResult{Err: fmt.Errorf("deepseek asr: %w", err)})
			return
		}
	}
	s.emit(provider.ASRResult{Text: strings.TrimSpace(text), Final: true})
}

func (s *stream) snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, len(s.buf))
	copy(out, s.buf)
	return out
}

func (s *stream) emit(r provider.ASRResult) bool {
	select {
	case s.results <- r:
		return true
	case <-s.ctx.Done():
		return false
	}
}

// statusError is a non-200 answer from the recognition tool.
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("deepseek asr: HTTP %d", e.code)
	}
	return fmt.Sprintf("deepseek asr: HTTP %d: %s", e.code, e.body)
}

// Status is the HTTP status of the refusal.
func (e *statusError) Status() int { return e.code }

// retryable reports the documented transient failures: a busy service, a
// timeout, or too many requests in progress.
func retryable(err error) bool {
	var se *statusError
	if !errors.As(err, &se) {
		return false
	}
	switch se.code {
	case http.StatusTooManyRequests, http.StatusRequestTimeout,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// transcribe sends one WAV through the recognition tool and returns its
// transcript.
func (a *ASR) transcribe(ctx context.Context, pcm []byte, rate int) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "utterance.wav")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(wavBytes(pcm, rate)); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Endpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.APIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := a.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", &statusError{code: resp.StatusCode, body: strings.TrimSpace(string(data))}
	}
	var out struct {
		Text       string `json:"text"`
		DurationMS int    `json:"duration_ms"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("deepseek asr: malformed response: %w", err)
	}
	return out.Text, nil
}

// wavBytes wraps PCM16 little-endian mono in the 44-byte RIFF header the tool
// documents (WAV is detected from the file content, so the header must be
// right down to the sample rate).
func wavBytes(pcm []byte, rate int) []byte {
	out := make([]byte, 44+len(pcm))
	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], uint32(36+len(pcm)))
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	binary.LittleEndian.PutUint32(out[16:20], 16) // fmt chunk size
	binary.LittleEndian.PutUint16(out[20:22], 1)  // PCM
	binary.LittleEndian.PutUint16(out[22:24], 1)  // mono
	binary.LittleEndian.PutUint32(out[24:28], uint32(rate))
	binary.LittleEndian.PutUint32(out[28:32], uint32(rate*2)) // bytes per second
	binary.LittleEndian.PutUint16(out[32:34], 2)              // bytes per frame
	binary.LittleEndian.PutUint16(out[34:36], 16)             // bits per sample
	copy(out[36:40], "data")
	binary.LittleEndian.PutUint32(out[40:44], uint32(len(pcm)))
	copy(out[44:], pcm)
	return out
}

// wavRates are the sample rates the tool documents for WAV input.
var wavRates = map[int]bool{8000: true, 16000: true, 22050: true, 24000: true,
	32000: true, 44100: true, 48000: true}

// wavRate picks the rate to declare, given the rate the audio is actually at.
// The pipeline rate is 16 kHz, so this normally passes the value through; a
// rate the tool does not accept cannot be relabelled, so 16 kHz is the only
// safe fallback (and would be wrong for the audio — the engine does not ask for
// one).
func wavRate(rate int) int {
	if wavRates[rate] {
		return rate
	}
	return provider.PipelineRate
}
