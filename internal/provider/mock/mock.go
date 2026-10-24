// Package mock provides deterministic ASR, LLM and TTS adapters.
//
// They exist so the duplex engine can be exercised end to end — barge-in,
// speculation, truncation accounting, pacing — with no credentials, no network
// and no timing luck. Every mock is driven by the audio and text it is given
// rather than by wall-clock randomness, so a test that passes once passes again.
package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/provider"
)

func init() {
	provider.RegisterASR("mock", func() (provider.ASR, error) { return NewASR("").withEnv(), nil })
	provider.RegisterLLM("mock", func() (provider.LLM, error) { return NewLLM().withEnv(), nil })
	provider.RegisterTTS("mock", func() (provider.TTS, error) { return NewTTS().withEnv(), nil })
}

// --- latency injection ---
//
// The mocks answer instantly, which is right for unit tests and useless for
// judging latency work: every stage costs zero, so nothing an engine does about
// a slow stage can show up. The GOLIVE_MOCK_* variables below give each stage
// the delays a real vendor has — a handshake, a recognizer running behind the
// speaker, a backend's time to first token, a synthesizer's time to first byte —
// so a change to the engine can be measured A/B on one machine with no
// credentials and no network weather. configs/sim.json documents a set modelled
// on the stage timings in BENCHMARK.md.
//
// Only the registered factories read them. Tests construct the mocks directly
// and are unaffected by whatever happens to be in the environment.

func envMS(key string) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n * float64(time.Millisecond))
}

func envOn(key string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(key)))
	return err == nil && b
}

// sleepCtx waits d, or returns false early when ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// --- ASR ---

// ASR "recognises" a fixed phrase, revealing more of it as more audio arrives.
// Partial hypotheses therefore grow monotonically with speech duration, which
// is exactly the shape the stability watch and the speculation path expect.
type ASR struct {
	phrase string
	// MSPerRune is how much audio one character of the phrase represents.
	MSPerRune float64

	// DialDelay is how long Open takes: the handshake a real recognizer needs.
	DialDelay time.Duration
	// Lag is how far behind the audio each hypothesis arrives. A streaming
	// recognizer is always a little behind the speaker, and that gap is what
	// decides whether a partial is trustworthy at the moment the speaker pauses.
	Lag time.Duration
	// FinalDelay is how long the final result takes after CloseSend.
	FinalDelay time.Duration
	// VoicedOnly counts only voiced audio toward the phrase. Off, the hypothesis
	// keeps growing through silence, which no recognizer does; it is off by
	// default only because the unit tests were timed against it.
	VoicedOnly bool

	// Phrases, when set, lets the audio choose which phrase is heard: a
	// stream reads the pitch of its first voiced PhraseKeyMS of audio and
	// takes Phrases[k] for a tone at PhraseBaseHz + k*PhraseStepHz. The
	// full-duplex bench (cmd/golivefdb) speaks each utterance as such a tone,
	// so a murmur, a question and an aside can reach the engine as different
	// words through the one path a real caller's audio takes, with nothing on
	// the side. Audio that is not one of those tones is heard as phrase 0.
	// Empty, the one fixed phrase above is heard whatever is said.
	Phrases []string
	// PhraseBaseHz, PhraseStepHz and PhraseKeyMS are the key's parameters;
	// zero takes the defaults (220 Hz, 40 Hz, 100 ms).
	PhraseBaseHz, PhraseStepHz float64
	PhraseKeyMS                float64
}

// Phrase key defaults, shared with the bench that speaks in it.
const (
	DefaultPhraseBaseHz = 220.0
	DefaultPhraseStepHz = 40.0
	DefaultPhraseKeyMS  = 100.0
)

// PhraseHz is the tone that selects Phrases[k].
func PhraseHz(k int) float64 { return DefaultPhraseBaseHz + float64(k)*DefaultPhraseStepHz }

func (a *ASR) withEnv() *ASR {
	a.DialDelay = envMS("GOLIVE_MOCK_ASR_DIAL_MS")
	a.Lag = envMS("GOLIVE_MOCK_ASR_LAG_MS")
	a.FinalDelay = envMS("GOLIVE_MOCK_ASR_FINAL_MS")
	a.VoicedOnly = envOn("GOLIVE_MOCK_ASR_VOICED_ONLY")
	if v := strings.TrimSpace(os.Getenv("GOLIVE_MOCK_PHRASES")); v != "" {
		for _, p := range strings.Split(v, "|") {
			a.Phrases = append(a.Phrases, strings.TrimSpace(p))
		}
	}
	if n, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("GOLIVE_MOCK_ASR_MS_PER_RUNE")), 64); err == nil && n > 0 {
		a.MSPerRune = n
	}
	return a
}

// keyed reports whether the phrase is chosen by the audio's pitch.
func (a *ASR) keyed() bool { return len(a.Phrases) > 0 }

// phraseFor maps a measured pitch to one of Phrases.
func (a *ASR) phraseFor(hz float64) string {
	base, step := a.PhraseBaseHz, a.PhraseStepHz
	if base <= 0 {
		base = DefaultPhraseBaseHz
	}
	if step <= 0 {
		step = DefaultPhraseStepHz
	}
	k := int(math.Floor((hz-base)/step + 0.5))
	if k < 0 || k >= len(a.Phrases) {
		k = 0
	}
	return a.Phrases[k]
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
	if !sleepCtx(ctx, a.DialDelay) {
		return nil, ctx.Err()
	}
	s := &asrStream{
		asr:     a,
		rate:    rate,
		results: make(chan provider.ASRResult, 32),
		ctx:     ctx,
		phrase:  a.phrase,
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
	// phrase is what this stream hears. With a keyed recognizer it is chosen
	// from the pitch of the first voiced audio; keyMS and keyCross accumulate
	// that measurement until keyMS reaches the key length.
	phrase   string
	keyed    bool
	keyMS    float64
	keyCross float64
	keyPrev  float32
	keyHave  bool
	// sendDone is set by CloseSend: no more audio, the final is on its way.
	sendDone bool
	// finished is set once the results channel has been closed.
	finished bool
}

func (s *asrStream) Write(pcm []byte) error {
	s.mu.Lock()
	if s.sendDone || s.finished {
		s.mu.Unlock()
		return nil
	}
	s.audioMS += s.heardMS(pcm)
	text := s.asr.prefixOf(s.phrase, s.audioMS)
	if s.keyed && s.keyMS < s.keyLength() {
		// The key is not read yet; nothing is heard until it is.
		text = ""
	}
	changed := text != s.lastText
	s.lastText = text
	s.mu.Unlock()

	if changed && text != "" {
		s.deliver(provider.ASRResult{Text: text}, s.asr.Lag)
	}
	return nil
}

// keyLength is how much voiced audio the pitch is read from.
func (s *asrStream) keyLength() float64 {
	if s.asr.PhraseKeyMS > 0 {
		return s.asr.PhraseKeyMS
	}
	return DefaultPhraseKeyMS
}

// heardMS is how much of pcm counts toward the phrase. On a keyed
// recognizer it also reads the pitch of the first voiced frames and, once
// enough have been heard, settles which phrase this stream hears.
func (s *asrStream) heardMS(pcm []byte) float64 {
	format := audio.PCM16(s.rate)
	if s.asr.keyed() && !s.keyed {
		s.keyed = true
	}
	if !s.asr.VoicedOnly && !s.keyed {
		return format.DurationMS(pcm)
	}
	frame := format.BytesForMS(20)
	if frame <= 0 {
		return format.DurationMS(pcm)
	}
	var ms float64
	for off := 0; off < len(pcm); off += frame {
		end := off + frame
		if end > len(pcm) {
			end = len(pcm)
		}
		samples, err := audio.DecodePCM16(pcm[off:end])
		if err != nil {
			continue
		}
		// -40 dBFS: far above digital silence, far below the synthetic speech
		// the harnesses send.
		if audio.RMS(samples) > 0.01 {
			frameMS := format.DurationMS(pcm[off:end])
			ms += frameMS
			if s.keyed && s.keyMS < s.keyLength() {
				// Crossings are counted across frame boundaries too: a tone
				// with a whole number of cycles a frame crosses on the
				// boundary every time, and losing those reads it a step low.
				for _, v := range samples {
					if s.keyHave && (s.keyPrev >= 0) != (v >= 0) {
						s.keyCross++
					}
					s.keyPrev, s.keyHave = v, true
				}
				s.keyMS += frameMS
				if s.keyMS >= s.keyLength() {
					// crossings per second, halved: a sine crosses twice a cycle.
					hz := s.keyCross / (s.keyMS / 1000) / 2
					s.phrase = s.asr.phraseFor(hz)
				}
			}
		}
	}
	if !s.asr.VoicedOnly {
		return format.DurationMS(pcm)
	}
	return ms
}

func (s *asrStream) CloseSend() error {
	s.mu.Lock()
	if s.sendDone || s.finished {
		s.mu.Unlock()
		return nil
	}
	s.sendDone = true
	text := s.asr.prefixOf(s.phrase, s.audioMS)
	if s.keyed && s.keyMS < s.keyLength() {
		text = ""
	}
	s.mu.Unlock()

	// The final never overtakes a partial still in flight.
	wait := s.asr.FinalDelay
	if s.asr.Lag > 0 && wait <= s.asr.Lag {
		wait = s.asr.Lag + time.Millisecond
	}
	s.deliver(provider.ASRResult{Text: text, Final: true}, wait)
	return nil
}

func (s *asrStream) Results() <-chan provider.ASRResult { return s.results }

func (s *asrStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finish()
	return nil
}

// finish closes the results channel once. The caller holds mu.
func (s *asrStream) finish() {
	if s.finished {
		return
	}
	s.finished = true
	close(s.results)
}

// deliver hands a result to the reader after delay, and closes the stream
// behind a final one.
func (s *asrStream) deliver(r provider.ASRResult, delay time.Duration) {
	if delay <= 0 {
		s.send(r)
		return
	}
	time.AfterFunc(delay, func() { s.send(r) })
}

func (s *asrStream) send(r provider.ASRResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return
	}
	select {
	case s.results <- r:
	default:
	}
	if r.Final {
		s.finish()
	}
}

// prefix maps elapsed audio to a prefix of the phrase, snapped to a rune
// boundary so partials are never half a character.
func (a *ASR) prefix(ms float64) string { return a.prefixOf(a.phrase, ms) }

func (a *ASR) prefixOf(phrase string, ms float64) string {
	runes := []rune(phrase)
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
	// FirstTokenDelay is the backend's time to first token: on a real stack,
	// the largest single term between the caller's last word and the reply.
	FirstTokenDelay time.Duration

	// UseTools makes the model behave like one that has been given functions:
	// when the user's message contains ToolTrigger and a tool is on offer, it
	// says a short holding line and calls the first tool; once the conversation
	// ends in a tool result, it reports that result. Off, tools are ignored and
	// every reply is the plain one.
	UseTools bool
	// ToolTrigger is the substring that makes a message worth a tool call.
	ToolTrigger string

	mu    sync.Mutex
	calls int
}

func (l *LLM) withEnv() *LLM {
	l.FirstTokenDelay = envMS("GOLIVE_MOCK_LLM_TTFT_MS")
	if d := envMS("GOLIVE_MOCK_LLM_RUNE_MS"); d > 0 {
		l.DelayPerRune = d
	}
	// On for the registered mock, so a demo session that configures a function
	// can watch a call go out and come back without a real backend.
	l.UseTools = true
	return l
}

// ToolPreface is what the mock says before calling a tool.
const ToolPreface = "好的，我查一下。"

// ToolReply is what the mock says once a tool result is in.
func ToolReply(result string) string {
	return "查到了，" + strings.TrimSpace(result) + "。"
}

// toolName pulls the function name out of a chat-completions tool definition.
func toolName(raw json.RawMessage) string {
	var def struct {
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &def) != nil {
		return ""
	}
	if def.Function.Name != "" {
		return def.Function.Name
	}
	return def.Name
}

// plan decides what this completion does: plain text, or text and a call.
func (l *LLM) plan(req provider.LLMRequest) (text string, call *provider.ToolCall) {
	var lastUser string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == provider.RoleUser {
			lastUser = req.Messages[i].Content
			break
		}
	}
	if !l.UseTools {
		return Reply(lastUser), nil
	}

	// A conversation that ends in tool results is waiting for them to be read
	// back, whatever tools are on offer now.
	if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == provider.RoleTool {
		var results []string
		for i := n - 1; i >= 0 && req.Messages[i].Role == provider.RoleTool; i-- {
			results = append([]string{req.Messages[i].Content}, results...)
		}
		return ToolReply(strings.Join(results, "，")), nil
	}

	trigger := l.ToolTrigger
	if trigger == "" {
		trigger = "查"
	}
	if len(req.Tools) == 0 || !strings.Contains(lastUser, trigger) {
		return Reply(lastUser), nil
	}
	if choice, ok := req.ToolChoice.(string); ok && choice == "none" {
		return Reply(lastUser), nil
	}
	name := toolName(req.Tools[0])
	if name == "" {
		return Reply(lastUser), nil
	}
	l.mu.Lock()
	l.calls++
	id := fmt.Sprintf("call_mock_%d", l.calls)
	l.mu.Unlock()
	args, _ := json.Marshal(map[string]string{"request": lastUser})
	return ToolPreface, &provider.ToolCall{ID: id, Name: name, Arguments: string(args)}
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
	reply, call := l.plan(req)

	out := make(chan provider.LLMDelta, 64)
	go func() {
		defer close(out)
		if !sleepCtx(ctx, l.FirstTokenDelay) {
			return
		}
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
		if call != nil {
			select {
			case out <- provider.LLMDelta{ToolCall: call}:
			case <-ctx.Done():
				return
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

	// DialDelay is how long Open takes: the connect-and-greet a persistent
	// synthesis session costs once.
	DialDelay time.Duration
	// FirstByteDelay is how long each Synthesize takes to produce its first
	// chunk.
	FirstByteDelay time.Duration
	// Serial makes one stream synthesize one segment at a time, like a vendor
	// task that is a single ordered stream. A second Synthesize waits for the
	// first to finish or be cancelled.
	Serial bool

	mu       sync.Mutex
	prewarms int
	opens    int
}

func (t *TTS) withEnv() *TTS {
	t.DialDelay = envMS("GOLIVE_MOCK_TTS_DIAL_MS")
	t.FirstByteDelay = envMS("GOLIVE_MOCK_TTS_TTFB_MS")
	t.Serial = envOn("GOLIVE_MOCK_TTS_SERIAL")
	return t
}

// Opens reports how many synthesis sessions were opened.
func (t *TTS) Opens() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.opens
}

// Prewarms reports how many times any stream this TTS opened was prewarmed.
func (t *TTS) Prewarms() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.prewarms
}

func (t *TTS) notePrewarm() {
	t.mu.Lock()
	t.prewarms++
	t.mu.Unlock()
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
	if !sleepCtx(ctx, t.DialDelay) {
		return nil, ctx.Err()
	}
	t.mu.Lock()
	t.opens++
	t.mu.Unlock()
	return &ttsStream{tts: t, rate: rate, speed: speed}, nil
}

type ttsStream struct {
	tts   *TTS
	rate  int
	speed float64
	// busy serializes Synthesize when the TTS is Serial.
	busy sync.Mutex
}

func (s *ttsStream) SampleRate() int { return s.rate }

// Prewarm implements provider.Prewarmer. The mock has no connection to make, so
// this only records that the engine asked — which is the part worth asserting:
// a prewarm that stops being called is a silent latency regression, invisible
// in every other test because the work still happens, just later.
func (s *ttsStream) Prewarm(context.Context) error {
	s.tts.notePrewarm()
	return nil
}

func (s *ttsStream) Close() error { return nil }

func (s *ttsStream) Synthesize(ctx context.Context, text string) (<-chan provider.TTSChunk, error) {
	runes := []rune(text)
	totalMS := float64(len(runes)) * s.tts.MSPerRune / s.speed
	totalSamples := int(totalMS / 1000 * float64(s.rate))

	out := make(chan provider.TTSChunk, 8)
	go func() {
		defer close(out)
		if s.tts.Serial {
			s.busy.Lock()
			defer s.busy.Unlock()
		}
		if !sleepCtx(ctx, s.tts.FirstByteDelay) {
			return
		}
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
