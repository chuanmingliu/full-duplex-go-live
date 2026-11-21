package server

import (
	"encoding/json"
	"fmt"

	"github.com/chuanmingliu/golive/internal/duplex"
	"github.com/chuanmingliu/golive/internal/live"
)

// Business events share the existing authenticated session/sideband boundary.
// The backend must select the matching session before forwarding an event.
func (s *Session) onBusinessEvent(p *peer, data []byte, eventID string) error {
	var event struct {
		Name string `json:"name"`
	}
	accepted := false
	if json.Unmarshal(data, &event) == nil {
		s.withEngine(func(e *duplex.Engine) { accepted = e.ApplyBusinessEvent(event.Name) })
	}
	if !accepted {
		err := fmt.Errorf("unsupported business event or working memory unavailable")
		s.reply(p, live.NewError("invalid_request_error", "invalid_business_event", err.Error(), eventID))
		return err
	}
	return nil
}
