package speko

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/gorilla/websocket"
)

const bridgeQueueBytes = 16000 // Two seconds of G.711; never an unbounded backlog.
const audioIdle = 200 * time.Millisecond

// bridgeAudio preserves the RTP input clock, fills input gaps, and paces
// output at 8 kHz in 20 ms frames so a provider batch cannot flood the SIP
// adapter's much smaller playout queue. Only the final partial frame is padded.
func (s *Service) bridgeAudio(ctx context.Context, client, upstream *websocket.Conn, input, output <-chan socketEvent, silence []byte, transport *pcmTransport, instructions string) error {
	backend := newClientBackend(instructions)
	defer backend.close()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var speaker []byte
	nextInput := time.Now().Add(20 * time.Millisecond)
	var responseID, transcript string
	var lastAudio time.Time
	suppressed := false
	finish := func(status, reason string) error {
		if responseID == "" {
			return nil
		}
		if err := s.send(client, map[string]any{"type": "response.audio.done", "response_id": responseID}); err != nil {
			return err
		}
		if transcript != "" {
			if err := s.send(client, map[string]any{"type": "response.audio_transcript.done", "response_id": responseID, "transcript": transcript}); err != nil {
				return err
			}
		}
		err := s.send(client, map[string]any{"type": "response.done", "response": map[string]string{"id": responseID, "status": status}, "bridge_boundary": reason})
		responseID, transcript = "", ""
		return err
	}
	begin := func() error {
		if responseID != "" {
			return nil
		}
		responseID = "resp_" + rand.Text()
		return s.send(client, map[string]any{"type": "response.created", "response": map[string]string{"id": responseID, "status": "in_progress"}})
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case result := <-backend.result:
			if result.id != backend.active {
				continue
			}
			text := result.text
			if result.err != nil {
				text = "The backend could not complete this request. Please tell the caller honestly that this information is unavailable and ask them to try again."
			}
			if err := s.send(upstream, map[string]any{"type": "session.commentary.append", "event_id": "result_" + rand.Text(), "delegation_id": result.id, "content": text}); err != nil {
				return err
			}
		case got := <-input:
			if got.err != nil {
				return nil
			}
			switch got.event.Type {
			case "input_audio_buffer.append":
				b, err := base64.StdEncoding.DecodeString(got.event.Audio)
				if err != nil || len(b) > bridgeQueueBytes {
					return errors.New("invalid or excessive caller audio")
				}
				// RTP already clocks caller audio. Forward it immediately, including
				// the bounded socket backlog received during upstream setup.
				// Re-pacing that backlog creates permanent input delay and
				// can overflow before the first model response.
				if len(b) > 0 {
					if err := s.send(upstream, map[string]string{"type": "session.input_audio.append", "audio": base64.StdEncoding.EncodeToString(transport.encode(b))}); err != nil {
						return err
					}
					nextInput = time.Now().Add(time.Duration(len(b)) * time.Second / 8000)
				}
			case "response.cancel":
				speaker = nil
				suppressed = true
				lastAudio = time.Now()
				if err := finish("cancelled", "client_cancel"); err != nil {
					return err
				}
				if err := s.send(client, map[string]string{"type": "output_audio_buffer.cleared"}); err != nil {
					return err
				}
				if err := s.send(upstream, map[string]any{"type": "session.instructions.append", "delegation_id": nil, "content": "Stop the current spoken response and listen for the caller's next request."}); err != nil {
					return err
				}
			case "input_audio_buffer.clear":
				if err := s.send(client, map[string]string{"type": "input_audio_buffer.cleared"}); err != nil {
					return err
				}
			case "session.close":
				_ = s.send(upstream, map[string]string{"type": "session.close"})
				return nil
			default:
				return errors.New("unsupported Realtime client event")
			}
		case got := <-output:
			if got.err != nil {
				return errors.New("Speko connection closed")
			}
			switch got.event.Type {
			case "session.output_audio.delta":
				b, err := base64.StdEncoding.DecodeString(got.event.Delta)
				if err != nil {
					return errors.New("invalid Speko audio")
				}
				b = transport.decode(b)
				if len(b) == 0 {
					continue
				}
				if suppressed {
					// Some Live streams send digital silence continuously. Silence
					// must not keep a cancelled response suppressed forever.
					if !codecSilence(b, silence[0]) {
						lastAudio = time.Now()
					}
					continue
				}
				lastAudio = time.Now()
				if len(speaker)+len(b) > bridgeQueueBytes {
					return errors.New("Speko audio exceeded the bounded playout queue")
				}
				speaker = append(speaker, b...)
			case "session.delegation.created":
				if s.cfg.DelegationMode == "client" && got.event.Delegation.Target == "client" {
					if err := backend.start(ctx, s, got.event.Delegation.ID); err != nil {
						return err
					}
				}
			case "session.output_transcript.delta":
				backend.add("assistant", got.event.Delta)
				if suppressed || got.event.Delta == "" {
					continue
				}
				if len(transcript)+len(got.event.Delta) > 64<<10 {
					return errors.New("Speko transcript exceeded the buffer limit")
				}
				if err := begin(); err != nil {
					return err
				}
				transcript += got.event.Delta
				if err := s.send(client, map[string]any{"type": "response.audio_transcript.delta", "response_id": responseID, "delta": got.event.Delta}); err != nil {
					return err
				}
			case "session.input_transcript.delta":
				backend.add("user", got.event.Delta)
				if err := s.send(client, map[string]any{"type": "conversation.item.input_audio_transcription.delta", "item_id": "input_stream", "delta": got.event.Delta}); err != nil {
					return err
				}
			case "session.usage.updated": // No equivalent Realtime response usage: preserve the cumulative snapshot as an extension later if needed.
			case "response.event":
				var inner struct {
					Type     string `json:"type"`
					Response struct {
						Status string `json:"status"`
					} `json:"response"`
				}
				if json.Unmarshal(got.event.Event, &inner) != nil {
					return errors.New("invalid delegated response event")
				}
				if inner.Type == "error" || inner.Type == "response.failed" || inner.Response.Status == "failed" {
					return errors.New("Speko delegated response failed")
				}
			case "error":
				return s.upstreamFailure("session", got.event.Error)
			case "session.closed":
				// Socket closure makes the adapter tear down SIP; discard unplayed
				// audio rather than delivering it after the upstream session ended.
				return errors.New("Speko session ended")
			}
		case now := <-tick.C:
			frame := append([]byte(nil), silence...)
			if !now.Before(nextInput) {
				if err := s.send(upstream, map[string]string{"type": "session.input_audio.append", "audio": base64.StdEncoding.EncodeToString(transport.encode(frame))}); err != nil {
					return err
				}
				nextInput = nextInput.Add(20 * time.Millisecond)
				if nextInput.Before(now) {
					nextInput = now.Add(20 * time.Millisecond)
				}
			}
			if suppressed && now.Sub(lastAudio) >= audioIdle {
				suppressed = false
			}
			if len(speaker) >= 160 || (len(speaker) > 0 && now.Sub(lastAudio) >= audioIdle) {
				if err := begin(); err != nil {
					return err
				}
				frame = append(frame[:0], silence...)
				n := min(len(speaker), len(frame))
				copy(frame, speaker[:n])
				speaker = speaker[n:]
				if err := s.send(client, map[string]string{"type": "response.audio.delta", "response_id": responseID, "delta": base64.StdEncoding.EncodeToString(frame)}); err != nil {
					return err
				}
			}
			if len(speaker) == 0 && !lastAudio.IsZero() && now.Sub(lastAudio) >= audioIdle {
				// GPT-Live has no per-turn done. This is explicitly a local
				// transport boundary, not a claim about a model turn or playback.
				if err := finish("completed", "audio_idle"); err != nil {
					return err
				}
			}
		}
	}
}

func codecSilence(data []byte, zero byte) bool {
	for _, sample := range data {
		// Both signs of digital zero are valid G.711 silence.
		if sample != zero && sample != zero^0x80 {
			return false
		}
	}
	return true
}
