package speko

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

func (s *Service) connectBridge(ctx context.Context, settings realtimeSettings, greeting string) (*websocket.Conn, error) {
	upstream, response, err := s.dial(ctx, http.Header{"Authorization": {"Bearer " + s.cfg.APIKey}, "Idempotency-Key": {rand.Text()}})
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, errors.New("Speko could not open the GPT-Live session")
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = upstream.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = upstream.Close() })
	defer stop()
	upstream.SetReadLimit(s.cfg.MaxMessageBytes)
	if err := s.send(upstream, settings.start(s.cfg.ResponsesModel, greeting, s.cfg.DelegationMode)); err != nil {
		return nil, errors.New("Speko session start failed")
	}
	_ = upstream.SetReadDeadline(time.Now().Add(s.cfg.SetupTimeout))
	var started wireEvent
	if upstream.ReadJSON(&started) != nil {
		return nil, errors.New("Speko session setup connection failed")
	}
	if started.Type == "error" {
		return nil, s.upstreamFailure("setup", started.Error)
	}
	if started.Type != "session.started" {
		return nil, errors.New("Speko did not accept the GPT-Live session")
	}
	// Refuse a negotiated codec different from the SIP media's codec.
	var negotiated struct {
		Audio struct {
			Format struct {
				Type string `json:"type"`
				Rate int    `json:"rate"`
			} `json:"format"`
		} `json:"audio"`
	}
	if json.Unmarshal(started.Session, &negotiated) != nil || (negotiated.Audio.Format.Type != "audio/pcm" || negotiated.Audio.Format.Rate != upstreamRate) {
		return nil, errors.New("Speko did not accept 24 kHz PCM")
	}
	_ = upstream.SetReadDeadline(time.Time{})
	accepted = true
	return upstream, nil
}
