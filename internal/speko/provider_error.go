package speko

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

var providerCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Upstream errors must remain diagnosable after the call. Never forward the
// provider's message to a client; private server logs redact both credentials.
func (s *Service) upstreamFailure(stage string, raw json.RawMessage) error {
	var detail struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &detail)
	if !providerCode.MatchString(detail.Code) {
		detail.Code = "unknown"
	}
	message := redactProviderMessage(detail.Message, s.cfg.APIKey, s.cfg.AuthToken)
	slog.Warn("speko: provider error", "stage", stage, "code", detail.Code, "detail", message)
	return fmt.Errorf("Speko %s failed (%s)", stage, detail.Code)
}

func redactProviderMessage(message string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	message = strings.Map(func(r rune) rune {
		if r < ' ' {
			return ' '
		}
		return r
	}, message)
	if len(message) > 1024 {
		message = strings.ToValidUTF8(message[:1024], "") + "..."
	}
	return message
}
