// Package config loads golive's settings from three layers, in the order the
// architecture notes require: compiled defaults, a JSON profile that names
// provider choices and safe runtime tuning, and the environment, which owns
// credentials and account-specific endpoints and nothing else.
//
// Keeping credentials out of the profile is what makes a profile committable.
package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config is the fully resolved service configuration.
type Config struct {
	// Server
	Addr     string `json:"addr"`
	LogLevel string `json:"log_level"`
	// LogFormat is "text" (default, human logs) or "json" (aggregators).
	LogFormat string `json:"log_format"`
	// WebRoot serves the demo client. Empty disables it.
	WebRoot string `json:"web_root"`

	// AuthToken is read only from GOLIVE_AUTH_TOKEN. It is never a profile
	// field, which is what keeps a profile committable.
	AuthToken string `json:"-"`
	// AuthRequired refuses to boot without AuthToken. Set it in a production
	// profile so a missing env var is a start-up failure rather than an open
	// socket.
	AuthRequired bool `json:"auth_required"`
	// AllowedOrigins is the WebSocket Origin allow-list. Empty means "allow
	// anything" when auth is off, and "same host only" when a token is set.
	AllowedOrigins []string `json:"allowed_origins"`
	// TrustProxy reads X-Forwarded-For / X-Real-IP for per-IP admission.
	TrustProxy bool `json:"trust_proxy"`

	// MaxSessions caps concurrent WebSocket sessions; 0 is unlimited.
	MaxSessions int `json:"max_sessions"`
	// MaxSessionsPerIP caps sessions from one client address; 0 is unlimited.
	MaxSessionsPerIP int `json:"max_sessions_per_ip"`
	// AllowProviderOverride lets a client pick ASR/LLM/TTS at session.start.
	// The demo page uses this; a production profile turns it off.
	AllowProviderOverride bool `json:"allow_provider_override"`
	// EnableMetrics serves Prometheus text at /metrics. On by default.
	EnableMetrics bool `json:"enable_metrics"`
	// EnablePprof serves /debug/pprof. Off by default; still behind auth when
	// a token is configured.
	EnablePprof bool `json:"enable_pprof"`
	// MetricsPublic serves /metrics without a token. Off by default; turn it
	// on only when the scrape endpoint is on a private network.
	MetricsPublic bool `json:"metrics_public"`
	// MetricsToken is an optional scrape credential, independent of the
	// session token, so Prometheus does not have to hold the caller's secret.
	// Read only from GOLIVE_METRICS_TOKEN.
	MetricsToken string `json:"-"`
	// AuthFailBurst is how many 401s one IP may collect inside
	// AuthFailWindowSeconds before further attempts are 429. 0 disables.
	AuthFailBurst int `json:"auth_fail_burst"`
	// AuthFailWindowSeconds is the window for AuthFailBurst. 0 means 60.
	AuthFailWindowSeconds int `json:"auth_fail_window_seconds"`
	// TLSCertFile / TLSKeyFile enable in-process TLS. Empty means HTTP; put
	// TLS at the edge unless you have a reason not to.
	TLSCertFile string `json:"tls_cert_file"`
	TLSKeyFile  string `json:"tls_key_file"`
	// ShutdownTimeoutSeconds is how long SIGTERM waits for sessions to close.
	ShutdownTimeoutSeconds int `json:"shutdown_timeout_seconds"`

	// StoreDir is where sessions started with store:true keep their recording
	// and their conversation, which is what GET …/content and …/fork read.
	// Empty disables storage: store:true is then accepted and ignored, and
	// those two endpoints answer 404.
	StoreDir string `json:"store_dir"`
	// LastCallFile, when set, is where the instructions and opening line of the
	// last answered phone call are kept, readable by the server's user alone.
	//
	// An originator's operator composes the prompt in a browser, and it lives
	// there: a call placed from anywhere else carries none of it. With this
	// file a call can be placed again from the machine itself exactly as the
	// last one was (scripts/phone-brain.sh call). It is the conversation's
	// instructions at rest on disk, so it is off unless a path is given.
	LastCallFile string `json:"last_call_file"`
	// Realtime configures /v1/realtime, the endpoint that speaks the OpenAI
	// Realtime event protocol over the same engine. It exists for clients that
	// cannot speak gpt-live-1 — a SIP media gateway, mostly.
	Realtime RealtimeConfig `json:"realtime"`

	// TelemetryURL, when set, receives a small JSON event for each boundary of
	// a turn (speech start and stop, final transcript, first token, first
	// audio) as it happens, for an external latency console. Posting is
	// asynchronous and lossy by design: it is never on the audio path.
	TelemetryURL string `json:"telemetry_url"`
	// TelemetryFormat is the shape of those events: "v1", one self-describing
	// record per boundary with a call id, a turn number and a monotonic
	// timestamp; or "legacy", the looser {at, component, event} envelope that
	// came before it.
	TelemetryFormat string `json:"telemetry_format"`

	// StoreTTLHours is how long a stored session is kept. gpt-live-1 keeps
	// recordings thirty days; the default here is one, because this is a disk
	// on your machine rather than somebody else's.
	StoreTTLHours int `json:"store_ttl_hours"`

	// Tools the assistant can call that golive carries out itself (see
	// internal/tools): quick ones answered in the same turn, slow ones
	// (async) submitted to a pool and reported when they finish. ToolsMemory
	// gives the model the update_memory builtin and renders the call's
	// working memory into its context. ToolsToken is read from ToolsTokenFile
	// and sent as a bearer to the tools' services. ToolsWorkers bounds the
	// pool; ToolsDeliverWaitMS is how long a finished task waits for a gap in
	// the conversation before it is announced over whatever is going on.
	Tools []ToolConfig `json:"tools,omitempty"`
	// ToolsBaseURL is where tools whose URL is a path live: the lead service,
	// on this machine or on the intranet (GOLIVE_TOOLS_BASE_URL).
	ToolsBaseURL       string `json:"tools_base_url"`
	ToolsMemory        bool   `json:"tools_memory"`
	ToolsTokenFile     string `json:"tools_token_file"`
	ToolsWorkers       int    `json:"tools_workers"`
	ToolsDeliverWaitMS int    `json:"tools_deliver_wait_ms"`

	// Providers, by registry name.
	ASR string `json:"asr"`
	LLM string `json:"llm"`
	TTS string `json:"tts"`

	// Session defaults
	Model string `json:"model"`
	Voice string `json:"voice"`
	// VoiceAliases maps a voice name a client asks for to the synthesis
	// provider's own id. A client written for gpt-live-1 sends "marin" or
	// "cedar"; a vendor wants its own identifier and fails the session on
	// anything else. An unlisted name is passed through unchanged, except for
	// the gpt-live-1 built-in names, which fall back to Voice — nobody means
	// for a provider to be handed "marin".
	VoiceAliases map[string]string `json:"voice_aliases"`
	Instructions string            `json:"instructions"`
	// Greeting is spoken as soon as a session opens, before the caller says
	// anything. Empty means the assistant waits to be spoken to.
	Greeting string  `json:"greeting"`
	Language string  `json:"language"`
	Speed    float64 `json:"speed"`
	// ClientRate is the default PCM rate offered to clients that do not name
	// one at session.start.
	ClientRate int `json:"client_rate"`

	// Backend / LLM
	BackendModel string `json:"backend_model"`
	// BackendModels maps the model a session names in
	// delegation.responses.model to one the configured provider actually has.
	// A gpt-live-1 client must send that field and sends an OpenAI model id;
	// passed through, it fails every turn with "model not found". A name that
	// is not listed here falls back to BackendModel, unless
	// AllowBackendModelPassthrough says the client is to be taken at its word.
	BackendModels map[string]string `json:"backend_models"`
	// AllowBackendModelPassthrough sends an unlisted delegation.responses.model
	// to the provider as written. For deployments where clients are your own
	// and name your own models.
	AllowBackendModelPassthrough bool    `json:"allow_backend_model_passthrough"`
	BackendBaseURL               string  `json:"backend_base_url"`
	Temperature                  float64 `json:"temperature"`
	MaxOutputTokens              int     `json:"max_output_tokens"`
	DisableThinking              bool    `json:"disable_thinking"`
	// LLMHedge is how many copies of each completion may be sent to the chat
	// model, the first to produce output being the one used. The model's time
	// to first token is the largest term in a turn's latency and varies
	// several-fold from one identical request to the next; the faster of two
	// is reliably nearer the fast end. The cost is the losing copy's tokens.
	// 0 or 1 sends one.
	LLMHedge int `json:"llm_hedge"`
	// LLMHedgeAfterMS is how long a copy is given to produce its first output
	// before the next is sent. 0 sends them together, which is the most
	// latency this buys; a figure near the model's usual time to first token
	// spends the extra request only on the slow turns.
	LLMHedgeAfterMS int `json:"llm_hedge_after_ms"`
	// TTSHedge, at 2, gives each session a second synthesis connection and
	// asks both for the first segment of every answer, playing whichever
	// produces audio first. The synthesizer's first audio is ~180 ms at the
	// median and over 300 one turn in ten; on a first segment that is silence
	// the caller hears. Later segments are not raced. It costs the first
	// segment's characters twice and one more request per turn against the
	// provider's rate limit. 0 or 1 is one connection.
	TTSHedge int `json:"tts_hedge"`
	// TTSHedgeRPM is how many synthesis requests a minute, across every
	// session of this process, the hedging may add to what would have been
	// made anyway: second requests, and the second connections they go to.
	// Past a vendor's rate limit it refuses the request that was going to be
	// made anyway, and the caller hears nothing, so what hedging adds is kept
	// small. It bounds the additions and not the total: a call makes 15–22
	// requests a minute by itself, and a bound on the total set near that
	// leaves no room for the second request when it is needed. 0 is no limit.
	TTSHedgeRPM int `json:"tts_hedge_rpm"`
	// TTSHedgeAfterMS, above zero, makes the hedging frugal: the second
	// connection is asked only for a segment the first has produced no audio
	// for after this long, so the extra request is spent on the slow
	// syntheses alone — one in ten at 220 ms against a median of 180 — and an
	// account with no room for two requests a turn can still be spared its
	// worst ones (1.3 and 1.8 s on one call). It then covers every segment,
	// later ones with three times the wait. 0 asks both at once for each
	// first segment.
	TTSHedgeAfterMS int `json:"tts_hedge_after_ms"`
	// TTSAccountRPM is how many synthesis requests a minute one account is
	// trusted with, when the synthesizer holds a pool of accounts (several
	// keys). Every request to an account is counted; a second, optional
	// request goes to the other account only while that one has room, and a
	// session whose account is at the figure moves to one that is not. It is
	// unused with a single key. 0 is no limit.
	TTSAccountRPM int `json:"tts_account_rpm"`
	// TTSPoolAfterMS is tts_hedge_after_ms for the first segment of an answer
	// when the second connection is on another account: 0 asks both accounts
	// at once, which a second account's own allowance is what makes affordable.
	TTSPoolAfterMS int `json:"tts_pool_after_ms"`
	// TTSFallback names a second synthesizer, another vendor, asked for a
	// segment the primary has produced no audio for after TTSFallbackAfterMS
	// (three times that for a segment after the first); whichever answers
	// first is heard. It is for the primary vendor stalling, which a second
	// connection to it does not cure. Its voice is that provider's own
	// (FISH_TTS_VOICE_ID for "fish"). Empty is no fallback.
	TTSFallback        string `json:"tts_fallback"`
	TTSFallbackAfterMS int    `json:"tts_fallback_after_ms"`
	HistoryTurns       int    `json:"history_turns"`
	// HistoryMaxChars bounds conversation history by size as well as by turns,
	// because size is what the backend charges for in latency.
	//
	// Measured on one machine, one model, one endpoint: a 79-character system
	// prompt produced a time-to-first-token of 259–546 ms, and a long persona
	// prompt at the same history_turns produced 2441–3385 ms. That is the
	// largest single term in the cascade — bigger than the silence threshold,
	// the recognizer and synthesis put together — and history_turns does not
	// constrain it, since sixteen exchanges can be four hundred characters or
	// four thousand. Zero disables the limit.
	HistoryMaxChars int `json:"history_max_chars"`

	// Duplex engine
	Duplex DuplexConfig `json:"duplex"`

	// VAD
	VAD VADProfile `json:"vad"`
}

// ToolConfig is one tool the assistant may call, as the profile declares it.
type ToolConfig struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	URL         string          `json:"url"`
	Async       bool            `json:"async,omitempty"`
	TimeoutMS   int             `json:"timeout_ms,omitempty"`
	ETASeconds  int             `json:"eta_s,omitempty"`
}

// DuplexConfig tunes the simulated full-duplex behaviour.
type DuplexConfig struct {
	// Backchannel emits short acknowledgements while the user holds the floor.
	Backchannel bool `json:"backchannel"`
	// BackchannelAfterMS is how long a user must keep talking before the first
	// acknowledgement. Too eager and the assistant sounds like it is
	// interrupting; too slow and it sounds absent.
	BackchannelAfterMS int `json:"backchannel_after_ms"`
	// BackchannelEveryMS is the minimum gap between acknowledgements.
	BackchannelEveryMS int `json:"backchannel_every_ms"`
	// BackchannelPhrases are spoken at random.
	BackchannelPhrases []string `json:"backchannel_phrases"`

	// UserBackchannelPhrases are the things the *caller* can say without taking
	// the floor: "嗯", "对", "uh huh". Hearing one of these while the assistant
	// is talking keeps the answer running instead of cutting it off.
	//
	// This is a stand-in for something a real full-duplex model does natively —
	// it hears an acknowledgement in the same stream as everything else and
	// keeps going, having learned that murmured agreement is not a request to
	// stop. golive has no such model, so it is given a list instead. A blunt
	// approximation, and deliberately a small one: every phrase here is a
	// phrase the caller can no longer use to interrupt, so the list should
	// contain noises, not words. Empty disables the behaviour entirely.
	UserBackchannelPhrases []string `json:"user_backchannel_phrases"`
	// UserBackchannelHoldMS is how long the assistant may keep talking while it
	// waits to find out which kind of speech it just heard.
	//
	// The cost of the feature lives in this number. Barge-in is acoustic and
	// immediate; recognizing what was said is neither, so a real interruption
	// is delayed by however long the transcript takes to become decisive — up
	// to this bound, after which the engine yields regardless. Most
	// interruptions resolve far sooner, because the first syllable that no
	// candidate starts with settles it. Talking over someone is worse than
	// stopping for nothing, so the timeout fails toward yielding.
	UserBackchannelHoldMS int `json:"user_backchannel_hold_ms"`

	// HoldingFiller keeps the conversation alive while the backend works.
	//
	// This is the behaviour that makes a delegating voice agent bearable: the
	// conversational layer is supposed to keep talking to you while the slow
	// part runs somewhere else, rather than going silent and leaving you
	// wondering whether the call dropped. It costs one extra synthesis, and it
	// can delay the real answer by however long the filler takes to
	// synthesize, so the threshold below should sit above your normal
	// time-to-first-audio — where it fires only on turns that were going to be
	// slow anyway.
	HoldingFiller bool `json:"holding_filler"`
	// HoldingFillerAfterMS is how long backend work may run silently first.
	HoldingFillerAfterMS int `json:"holding_filler_after_ms"`
	// HoldingFillerPhrases are spoken at random.
	HoldingFillerPhrases []string `json:"holding_filler_phrases"`

	// LegacyAudioField also sends output audio under "audio", the key golive
	// used before it matched gpt-live-1, which carries it in "delta". It costs
	// every chunk twice on the wire, so it is off: turn it on only for a client
	// you cannot update, and turn it back off when you have.
	LegacyAudioField bool `json:"legacy_audio_field"`

	// ClientDelegation decides who talks in client-delegation mode.
	//
	//	always  every caller turn is announced as a delegation and golive says
	//	        nothing of its own. The application answers with
	//	        session.commentary.append. No model call sits in front of the
	//	        application, so this is the fast one.
	//	auto    the configured backend acts as the conversational front, the way
	//	        gpt-live-1's own model does: it answers small talk itself and
	//	        calls a built-in "delegate" function when the request needs the
	//	        application. Closer to the real thing, and one model call slower
	//	        on every delegated turn.
	ClientDelegation string `json:"client_delegation"`

	// Speculative starts generation from a stable partial transcript.
	Speculative bool `json:"speculative"`
	// SpeculativeStableMS is how long the speaker must be audibly quiet, with
	// the partial transcript unchanged throughout, before the backend is
	// started early. It is measured from the first frame of the pause, so the
	// head start it buys is vad.min_silence_ms minus this, plus however long
	// the final transcript takes to arrive. Raise it if the log shows frequent
	// "think: speculation missed"; below about 120 ms the recognizer has not
	// caught up and the guess is a prefix.
	SpeculativeStableMS int `json:"speculative_stable_ms"`
	// SpeculativeMinChars avoids speculating on a one-word fragment.
	//
	// This can sit lower than it first appears, because its original job is now
	// done better elsewhere. It existed to stop the engine generating an answer
	// to "嗯" — but UserBackchannelPhrases catches those by name, and catches
	// them whatever their length. What is left for this setting is genuinely
	// short questions ("多少钱"), which are worth speculating on: a real call
	// showed every delegation arriving as final_transcript, most of them
	// blocked here, while the backend spent 2.1 s on each one.
	SpeculativeMinChars int `json:"speculative_min_chars"`
	// SpeculativeEagerMS replaces SpeculativeStableMS when the words so far
	// read as a finished thought — a question, a closed reply. Those are the
	// turns where waiting buys nothing, and a guess that turns out wrong is
	// abandoned the moment the speaker carries on. 0 disables the shortcut.
	SpeculativeEagerMS int `json:"speculative_eager_ms"`
	// SpeculativeSettleMS is how long the transcript must have been still
	// before a guess is made, when it changed during the pause. A recognizer
	// runs behind the speaker, so the last words arrive a little way into the
	// silence; once they have, nothing more is coming, and this is the short
	// wait that confirms it. Raise it if the log shows speculation being
	// abandoned because "the transcript moved on". 0 uses the whole window,
	// which is the older, more cautious behaviour.
	SpeculativeSettleMS int `json:"speculative_settle_ms"`
	// SemanticEndpointing lets the partial transcript inform how a pause is
	// read, in addition to how long it is. See internal/endpoint. It never
	// shortens the silence a turn is given.
	SemanticEndpointing bool `json:"semantic_endpointing"`
	// PatientSilenceMS replaces vad.min_silence_ms while the words so far end
	// on something nobody stops on ("然后", "我想问一下", "and"). It is the
	// difference between hearing someone think and cutting them off to answer
	// half a sentence. Ignored unless it is longer than vad.min_silence_ms and
	// SemanticEndpointing is on; 0 disables it.
	PatientSilenceMS int `json:"patient_silence_ms"`
	// SpeculateWhilePatient begins the answer during the longer wait
	// PatientSilenceMS gives a caller who seems to be mid-thought, instead of
	// only once that wait has run out. The wait is for the caller; preparing
	// the answer does not need it, and a turn that does end there was being
	// answered a whole backend round later than any other — 1400 ms and more
	// after the caller's last sound. Nothing is heard before the turn is taken,
	// so what it costs is a completion thrown away each time they do carry on.
	// It applies to a guess made on a final asked for early (EarlyFinalMS).
	SpeculateWhilePatient bool `json:"speculate_while_patient"`
	// SettledCloseMS ends a turn when the caller's words have not changed for
	// this long although the line never went quiet enough for the detector to
	// end it. The detector waits for silence, and a line with a room, a car or
	// a carrier's noise on it can go seconds without any: a real call held a
	// finished question open for 3.3 s that way. The recognizer is the better
	// witness there — speech makes words, noise does not. It applies only
	// while less than half of that time was voiced, so a recognizer that has
	// merely fallen behind a talking caller does not end their turn. 0 turns
	// it off.
	SettledCloseMS int `json:"settled_close_ms"`
	// EarlyFinalMS asks the recognizer for its final transcript this far into
	// a pause, instead of when the pause has run its whole length and ended
	// the turn.
	//
	// A streaming recognizer's hypotheses trail the audio — by 300 ms and more
	// on the one measured — so through most of a pause the words so far are
	// not yet all there to guess on, and the answer was being started only
	// once the turn had closed and the final had come back: the whole of
	// vad.min_silence_ms plus a round trip, on every turn. Ending the
	// recognition early gets the final a round trip after the request, and the
	// answer is begun on it while the rest of the pause is still being waited
	// out. If the caller carries on, what they say next is recognized on a
	// fresh connection and joined to what was already settled.
	//
	// It needs duplex.speculative, and should be longer than the stops inside
	// a word (vad.gap_tolerance_ms is the scale). 0 turns it off.
	EarlyFinalMS int `json:"early_final_ms"`

	// ASRPredial opens the recognizer's connection when the detector first
	// suspects speech, rather than when it is sure. The detector takes
	// vad.min_speech_ms to be sure, which is about what the handshake costs, so
	// the two overlap instead of adding up. A connection dialled for a noise is
	// closed unused a few seconds later.
	ASRPredial bool `json:"asr_predial"`
	// TTSNativeRate asks the synthesizer for audio at the session's own rate
	// when it can produce it, instead of at 16 kHz to be resampled. A 24 kHz
	// session then gets 24 kHz speech rather than 16 kHz speech stretched to
	// fit, and a telephony session skips a conversion.
	TTSNativeRate bool `json:"tts_native_rate"`
	// PhraseCache keeps the synthesized audio of the fixed things the assistant
	// says — the greeting, acknowledgements, holding fillers — so they play
	// from memory instead of being synthesized again on every call.
	PhraseCache bool `json:"phrase_cache"`
	// PhraseCacheLearnChars, above zero, also keeps any segment of an ordinary
	// answer that is at most this many characters long, and plays it from
	// memory the next time those words come up in that voice. An assistant
	// opens most of its answers with one of a handful of clauses — "好的，",
	// "是的，", "您说，" — and each is a synthesis the caller waits through in
	// silence: 180 ms when the synthesizer is prompt, and a second or more when
	// it stalls, which no second connection to the same vendor helped with. On
	// two calls one answer in eight opened with words already said on them.
	// Needs phrase_cache. 0 keeps only the fixed phrases.
	PhraseCacheLearnChars int `json:"phrase_cache_learn_chars"`
	// BackendPrefixWarm sends the backend a one-token request carrying the
	// conversation so far when the caller starts speaking, so a provider with a
	// prefix cache has it loaded by the time the real request follows. It
	// spends prompt tokens on every utterance to save time to first token on a
	// long prompt, and whether that trade is worth anything depends entirely on
	// the provider: measure before leaving it on.
	BackendPrefixWarm bool `json:"backend_prefix_warm"`
	// BackendPrefixWarmStart sends that same one-token request when the session
	// opens and again when its opening line is spoken, and at no other time.
	// The first completion of a call was measured at 1100 ms to its first token
	// with every later one near 450 — on a prompt the provider already had
	// cached, so it is the first use that is slow, not the first sight. A phone
	// call spends seconds ringing and then seconds on its greeting before the
	// caller can have said anything; this spends them.
	BackendPrefixWarmStart bool `json:"backend_prefix_warm_start"`
	// IgnoreHesitations leaves a turn that was only a hesitation — "呃", "um" —
	// unanswered. The caller has not said anything yet; an answer to it is an
	// answer to nothing, and on a real call it was cut off 200 ms in by the
	// sentence the hesitation was the start of.
	IgnoreHesitations bool `json:"ignore_hesitations"`

	// Focus keeps the conversation on one voice, with a speaker-activity
	// model run beside the audio (internal/focus). "" is off. "veto" abandons
	// an answer begun on an utterance that was mostly somebody else's — a
	// television, a bystander — before it is heard; the model's labels arrive
	// too late to decide turns, and early enough for that. "observe" runs the
	// model and logs attributions without acting on them.
	Focus string `json:"focus"`
	// FocusURL is the diarization WebSocket (the edge gateway's
	// /v1/audio/diarize?model=nemotron-diar); FocusTokenFile holds its bearer
	// token.
	FocusURL       string `json:"focus_url"`
	FocusTokenFile string `json:"focus_token_file"`
	// FocusVetoShare is the share of an utterance's voiced frames that must be
	// another voice's, with the primary silent, before its answer is vetoed;
	// FocusMinVoicedMS is how much voiced audio the model must have heard in
	// it before any judgment is made.
	FocusVetoShare   float64 `json:"focus_veto_share"`
	FocusMinVoicedMS int     `json:"focus_min_voiced_ms"`
	// FocusBargeInWaitMS is how long a sound over the answer is held for the
	// model's labels before the recognizer's words alone decide it (900). A
	// barge-in whose labelled voice is not the caller's — the assistant's
	// own return, a television, a bystander — does not stop the answer, in
	// veto mode; in observe mode the verdict is only logged. 0 for the
	// default, -1 to decide on the words alone as before.
	FocusBargeInWaitMS int `json:"focus_barge_in_wait_ms"`

	// AllowBargeIn lets user speech during playback cut the assistant off.
	AllowBargeIn bool `json:"allow_barge_in"`
	// ResetTTSOnInterrupt closes the synthesis connection whenever an answer is
	// cut short, so the next turn starts on a fresh one.
	//
	// Belt and braces. A correct adapter resynchronizes its own stream after an
	// abandoned call, and the bundled ones do; this exists for providers that
	// cannot, where the alternative is the tail of the interrupted sentence
	// playing at the start of the next answer. It costs a reconnect on the turn
	// after every barge-in, so leave it off unless you hear that leak.
	ResetTTSOnInterrupt bool `json:"reset_tts_on_interrupt"`

	// OnNewQuery decides what happens to an answer still in flight when the
	// user starts speaking again. The right choice is situational, which is
	// why it is a setting rather than a constant:
	//
	//	cut             stop immediately, mid-word. Best for a fast assistant
	//	                where the user expects to be able to redirect it.
	//	finish_sentence let the sentence being spoken complete, then stop and
	//	                answer the new query. Sounds composed, and is usually
	//	                right on a phone line where cutting mid-word reads as a
	//	                dropped call.
	//	queue           say everything, then answer the new query. Almost never
	//	                what a caller wants — it talks over them and answers a
	//	                question they have moved past — but it is what a
	//	                half-duplex cascade does, so it is available for
	//	                comparison.
	OnNewQuery string `json:"on_new_query"`

	// PlaybackChunkMS is the size of each session.output_audio.delta.
	PlaybackChunkMS int `json:"playback_chunk_ms"`
	// PlaybackPaced sends audio in real time rather than as fast as the socket
	// accepts it. Paced output is what makes truncation accounting meaningful:
	// if the whole answer is already in the client's buffer, "how much did the
	// user hear" has no answer the server can compute.
	PlaybackPaced bool `json:"playback_paced"`
	// PlaybackLeadMS is how far ahead of the playhead the server is allowed to
	// run, absorbing network jitter without losing truncation accuracy.
	PlaybackLeadMS int `json:"playback_lead_ms"`

	// StreamFirstChunkChars is how few characters may form the first TTS
	// segment. A short first segment buys a dramatically earlier first
	// syllable; later segments should be longer for better prosody.
	StreamFirstChunkChars int `json:"stream_first_chunk_chars"`
	// StreamFirstChunkDeadlineMS bounds how long the first segment waits for a
	// sentence boundary before being spoken anyway. Punctuation arrives when
	// the model chooses; the caller's patience does not, and this is silence
	// they are listening to. 0 disables the deadline.
	StreamFirstChunkDeadlineMS int `json:"stream_first_chunk_deadline_ms"`
	// StreamFirstChunkGraceMS is how long the first segment goes on waiting once
	// it has reached a comma or full stop that came too early to count — "好的，"
	// against a six-character minimum. A longer clause may complete in that
	// time and make a better opening; if it does not, the short one is spoken,
	// cut cleanly at its punctuation, rather than waiting out the whole
	// deadline to be cut wherever the text happens to have got to. 0 disables
	// it and the deadline alone applies.
	StreamFirstChunkGraceMS int `json:"stream_first_chunk_grace_ms"`
	// StreamMinChunkChars is the floor for subsequent segments.
	StreamMinChunkChars int `json:"stream_min_chunk_chars"`
	// StreamMaxChunkChars force-flushes a segment that never hits punctuation.
	StreamMaxChunkChars int `json:"stream_max_chunk_chars"`

	// DelegationMode is "client", "responses" or "auto".
	DelegationMode string `json:"delegation_mode"`
	// DelegateMinChars is the shortest user turn worth sending to the backend.
	DelegateMinChars int `json:"delegate_min_chars"`

	// MaxAppendChars bounds one session.instructions/thinking/commentary
	// append. gpt-live-1 documents a 500-token cap on instruction appends;
	// without a tokenizer this is the character-count approximation of it, and
	// it is generous enough that ordinary use never meets it.
	MaxAppendChars int `json:"max_append_chars"`

	// SessionMaxSeconds force-closes a session; 0 disables.
	SessionMaxSeconds int `json:"session_max_seconds"`
	// IdleTimeoutSeconds closes a session with no audio at all; 0 disables.
	IdleTimeoutSeconds int `json:"idle_timeout_seconds"`
	// MaxEventsPerSecond caps non-audio client events. Audio frames are
	// expected at 50/s and are bounded by MaxAudioKbps instead. 0 is unlimited.
	MaxEventsPerSecond int `json:"max_events_per_second"`
	// MaxAudioKbps caps inbound PCM. 24 kHz PCM16 is 384 kbps; a value around
	// 512 leaves headroom for jitter without allowing a flood. 0 is unlimited.
	MaxAudioKbps int `json:"max_audio_kbps"`
}

// VADProfile is the JSON-facing form of the detector's tuning.
type VADProfile struct {
	FrameMS      int `json:"frame_ms"`
	MinSpeechMS  int `json:"min_speech_ms"`
	MinSilenceMS int `json:"min_silence_ms"`
	SpeechPadMS  int `json:"speech_pad_ms"`
	MaxSpeechMS  int `json:"max_speech_ms"`
	// SnapshotMS is how often an open utterance's audio is handed to the
	// recognizer. It is latency, not bookkeeping: audio waiting for the next
	// snapshot is audio the recognizer has not heard, so every hypothesis — and
	// the final one that the reply waits on — runs behind the speaker by up to
	// this much. 40 ms is what realtime recognizers ask for.
	SnapshotMS         int     `json:"snapshot_ms"`
	NoiseFloorDB       float64 `json:"noise_floor_db"`
	MaxNoiseFloorDB    float64 `json:"max_noise_floor_db"`
	SpeechMarginDB     float64 `json:"speech_margin_db"`
	MaxZCR             float64 `json:"max_zcr"`
	GapToleranceMS     int     `json:"gap_tolerance_ms"`
	BargeInMarginDB    float64 `json:"barge_in_margin_db"`
	BargeInMinSpeechMS int     `json:"barge_in_min_speech_ms"`
	// EchoTailMS keeps the barge-in bar raised for this long after the
	// assistant stops sending audio: on a phone line the caller is still
	// hearing it, and its echo is still coming back. 0 for a line with none.
	EchoTailMS int `json:"echo_tail_ms"`
	// FloorTrackMS raises the noise floor to the quietest frame of the last
	// this-many ms of an open utterance when none of it dipped under the
	// bar: a line whose noise sits above the bar (a subway, a street) can
	// otherwise hold an utterance open until max_speech_ms. 0 for the
	// default, -1 to turn it off.
	FloorTrackMS int `json:"floor_track_ms"`
	// GateTraceMS is how often the gate's state is sampled for the stored
	// session's record (the journal draws it). 0 for the default, -1 for
	// none.
	GateTraceMS int `json:"gate_trace_ms"`
}

// Default returns a configuration that runs end to end with the mock providers
// and no credentials at all.
// RealtimeConfig is the OpenAI-Realtime-compatible endpoint.
type RealtimeConfig struct {
	// Enabled serves /v1/realtime, /v1/opening and /v1/pool.
	Enabled bool `json:"enabled"`
	// SampleRate is the rate of "pcm16" audio on this endpoint, in both
	// directions, for a client that does not say. OpenAI's own is 24000. A
	// media gateway usually has one fixed internal rate and no way to state it
	// — Active Call's is 16000 — so this is where it is told.
	SampleRate int `json:"sample_rate"`
	// Dialect picks the event names for assistant audio: "beta"
	// (response.audio.delta) or "ga" (response.output_audio.delta). OpenAI
	// renamed them, and older clients match only the old names.
	Dialect string `json:"dialect"`
	// HoldForOpening keeps a new session deaf and silent until POST
	// /v1/opening releases it. An outbound phone call connects its agent before
	// anyone has answered, and what arrives in the meantime is ringback.
	HoldForOpening bool `json:"hold_for_opening"`
	// HoldTimeoutSeconds releases a held session that nobody released. A call
	// that has not been answered by then is not going to be.
	HoldTimeoutSeconds int `json:"hold_timeout_seconds"`
	// HonorTurnDetection takes the end-of-turn silence from the client's
	// turn_detection.silence_duration_ms instead of vad.min_silence_ms. That
	// is what the field means in the protocol, and a client that sets it —
	// a call originator tuning for its line — expects to be obeyed.
	HonorTurnDetection bool `json:"honor_turn_detection"`
	// MinSilenceFloorMS is the least silence a client can ask for. The
	// detector here judges frames by energy, and at frame scale the stops and
	// unvoiced consonants inside a word are silence; a model-based detector
	// can be asked for 150 ms, this one cannot yet be trusted with it.
	MinSilenceFloorMS int `json:"min_silence_floor_ms"`
	// StoreCalls keeps every call on this endpoint as a stored session — the
	// caller and the assistant as two tracks on one timeline, under store_dir
	// for store_ttl_hours — as store:true does for a gpt-live-1 session. A
	// Realtime client has no way to ask, and a phone gateway would not know
	// to. It is how a call is measured from the audio afterwards (echoprobe).
	// Off by default: it is somebody's voice on disk.
	StoreCalls bool `json:"store_calls"`
}

func Default() Config {
	return Config{
		Addr:                   ":8080",
		LogLevel:               "info",
		LogFormat:              "text",
		WebRoot:                "web",
		AllowProviderOverride:  true,
		EnableMetrics:          true,
		AuthFailBurst:          30,
		AuthFailWindowSeconds:  60,
		ShutdownTimeoutSeconds: 10,
		StoreTTLHours:          24,
		Realtime: RealtimeConfig{
			Enabled:            true,
			SampleRate:         24000,
			Dialect:            "beta",
			HoldTimeoutSeconds: 45,
			HonorTurnDetection: true,
			MinSilenceFloorMS:  200,
		},
		ASR:        "mock",
		LLM:        "mock",
		TTS:        "mock",
		Model:      "golive-1",
		Voice:      "",
		Language:   "zh",
		Speed:      1.0,
		ClientRate: 24000,
		Instructions: "You are a calm, friendly voice assistant. Speak warmly and " +
			"naturally, at an unhurried pace. Keep routine answers to one or two " +
			"short sentences. Never read out markup, lists or code.",
		BackendModel:    "deepseek-chat",
		BackendBaseURL:  "https://api.deepseek.com",
		Temperature:     0.7,
		MaxOutputTokens: 512,
		DisableThinking: true,
		HistoryTurns:    16,
		HistoryMaxChars: 2000,
		Duplex: DuplexConfig{
			// Off by default, along with the holding filler and the floor
			// hold below.
			//
			// All three were built to make a delegating agent feel less like a
			// half-duplex cascade, and all three turned out to cost more in
			// practice than they returned: an acknowledgement lands on top of
			// the caller, a filler is heard as the agent having nothing to say,
			// and the hold delays a real interruption by however long the
			// recognizer takes to disagree. The phrase lists stay populated so
			// switching any of them back on is one boolean, not a retyped list.
			Backchannel:        false,
			BackchannelAfterMS: 2600,
			BackchannelEveryMS: 4200,
			BackchannelPhrases: []string{"嗯", "好的", "我在听"},
			// Empty by default, so any word the recognizer does hear takes
			// the floor at once. Populating it is what makes "对" stop
			// counting as an interruption; that is the part of this machinery
			// that decides on the caller's behalf, and it stays opt-in.
			//
			// Suggested contents, since they are tedious to retype:
			//   嗯 嗯嗯 哦 噢 啊 对 对对 是 是的 好 好的 行 懂了 知道了
			//   ok okay mm mhm uh huh yeah yep right i see
			UserBackchannelPhrases: nil,
			// The hold itself stays on, and it is not the same feature.
			//
			// It arrived with the phrase list, but what it actually does is
			// wait for the transcript before yielding the floor — and the case
			// that matters has no phrase list in it at all. A cough, a door, or
			// the assistant's own voice through a speakerphone opens the VAD,
			// the recognizer finds no words, and without the hold the greeting
			// is cut mid-name. TestNoiseDoesNotCostTheAssistantItsTurn cuts it
			// at 您好，我是众安保险的车险管家小翠 with this at 0.
			//
			// The cost is paid only while the transcript is ambiguous: a real
			// interruption releases it on the first word that no candidate
			// starts with, which with an empty list is the first word.
			UserBackchannelHoldMS:      600,
			HoldingFiller:              false,
			HoldingFillerAfterMS:       1500,
			HoldingFillerPhrases:       []string{"我看一下", "稍等一下", "让我查一下"},
			ClientDelegation:           "always",
			Speculative:                true,
			SpeculativeStableMS:        180,
			SpeculativeMinChars:        4,
			SpeculativeEagerMS:         100,
			SpeculativeSettleMS:        80,
			SemanticEndpointing:        true,
			PatientSilenceMS:           700,
			SettledCloseMS:             600,
			ASRPredial:                 true,
			TTSNativeRate:              true,
			PhraseCache:                true,
			AllowBargeIn:               true,
			OnNewQuery:                 "cut",
			ResetTTSOnInterrupt:        false,
			PlaybackChunkMS:            40,
			PlaybackPaced:              true,
			PlaybackLeadMS:             300,
			StreamFirstChunkChars:      6,
			StreamFirstChunkDeadlineMS: 220,
			StreamFirstChunkGraceMS:    60,
			StreamMinChunkChars:        24,
			StreamMaxChunkChars:        120,
			DelegationMode:             "auto",
			DelegateMinChars:           1,
			MaxAppendChars:             1500,
			SessionMaxSeconds:          3600,
			IdleTimeoutSeconds:         300,
		},
		VAD: VADProfile{
			FrameMS:            20,
			MinSpeechMS:        200,
			MinSilenceMS:       380,
			SpeechPadMS:        240,
			MaxSpeechMS:        30000,
			SnapshotMS:         40,
			NoiseFloorDB:       -60,
			MaxNoiseFloorDB:    -28,
			SpeechMarginDB:     10,
			MaxZCR:             0.55,
			GapToleranceMS:     160,
			BargeInMarginDB:    6,
			BargeInMinSpeechMS: 320,
		},
	}
}

// Load builds a configuration from an optional JSON profile plus the
// environment. A profile path of "" uses the defaults.
func Load(profilePath string) (Config, error) {
	cfg := Default()
	if profilePath != "" {
		data, err := os.ReadFile(profilePath)
		if err != nil {
			return cfg, fmt.Errorf("config: reading profile %s: %w", profilePath, err)
		}
		// Decoding onto the populated struct leaves absent keys at their
		// default, so a profile only has to state what it changes.
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("config: parsing profile %s: %w", profilePath, err)
		}
	}
	cfg.applyEnv()
	return cfg, cfg.Validate()
}

// applyEnv lets a small set of GOLIVE_* variables override the profile. Vendor
// credentials are read by the adapters themselves, not here, so this package
// never holds a secret.
func (c *Config) applyEnv() {
	setString(&c.Addr, "GOLIVE_ADDR")
	setString(&c.LogLevel, "GOLIVE_LOG_LEVEL")
	setString(&c.LogFormat, "GOLIVE_LOG_FORMAT")
	setString(&c.WebRoot, "GOLIVE_WEB_ROOT")
	setString(&c.AuthToken, "GOLIVE_AUTH_TOKEN")
	setBool(&c.AuthRequired, "GOLIVE_AUTH_REQUIRED")
	setCSV(&c.AllowedOrigins, "GOLIVE_ALLOWED_ORIGINS")
	setBool(&c.TrustProxy, "GOLIVE_TRUST_PROXY")
	setInt(&c.MaxSessions, "GOLIVE_MAX_SESSIONS")
	setInt(&c.MaxSessionsPerIP, "GOLIVE_MAX_SESSIONS_PER_IP")
	setBool(&c.AllowProviderOverride, "GOLIVE_ALLOW_PROVIDER_OVERRIDE")
	setBool(&c.EnableMetrics, "GOLIVE_ENABLE_METRICS")
	setBool(&c.EnablePprof, "GOLIVE_PPROF")
	setBool(&c.MetricsPublic, "GOLIVE_METRICS_PUBLIC")
	setString(&c.MetricsToken, "GOLIVE_METRICS_TOKEN")
	setInt(&c.AuthFailBurst, "GOLIVE_AUTH_FAIL_BURST")
	setInt(&c.AuthFailWindowSeconds, "GOLIVE_AUTH_FAIL_WINDOW_SECONDS")
	setString(&c.TLSCertFile, "GOLIVE_TLS_CERT_FILE")
	setString(&c.TLSKeyFile, "GOLIVE_TLS_KEY_FILE")
	setInt(&c.ShutdownTimeoutSeconds, "GOLIVE_SHUTDOWN_TIMEOUT_SECONDS")
	setString(&c.ASR, "GOLIVE_ASR")
	setString(&c.LLM, "GOLIVE_LLM")
	setString(&c.TTS, "GOLIVE_TTS")
	setString(&c.Model, "GOLIVE_MODEL")
	setString(&c.Voice, "GOLIVE_VOICE")
	setString(&c.Language, "GOLIVE_LANGUAGE")
	setString(&c.Instructions, "GOLIVE_INSTRUCTIONS")
	setString(&c.Greeting, "GOLIVE_GREETING")
	setString(&c.BackendModel, "GOLIVE_BACKEND_MODEL")
	setInt(&c.LLMHedge, "GOLIVE_LLM_HEDGE")
	setInt(&c.LLMHedgeAfterMS, "GOLIVE_LLM_HEDGE_AFTER_MS")
	setInt(&c.TTSHedge, "GOLIVE_TTS_HEDGE")
	setInt(&c.TTSHedgeRPM, "GOLIVE_TTS_HEDGE_RPM")
	setInt(&c.TTSHedgeAfterMS, "GOLIVE_TTS_HEDGE_AFTER_MS")
	setInt(&c.TTSAccountRPM, "GOLIVE_TTS_ACCOUNT_RPM")
	setInt(&c.TTSPoolAfterMS, "GOLIVE_TTS_POOL_AFTER_MS")
	setString(&c.TTSFallback, "GOLIVE_TTS_FALLBACK")
	setInt(&c.TTSFallbackAfterMS, "GOLIVE_TTS_FALLBACK_AFTER_MS")
	setString(&c.BackendBaseURL, "GOLIVE_BACKEND_BASE_URL")
	setString(&c.Duplex.DelegationMode, "GOLIVE_DELEGATION_MODE")
	setString(&c.Duplex.ClientDelegation, "GOLIVE_CLIENT_DELEGATION")
	setString(&c.StoreDir, "GOLIVE_STORE_DIR")
	setString(&c.LastCallFile, "GOLIVE_LAST_CALL_FILE")
	setInt(&c.StoreTTLHours, "GOLIVE_STORE_TTL_HOURS")
	setBool(&c.Realtime.Enabled, "GOLIVE_REALTIME_ENABLED")
	setInt(&c.Realtime.SampleRate, "GOLIVE_REALTIME_RATE")
	setString(&c.Realtime.Dialect, "GOLIVE_REALTIME_DIALECT")
	setBool(&c.Realtime.HoldForOpening, "GOLIVE_REALTIME_HOLD")
	setBool(&c.Realtime.HonorTurnDetection, "GOLIVE_REALTIME_HONOR_TURN_DETECTION")
	setBool(&c.Realtime.StoreCalls, "GOLIVE_REALTIME_STORE_CALLS")
	setInt(&c.Realtime.MinSilenceFloorMS, "GOLIVE_REALTIME_MIN_SILENCE_FLOOR_MS")
	setString(&c.TelemetryURL, "GOLIVE_TELEMETRY_URL")
	setString(&c.TelemetryFormat, "GOLIVE_TELEMETRY_FORMAT")
	setBool(&c.Duplex.LegacyAudioField, "GOLIVE_LEGACY_AUDIO_FIELD")
	setString(&c.ToolsTokenFile, "GOLIVE_TOOLS_TOKEN_FILE")
	setString(&c.ToolsBaseURL, "GOLIVE_TOOLS_BASE_URL")
	setString(&c.Duplex.Focus, "GOLIVE_FOCUS")
	setString(&c.Duplex.FocusURL, "GOLIVE_FOCUS_URL")
	setString(&c.Duplex.FocusTokenFile, "GOLIVE_FOCUS_TOKEN_FILE")
	setBool(&c.Duplex.SemanticEndpointing, "GOLIVE_SEMANTIC_ENDPOINTING")
	setBool(&c.Duplex.BackendPrefixWarm, "GOLIVE_BACKEND_PREFIX_WARM")
	setInt(&c.ClientRate, "GOLIVE_CLIENT_RATE")
	setInt(&c.MaxOutputTokens, "GOLIVE_MAX_OUTPUT_TOKENS")
	setFloat(&c.Temperature, "GOLIVE_TEMPERATURE")
	setFloat(&c.Speed, "GOLIVE_SPEED")
	setBool(&c.Duplex.Backchannel, "GOLIVE_BACKCHANNEL")
	setBool(&c.Duplex.Speculative, "GOLIVE_SPECULATIVE")
	setBool(&c.Duplex.AllowBargeIn, "GOLIVE_ALLOW_BARGE_IN")
	setString(&c.Duplex.OnNewQuery, "GOLIVE_ON_NEW_QUERY")
	setBool(&c.Duplex.ResetTTSOnInterrupt, "GOLIVE_RESET_TTS_ON_INTERRUPT")
	setBool(&c.Duplex.PlaybackPaced, "GOLIVE_PLAYBACK_PACED")
	setInt(&c.Duplex.MaxEventsPerSecond, "GOLIVE_MAX_EVENTS_PER_SECOND")
	setInt(&c.Duplex.MaxAudioKbps, "GOLIVE_MAX_AUDIO_KBPS")
	setInt(&c.Duplex.IdleTimeoutSeconds, "GOLIVE_IDLE_TIMEOUT_SECONDS")
}

// Validate rejects combinations that would fail confusingly later.
func (c *Config) Validate() error {
	switch c.LogFormat {
	case "", "text", "json":
		if c.LogFormat == "" {
			c.LogFormat = "text"
		}
	default:
		return fmt.Errorf("config: log_format %q must be text or json", c.LogFormat)
	}
	if c.AuthRequired && c.AuthToken == "" {
		return fmt.Errorf("config: auth_required is set but GOLIVE_AUTH_TOKEN is empty")
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return fmt.Errorf("config: tls_cert_file and tls_key_file must be set together")
	}
	if c.ShutdownTimeoutSeconds < 0 {
		return fmt.Errorf("config: shutdown_timeout_seconds must be >= 0")
	}
	if c.MaxSessions < 0 || c.MaxSessionsPerIP < 0 {
		return fmt.Errorf("config: session caps must be >= 0")
	}
	if c.AuthFailBurst < 0 || c.AuthFailWindowSeconds < 0 {
		return fmt.Errorf("config: auth fail throttle must be >= 0")
	}
	if c.Duplex.MaxEventsPerSecond < 0 || c.Duplex.MaxAudioKbps < 0 {
		return fmt.Errorf("config: rate caps must be >= 0")
	}
	switch c.ClientRate {
	case 8000, 16000, 24000, 48000:
	default:
		return fmt.Errorf("config: client_rate %d is not one of 8000, 16000, 24000, 48000", c.ClientRate)
	}
	switch c.Duplex.OnNewQuery {
	case "cut", "finish_sentence", "queue":
	default:
		return fmt.Errorf("config: on_new_query %q must be cut, finish_sentence or queue",
			c.Duplex.OnNewQuery)
	}
	switch c.Duplex.DelegationMode {
	case "client", "responses", "auto":
	default:
		return fmt.Errorf("config: delegation_mode %q must be client, responses or auto", c.Duplex.DelegationMode)
	}
	switch c.Duplex.ClientDelegation {
	case "":
		c.Duplex.ClientDelegation = "always"
	case "always", "auto":
	default:
		return fmt.Errorf("config: client_delegation %q must be always or auto", c.Duplex.ClientDelegation)
	}
	if c.VAD.SnapshotMS > 0 && c.VAD.FrameMS > 0 && c.VAD.SnapshotMS%c.VAD.FrameMS != 0 {
		return fmt.Errorf("config: vad.snapshot_ms (%d) must be a multiple of vad.frame_ms (%d)",
			c.VAD.SnapshotMS, c.VAD.FrameMS)
	}
	switch c.Realtime.SampleRate {
	case 0:
		c.Realtime.SampleRate = 24000
	case 8000, 16000, 24000, 48000:
	default:
		return fmt.Errorf("config: realtime.sample_rate %d is not one of 8000, 16000, 24000, 48000", c.Realtime.SampleRate)
	}
	switch c.Realtime.Dialect {
	case "":
		c.Realtime.Dialect = "beta"
	case "beta", "ga":
	default:
		return fmt.Errorf("config: realtime.dialect %q must be beta or ga", c.Realtime.Dialect)
	}
	switch c.TelemetryFormat {
	case "":
		c.TelemetryFormat = "v1"
	case "v1", "legacy":
	default:
		return fmt.Errorf("config: telemetry_format %q must be v1 or legacy", c.TelemetryFormat)
	}
	if c.Realtime.HoldTimeoutSeconds <= 0 {
		c.Realtime.HoldTimeoutSeconds = 45
	}
	if c.Realtime.MinSilenceFloorMS <= 0 {
		c.Realtime.MinSilenceFloorMS = 200
	}
	if c.StoreTTLHours < 0 {
		return fmt.Errorf("config: store_ttl_hours must be >= 0")
	}
	if c.Duplex.PlaybackChunkMS <= 0 {
		return fmt.Errorf("config: playback_chunk_ms must be positive")
	}
	if c.VAD.FrameMS <= 0 || 1000%c.VAD.FrameMS != 0 {
		return fmt.Errorf("config: vad.frame_ms must divide 1000 evenly, got %d", c.VAD.FrameMS)
	}
	if c.Duplex.StreamFirstChunkChars > c.Duplex.StreamMaxChunkChars {
		return fmt.Errorf("config: stream_first_chunk_chars exceeds stream_max_chunk_chars")
	}
	return nil
}

// LoadDotEnv reads KEY=VALUE lines into the process environment without
// overwriting variables that are already set, so a real environment always wins
// over a checked-in file. A missing file is not an error.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("config: %s:%d is not KEY=VALUE", path, lineNo)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// Env reads a variable, falling back to def.
func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// EnvInt reads an integer variable, falling back to def.
func EnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// EnvFloat reads a float variable, falling back to def.
func EnvFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// EnvBool reads a boolean variable, falling back to def.
func EnvBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// EnvRequired reads a variable that has no sensible default.
func EnvRequired(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("config: %s is required but not set", key)
	}
	return v, nil
}

func setString(dst *string, key string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

func setInt(dst *int, key string) {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
}

func setFloat(dst *float64, key string) {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			*dst = f
		}
	}
}

func setBool(dst *bool, key string) {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			*dst = b
		}
	}
}

func setCSV(dst *[]string, key string) {
	if v := os.Getenv(key); v != "" {
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		*dst = out
	}
}
