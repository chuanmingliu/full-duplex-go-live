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
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
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
	ServerInputAudioMuted       = "session.input_audio.muted"
	ServerInputAudioUnmuted     = "session.input_audio.unmuted"
	ServerUsageUpdated          = "session.usage.updated"
	ServerResponseEvent         = "response.event"
	ServerError                 = "error"
)

// golive extension events. These have no gpt-live-1 equivalent; they expose
// the seams of the simulated duplex engine so you can see what a real
// full-duplex model would be doing internally.
const (
	ExtSpeechStarted    = "golive.speech.started"
	ExtSpeechStopped    = "golive.speech.stopped"
	ExtAudioTruncated   = "golive.output_audio.truncated"
	ExtTurnMetrics      = "golive.turn.metrics"
	ExtBackchannel      = "golive.backchannel"
	ExtInputBackchannel = "golive.input_backchannel"
	ExtChannelState     = "golive.channel.state"
	// ExtInputTranscript carries the recognizer's whole current hypothesis for
	// an utterance, revisions included. See InputTranscriptEvent.
	ExtInputTranscript = "golive.input_transcript.updated"
	// ExtDelegationSpeculative is an early, retractable hint that a client
	// delegation is probably coming. See SpeculativeDelegationEvent.
	ExtDelegationSpeculative = "golive.delegation.speculative"
)

// Close reasons carried on session.closed.
const (
	CloseRequested      = "close_requested"
	CloseExpired        = "expired"
	CloseContent        = "content"
	CloseRemoteHangup   = "remote_hangup"
	CloseConnectionLost = "connection_lost"
	CloseError          = "error"
	CloseShutdown       = "server_shutdown"
	CloseFlooded        = "rate_limited"
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

// AudioFormat is the audio a WebSocket session negotiates at startup, for both
// directions. gpt-live-1 fixes this at session.start and refuses to change it
// mid-session, because a rate change mid-stream would desynchronise every
// timestamp already issued.
//
// The types are the ones gpt-live-1 accepts: audio/pcm (signed 16-bit
// little-endian, 24000 or 16000 Hz) and the two G.711 laws a phone line
// speaks, audio/pcmu and audio/pcma, which are always 8000 Hz and one byte a
// sample. golive additionally accepts audio/pcm at 8000 and 48000.
type AudioFormat struct {
	Type string `json:"type"`
	Rate int    `json:"rate,omitempty"`
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
// Every field is optional on session.update, where an omitted setting keeps its
// value — which is why the two that have a meaningful zero are pointers.
type ResponsesDelegation struct {
	Model        string `json:"model,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	// Tools are in the Responses form, {"type":"function","name":…}. Only
	// function tools can be honoured: a hosted tool such as web_search needs a
	// backend that runs it, and an OpenAI-compatible chat endpoint does not.
	Tools             []json.RawMessage `json:"tools,omitempty"`
	ToolChoice        any               `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
	MaxOutputTokens   *int              `json:"max_output_tokens,omitempty"`
	ServiceTier       string            `json:"service_tier,omitempty"`
	// Reasoning and Text are accepted and echoed so a gpt-live-1 session
	// object round-trips; the chat-completions backends golive drives have no
	// equivalent to map them onto.
	Reasoning json.RawMessage `json:"reasoning,omitempty"`
	Text      json.RawMessage `json:"text,omitempty"`
}

// Merge overlays the settings present in update onto r, the way session.update
// is specified: what is omitted keeps its value.
func (r *ResponsesDelegation) Merge(update *ResponsesDelegation) {
	if update == nil {
		return
	}
	if update.Model != "" {
		r.Model = update.Model
	}
	if update.Instructions != "" {
		r.Instructions = update.Instructions
	}
	if update.Tools != nil {
		r.Tools = update.Tools
	}
	if update.ToolChoice != nil {
		r.ToolChoice = update.ToolChoice
	}
	if update.ParallelToolCalls != nil {
		r.ParallelToolCalls = update.ParallelToolCalls
	}
	if update.MaxOutputTokens != nil {
		r.MaxOutputTokens = update.MaxOutputTokens
	}
	if update.ServiceTier != "" {
		r.ServiceTier = update.ServiceTier
	}
	if update.Reasoning != nil {
		r.Reasoning = update.Reasoning
	}
	if update.Text != nil {
		r.Text = update.Text
	}
}

// DelegationConfig selects who runs backend work.
type DelegationConfig struct {
	Type      string               `json:"type,omitempty"` // "client" | "responses"
	Responses *ResponsesDelegation `json:"responses,omitempty"`
}

// InputMessage seeds conversation history at session.start.
type InputMessage struct {
	Type string `json:"type,omitempty"` // "message"
	// Role is "developer", "user" or "assistant". "system" is accepted as a
	// synonym for developer.
	Role    string         `json:"role"`
	Content MessageContent `json:"content"`
}

// ContentPart is one piece of a message: input_text from a user or developer,
// output_text (or text) from the assistant.
type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// MessageContent is a message body. gpt-live-1 writes it as a list of typed
// parts; a bare string is accepted too, because that is what every earlier
// golive client sent and what a person writes by hand.
type MessageContent []ContentPart

// TextContent builds a single-part body of the given part type.
func TextContent(partType, text string) MessageContent {
	return MessageContent{{Type: partType, Text: text}}
}

// UnmarshalJSON accepts either form.
func (c *MessageContent) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*c = nil
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		*c = MessageContent{{Type: "input_text", Text: s}}
		return nil
	}
	var parts []ContentPart
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return fmt.Errorf("content must be a string or a list of text parts: %w", err)
	}
	*c = parts
	return nil
}

// Text joins the textual parts. Parts of any other kind — an image, say — are
// skipped: the live layer is audio and text, and anything else belongs to a
// backend that can read it.
func (c MessageContent) Text() string {
	var b strings.Builder
	for _, p := range c {
		switch p.Type {
		case "input_text", "output_text", "text", "":
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// FlexString is a string field that also accepts any other JSON value, kept as
// its literal text. A function result is specified as a JSON string, and
// applications send the object itself often enough that refusing it would be
// pedantry: the model reads either the same way.
type FlexString string

// UnmarshalJSON implements json.Unmarshaler.
func (f *FlexString) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		*f = FlexString(s)
		return nil
	}
	if bytes.Equal(trimmed, []byte("null")) {
		*f = ""
		return nil
	}
	*f = FlexString(trimmed)
	return nil
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
	// UserBackchannelPhrases are the things the caller may say without taking
	// the floor — "嗯", "对", "uh huh". Hearing one while the assistant is
	// speaking keeps the answer running instead of cutting it off.
	//
	// A pointer for the same reason as Greeting: absent means the server's
	// list, and an explicitly empty list means no list at all, which restores
	// the plain behaviour where any speech interrupts. Those are different
	// instructions and a slice alone cannot tell them apart.
	UserBackchannelPhrases *[]string `json:"user_backchannel_phrases,omitempty"`
	// ClientDelegation overrides duplex.client_delegation for this session:
	// "always" or "auto".
	ClientDelegation string `json:"client_delegation,omitempty"`
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
// DelegationID names the client delegation an append belongs to; null or
// absent means it is session-wide.
type AppendEvent struct {
	Envelope
	Content      string `json:"content"`
	DelegationID string `json:"delegation_id,omitempty"`
}

// ResponseItem is an item queued for the backend: a function result, or a
// message the user typed rather than spoke.
type ResponseItem struct {
	Type    string         `json:"type"` // "function_call_output" | "message"
	CallID  string         `json:"call_id,omitempty"`
	Output  FlexString     `json:"output,omitempty"`
	Role    string         `json:"role,omitempty"`
	Content MessageContent `json:"content,omitempty"`
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

// SessionStartedEvent is session.started, and session.updated, which has the
// same shape. ClientEventID names the command it answers.
type SessionStartedEvent struct {
	Envelope
	ClientEventID string        `json:"client_event_id,omitempty"`
	Session       SessionConfig `json:"session"`
}

// TranscriptDelta is session.input_transcript.delta and
// session.output_transcript.delta.
//
// Delta is a fragment and the stream is append-only, which is the whole of the
// gpt-live-1 contract: a client concatenates what arrives and never has to
// take anything back. That is a real constraint on a cascade, because a
// streaming recognizer revises words it has already emitted. So the caller's
// side only ever carries text the recognizer has stopped changing — a prefix
// two consecutive hypotheses agree on, then the rest when the utterance is
// final — and the live, revisable hypothesis travels separately on
// golive.input_transcript.updated for a client that wants captions that keep
// up. The assistant's side carries each sentence as its audio starts to play,
// so what is in the transcript is what was said, not what was drafted.
//
// StartMS and EndMS are on the session clock. On the assistant's side EndMS is
// the start again while a sentence is still being synthesized: its length is
// not known until its audio has all arrived, and the text should not wait for
// that.
type TranscriptDelta struct {
	Envelope
	Delta   string `json:"delta"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`

	// ItemID and Final are golive extensions. gpt-live-1 leaves grouping
	// fragments into turns to the application; these do it for a client that
	// would rather not. Final marks the last fragment of a caller utterance.
	ItemID string `json:"item_id,omitempty"`
	Final  bool   `json:"final,omitempty"`
}

// InputTranscriptEvent is golive.input_transcript.updated: the recognizer's
// whole current hypothesis for one utterance.
//
// Unlike the standard delta it is not append-only. Text replaces whatever the
// client showed for ItemID, so a correction three words back simply appears.
// The last one for an utterance has Final set and equals the concatenation of
// that utterance's standard deltas.
type InputTranscriptEvent struct {
	Envelope
	ItemID  string `json:"item_id"`
	Text    string `json:"text"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Final   bool   `json:"final,omitempty"`
}

// OutputAudioDelta is session.output_audio.delta: base64 audio in the session
// format, under "delta". There is deliberately no "done" event — gpt-live-1
// does not emit one, because generation end and playback end are different
// moments and only the client knows the latter.
type OutputAudioDelta struct {
	Envelope
	Delta string `json:"delta"`
	// Audio repeats Delta under the key golive used before it matched
	// gpt-live-1. Only sent when duplex.legacy_audio_field is on.
	Audio string `json:"audio,omitempty"`
	// StartMS and EndMS place the chunk on the session clock: when the listener
	// hears it, not when it was sent, since the server runs a little ahead of
	// the playhead.
	StartMS int64 `json:"start_ms"`
	EndMS   int64 `json:"end_ms"`
	// ItemID ties audio back to the assistant turn that produced it, so a
	// client can drop audio belonging to a turn it already truncated.
	ItemID string `json:"item_id,omitempty"`

	// PCM is the chunk as linear PCM16 at the session rate. It never reaches
	// the wire: the engine fills it in, and the session encodes it into Delta
	// in whatever format the client negotiated.
	PCM []byte `json:"-"`
}

// Delegation describes a unit of backend work.
type Delegation struct {
	ID string `json:"id"`
	// Type is always "delegation".
	Type   string `json:"type"`
	Target string `json:"target"` // "client" | "responses"
	// ResponseID names the backend response, in responses mode.
	ResponseID string `json:"response_id,omitempty"`

	// The rest are golive extensions.

	// Reason is a short machine-readable hint about why the live layer
	// delegated: final_transcript, speculative, revision, typed_input,
	// tool_result or model.
	Reason string `json:"reason,omitempty"`
	// Transcript is the user text that triggered the delegation. gpt-live-1
	// expects the client to reconstruct this from transcript events; golive
	// includes it so a client does not have to.
	Transcript string `json:"transcript,omitempty"`
	// Request is what the conversational model asked the backend for, when the
	// delegation was its own decision rather than a turn boundary.
	Request string `json:"request,omitempty"`
}

// DelegationCreatedEvent is session.delegation.created. OffsetMS is where on
// the session clock the delegation was made.
type DelegationCreatedEvent struct {
	Envelope
	OffsetMS   int64      `json:"offset_ms"`
	Delegation Delegation `json:"delegation"`
}

// SpeculativeDelegationEvent is golive.delegation.speculative: notice that the
// caller has paused on what looks like a finished request, work on it has
// begun, and a session.delegation.created for it will probably follow.
//
// It exists because the standard event is a promise. A delegation, once
// announced, is real — gpt-live-1 never takes one back — so golive does not
// announce one until the caller's turn is certain. In client mode, an
// application that can start reversible work early (a lookup, a cache warm)
// may act on this hint and save the wait; one that books flights should ignore
// it. If the caller carries on talking the hint is simply never confirmed, and
// the delegation that eventually arrives may have a different id and different
// words. In responses mode there is nothing to act on, and the event is there
// to show the head start being taken.
type SpeculativeDelegationEvent struct {
	Envelope
	OffsetMS     int64  `json:"offset_ms"`
	DelegationID string `json:"delegation_id"`
	Target       string `json:"target"`
	Transcript   string `json:"transcript"`
}

// ResponseEventEnvelope is response.event: a Responses-backend event wrapped
// with the delegation it belongs to.
type ResponseEventEnvelope struct {
	Envelope
	DelegationID string          `json:"delegation_id"`
	Event        json.RawMessage `json:"event"`
}

// Usage is session.usage.updated. Every one is a cumulative snapshot that
// replaces the one before: do not sum them.
type Usage struct {
	Envelope
	Usage         UsageBody     `json:"usage"`
	ContextWindow ContextWindow `json:"context_window"`
}

// UsageBody carries cumulative session cost drivers. Seconds is the one
// gpt-live-1 bills on; the rest are golive's own, because a cascade's cost is
// its three vendors' and none of them bills by the session second.
type UsageBody struct {
	Seconds             float64 `json:"seconds"`
	InputAudioSeconds   float64 `json:"input_audio_seconds"`
	OutputAudioSeconds  float64 `json:"output_audio_seconds"`
	BackendInputTokens  int     `json:"backend_input_tokens,omitempty"`
	BackendOutputTokens int     `json:"backend_output_tokens,omitempty"`
	// BackendCachedInputTokens is how many of the input tokens the provider
	// served from its prefix cache, where it says.
	BackendCachedInputTokens int `json:"backend_cached_input_tokens,omitempty"`
}

// ContextWindow reports how full the conversational context is, from 0 to 1.
type ContextWindow struct {
	UsageRatio float64 `json:"usage_ratio"`
}

// SessionClosedEvent is session.closed: the last event of a session, carrying
// its final usage and the configuration it ran with.
type SessionClosedEvent struct {
	Envelope
	Reason  string         `json:"reason"`
	Usage   *UsageBody     `json:"usage,omitempty"`
	Session *SessionConfig `json:"session,omitempty"`
}

// ErrorBody is the error payload. ClientEventID, when set, names the client
// event that was rejected — the discriminator between "your command was
// refused" and "the session failed".
type ErrorBody struct {
	Type          string `json:"type"`
	Code          string `json:"code,omitempty"`
	Message       string `json:"message"`
	Param         string `json:"param,omitempty"`
	ClientEventID string `json:"client_event_id,omitempty"`
}

// ErrorEvent is the error server event.
//
// ClientEventID appears twice. gpt-live-1 puts it inside the error object;
// golive originally put it beside it, and clients written against that are
// still out there. Both are filled in.
type ErrorEvent struct {
	Envelope
	Error         ErrorBody `json:"error"`
	ClientEventID string    `json:"client_event_id,omitempty"`
}

// AckEvent is the shared shape of the acknowledgements: session.*.appended,
// and session.input_audio.muted / unmuted. ClientEventID names the command.
// StartMS and EndMS, on an append, say where on the session clock the content
// entered the conversation — when it was taken in, not when anything was said.
type AckEvent struct {
	Envelope
	ClientEventID string `json:"client_event_id,omitempty"`
	DelegationID  string `json:"delegation_id,omitempty"`
	StartMS       *int64 `json:"start_ms,omitempty"`
	EndMS         *int64 `json:"end_ms,omitempty"`
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

	// TurnStartMS is speech end to the moment the turn's answer was begun.
	// Negative for a speculative turn: that is how much of the caller's pause
	// the backend was already working through, and with LLMFirstTokenMS it
	// gives the backend's own time to first token, which no other pair of
	// figures here does.
	TurnStartMS int64 `json:"turn_start_ms"`
	// ASRFinalMS is speech end to the final transcript.
	ASRFinalMS int64 `json:"asr_final_ms"`
	// LLMFirstTokenMS is speech end to the backend's first token. Negative when
	// speculation started the turn before the user stopped — which is the point
	// of speculating, so the sign is meaningful, not an error.
	LLMFirstTokenMS int64 `json:"llm_first_token_ms"`
	// FirstSegmentMS is speech end to the first speakable segment reaching the
	// synthesizer. FirstSegment minus FirstToken is time spent waiting for a
	// sentence boundary; TTSFirstAudio minus FirstSegment is the provider.
	FirstSegmentMS int64 `json:"first_segment_ms"`
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

// Backchannel kinds. Two quite different behaviours share one event, and a log
// that does not distinguish them is misleading: "backchannel: 稍等一下" on every
// turn reads as over-eager acknowledgement, when it is actually the assistant
// covering a slow backend.
const (
	// BackchannelAck is a short acknowledgement spoken while the *user* is
	// still talking — the audible half of full duplex.
	BackchannelAck = "ack"
	// BackchannelHoldingFiller is spoken while backend work runs, to keep a
	// delegating agent from sounding like a dropped call.
	BackchannelHoldingFiller = "holding_filler"
)

// BackchannelEvent is golive.backchannel: the assistant said something short
// that is not part of an answer. Kind says which of the two it was.
type BackchannelEvent struct {
	Envelope
	Kind string `json:"kind,omitempty"`
	Text string `json:"text"`
}

// InputBackchannelEvent is golive.input_backchannel: the caller said something
// that was heard, recognised as an acknowledgement, and deliberately not
// treated as an interruption.
//
// It is emitted instead of the transcript delta and turn the utterance would
// otherwise have produced, so a client that draws conversation history has one
// event telling it that speech happened and was intentionally dropped. Without
// it the caller appears to have said nothing at all, which is indistinguishable
// from the recognizer having failed.
type InputBackchannelEvent struct {
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
		Envelope: Envelope{Type: ServerError},
		Error: ErrorBody{
			Type:          kind,
			Code:          code,
			Message:       message,
			ClientEventID: clientEventID,
		},
		ClientEventID: clientEventID,
	}
}

// StampEventID inserts an event_id into an already-encoded server event.
//
// Every gpt-live-1 server event has one, and stamping at the socket is the one
// place that can number them in the order they actually went out. Doing it on
// the encoded bytes avoids a second encode of every audio chunk. It relies on
// "type" being the first key, which holds for every event here because they
// all embed Envelope first; anything else is returned untouched.
func StampEventID(data []byte, id string) []byte {
	const prefix = `{"type":"`
	if id == "" || !bytes.HasPrefix(data, []byte(prefix)) {
		return data
	}
	end := bytes.IndexByte(data[len(prefix):], '"')
	if end < 0 {
		return data
	}
	at := len(prefix) + end + 1
	if bytes.HasPrefix(data[at:], []byte(`,"event_id"`)) {
		return data
	}
	out := make([]byte, 0, len(data)+len(id)+16)
	out = append(out, data[:at]...)
	out = append(out, `,"event_id":"`...)
	out = append(out, id...)
	out = append(out, '"')
	out = append(out, data[at:]...)
	return out
}
