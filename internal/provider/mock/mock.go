// Package mock provides deterministic ASR, LLM and TTS adapters.
//
// They exist so the duplex engine can be exercised end to end — barge-in,
// speculation, truncation accounting, pacing — with no credentials, no network
// and no timing luck. Every mock is driven by the audio and text it is given
// rather than by wall-clock randomness, so a test that passes once passes again.
package mock

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/provider"
)

func init() {
	provider.RegisterASR("mock", func() (provider.ASR, error) { return NewASR(""), nil })
	provider.RegisterLLM("mock", func() (provider.LLM, error) { return NewLLM(), nil })
	provider.RegisterTTS("mock", func() (provider.TTS, error) { return NewTTS(), nil })
}

// --- ASR ---

// ASR "recognises" a fixed phrase, revealing more of it as more audio arrives.
// Partial hypotheses therefore grow monotonically with speech duration, which
// is exactly the shape the stability watch and the speculation path expect.
type ASR struct {
	phrase string
	// MSPerRune is how much audio one character of the phrase represents.
	MSPerRune float64
}

// NewASR builds a mock recognizer. An empty phrase falls back to
// GOLIVE_MOCK_TRANSCRIPT, then to a built-in Chinese sentence.
func NewASR(phrase string) *ASR {
	if phrase == "" {
		phrase = os.Getenv("GOLIVE_MOCK_TRANSCRIPT")
	}
	if phrase == "" {
		phrase = "你好，帮我查一下明天的天气"
	}
	return &ASR{phrase: phrase, MSPerRune: 180}
}

// Name implements provider.ASR.
func (a *ASR) Name() string { return "mock" }

// Open implements provider.ASR.
func (a *ASR) Open(ctx context.Context, opts provider.ASROptions) (provider.ASRStream, error) {
	rate := opts.SampleRate
	if rate <= 0 {
		rate = provider.PipelineRate
	}
	s := &asrStream{
		asr:     a,
		rate:    rate,
		results: make(chan provider.ASRResult, 32),
		ctx:     ctx,
	}
	return s, nil
}

type asrStream struct {
	asr     *ASR
	rate    int
	results chan provider.ASRResult
	ctx     context.Context

	mu       sync.Mutex
	audioMS  float64
	lastText string
	closed   bool
}

func (s *asrStream) Write(pcm []byte) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.audioMS += audio.PCM16(s.rate).DurationMS(pcm)
	text := s.asr.prefix(s.audioMS)
	changed := text != s.lastText
	s.lastText = text
	s.mu.Unlock()

	if changed && text != "" {
		s.send(provider.ASRResult{Text: text})
	}
	return nil
}

func (s *asrStream) CloseSend() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	text := s.asr.prefix(s.audioMS)
	s.mu.Unlock()

	s.send(provider.ASRResult{Text: text, Final: true})
	close(s.results)
	return nil
}

func (s *asrStream) Results() <-chan provider.ASRResult { return s.results }

func (s *asrStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.results)
	return nil
}

func (s *asrStream) send(r provider.ASRResult) {
	select {
	case s.results <- r:
	case <-s.ctx.Done():
	default:
	}
}

// prefix maps elapsed audio to a prefix of the phrase, snapped to a rune
// boundary so partials are never half a character.
func (a *ASR) prefix(ms float64) string {
	runes := []rune(a.phrase)
	n := int(ms / a.MSPerRune)
	if n > len(runes) {
		n = len(runes)
	}
	if n <= 0 {
		return ""
	}
	return string(runes[:n])
}

// --- LLM ---

// LLM produces a deterministic reply that restates the user's last message and
// adds enough sentences to be worth interrupting.
type LLM struct {
	// DelayPerRune paces the stream so barge-in has something to cut into.
	DelayPerRune time.Duration
}

// NewLLM builds a mock language model.
func NewLLM() *LLM { return &LLM{DelayPerRune: 12 * time.Millisecond} }

// Name implements provider.LLM.
func (l *LLM) Name() string { return "mock" }

// Stream implements provider.LLM.
func (l *LLM) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	var last string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == provider.RoleUser {
			last = req.Messages[i].Content
			break
		}
	}
	reply := Reply(last)

	out := make(chan provider.LLMDelta, 64)
	go func() {
		defer close(out)
		for _, r := range reply {
			select {
			case <-ctx.Done():
				return
			case out <- provider.LLMDelta{Text: string(r)}:
			}
			if l.DelayPerRune > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(l.DelayPerRune):
				}
			}
		}
		select {
		case out <- provider.LLMDelta{Usage: &provider.TokenUsage{
			InputTokens:  len([]rune(last)),
			OutputTokens: len([]rune(reply)),
		}}:
		case <-ctx.Done():
		}
	}()
	return out, nil
}

// Reply is the deterministic mock answer for a user utterance. It is exported
// so tests can assert against it without duplicating the format string.
func Reply(user string) string {
	user = strings.TrimSpace(user)
	if user == "" {
		return "我在，请讲。"
	}
	return fmt.Sprintf("好的，我听到你说%s。让我确认一下细节，然后给你一个完整的答复。"+
		"这句话故意说得长一点，方便你在中途打断我，测试打断之后的对话历史是否只保留你真正听到的部分。", user)
}

// --- TTS ---

// TTS synthesizes a recognisable but synthetic waveform: a decaying two-tone
// blip per character, so output audio is audible, length-proportional to the
// text, and trivially verifiable in a test.
type TTS struct {
	// MSPerRune is the spoken duration of one character.
	MSPerRune float64
	// ChunkMS is the streaming granularity.
	ChunkMS int
	// Realtime paces synthesis to roughly speech speed. Off by default so
	// tests run fast; the engine's own player provides playback pacing.
	Realtime bool
}

// NewTTS builds a mock synthesizer.
func NewTTS() *TTS { return &TTS{MSPerRune: 110, ChunkMS: 100} }

// Name implements provider.TTS.
func (t *TTS) Name() string { return "mock" }

// Open implements provider.TTS.
func (t *TTS) Open(ctx context.Context, opts provider.TTSOptions) (provider.TTSStream, error) {
	rate := opts.SampleRate
	if rate <= 0 {
		rate = provider.PipelineRate
	}
	speed := opts.Speed
	if speed <= 0 {
		speed = 1
	}
	return &ttsStream{tts: t, rate: rate, speed: speed}, nil
}

type ttsStream struct {
	tts   *TTS
	rate  int
	speed float64
}

func (s *ttsStream) SampleRate() int { return s.rate }

func (s *ttsStream) Close() error { return nil }

func (s *ttsStream) Synthesize(ctx context.Context, text string) (<-chan provider.TTSChunk, error) {
	runes := []rune(text)
	totalMS := float64(len(runes)) * s.tts.MSPerRune / s.speed
	totalSamples := int(totalMS / 1000 * float64(s.rate))

	out := make(chan provider.TTSChunk, 8)
	go func() {
		defer close(out)
		chunkSamples := s.rate * s.tts.ChunkMS / 1000
		if chunkSamples <= 0 {
			chunkSamples = s.rate / 10
		}
		for start := 0; start < totalSamples; start += chunkSamples {
			end := start + chunkSamples
			if end > totalSamples {
				end = totalSamples
			}
			buf := make([]float32, end-start)
			for i := range buf {
				n := start + i
				// One blip per character: a 220 Hz carrier with a 3 Hz
				// syllable envelope, kept well below full scale.
				tSec := float64(n) / float64(s.rate)
				env := 0.35 * (0.55 + 0.45*math.Sin(2*math.Pi*3.2*tSec))
				buf[i] = float32(env * math.Sin(2*math.Pi*220*tSec))
			}
			select {
			case <-ctx.Done():
				return
			case out <- provider.TTSChunk{PCM: audio.EncodePCM16(buf)}:
			}
			if s.tts.Realtime {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(s.tts.ChunkMS) * time.Millisecond):
				}
			}
		}
	}()
	return out, nil
}
