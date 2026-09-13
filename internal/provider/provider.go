// Package provider defines the three adapter contracts the duplex engine is
// built on — streaming ASR, streaming LLM, streaming TTS — plus a registry so a
// new vendor can be added without touching the engine.
//
// Every contract is streaming. That is not decoration: the whole reason a
// cascade can imitate a full-duplex model is that each stage starts emitting
// before the stage above it has finished, so the user hears a first syllable
// while the sentence is still being written.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// PipelineRate is the sample rate the engine uses between stages. Providers
// convert at their own boundary.
const PipelineRate = 16000

// Role values for LLM messages.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is one conversation turn handed to an LLM.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCallID ties a RoleTool message to the call it answers.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Name is the tool name for RoleTool messages.
	Name string `json:"name,omitempty"`
}

// --- ASR ---

// ASROptions configures one recognition stream.
type ASROptions struct {
	SampleRate int
	Language   string
	// Engine is the vendor's model identifier (Tencent: "16k_zh").
	Engine string
	// Interim asks the provider for partial hypotheses. Turning it off saves
	// nothing on most providers but makes logs far quieter.
	Interim bool
}

// ASRResult is one hypothesis from a recognition stream.
//
// Text is the full current hypothesis for the utterance, not a delta. Providers
// revise earlier words as context arrives, so a delta-only contract would force
// every caller to implement the same reconciliation.
type ASRResult struct {
	Text  string
	Final bool
	Err   error
}

// ASRStream is one open recognition of one utterance.
type ASRStream interface {
	// Write pushes PCM16 mono audio at the negotiated rate. It must not block
	// on the network for longer than a frame.
	Write(pcm []byte) error
	// Results yields hypotheses until the stream ends, then closes.
	Results() <-chan ASRResult
	// CloseSend signals end-of-utterance and asks for the final result.
	CloseSend() error
	// Close tears the stream down immediately, final result or not.
	Close() error
}

// ASR opens recognition streams.
type ASR interface {
	Name() string
	Open(ctx context.Context, opts ASROptions) (ASRStream, error)
}

// --- LLM ---

// ToolCall is a function call the model requested.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// TokenUsage reports what a completion cost.
type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// LLMDelta is one event from a streaming completion.
type LLMDelta struct {
	Text     string
	ToolCall *ToolCall
	Usage    *TokenUsage
	Err      error
}

// LLMRequest is one completion.
type LLMRequest struct {
	Model       string
	Messages    []Message
	Temperature float64
	MaxTokens   int
	Tools       []json.RawMessage
	// DisableThinking suppresses reasoning traces on models that emit them.
	// In a voice session a reasoning trace is pure added latency before the
	// first spoken syllable.
	DisableThinking bool
}

// LLM streams completions.
type LLM interface {
	Name() string
	Stream(ctx context.Context, req LLMRequest) (<-chan LLMDelta, error)
}

// --- TTS ---

// TTSOptions configures a synthesis session.
type TTSOptions struct {
	SampleRate int
	Voice      string
	Speed      float64
	Model      string
	Language   string
}

// TTSChunk is one piece of synthesized PCM16 audio.
type TTSChunk struct {
	PCM []byte
	Err error
}

// TTSStream is a synthesis session. Implementations should hold one connection
// open across many Synthesize calls: on a conversational cadence, connection
// setup dominates time-to-first-audio.
type TTSStream interface {
	// Synthesize speaks one text segment. The returned channel yields audio
	// chunks and closes when that segment is complete. Calls are sequential;
	// the caller does not overlap them on a single stream.
	//
	// A call whose context is cancelled is abandoned mid-sentence, which is the
	// normal outcome of a barge-in. An implementation holding a persistent
	// connection MUST resynchronize before the next call: the vendor keeps
	// generating audio for text it has already been given, and a stream left
	// undrained delivers the tail of the interrupted sentence at the start of
	// the next one. The caller cannot detect that — the audio arrives on the
	// new turn's channel, correctly formed and completely wrong — so the
	// obligation is here.
	Synthesize(ctx context.Context, text string) (<-chan TTSChunk, error)
	// SampleRate of the PCM16 chunks.
	SampleRate() int
	Close() error
}

// TTS opens synthesis sessions.
type TTS interface {
	Name() string
	Open(ctx context.Context, opts TTSOptions) (TTSStream, error)
}

// Prewarmer is an optional capability on a provider or a stream: make the
// network path ready before anything is waiting on it.
//
// The motivation is that a cascade's first turn is systematically worse than
// its others, and for an uninteresting reason — a TCP handshake, a TLS
// handshake and a protocol greeting, all on the critical path between the
// caller finishing their sentence and hearing a syllable. None of that needs to
// be there: the engine knows a turn is coming as soon as the microphone opens,
// which is a second or more of warning.
//
// Implementations must be safe to call at any time, including concurrently with
// real work, and must be cheap when the path is already warm. A Prewarm that
// fails is not an error the caller should surface: the work it was avoiding
// simply happens later, on the critical path, exactly as it did before.
type Prewarmer interface {
	Prewarm(ctx context.Context) error
}

// --- Registry ---

type registry struct {
	mu  sync.RWMutex
	asr map[string]func() (ASR, error)
	llm map[string]func() (LLM, error)
	tts map[string]func() (TTS, error)
}

var reg = registry{
	asr: map[string]func() (ASR, error){},
	llm: map[string]func() (LLM, error){},
	tts: map[string]func() (TTS, error){},
}

// RegisterASR adds an ASR factory under name. Factories are called lazily at
// session start so a missing credential for an unused provider is not a
// start-up failure.
func RegisterASR(name string, f func() (ASR, error)) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.asr[name] = f
}

// RegisterLLM adds an LLM factory under name.
func RegisterLLM(name string, f func() (LLM, error)) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.llm[name] = f
}

// RegisterTTS adds a TTS factory under name.
func RegisterTTS(name string, f func() (TTS, error)) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.tts[name] = f
}

// OpenASR builds the named ASR provider.
func OpenASR(name string) (ASR, error) {
	reg.mu.RLock()
	f, ok := reg.asr[name]
	reg.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("provider: no ASR named %q (have %v)", name, ASRNames())
	}
	return f()
}

// OpenLLM builds the named LLM provider.
func OpenLLM(name string) (LLM, error) {
	reg.mu.RLock()
	f, ok := reg.llm[name]
	reg.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("provider: no LLM named %q (have %v)", name, LLMNames())
	}
	return f()
}

// OpenTTS builds the named TTS provider.
func OpenTTS(name string) (TTS, error) {
	reg.mu.RLock()
	f, ok := reg.tts[name]
	reg.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("provider: no TTS named %q (have %v)", name, TTSNames())
	}
	return f()
}

// ASRNames lists registered ASR providers.
func ASRNames() []string { reg.mu.RLock(); defer reg.mu.RUnlock(); return keys(reg.asr) }

// LLMNames lists registered LLM providers.
func LLMNames() []string { reg.mu.RLock(); defer reg.mu.RUnlock(); return keys(reg.llm) }

// TTSNames lists registered TTS providers.
func TTSNames() []string { reg.mu.RLock(); defer reg.mu.RUnlock(); return keys(reg.tts) }

func keys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
