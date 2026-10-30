package tools

import (
	"encoding/json"
	"time"
)

// QuoteReadySnapshot contains completed, still-valid quotations for one exact
// phone call. It is a read-only hint; a foreground quote remains authoritative.
type QuoteReadySnapshot struct {
	Call   string       `json:"call"`
	Quotes []ReadyQuote `json:"quotes"`
}

// ReadyQuote preserves the quotation result without interpreting its pricing.
// ID is opaque and stable for this completed result; expiry must be honored.
type ReadyQuote struct {
	ID        string          `json:"id"`
	ExpiresAt time.Time       `json:"expires_at"`
	Result    json.RawMessage `json:"result"`
}
