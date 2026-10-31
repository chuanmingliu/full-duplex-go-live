package speko

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/gorilla/websocket"
)

const bridgeQueueBytes = 16000 // Two seconds of G.711; never an unbounded backlog.
const audioIdle = 200 * time.Millisecond

// bridgeAudio preserves the RTP input clock, fills input gaps, and paces
// output at 8 kHz in 20 ms frames so a provider batch cannot flood the SIP
// adapter's much smaller playout queue. Only the final partial frame is padded.
func (s *Service) bridgeAudio(ctx context.Context, client, upstream *websocket.Conn, input, output <-chan socketEvent, silence []byte, transport *pcmTransport, instructions, callID string) (result error) {
	backend := newClientBackend(instructions)
	defer backend.close()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var speaker []byte
	started := time.Now()
	var received, played, maxQueue, delegations, trimmed, interruptions, overflows int
	defer func() {
		reason := "closed"
		if result != nil {
			reason = result.Error()
		}
		slog.Info("speko: bridge summary", "call_id", callID, "elapsed_ms", time.Since(started).Milliseconds(), "received_audio_ms", received/8, "played_audio_ms", played/8, "max_queue_ms", maxQueue/8, "delegations", delegations, "trimmed_silence_ms", trimmed/8, "interruptions", interruptions, "playout_overflows", overflows, "reason", reason)
	}()
	nextMetrics := started.Add(5 * time.Second)
	nextInput := time.Now().Add(60 * time.Millisecond)
	inputRateWarned := false
	var responseID, transcript string
	var lastAudio, lastVoicedPlayback time.Time
	suppressed := false
	var detector bargeDetector
	var goodbye goodbyeGate
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
	interrupt := func(reason string) error {
		speaker = nil
		goodbye.output = ""
		suppressed = true
		lastAudio = time.Now()
		instruction := "Stop speaking immediately. The caller has the floor. Listen until they finish, then answer their latest request briefly. Do not resume the interrupted explanation."
		if reason == "playout_overflow" {
			overflows++
			instruction = "Playback backlog exceeded the safe limit. Stop speaking and discard the interrupted explanation. Wait for the caller's next utterance, then answer briefly."
		} else {
			interruptions++
		}
		if err := finish("cancelled", reason); err != nil {
			return err
		}
		if err := s.send(client, map[string]string{"type": "output_audio_buffer.cleared", "bridge_boundary": reason}); err != nil {
			return err
		}
		slog.Info("speko: playback interrupted", "call_id", callID, "reason", reason)
		return s.send(upstream, map[string]any{"type": "session.instructions.append", "event_id": "interrupt_" + rand.Text(), "delegation_id": nil, "content": instruction})
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case result := <-backend.result:
			if result.id != backend.active {
				continue
			}
			slog.Info("speko: backend completed", "call_id", callID, "elapsed_ms", result.elapsed.Milliseconds(), "ok", result.err == nil)
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
					if s.cfg.AutoHangup {
						goodbye.audio(b, transport.codec, time.Now())
					}
					if s.cfg.LocalBargeIn && detector.observe(b, transport.codec, time.Now()) && !suppressed &&
						(time.Since(lastVoicedPlayback) < 300*time.Millisecond || !codecSilence(speaker, silence[0])) {
						if err := interrupt("caller_speech"); err != nil {
							return err
						}
					}

					if err := s.send(upstream, map[string]string{"type": "session.input_audio.append", "audio": base64.StdEncoding.EncodeToString(transport.encode(b))}); err != nil {
						return err
					}
					// Tolerate arrival jitter before adding synthetic silence; otherwise
					// a late real frame and a filler can double-feed the upstream clock.
					nextInput = time.Now().Add(time.Duration(len(b))*time.Second/8000 + 40*time.Millisecond)
				}
			case "response.cancel":
				if err := interrupt("client_cancel"); err != nil {
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
				received += len(b)
				if len(b) == 0 {
					continue
				}
				if s.cfg.LocalBargeIn && !suppressed && detector.talking(time.Now()) && !codecSilence(b, silence[0]) {
					if err := interrupt("caller_speech"); err != nil {
						return err
					}
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
				speaker = append(speaker, b...)
				if lastVoicedPlayback.IsZero() || time.Since(lastVoicedPlayback) >= audioIdle {
					var removed int
					speaker, removed = trimIdleSilence(speaker, silence[0])
					trimmed += removed
				}
				maxQueue = max(maxQueue, len(speaker))
				if len(speaker) > bridgeQueueBytes {
					// Keep the call and input stream alive. Expanding the queue
					// would hide delay; silently dropping samples would corrupt speech.
					// Cancel this whole reply and expose the loss explicitly instead.
					if err := begin(); err != nil {
						return err
					}
					if err := interrupt("playout_overflow"); err != nil {
						return err
					}
				}
			case "session.delegation.created":
				delegations++
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
				if s.cfg.AutoHangup {
					goodbye.assistant(got.event.Delta, time.Now())
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
				if s.cfg.AutoHangup && got.event.Delta != "" {
					goodbye.caller(got.event.Delta, time.Now())
				}
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
				var provider struct {
					Code string `json:"code"`
				}
				_ = json.Unmarshal(got.event.Error, &provider)
				if provider.Code == "input_audio_rate_limit_exceeded" {
					if !inputRateWarned {
						_ = s.upstreamFailure("input_rate_warning", got.event.Error)
						inputRateWarned = true
					}
					continue // Dropped input is recoverable; do not hang up the SIP call.
				}
				return s.upstreamFailure("session", got.event.Error)
			case "session.closed":
				// Socket closure makes the adapter tear down SIP; discard unplayed
				// audio rather than delivering it after the upstream session ended.
				return errors.New("Speko session ended")
			}
		case now := <-tick.C:
			if s.cfg.AutoHangup && !suppressed && goodbye.ready(now, lastVoicedPlayback, codecSilence(speaker, silence[0])) {
				if err := finish("completed", "reciprocal_goodbye"); err != nil {
					return err
				}
				slog.Info("speko: closing after reciprocal goodbye", "call_id", callID)
				if err := client.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, live.CloseAssistantHangup), time.Now().Add(time.Second)); err != nil {
					return err
				}
				_ = s.send(upstream, map[string]string{"type": "session.close"})
				return nil
			}
			if !now.Before(nextMetrics) {
				slog.Info("speko: audio clock", "call_id", callID, "elapsed_ms", now.Sub(started).Milliseconds(), "queue_ms", len(speaker)/8, "received_audio_ms", received/8, "played_audio_ms", played/8)
				nextMetrics = now.Add(5 * time.Second)
			}
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
			if suppressed && now.Sub(lastAudio) >= audioIdle && !detector.talking(now) {
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
				played += n
				if !codecSilence(frame, silence[0]) {
					lastVoicedPlayback = now
				}
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
