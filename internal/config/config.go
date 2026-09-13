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
	// WebRoot serves the demo client. Empty disables it.
	WebRoot string `json:"web_root"`

	// Providers, by registry name.
	ASR string `json:"asr"`
	LLM string `json:"llm"`
	TTS string `json:"tts"`

	// Session defaults
	Model        string `json:"model"`
	Voice        string `json:"voice"`
	Instructions string `json:"instructions"`
	// Greeting is spoken as soon as a session opens, before the caller says
	// anything. Empty means the assistant waits to be spoken to.
	Greeting string  `json:"greeting"`
	Language string  `json:"language"`
	Speed    float64 `json:"speed"`
	// ClientRate is the default PCM rate offered to clients that do not name
	// one at session.start.
	ClientRate int `json:"client_rate"`

	// Backend / LLM
	BackendModel    string  `json:"backend_model"`
	BackendBaseURL  string  `json:"backend_base_url"`
	Temperature     float64 `json:"temperature"`
	MaxOutputTokens int     `json:"max_output_tokens"`
	DisableThinking bool    `json:"disable_thinking"`
	HistoryTurns    int     `json:"history_turns"`

	// Duplex engine
	Duplex DuplexConfig `json:"duplex"`

	// VAD
	VAD VADProfile `json:"vad"`
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

	// Speculative starts generation from a stable partial transcript.
	Speculative bool `json:"speculative"`
	// SpeculativeStableMS is how long a partial transcript must stop changing
	// before it is trusted enough to speculate on.
	SpeculativeStableMS int `json:"speculative_stable_ms"`
	// SpeculativeMinChars avoids speculating on a one-word fragment.
	SpeculativeMinChars int `json:"speculative_min_chars"`

	// AllowBargeIn lets user speech during playback cut the assistant off.
	AllowBargeIn bool `json:"allow_barge_in"`
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
	// StreamMinChunkChars is the floor for subsequent segments.
	StreamMinChunkChars int `json:"stream_min_chunk_chars"`
	// StreamMaxChunkChars force-flushes a segment that never hits punctuation.
	StreamMaxChunkChars int `json:"stream_max_chunk_chars"`

	// DelegationMode is "client", "responses" or "auto".
	DelegationMode string `json:"delegation_mode"`
	// DelegateMinChars is the shortest user turn worth sending to the backend.
	DelegateMinChars int `json:"delegate_min_chars"`

	// SessionMaxSeconds force-closes a session; 0 disables.
	SessionMaxSeconds int `json:"session_max_seconds"`
	// IdleTimeoutSeconds closes a session with no audio at all; 0 disables.
	IdleTimeoutSeconds int `json:"idle_timeout_seconds"`
}

// VADProfile is the JSON-facing form of the detector's tuning.
type VADProfile struct {
	FrameMS            int     `json:"frame_ms"`
	MinSpeechMS        int     `json:"min_speech_ms"`
	MinSilenceMS       int     `json:"min_silence_ms"`
	SpeechPadMS        int     `json:"speech_pad_ms"`
	MaxSpeechMS        int     `json:"max_speech_ms"`
	SnapshotMS         int     `json:"snapshot_ms"`
	NoiseFloorDB       float64 `json:"noise_floor_db"`
	MaxNoiseFloorDB    float64 `json:"max_noise_floor_db"`
	SpeechMarginDB     float64 `json:"speech_margin_db"`
	MaxZCR             float64 `json:"max_zcr"`
	GapToleranceMS     int     `json:"gap_tolerance_ms"`
	BargeInMarginDB    float64 `json:"barge_in_margin_db"`
	BargeInMinSpeechMS int     `json:"barge_in_min_speech_ms"`
}

// Default returns a configuration that runs end to end with the mock providers
// and no credentials at all.
func Default() Config {
	return Config{
		Addr:       ":8080",
		LogLevel:   "info",
		WebRoot:    "web",
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
		Duplex: DuplexConfig{
			Backchannel:           true,
			BackchannelAfterMS:    2600,
			BackchannelEveryMS:    4200,
			BackchannelPhrases:    []string{"嗯", "好的", "我在听"},
			Speculative:           true,
			SpeculativeStableMS:   260,
			SpeculativeMinChars:   6,
			AllowBargeIn:          true,
			OnNewQuery:            "cut",
			PlaybackChunkMS:       40,
			PlaybackPaced:         true,
			PlaybackLeadMS:        300,
			StreamFirstChunkChars: 8,
			StreamMinChunkChars:   24,
			StreamMaxChunkChars:   120,
			DelegationMode:        "auto",
			DelegateMinChars:      1,
			SessionMaxSeconds:     3600,
			IdleTimeoutSeconds:    300,
		},
		VAD: VADProfile{
			FrameMS:            20,
			MinSpeechMS:        200,
			MinSilenceMS:       380,
			SpeechPadMS:        240,
			MaxSpeechMS:        30000,
			SnapshotMS:         200,
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
	setString(&c.WebRoot, "GOLIVE_WEB_ROOT")
	setString(&c.ASR, "GOLIVE_ASR")
	setString(&c.LLM, "GOLIVE_LLM")
	setString(&c.TTS, "GOLIVE_TTS")
	setString(&c.Model, "GOLIVE_MODEL")
	setString(&c.Voice, "GOLIVE_VOICE")
	setString(&c.Language, "GOLIVE_LANGUAGE")
	setString(&c.Instructions, "GOLIVE_INSTRUCTIONS")
	setString(&c.Greeting, "GOLIVE_GREETING")
	setString(&c.BackendModel, "GOLIVE_BACKEND_MODEL")
	setString(&c.BackendBaseURL, "GOLIVE_BACKEND_BASE_URL")
	setString(&c.Duplex.DelegationMode, "GOLIVE_DELEGATION_MODE")
	setInt(&c.ClientRate, "GOLIVE_CLIENT_RATE")
	setInt(&c.MaxOutputTokens, "GOLIVE_MAX_OUTPUT_TOKENS")
	setFloat(&c.Temperature, "GOLIVE_TEMPERATURE")
	setFloat(&c.Speed, "GOLIVE_SPEED")
	setBool(&c.Duplex.Backchannel, "GOLIVE_BACKCHANNEL")
	setBool(&c.Duplex.Speculative, "GOLIVE_SPECULATIVE")
	setBool(&c.Duplex.AllowBargeIn, "GOLIVE_ALLOW_BARGE_IN")
	setString(&c.Duplex.OnNewQuery, "GOLIVE_ON_NEW_QUERY")
	setBool(&c.Duplex.PlaybackPaced, "GOLIVE_PLAYBACK_PACED")
}

// Validate rejects combinations that would fail confusingly later.
func (c *Config) Validate() error {
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
