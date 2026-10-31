// Package speko connects native GPT-Live clients to the Speko voice router.
// It does not run GoLive's local ASR/LLM/TTS engine.
package speko

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

const DefaultURL = "wss://router.speko.dev/v1/live"
const Model = "gpt-live-1"

// Config contains only server-side settings. Credentials are never serialized.
type Config struct {
	URL                string
	APIKey             string `json:"-"`
	AuthToken          string `json:"-"`
	AllowedOrigins     []string
	MaxSessions        int
	SessionTimeout     time.Duration
	SetupTimeout       time.Duration
	WriteTimeout       time.Duration
	MaxMessageBytes    int64
	DelegationMode     string
	ResponsesModel     string
	ResponsesReasoning string
	HoldTimeout        time.Duration
	Preconnect         bool
	LocalBargeIn       bool
	AutoHangup         bool
}

// FromEnv deliberately permits absent keys: health works while readiness and
// session admission stay closed until credentials are supplied and restarted.
func FromEnv() Config {
	return Config{
		URL:                os.Getenv("SPEKO_LIVE_URL"),
		Preconnect:         os.Getenv("SPEKO_PRECONNECT") == "1",
		LocalBargeIn:       os.Getenv("SPEKO_LOCAL_BARGE_IN") == "1",
		AutoHangup:         os.Getenv("SPEKO_AUTO_HANGUP") == "1",
		APIKey:             os.Getenv("SPEKO_API_KEY"),
		AuthToken:          os.Getenv("GOLIVE_AUTH_TOKEN"),
		ResponsesModel:     os.Getenv("SPEKO_RESPONSES_MODEL"),
		ResponsesReasoning: os.Getenv("SPEKO_RESPONSES_REASONING"),
		DelegationMode:     os.Getenv("SPEKO_DELEGATION_MODE"),
	}
}

func (c *Config) normalize() error {
	if c.URL == "" {
		c.URL = DefaultURL
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "/v1/live" || u.RawPath != "" {
		return fmt.Errorf("speko: URL must be a WebSocket origin followed by /v1/live, without credentials, query or fragment")
	}
	if u.Scheme != "wss" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "ws" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return fmt.Errorf("speko: URL requires wss (ws is allowed only on loopback for tests)")
		}
	}
	for _, credential := range []string{c.APIKey, c.AuthToken} {
		if credential != strings.TrimSpace(credential) || strings.ContainsAny(credential, "\r\n\t") {
			return fmt.Errorf("speko: credentials must not contain surrounding whitespace or control separators")
		}
	}
	if c.MaxSessions < 0 || c.SessionTimeout < 0 || c.SetupTimeout < 0 || c.WriteTimeout < 0 || c.MaxMessageBytes < 0 || c.HoldTimeout < 0 {
		return fmt.Errorf("speko: limits must be positive or zero for defaults")
	}
	if c.MaxSessions == 0 {
		c.MaxSessions = 32
	}
	if c.SessionTimeout == 0 {
		c.SessionTimeout = time.Hour
	}
	if c.SetupTimeout == 0 {
		c.SetupTimeout = 10 * time.Second
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = 10 * time.Second
	}
	if c.MaxMessageBytes == 0 {
		c.MaxMessageBytes = 1 << 20
	}
	if c.DelegationMode == "" {
		c.DelegationMode = "responses"
	}
	if c.DelegationMode != "responses" && c.DelegationMode != "client" {
		return fmt.Errorf("speko: delegation mode must be responses or client")
	}
	if c.ResponsesModel == "" {
		c.ResponsesModel = "gpt-6-luna"
	}
	if c.ResponsesReasoning == "" {
		c.ResponsesReasoning = "none"
	}
	switch c.ResponsesReasoning {
	case "none", "minimal", "low", "medium", "high", "xhigh", "provider_default":
	default:
		return fmt.Errorf("speko: invalid Responses reasoning effort")
	}
	if c.HoldTimeout == 0 {
		c.HoldTimeout = 180 * time.Second
	}
	c.AllowedOrigins = append([]string(nil), c.AllowedOrigins...)
	return nil
}
