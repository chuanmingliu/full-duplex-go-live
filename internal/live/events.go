// Package live implements the gpt-live-1 wire protocol: the client and server
// event envelopes, session configuration, and the delegation events that carry
// backend work in and out of a voice session.
//
// The event names, field names and lifecycle here follow OpenAI's GPT-Live
// guides so that a client written against gpt-live-1 can point at golive
// unchanged. golive's own additions are namespaced under "golive." and are
// always optional — a conformant client that ignores unknown event types still
// gets a complete session.
package live

import (
	"encoding/json"
	"fmt"
)

// Client event types (application -> service).
const (
	ClientSessionStart       = "session.start"
	ClientSessionUpdate      = "session.update"
	ClientSessionClose       = "session.close"
	ClientInputAudioAppend   = "session.input_audio.append"
	ClientInputAudioMute     = "session.input_audio.mute"
	ClientInputAudioUnmute   = "session.input_audio.unmute"
	ClientInstructionsAppend = "session.instructions.append"
	ClientThinkingAppend     = "session.thinking.append"
	ClientCommentaryAppend   = "session.commentary.append"
	ClientResponseItemCreate = "response.item.create"
	ClientResponseCreate     = "response.create"
)

// Server event types (service -> application).
const (
	ServerSessionStarted        = "session.started"
	ServerSessionUpdated        = "session.updated"
	ServerSessionClosed         = "session.closed"
	ServerInputTranscriptDelta  = "session.input_transcript.delta"
	ServerOutputTranscriptDelta = "session.output_transcript.delta"
	ServerOutputAudioDelta      = "session.output_audio.delta"
	ServerDelegationCreated     = "session.delegation.created"
	ServerThinkingAppended      = "session.thinking.appended"
	ServerCommentaryAppended    = "session.commentary.appended"
	ServerInstructionsAppended  = "session.instructions.appended"
	ServerUsageUpdated          = "session.usage.updated"
	ServerResponseEvent         = "response.event"
	ServerError                 = "error"
)

// golive extension events. These have no gpt-live-1 equivalent; they expose
// the seams of the simulated duplex engine so you can see what a real
// full-duplex model would be doing internally.
const (
	ExtSpeechStarted  = "golive.speech.started"
	ExtSpeechStopped  = "golive.speech.stopped"
	ExtAudioTruncated = "golive.output_audio.truncated"
	ExtTurnMetrics    = "golive.turn.metrics"
	ExtBackchannel    = "golive.backchannel"
	ExtChannelState   = "golive.channel.state"
)

// Close reasons carried on session.closed.
const (
	CloseRequested      = "close_requested"
	CloseExpired        = "expired"
	CloseContent        = "content"
	CloseRemoteHangup   = "remote_hangup"
	CloseConnectionLost = "connection_lost"
	CloseError          = "error"
)

// Delegation targets.
const (
	DelegationClient    = "client"
	DelegationResponses = "responses"
)

// Envelope is the common shape of every event in both directions. Type is the
// discriminator; the rest of the payload is decoded per type.
type Envelope struct {
	Type    string `json:"type"`
	EventID string `json:"event_id,omitempty"`
}

// AudioFormat is the PCM description a WebSocket session negotiates at startup.
// gpt-live-1 fixes this at session.start and refuses to change it mid-session,
// because a rate change mid-stream would desynchronise every timestamp already
// issued.
type AudioFormat struct {
	Type string `json:"type"` // "audio/pcm"
	Rate int    `json:"rate"` // 8000, 16000 or 24000
}

// DefaultAudioFormat matches gpt-live-1's WebSocket default.
func DefaultAudioFormat() AudioFormat {
	return AudioFormat{Type: "audio/pcm", Rate: 24000}
}

// OutputAudio configures the spoken side of the session.
type OutputAudio struct {
	Voice string  `json:"voice,omitempty"`
	Speed float64 `json:"speed,omitempty"`
}

// AudioConfig is session.audio.
type AudioConfig struct {
	Format *AudioFormat `json:"format,omitempty"`
	Output *OutputAudio `json:"output,omitempty"`
}

// ResponsesDelegation configures the managed backend in "responses" mode.
type ResponsesDelegation struct {
	Model             string            `json:"model,omitempty"`
	Instructions      string            `json:"instructions,omitempty"`
	Tools             []json.RawMessage `json:"tools,omitempty"`
	ToolChoice        any               `json:"tool_choice,omitempty"`
	ParallelToolCalls bool              `json:"parallel_tool_calls,omitempty"`
	MaxOutputTokens   int               `json:"max_output_tokens,omitempty"`
	ServiceTier       string            `json:"service_tier,omitempty"`
}

// DelegationConfig selects who runs backend work.
type DelegationConfig struct {
	Type      string               `json:"type,omitempty"` // "client" | "responses"
	Responses *ResponsesDelegation `json:"responses,omitempty"`
}

// InputMessage seeds conversation history at session.start.
type InputMessage struct {
	Type    string `json:"type,omitempty"` // "message"
	Role    string `json:"role"`           // "user" | "assistant" | "system"
	Content string `json:"content"`
}

// SessionConfig is the session object exchanged on session.start /
// session.started.
type SessionConfig struct {
	ID           string            `json:"id,omitempty"`
	Model        string            `json:"model,omitempty"`
	Instructions string            `json:"instructions,omitempty"`
	Input        []InputMessage    `json:"input,omitempty"`
	Audio        *AudioConfig      `json:"audio,omitempty"`
	Delegation   *DelegationConfig `json:"delegation,omitempty"`
	Store        bool              `json:"store,omitempty"`

	// Golive is an optional extension block for engine tuning. A gpt-live-1
	// client never sends it and the defaults are always usable.
	Golive *GoliveConfig `json:"golive,omitempty"`
}

// GoliveConfig exposes the duplex engine knobs a client may want per session.
type GoliveConfig struct {
	// Backchannel enables short spoken acknowledgements while the user talks
	// or while backend work runs. This is the audible half of "full duplex".
	Backchannel *bool `json:"backchannel,omitempty"`
	// Speculative starts a turn from a stable partial transcript instead of
	// waiting for the final one.
	Speculative *bool `json:"speculative,omitempty"`
	// ASR, LLM and TTS override the configured provider for this session,
	// mostly so the demo page can switch to mocks without a restart.
	ASR string `json:"asr,omitempty"`
	LLM string `json:"llm,omitempty"`
	TTS string `json:"tts,omitempty"`
	// Language hints the ASR engine ("zh", "en").
	Language string `json:"language,omitempty"`
	// OnNewQuery overrides what happens to an in-flight answer when the user
	// speaks again: "cut", "finish_sentence" or "queue".
	OnNewQuery string `json:"on_new_query,omitempty"`
	// Greeting is spoken the moment the session opens, before the caller says
	// anything. A pointer so the three cases stay distinct: absent uses the
	// server default, an empty string explicitly suppresses it, and any other
	// value replaces it.
	Greeting *string `json:"greeting,omitempty"`
}

// --- Client events ---

// SessionStartEvent is session.start.
type SessionStartEvent struct {
	Envelope
	Session SessionConfig `json:"session"`
}

// SessionUpdateEvent is session.update. Only delegation.responses may change.
type SessionUpdateEvent struct {
	Envelope
	Session SessionConfig `json:"session"`
}

// InputAudioAppendEvent is session.input_audio.append. Audio is base64 PCM in
// the negotiated session format.
type InputAudioAppendEvent struct {
	Envelope
	Audio string `json:"audio"`
}

// AppendEvent covers the three "append" client events, which share a shape.
// DelegationID is set on thinking/commentary appends that answer a delegation.
type AppendEvent struct {
	Envelope
	Content      string `json:"content"`
	DelegationID string `json:"delegation_id,omitempty"`
}

// ResponseItem is an item queued for the Responses backend.
type ResponseItem struct {
	Type    string `json:"type"` // "function_call_output" | "message"
	CallID  string `json:"call_id,omitempty"`
	Output  string `json:"output,omitempty"`
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// ResponseItemCreateEvent is response.item.create.
type ResponseItemCreateEvent struct {
	Envelope
	Item         ResponseItem `json:"item"`
	DelegationID string       `json:"delegation_id,omitempty"`
}

// ResponseCreateEvent is response.create.
type ResponseCreateEvent struct {
	Envelope
	DelegationID string `json:"delegation_id,omitempty"`
}

// --- Server events ---

// SessionStartedEvent is session.started.
type SessionStartedEvent struct {
	Envelope
	Session SessionConfig `json:"session"`
}

// TranscriptDelta is session.input_transcript.delta and
// session.output_transcript.delta.
//
// Deltas are fragments, not a running total. Clients append them to a row keyed
// by ItemID; a fragment whose Final is true supersedes the earlier fragments
// covering the same span, which is how a streaming ASR correction reaches the
// UI without rewriting the whole transcript.
type TranscriptDelta struct {
	Envelope
	ItemID  string `json:"item_id,omitempty"`
	Content string `json:"content"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Final   bool   `json:"final,omitempty"`

	// Replace is a golive extension. Streaming recognizers revise words they
	// already emitted; when that happens Content is the whole corrected
	// hypothesis for the row rather than a fragment to append. Clients that
	// ignore the field still converge, because the final delta always carries
	// the complete text with Replace set.
	Replace bool `json:"replace,omitempty"`
}

// OutputAudioDelta is session.output_audio.delta: base64 PCM in the session
// format. There is deliberately no "done" event — gpt-live-1 does not emit one,
// because generation end and playback end are different moments and only the
// client knows the latter.
type OutputAudioDelta struct {
	Envelope
	Audio string `json:"audio"`
	// ItemID ties audio back to the assistant turn that produced it, so a
	// client can drop audio belonging to a turn it already truncated.
	ItemID string `json:"item_id,omitempty"`
}

// Delegation describes a unit of backend work.
type Delegation struct {
	ID     string `json:"id"`
	Target string `json:"target"` // "client" | "responses"
	// Reason is a golive extension: a short machine-readable hint about why
	// the live layer delegated.
	Reason string `json:"reason,omitempty"`
	// Transcript is the user text that triggered the delegation. gpt-live-1
	// expects the client to reconstruct this from transcript events; golive
	// includes it so a client does not have to.
	Transcript  string `json:"transcript,omitempty"`
	CreatedAtMS int64  `json:"created_at_ms,omitempty"`
}

// DelegationCreatedEvent is session.delegation.created.
type DelegationCreatedEvent struct {
	Envelope
	Delegation Delegation `json:"delegation"`
}

// ResponseEventEnvelope is response.event: a Responses-backend event wrapped
// with the delegation it belongs to.
type ResponseEventEnvelope struct {
	Envelope
	DelegationID string          `json:"delegation_id"`
	Event        json.RawMessage `json:"event"`
}

// Usage is session.usage.updated.
type Usage struct {
	Envelope
	Usage UsageBody `json:"usage"`
}

// UsageBody carries cumulative session cost drivers.
type UsageBody struct {
	VoiceDurationSeconds float64 `json:"voice_duration_seconds"`
	InputAudioSeconds    float64 `json:"input_audio_seconds"`
	OutputAudioSeconds   float64 `json:"output_audio_seconds"`
	BackendInputTokens   int     `json:"backend_input_tokens,omitempty"`
	BackendOutputTokens  int     `json:"backend_output_tokens,omitempty"`
}

// SessionClosedEvent is session.closed.
type SessionClosedEvent struct {
	Envelope
	Reason string `json:"reason"`
}

// ErrorBody is the error payload.
type ErrorBody struct {
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
	Param   string `json:"param,omitempty"`
}

// ErrorEvent is the error server event. ClientEventID, when set, names the
// client event that was rejected — the discriminator between "your command was
// refused" and "the session failed".
type ErrorEvent struct {
	Envelope
	Error         ErrorBody `json:"error"`
	ClientEventID string    `json:"client_event_id,omitempty"`
}

// AckEvent is the shared shape of session.*.appended acknowledgements.
type AckEvent struct {
	Envelope
	DelegationID string `json:"delegation_id,omitempty"`
}

// --- Extension events ---

// SpeechEvent is golive.speech.started / golive.speech.stopped.
type SpeechEvent struct {
	Envelope
	StartMS int64 `json:"start_ms"`
	EndMS   int64 `json:"end_ms,omitempty"`
	BargeIn bool  `json:"barge_in,omitempty"`
}

// AudioTruncatedEvent is golive.output_audio.truncated: the assistant was cut
// off, and PlayedMS is how much of the turn the user actually heard. The
// engine rewrites conversation history to that prefix, which is what stops the
// model from believing it said things the user never heard.
type AudioTruncatedEvent struct {
	Envelope
	ItemID   string `json:"item_id,omitempty"`
	PlayedMS int64  `json:"played_ms"`
	TotalMS  int64  `json:"total_ms"`
	Text     string `json:"text,omitempty"`
}

// TurnMetricsEvent is golive.turn.metrics: per-stage latency for one turn.
//
// Every stage figure is measured from the same origin — the moment the VAD
// decided the user had stopped talking — because that is the instant the caller
// starts waiting. Measuring from turn creation instead flatters a speculative
// turn (which starts before the user finishes, so its stages appear to take
// negative time) and makes runs incomparable across engines.
type TurnMetricsEvent struct {
	Envelope
	TurnID      string `json:"turn_id"`
	Revision    int    `json:"revision"`
	Speculative bool   `json:"speculative,omitempty"`

	// SpeechEndMS is the session-relative clock at VAD close: the origin every
	// other figure here is relative to.
	SpeechEndMS int64 `json:"speech_end_ms"`

	// ASRFinalMS is speech end to the final transcript.
	ASRFinalMS int64 `json:"asr_final_ms"`
	// LLMFirstTokenMS is speech end to the backend's first token. Negative when
	// speculation started the turn before the user stopped — which is the point
	// of speculating, so the sign is meaningful, not an error.
	LLMFirstTokenMS int64 `json:"llm_first_token_ms"`
	// TTSFirstAudioMS is speech end to the first synthesized PCM.
	TTSFirstAudioMS int64 `json:"tts_first_audio_ms"`

	// FirstAudioOutMS is speech end to the first audio byte actually written to
	// the client. This is the headline: the only latency a caller experiences.
	FirstAudioOutMS int64 `json:"first_audio_out_ms"`
	// TurnCompleteMS is speech end to the last audio byte of the turn.
	TurnCompleteMS int64 `json:"turn_complete_ms"`
	// OutputAudioMS is how much audio the turn produced.
	OutputAudioMS int64 `json:"output_audio_ms"`
	// Truncated marks a turn the user cut short; its figures describe a partial
	// answer and should be excluded from latency aggregates.
	Truncated bool `json:"truncated,omitempty"`
}

// ChannelStateEvent is golive.channel.state: which simulated duplex channels
// are live right now. It is what makes the simulation legible — during a real
// barge-in you can watch listen and speak overlap.
type ChannelStateEvent struct {
	Envelope
	Listening    bool   `json:"listening"`
	Transcribing bool   `json:"transcribing"`
	Thinking     bool   `json:"thinking"`
	Speaking     bool   `json:"speaking"`
	Muted        bool   `json:"muted,omitempty"`
	TurnID       string `json:"turn_id,omitempty"`
}

// BackchannelEvent is golive.backchannel: a short acknowledgement was spoken
// while the user held the floor.
type BackchannelEvent struct {
	Envelope
	Text string `json:"text"`
}

// DecodeType peeks at the discriminator without decoding the whole payload.
func DecodeType(raw []byte) (string, string, error) {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", "", fmt.Errorf("live: malformed event: %w", err)
	}
	if env.Type == "" {
		return "", "", fmt.Errorf("live: event is missing the required \"type\" field")
	}
	return env.Type, env.EventID, nil
}

// NewError builds an error event, optionally naming the rejected client event.
func NewError(kind, code, message, clientEventID string) ErrorEvent {
	return ErrorEvent{
		Envelope:      Envelope{Type: ServerError},
		Error:         ErrorBody{Type: kind, Code: code, Message: message},
		ClientEventID: clientEventID,
	}
}
