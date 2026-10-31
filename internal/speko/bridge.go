package speko

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

type wireEvent struct {
	Type       string          `json:"type"`
	Audio      string          `json:"audio"`
	Delta      string          `json:"delta"`
	Session    json.RawMessage `json:"session"`
	Error      json.RawMessage `json:"error"`
	Event      json.RawMessage `json:"event"`
	Delegation struct {
		ID     string `json:"id"`
		Target string `json:"target"`
	} `json:"delegation"`
}

type socketEvent struct {
	event wireEvent
	err   error
}

// One reader per socket; the bridge loop owns both writers and all audio state.
func readEvents(ctx context.Context, c *websocket.Conn, disconnect context.CancelFunc) (<-chan socketEvent, <-chan struct{}) {
	out := make(chan socketEvent, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			var event wireEvent
			err := c.ReadJSON(&event)
			if err != nil && disconnect != nil {
				disconnect()
			}
			select {
			case out <- socketEvent{event, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return out, done
}

func (s *Service) send(c *websocket.Conn, event any) error {
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return s.write(c, websocket.TextMessage, b)
}

func (s *Service) bridge(parent context.Context, client *websocket.Conn, callID string) (result error) {
	ctx, cancel := context.WithCancel(parent)
	var clientDone, upstreamDone <-chan struct{}
	var upstream *websocket.Conn
	defer func() {
		if result != nil && parent.Err() == nil {
			s.sendError(client, "bridge_failed", result.Error())
		}
		cancel()
		_ = client.Close()
		if upstream != nil {
			_ = upstream.Close()
		}
		if clientDone != nil {
			<-clientDone
		}
		if upstreamDone != nil {
			<-upstreamDone
		}
	}()
	call := &bridgeCall{release: make(chan string, 1)}
	s.mu.Lock()
	if s.calls[callID] != nil {
		s.mu.Unlock()
		return errors.New("call ID already connected")
	}
	s.calls[callID] = call
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.calls, callID); s.mu.Unlock() }()
	if err := s.send(client, map[string]any{"type": "session.created", "session": map[string]any{"id": callID, "model": Model}}); err != nil {
		return err
	}
	_ = client.SetReadDeadline(time.Now().Add(s.cfg.SetupTimeout))
	var first wireEvent
	if client.ReadJSON(&first) != nil {
		return errors.New("Realtime session.update required")
	}
	settings, err := parseRealtimeStart(first)
	if err != nil {
		return err
	}
	_ = client.SetReadDeadline(time.Time{})
	if err := s.send(client, map[string]any{"type": "session.updated", "session": map[string]any{
		"id": callID, "model": Model, "input_audio_format": settings.format, "output_audio_format": settings.format,
	}}); err != nil {
		return err
	}
	input, readerDone := readEvents(ctx, client, cancel)
	clientDone = readerDone
	hold := time.NewTimer(s.cfg.HoldTimeout)
	defer hold.Stop()
	var greeting string
waiting:
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-hold.C:
			return errors.New("call was not answered before the hold timeout")
		case greeting = <-call.release:
			break waiting
		case got := <-input:
			if got.err != nil {
				return nil
			}
			if got.event.Type != "input_audio_buffer.append" {
				return errors.New("only audio may be sent while waiting for answer")
			}
			// Discard ringback; never buffer or forward it to the model.
		}
	}
	var response *http.Response
	upstream, response, err = s.dial(ctx, http.Header{"Authorization": {"Bearer " + s.cfg.APIKey}, "Idempotency-Key": {rand.Text()}})
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return errors.New("Speko could not open the GPT-Live session")
	}
	stop := context.AfterFunc(ctx, func() { _ = upstream.Close() })
	defer stop()
	upstream.SetReadLimit(s.cfg.MaxMessageBytes)
	if err := s.send(upstream, settings.start(s.cfg.ResponsesModel, greeting, s.cfg.DelegationMode)); err != nil {
		return errors.New("Speko session start failed")
	}
	_ = upstream.SetReadDeadline(time.Now().Add(s.cfg.SetupTimeout))
	var started wireEvent
	if upstream.ReadJSON(&started) != nil {
		return errors.New("Speko session setup connection failed")
	}
	if started.Type == "error" {
		return s.upstreamFailure("setup", started.Error)
	}
	if started.Type != "session.started" {
		return errors.New("Speko did not accept the GPT-Live session")
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
		return errors.New("Speko did not accept 24 kHz PCM")
	}
	_ = upstream.SetReadDeadline(time.Time{})
	output, readerDone := readEvents(ctx, upstream, nil)
	upstreamDone = readerDone
	// Start the input clock before the opening cue, even with silence suppression
	// at the handset. GPT-Live is full duplex and needs an active input stream.
	silence := make([]byte, 160)
	for i := range silence {
		silence[i] = settings.silence
	}
	if err := s.send(upstream, map[string]any{"type": "session.input_audio.append", "audio": base64.StdEncoding.EncodeToString(newPCMTransport(settings.format).encode(silence))}); err != nil {
		return err
	}
	if greeting != "" {
		if err := s.send(upstream, map[string]any{"type": "session.commentary.append", "event_id": "greet_" + rand.Text(), "delegation_id": nil, "content": greeting}); err != nil {
			return err
		}
	}
	return s.bridgeAudio(ctx, client, upstream, input, output, silence, newPCMTransport(settings.format), settings.instructions)
}

// The adapter's Realtime subset deliberately excludes tools, format changes,
// client VAD and PCM. Unsupported settings fail visibly instead of being ignored.
type realtimeSettings struct {
	format, mime, instructions, voice string
	silence                           byte
}

func parseRealtimeStart(event wireEvent) (realtimeSettings, error) {
	var s realtimeSettings
	var fields map[string]json.RawMessage
	if event.Type != "session.update" || json.Unmarshal(event.Session, &fields) != nil || fields == nil {
		return s, errors.New("Realtime session.update with a session object required")
	}
	for name := range fields {
		switch name {
		case "instructions", "voice", "input_audio_format", "output_audio_format":
		default:
			return s, errors.New("unsupported Realtime session setting")
		}
	}
	var output string
	if json.Unmarshal(fields["input_audio_format"], &s.format) != nil || json.Unmarshal(fields["output_audio_format"], &output) != nil || output != s.format {
		return s, errors.New("matching G.711 input and output formats required")
	}
	if raw, ok := fields["instructions"]; ok && json.Unmarshal(raw, &s.instructions) != nil {
		return s, errors.New("instructions must be text")
	}
	if raw, ok := fields["voice"]; ok && json.Unmarshal(raw, &s.voice) != nil {
		return s, errors.New("voice must be text")
	}
	if len(s.instructions) > 64<<10 || len(s.voice) > 256 {
		return s, errors.New("session settings too large")
	}
	switch s.format {
	case "g711_alaw":
		s.mime, s.silence = "audio/pcma", 0xd5
	case "g711_ulaw":
		s.mime, s.silence = "audio/pcmu", 0xff
	default:
		return s, errors.New("the SIP bridge requires g711_alaw or g711_ulaw")
	}
	return s, nil
}

func (s realtimeSettings) start(model, greeting string, mode ...string) any {
	audio := map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": upstreamRate}}
	if s.voice != "" {
		audio["output"] = map[string]string{"voice": s.voice}
	}
	session := map[string]any{"model": Model, "instructions": s.instructions, "audio": audio,
		"delegation": map[string]any{"type": "responses", "responses": map[string]any{"model": model, "instructions": s.instructions}}}
	if len(mode) > 0 && mode[0] == "client" {
		session["delegation"] = map[string]string{"type": "client"}
	}
	if greeting != "" {
		session["input"] = []any{map[string]any{"type": "message", "role": "developer", "content": []any{map[string]string{"type": "input_text", "text": "Greet the caller once: " + greeting}}}}
	}
	return map[string]any{"type": "session.start", "session": session}
}
