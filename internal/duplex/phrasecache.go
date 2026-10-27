package duplex

import (
	"container/list"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/segment"
	"github.com/chuanmingliu/golive/internal/tools"
)

// PhraseKey identifies one piece of synthesized audio: these exact words, in
// this voice, from this provider, at this rate.
type PhraseKey struct {
	Provider string
	Voice    string
	Speed    float64
	Language string
	Rate     int
	Text     string
}

// PhraseAudio is cached speech.
type PhraseAudio struct {
	PCM  []byte
	Rate int
}

// PhraseCache keeps the audio of the fixed things an assistant says.
//
// A voice agent's greeting is the same sentence on every call, and so are its
// acknowledgements and its "one moment". Synthesizing them again each time is
// pure waiting: the greeting is the first thing a caller hears, and it sat
// behind a connection handshake and a synthesis round trip that had the same
// answer as last time. Cached, it starts the instant the session does. A
// holding filler has a worse problem than delay — it is synthesized on the same
// ordered connection as the answer it is covering for, so it could stand in
// the answer's way; from the cache it touches the synthesizer not at all.
//
// The cache is shared by every session in the process, because the phrases are,
// and bounded by total audio rather than by entry count, because that is what
// the memory is. Least recently used goes first.
type PhraseCache struct {
	mu       sync.Mutex
	maxBytes int
	bytes    int
	order    *list.List // front = most recent; values are *phraseEntry
	entries  map[PhraseKey]*list.Element
}

type phraseEntry struct {
	key   PhraseKey
	audio PhraseAudio
}

// maxPhraseBytes is the longest single phrase worth keeping: thirty seconds at
// 24 kHz. Anything longer is not a phrase.
const maxPhraseBytes = 24000 * 2 * 30

// NewPhraseCache builds a cache holding at most maxBytes of PCM.
func NewPhraseCache(maxBytes int) *PhraseCache {
	return &PhraseCache{
		maxBytes: maxBytes,
		order:    list.New(),
		entries:  map[PhraseKey]*list.Element{},
	}
}

var sharedPhrases = NewPhraseCache(32 << 20)

// Phrases is the process-wide cache.
func Phrases() *PhraseCache { return sharedPhrases }

// Get returns cached audio for key.
func (c *PhraseCache) Get(key PhraseKey) (PhraseAudio, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return PhraseAudio{}, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*phraseEntry).audio, true
}

// Put stores audio for key. Empty or oversized audio is ignored.
func (c *PhraseCache) Put(key PhraseKey, a PhraseAudio) {
	if len(a.PCM) == 0 || len(a.PCM) > maxPhraseBytes || len(a.PCM) > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		old := el.Value.(*phraseEntry)
		c.bytes += len(a.PCM) - len(old.audio.PCM)
		old.audio = a
		c.order.MoveToFront(el)
	} else {
		c.entries[key] = c.order.PushFront(&phraseEntry{key: key, audio: a})
		c.bytes += len(a.PCM)
	}
	for c.bytes > c.maxBytes {
		last := c.order.Back()
		if last == nil {
			break
		}
		ent := last.Value.(*phraseEntry)
		c.order.Remove(last)
		delete(c.entries, ent.key)
		c.bytes -= len(ent.audio.PCM)
	}
}

// Len reports how many phrases are held.
func (c *PhraseCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Reset empties the cache. For tests.
func (c *PhraseCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.order.Init()
	c.entries = map[PhraseKey]*list.Element{}
	c.bytes = 0
}

// SynthesisRate is the rate the engine asks a synthesizer for on a session at
// clientRate. It is the one place that decision is made, so a phrase warmed at
// start-up and the session that later looks for it agree on the key.
func SynthesisRate(cfg config.Config, clientRate int) int {
	if cfg.Duplex.TTSNativeRate && clientRate > 0 {
		return clientRate
	}
	return provider.PipelineRate
}

// ResolveVoice maps the voice a client asked for to the one the provider is
// given. See config.Config.VoiceAliases.
func ResolveVoice(cfg config.Config, requested string) string {
	if requested == "" {
		return cfg.Voice
	}
	if v, ok := cfg.VoiceAliases[requested]; ok {
		return v
	}
	if liveVoiceNames[requested] || realtimeVoiceNames[requested] {
		// A gpt-live-1 voice name with no mapping. Handing it to a vendor
		// fails the session at the first sentence; the configured voice is
		// what the operator meant a caller to hear.
		return cfg.Voice
	}
	return requested
}

// liveVoiceNames are gpt-live-1's built-in voices.
var liveVoiceNames = map[string]bool{
	"marin": true, "cedar": true, "quartz": true, "ripple": true, "vesper": true,
	"willow": true, "stone": true, "gleam": true, "meridian": true, "bossa": true,
	"tempo": true, "beacon": true, "delta": true, "cinder": true,
}

// realtimeVoiceNames are the OpenAI Realtime API's voices, which is what a
// client of /v1/realtime sends — "alloy", unless it was told otherwise.
var realtimeVoiceNames = map[string]bool{
	"alloy": true, "ash": true, "ballad": true, "coral": true, "echo": true,
	"sage": true, "shimmer": true, "verse": true, "fable": true, "onyx": true,
	"nova": true,
}

// segmentWhole cuts a complete piece of text the way a speech pipe does when
// the whole of it arrives at once. Warm-up uses it so the phrases it caches
// are the segments a session will actually ask for.
func segmentWhole(cfg config.Config, text string) []string {
	seg := segment.New(segment.Config{
		FirstChunkChars: cfg.Duplex.StreamFirstChunkChars,
		MinChunkChars:   cfg.Duplex.StreamMinChunkChars,
		MaxChunkChars:   cfg.Duplex.StreamMaxChunkChars,
	})
	out := seg.Push(text)
	if rest := seg.Flush(); rest != "" {
		out = append(out, rest)
	}
	return out
}

// WarmPhrases synthesizes the server's fixed phrases into the shared cache, for
// the default voice at the default client rate, and returns how many it added.
//
// Run it at start-up, off the request path. A session that asks for a
// different voice or rate simply misses the cache and fills its own entries as
// it speaks; nothing depends on this having run.
func WarmPhrases(ctx context.Context, cfg config.Config, tts provider.TTS, log *slog.Logger) (int, error) {
	if !cfg.Duplex.PhraseCache || tts == nil {
		return 0, nil
	}
	if log == nil {
		log = slog.Default()
	}
	var texts []string
	if cfg.Greeting != "" {
		texts = append(texts, segmentWhole(cfg, cfg.Greeting)...)
	}
	if cfg.Duplex.Backchannel {
		texts = append(texts, cfg.Duplex.BackchannelPhrases...)
	}
	if cfg.Duplex.HoldingFiller {
		texts = append(texts, cfg.Duplex.HoldingFillerPhrases...)
	}
	if cfg.Duplex.Opener {
		texts = append(texts, cfg.Duplex.OpenerPhrases...)
		texts = append(texts, cfg.Duplex.OpenerQuestionPhrases...)
	}
	for _, t := range cfg.Tools {
		if t.Ack != "" {
			texts = append(texts, segmentWhole(cfg, t.Ack)...)
		}
		if t.ResultOpening != "" {
			texts = append(texts, segmentWhole(cfg, t.ResultOpening)...)
		}
	}
	added, _, err := warmTexts(ctx, cfg, tts, texts, ResolveVoice(cfg, ""), cfg.ClientRate, cfg.Language, log)
	return added, err
}

// WarmTexts synthesizes short phrases exactly as given into the shared cache
// for the default voice and rate — the openers a panel has just saved, so
// the first caller to draw one hears it at the pause. It returns how many it
// added (phrases already cached count as done, not added).
func WarmTexts(ctx context.Context, cfg config.Config, tts provider.TTS, texts []string, log *slog.Logger) (int, error) {
	if tts == nil || len(texts) == 0 {
		return 0, nil
	}
	if log == nil {
		log = slog.Default()
	}
	added, _, err := warmTexts(ctx, cfg, tts, texts, ResolveVoice(cfg, ""), cfg.ClientRate, cfg.Language, log)
	return added, err
}

// WarmOpeners reads the lead service's `openers` config and caches every
// phrase in it. Run at start-up after WarmPhrases, and by /v1/phrases when
// the panel saves the config.
func WarmOpeners(ctx context.Context, cfg config.Config, tts provider.TTS, log *slog.Logger) (tools.Openers, int, error) {
	if !cfg.Duplex.Opener || !cfg.Duplex.PhraseCache || cfg.ToolsBaseURL == "" {
		return tools.Openers{}, 0, nil
	}
	token := ""
	if cfg.ToolsTokenFile != "" {
		if b, err := os.ReadFile(cfg.ToolsTokenFile); err == nil {
			token = strings.TrimSpace(string(b))
		}
	}
	o, err := tools.FetchOpeners(cfg.ToolsBaseURL, token, 3*time.Second)
	if err != nil {
		return tools.Openers{}, 0, err
	}
	n, err := WarmTexts(ctx, cfg, tts, o.Phrases(), log)
	return o, n, err
}

// WarmSpeech synthesizes one piece of text into the shared cache ahead of the
// session that will say it, cut into the segments that session will ask for.
// It returns how many bytes of audio the whole of it comes to, cached before
// this call or by it.
//
// This is how an opening line is ready the instant a phone is answered: the
// call is known about while it is still ringing, and the first thing the
// assistant says on it is known too. voice and language are as a session would
// state them; clientRate is the session's audio rate.
func WarmSpeech(ctx context.Context, cfg config.Config, tts provider.TTS, text, voice string, clientRate int, language string, log *slog.Logger) (int, error) {
	if tts == nil {
		return 0, fmt.Errorf("no synthesizer")
	}
	if log == nil {
		log = slog.Default()
	}
	if language == "" {
		language = cfg.Language
	}
	texts := segmentWhole(cfg, strings.TrimSpace(text))
	if len(texts) == 0 {
		return 0, nil
	}
	_, bytes, err := warmTexts(ctx, cfg, tts, texts, ResolveVoice(cfg, voice), clientRate, language, log)
	return bytes, err
}

// warmTexts fills the cache for segments exactly as given, and reports how many
// it added and the size of all of them.
func warmTexts(ctx context.Context, cfg config.Config, tts provider.TTS, texts []string, voice string, clientRate int, language string, log *slog.Logger) (added, bytes int, err error) {
	want := SynthesisRate(cfg, clientRate)
	key := func(text string) PhraseKey {
		return PhraseKey{
			Provider: provider.VoiceName(tts), Voice: voice, Speed: cfg.Speed,
			Language: language, Rate: want, Text: text,
		}
	}
	var missing []string
	for _, text := range texts {
		if cached, ok := Phrases().Get(key(text)); ok {
			bytes += len(cached.PCM)
		} else {
			missing = append(missing, text)
		}
	}
	if len(missing) == 0 {
		return 0, bytes, nil
	}

	stream, err := tts.Open(ctx, provider.TTSOptions{
		SampleRate: want,
		Voice:      voice,
		Speed:      cfg.Speed,
		Language:   language,
	})
	if err != nil {
		return 0, bytes, fmt.Errorf("warming phrases: %w", err)
	}
	defer stream.Close()

	for _, text := range missing {
		started := time.Now()
		pcm, err := synthesizeWhole(ctx, stream, text)
		if err != nil {
			return added, bytes, fmt.Errorf("warming %q: %w", text, err)
		}
		Phrases().Put(key(text), PhraseAudio{PCM: pcm, Rate: stream.SampleRate()})
		added++
		bytes += len(pcm)
		log.Debug("phrase cached",
			"text", text,
			"audio_ms", int(audio.PCM16(stream.SampleRate()).DurationMS(pcm)),
			"ms", time.Since(started).Milliseconds())
	}
	return added, bytes, nil
}

// synthesizeWhole runs one segment to completion and returns its audio.
func synthesizeWhole(ctx context.Context, stream provider.TTSStream, text string) ([]byte, error) {
	segCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	chunks, err := stream.Synthesize(segCtx, text)
	if err != nil {
		return nil, err
	}
	var pcm []byte
	for chunk := range chunks {
		if chunk.Err != nil {
			return nil, chunk.Err
		}
		pcm = append(pcm, chunk.PCM...)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(pcm) == 0 {
		return nil, fmt.Errorf("the provider returned no audio")
	}
	return pcm, nil
}
