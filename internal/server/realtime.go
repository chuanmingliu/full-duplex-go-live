package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/duplex"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
)

// This file is /v1/realtime: the same session and the same engine, spoken to in
// the OpenAI Realtime event protocol instead of gpt-live-1.
//
// It exists because of who is on the other end of a phone call. A SIP media
// gateway — Active Call is the one this was written against — terminates the
// call, and reaches its voice agent over a WebSocket in the one protocol it
// knows, which is OpenAI's Realtime API. It will not learn gpt-live-1. So for
// golive to be the voice on a telephone line, it has to answer in that
// protocol; and since everything that makes a call feel right is in the engine,
// not in the wire format, a translation at the edge of the session is all that
// takes.
//
// What is translated is deliberately the subset a gateway uses, checked against
// Active Call's realtime client:
//
//	in   session.update              instructions, voice, tools; starts the session
//	     input_audio_buffer.append   the caller's audio, base64 PCM16
//	     response.cancel             stop talking
//	     conversation.item.create    typed input, function results
//	     response.create             answer them
//	out  session.created / session.updated
//	     response.audio.delta        the assistant's audio, base64 PCM16
//	     input_audio_buffer.speech_started / speech_stopped
//	     conversation.item.input_audio_transcription.delta / .completed
//	     response.audio_transcript.delta
//	     response.created / response.done
//	     response.function_call_arguments.done
//	     error
//
// Two things about it are not obvious, and both are about who controls the
// floor.
//
// A gateway stops its own playback the moment it is told the caller has started
// speaking, and tells the agent to cancel. In OpenAI's API that is the whole of
// barge-in. Here it would undo the engine's judgement: golive deliberately
// keeps talking through "嗯" and through a cough, and decides for itself when a
// sound is an interruption. So input_audio_buffer.speech_started is sent when
// the caller starts speaking into silence, and otherwise only when the engine
// has actually given up the floor — at which point flushing the gateway's
// playback queue is exactly what should happen — and the response.cancel the
// gateway sends straight back is recognised as the echo it is.
//
// And an outbound call connects its agent before anyone has answered. What
// arrives until then is ringback, and whatever the agent says is spoken to
// nobody. See RealtimeConfig.HoldForOpening and Server.handleOpening.

// RealtimeOptions are one connection's settings for the Realtime dialect.
type RealtimeOptions struct {
	// Rate is the sample rate of "pcm16" audio when the client does not give
	// one.
	Rate int
	// Dialect is "beta" or "ga": which names the assistant's audio events use.
	Dialect string
	// Hold starts the session deaf and silent until Session.Open.
	Hold        bool
	HoldTimeout time.Duration
	// CallID is the telephone call this connection is the agent for, as the
	// originator named it: ?call= on the URL. It is how an opening is released
	// to the right session, and what telemetry is filed under.
	CallID string
}

// realtimeState is what translating a session into the Realtime protocol has
// to remember. One mutex covers it and the order events are queued in, because
// a response.created that arrives after its first audio is worse than useless.
type realtimeState struct {
	opts RealtimeOptions

	mu  sync.Mutex
	seq int64

	// The response in progress. A Realtime client thinks in responses; the
	// engine thinks in turns. One assistant turn is one response.
	respSeq  int
	respID   string
	respItem string
	respOpen bool

	// The caller's utterance in progress.
	startedSent   bool
	startMS       int64
	lastStartedAt time.Time
	heardItem     string
	heard         strings.Builder

	// held is set while the session waits to be released by the opening; opened
	// once it has been.
	held   atomic.Bool
	opened atomic.Bool
	timer  *time.Timer

	// silenceMS is the end-of-turn silence taken from the client's turn
	// detection, zero when the server's own is in use. Written once, before
	// the session starts.
	silenceMS int

	// instructions and voice are what the client's session.update asked for,
	// as it asked. Written once, before the session starts.
	instructions string
	voice        string
}

func newRealtimeState(opts RealtimeOptions) *realtimeState {
	if opts.Dialect != "ga" {
		opts.Dialect = "beta"
	}
	return &realtimeState{opts: opts}
}

// names of the assistant-audio events, which OpenAI renamed between the beta
// and the GA of the API.
func (rt *realtimeState) audioDelta() string {
	if rt.opts.Dialect == "ga" {
		return "response.output_audio.delta"
	}
	return "response.audio.delta"
}

func (rt *realtimeState) audioDone() string {
	if rt.opts.Dialect == "ga" {
		return "response.output_audio.done"
	}
	return "response.audio.done"
}

func (rt *realtimeState) transcriptDelta() string {
	if rt.opts.Dialect == "ga" {
		return "response.output_audio_transcript.delta"
	}
	return "response.audio_transcript.delta"
}

// --- out: engine events, as a Realtime client reads them ---

// deliverRealtime queues an engine event for the primary connection in the
// Realtime protocol. Most gpt-live-1 events have no counterpart and produce
// nothing.
func (s *Session) deliverRealtime(event any) {
	rt := s.rt
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, msg := range rt.translate(event) {
		s.queueRealtime(msg)
	}
}

// queueRealtime stamps and queues one Realtime event. rt.mu must be held.
func (s *Session) queueRealtime(msg map[string]any) {
	s.rt.seq++
	msg["event_id"] = "event_" + strconv.FormatInt(s.rt.seq, 10)
	data, err := json.Marshal(msg)
	if err != nil {
		s.log.Error("marshalling realtime event", "err", err)
		return
	}
	if t, _ := msg["type"].(string); t != s.rt.audioDelta() {
		s.log.Debug("realtime event", "type", t)
	}
	s.enqueue(s.primary, data)
}

func (rt *realtimeState) translate(event any) []map[string]any {
	switch ev := event.(type) {
	case live.SessionStartedEvent:
		return []map[string]any{{
			"type":    "session.updated",
			"session": withTurnDetection(realtimeSession(ev.Session), rt.silenceMS),
		}}

	case live.SpeechEvent:
		switch ev.Type {
		case live.ExtSpeechStarted:
			rt.startMS = ev.StartMS
			rt.startedSent = false
			if rt.respOpen {
				// The assistant has the floor, and whether this sound takes it
				// is the engine's decision, not the gateway's. Say nothing
				// until it is made.
				return nil
			}
			return []map[string]any{rt.speechStarted()}
		case live.ExtSpeechStopped:
			if !rt.startedSent {
				return nil
			}
			rt.startedSent = false
			return []map[string]any{{
				"type":         "input_audio_buffer.speech_stopped",
				"audio_end_ms": ev.EndMS,
				"item_id":      rt.heardItem,
			}}
		}

	case live.AudioTruncatedEvent:
		// The engine has yielded: what the gateway still has queued must not be
		// played, and speech_started is how a Realtime client is told that.
		var out []map[string]any
		if rt.respOpen {
			out = append(out, rt.finish("cancelled")...)
		}
		return append(out, rt.speechStarted())

	case live.TranscriptDelta:
		if ev.Type == live.ServerInputTranscriptDelta {
			if ev.ItemID != rt.heardItem {
				rt.heardItem = ev.ItemID
				rt.heard.Reset()
			}
			var out []map[string]any
			if ev.Delta != "" {
				rt.heard.WriteString(ev.Delta)
				out = append(out, map[string]any{
					"type":          "conversation.item.input_audio_transcription.delta",
					"item_id":       ev.ItemID,
					"content_index": 0,
					"delta":         ev.Delta,
				})
			}
			if ev.Final {
				out = append(out, map[string]any{
					"type":          "conversation.item.input_audio_transcription.completed",
					"item_id":       ev.ItemID,
					"content_index": 0,
					"transcript":    rt.heard.String(),
				})
			}
			return out
		}
		if ev.Delta == "" {
			return nil
		}
		out := rt.begin(ev.ItemID)
		return append(out, map[string]any{
			"type":          rt.transcriptDelta(),
			"response_id":   rt.respID,
			"item_id":       ev.ItemID,
			"output_index":  0,
			"content_index": 0,
			"delta":         ev.Delta,
		})

	case live.OutputAudioDelta:
		out := rt.begin(ev.ItemID)
		return append(out, map[string]any{
			"type":          rt.audioDelta(),
			"response_id":   rt.respID,
			"item_id":       ev.ItemID,
			"output_index":  0,
			"content_index": 0,
			"delta":         ev.Delta,
		})

	case live.TurnMetricsEvent:
		if rt.respOpen && rt.respItem == ev.TurnID {
			return rt.finish("completed")
		}

	case live.ResponseEventEnvelope:
		// The one backend event a gateway acts on: a function to run.
		var inner struct {
			Type string `json:"type"`
			Item struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"item"`
			OutputIndex int `json:"output_index"`
		}
		if json.Unmarshal(ev.Event, &inner) != nil ||
			inner.Type != "response.output_item.done" || inner.Item.Type != "function_call" {
			return nil
		}
		return []map[string]any{{
			"type":         "response.function_call_arguments.done",
			"response_id":  rt.respID,
			"item_id":      inner.Item.ID,
			"output_index": inner.OutputIndex,
			"call_id":      inner.Item.CallID,
			"name":         inner.Item.Name,
			"arguments":    inner.Item.Arguments,
		}}

	case live.ErrorEvent:
		body := map[string]any{
			"type":    ev.Error.Type,
			"code":    ev.Error.Code,
			"message": ev.Error.Message,
		}
		if ev.Error.ClientEventID != "" {
			body["event_id"] = ev.Error.ClientEventID
		}
		return []map[string]any{{"type": "error", "error": body}}
	}
	return nil
}

func (rt *realtimeState) speechStarted() map[string]any {
	rt.startedSent = true
	rt.lastStartedAt = time.Now()
	return map[string]any{
		"type":           "input_audio_buffer.speech_started",
		"audio_start_ms": rt.startMS,
		"item_id":        rt.heardItem,
	}
}

// begin opens a response for an assistant turn, closing the one before it if
// that never reported its own end — a backchannel does not.
func (rt *realtimeState) begin(itemID string) []map[string]any {
	if rt.respOpen && rt.respItem == itemID {
		return nil
	}
	var out []map[string]any
	if rt.respOpen {
		out = rt.finish("completed")
	}
	rt.respSeq++
	rt.respID = "resp_" + strconv.Itoa(rt.respSeq)
	rt.respItem = itemID
	rt.respOpen = true
	return append(out, map[string]any{
		"type":     "response.created",
		"response": map[string]any{"id": rt.respID, "object": "realtime.response", "status": "in_progress"},
	})
}

func (rt *realtimeState) finish(status string) []map[string]any {
	rt.respOpen = false
	return []map[string]any{
		{
			"type":          rt.audioDone(),
			"response_id":   rt.respID,
			"item_id":       rt.respItem,
			"output_index":  0,
			"content_index": 0,
		},
		{
			"type":     "response.done",
			"response": map[string]any{"id": rt.respID, "object": "realtime.response", "status": status},
		},
	}
}

// withTurnDetection reports the end-of-turn silence actually in force, which
// is the client's request only where the floor allowed it.
func withTurnDetection(session map[string]any, silenceMS int) map[string]any {
	if silenceMS > 0 {
		session["turn_detection"] = map[string]any{"type": "server_vad", "silence_duration_ms": silenceMS}
	}
	return session
}

// realtimeSession is the session object in the shape a Realtime client expects,
// in both its older flat form and the newer nested one.
func realtimeSession(c live.SessionConfig) map[string]any {
	format, rate, voice := "pcm16", 0, ""
	mime := audio.MIMEPCM
	if c.Audio != nil {
		if c.Audio.Format != nil {
			rate = c.Audio.Format.Rate
			mime = c.Audio.Format.Type
			switch c.Audio.Format.Type {
			case audio.MIMEPCMU:
				format = "g711_ulaw"
			case audio.MIMEPCMA:
				format = "g711_alaw"
			}
		}
		if c.Audio.Output != nil {
			voice = c.Audio.Output.Voice
		}
	}
	wire := map[string]any{"type": mime, "rate": rate}
	return map[string]any{
		"id":                  c.ID,
		"object":              "realtime.session",
		"type":                "realtime",
		"model":               c.Model,
		"instructions":        c.Instructions,
		"voice":               voice,
		"modalities":          []string{"text", "audio"},
		"input_audio_format":  format,
		"output_audio_format": format,
		"turn_detection":      map[string]any{"type": "server_vad"},
		"audio": map[string]any{
			"input":  map[string]any{"format": wire},
			"output": map[string]any{"format": wire, "voice": voice},
		},
	}
}

// --- in: a Realtime client's commands ---

// realtimeSessionUpdate is the part of session.update that is read. Both shapes
// OpenAI has used for the audio settings are accepted.
type realtimeSessionUpdate struct {
	Session struct {
		Model             string            `json:"model"`
		Instructions      string            `json:"instructions"`
		Voice             string            `json:"voice"`
		InputAudioFormat  string            `json:"input_audio_format"`
		OutputAudioFormat string            `json:"output_audio_format"`
		Tools             []json.RawMessage `json:"tools"`
		ToolChoice        any               `json:"tool_choice"`
		// TurnDetection is where the beta dialect puts it; the GA dialect
		// moved it under audio.input. Either is read.
		TurnDetection *realtimeTurnDetection `json:"turn_detection"`
		Audio         *struct {
			Input *struct {
				Format        *live.AudioFormat      `json:"format"`
				TurnDetection *realtimeTurnDetection `json:"turn_detection"`
			} `json:"input"`
			Output *struct {
				Format *live.AudioFormat `json:"format"`
				Voice  string            `json:"voice"`
			} `json:"output"`
		} `json:"audio"`
	} `json:"session"`
}

// realtimeTurnDetection is the part of a client's turn detection this server
// can act on. The threshold is a probability from a model this server does not
// run, and has no equivalent here.
type realtimeTurnDetection struct {
	Type              string `json:"type"`
	SilenceDurationMS int    `json:"silence_duration_ms"`
	// The same setting as some gateways write it. The phone gateway on the
	// test box sends {"silenceDurationMs":150,"prefixPaddingMs":80,…}, which
	// read as no setting at all until its session.update was logged.
	SilenceDurationMsCamel int `json:"silenceDurationMs"`
}

// silenceMS is the end-of-turn silence asked for, under either spelling.
func (t *realtimeTurnDetection) silenceMS() int {
	if t == nil {
		return 0
	}
	if t.SilenceDurationMS > 0 {
		return t.SilenceDurationMS
	}
	return t.SilenceDurationMsCamel
}

// sessionUpdateShape describes a session.update without its content: which
// settings the client sent, and its turn detection as sent. What a client asks
// for and what this server reads are not the same list, and a setting that is
// silently not acted on — an end-of-turn silence under a key this server does
// not look at — costs latency on every turn with nothing in the log to say so.
func sessionUpdateShape(data []byte) (keys []string, turnDetection string) {
	var raw struct {
		Session map[string]json.RawMessage `json:"session"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return nil, ""
	}
	for k := range raw.Session {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	td := raw.Session["turn_detection"]
	if td == nil {
		var audio struct {
			Input map[string]json.RawMessage `json:"input"`
		}
		if json.Unmarshal(raw.Session["audio"], &audio) == nil {
			td = audio.Input["turn_detection"]
		}
	}
	turnDetection = "none"
	if td != nil {
		turnDetection = string(td)
		if len(turnDetection) > 240 {
			turnDetection = turnDetection[:240] + "…"
		}
	}
	return keys, turnDetection
}

func (s *Session) realtimeError(code, message, eventID string) {
	s.reply(s.primary, live.NewError("invalid_request_error", code, message, eventID))
}

func (s *Session) handleRealtimeEvent(ctx context.Context, p *peer, data []byte) error {
	eventType, eventID, err := live.DecodeType(data)
	if err != nil {
		s.realtimeError("malformed_event", err.Error(), "")
		return err
	}
	if eventType != "input_audio_buffer.append" {
		s.log.Debug("realtime client event", "type", eventType, "event_id", eventID, "bytes", len(data))
		if !p.rate.allowEvent(s.cfg.Duplex.MaxEventsPerSecond) {
			s.floodClose(p, "events")
			return fmt.Errorf("live: rate limited")
		}
	}

	switch eventType {
	case "session.update":
		var ev realtimeSessionUpdate
		if err := json.Unmarshal(data, &ev); err != nil {
			s.realtimeError("malformed_event", err.Error(), eventID)
			return err
		}
		keys, turnDetection := sessionUpdateShape(data)
		s.log.Info("realtime: session.update", "bytes", len(data), "session_keys", keys,
			"turn_detection", turnDetection, "instructions_chars", len([]rune(ev.Session.Instructions)),
			"instructions_sha", shortSHA(ev.Session.Instructions),
			"tools", len(ev.Session.Tools))
		s.mu.Lock()
		started, current := s.started, s.config
		s.mu.Unlock()
		if started {
			// gpt-live-1's rule holds whatever the dialect: a session's
			// instructions, voice and audio format are fixed once it has
			// started. The client is shown the session as it is.
			s.reply(p, live.SessionStartedEvent{
				Envelope: live.Envelope{Type: live.ServerSessionUpdated},
				Session:  current,
			})
			return nil
		}
		return s.startRealtime(ctx, p, &ev, eventID)

	case "input_audio_buffer.append":
		var ev live.InputAudioAppendEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			s.realtimeError("malformed_event", err.Error(), eventID)
			return err
		}
		if ev.Audio == "" {
			return nil
		}
		// A client that sends audio without ever configuring the session gets
		// one with the server's defaults, as OpenAI's would.
		s.mu.Lock()
		started := s.started
		s.mu.Unlock()
		if !started {
			if err := s.startRealtime(ctx, p, nil, ""); err != nil {
				return err
			}
		}
		wire, err := base64.StdEncoding.DecodeString(ev.Audio)
		if err != nil {
			s.realtimeError("bad_audio", "audio must be base64-encoded audio in the session format", eventID)
			return err
		}
		if !p.rate.allowAudio(len(wire), s.cfg.Duplex.MaxAudioKbps) {
			s.floodClose(p, "audio")
			return fmt.Errorf("live: rate limited")
		}
		n := s.audioEvents.Add(1)
		if n%250 == 1 {
			s.log.Debug("client audio", "frames", n, "bytes", len(wire))
		}
		s.pushAudio(p, wire, eventID)
		return nil

	case "input_audio_buffer.commit", "input_audio_buffer.clear", "output_audio_buffer.clear",
		"conversation.item.truncate":
		// Turn-taking is the service's here, as in gpt-live-1: there is no
		// buffer for a client to commit, and nothing of the assistant's for it
		// to truncate that the engine has not already accounted for.
		return nil

	case "conversation.item.create":
		var ev struct {
			Item live.ResponseItem `json:"item"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			s.realtimeError("malformed_event", err.Error(), eventID)
			return err
		}
		s.withEngine(func(e *duplex.Engine) { e.SubmitItem(ev.Item, eventID) })
		return nil

	case "response.create":
		s.withEngine(func(e *duplex.Engine) { e.ContinueResponse(eventID) })
		return nil

	case "response.cancel":
		// A gateway sends this in answer to every speech_started. When that
		// was the engine yielding, there is nothing left to cancel; when it
		// was the caller speaking into silence, there was never anything. Only
		// a cancel that arrives unprompted is a request.
		s.rt.mu.Lock()
		echo := time.Since(s.rt.lastStartedAt) < 750*time.Millisecond
		s.rt.mu.Unlock()
		if !echo {
			s.withEngine(func(e *duplex.Engine) { e.Interrupt() })
		}
		return nil

	default:
		s.realtimeError("unknown_event_type", fmt.Sprintf("unsupported client event %q", eventType), eventID)
		return fmt.Errorf("live: unsupported realtime event %q", eventType)
	}
}

// startRealtime starts the session from a Realtime client's session.update, by
// way of the session.start a gpt-live-1 client would have sent: everything
// about resolving a session, opening its providers and building its engine is
// the same, and is done by the same code.
func (s *Session) startRealtime(ctx context.Context, p *peer, ev *realtimeSessionUpdate, eventID string) error {
	format := live.AudioFormat{Type: audio.MIMEPCM, Rate: s.rt.opts.Rate}
	voice := ""
	quiet := ""
	start := live.SessionStartEvent{
		Envelope: live.Envelope{Type: live.ClientSessionStart, EventID: eventID},
		// No greeting of the session's own. An OpenAI Realtime session does not
		// speak until spoken to, and on a phone line the opening is released by
		// whoever knows the call was answered.
		Session: live.SessionConfig{Golive: &live.GoliveConfig{Greeting: &quiet}},
	}
	if ev != nil {
		u := ev.Session
		start.Session.Instructions = u.Instructions
		s.rt.instructions = u.Instructions
		voice = u.Voice
		switch strings.ToLower(u.InputAudioFormat) {
		case "g711_ulaw":
			format = live.AudioFormat{Type: audio.MIMEPCMU}
		case "g711_alaw":
			format = live.AudioFormat{Type: audio.MIMEPCMA}
		}
		if u.Audio != nil {
			if u.Audio.Input != nil && u.Audio.Input.Format != nil {
				f := *u.Audio.Input.Format
				if f.Type != "" {
					format.Type = f.Type
					if f.Type != audio.MIMEPCM {
						format.Rate = 0
					}
				}
				if f.Rate != 0 {
					format.Rate = f.Rate
				}
			}
			if u.Audio.Output != nil && u.Audio.Output.Voice != "" {
				voice = u.Audio.Output.Voice
			}
		}
		td := u.TurnDetection
		if u.Audio != nil && u.Audio.Input != nil && u.Audio.Input.TurnDetection != nil {
			td = u.Audio.Input.TurnDetection
		}
		if td.silenceMS() > 0 {
			rc := s.cfg.Realtime
			want := td.silenceMS()
			switch {
			case !rc.HonorTurnDetection:
				s.log.Info("realtime: the client asked for an end-of-turn silence; keeping the server's",
					"asked_ms", want, "using_ms", s.cfg.VAD.MinSilenceMS)
			default:
				use := want
				if use < rc.MinSilenceFloorMS {
					use = rc.MinSilenceFloorMS
				}
				if use > maxSessionSilenceMS {
					use = maxSessionSilenceMS
				}
				start.Session.Golive.MinSilenceMS = use
				s.rt.silenceMS = use
				s.log.Info("realtime: end-of-turn silence taken from the client's turn detection",
					"asked_ms", want, "using_ms", use, "floor_ms", rc.MinSilenceFloorMS)
			}
		}
		if len(u.Tools) > 0 {
			start.Session.Delegation = &live.DelegationConfig{
				Type:      live.DelegationResponses,
				Responses: &live.ResponsesDelegation{Tools: u.Tools, ToolChoice: u.ToolChoice},
			}
		}
	}
	s.rt.voice = voice
	start.Session.Audio = &live.AudioConfig{
		Format: &format,
		Output: &live.OutputAudio{Voice: voice},
	}
	data, err := json.Marshal(start)
	if err != nil {
		return err
	}
	if err := s.onSessionStart(ctx, p, data, eventID); err != nil {
		return err
	}

	if s.rt.opts.Hold {
		s.rt.held.Store(true)
		s.withEngine(func(e *duplex.Engine) { e.SetMuted(true, "") })
		timeout := s.rt.opts.HoldTimeout
		if timeout <= 0 {
			timeout = 45 * time.Second
		}
		s.rt.mu.Lock()
		s.rt.timer = time.AfterFunc(timeout, func() {
			if s.release() {
				s.log.Warn("realtime: the session was never opened; listening anyway",
					"waited_s", int(timeout.Seconds()))
			}
		})
		s.rt.mu.Unlock()
		s.log.Debug("realtime: holding the session until the call is answered")
	}
	return nil
}

// release lets a held session hear. It reports whether it was held.
func (s *Session) release() bool {
	if s.rt == nil || !s.rt.held.CompareAndSwap(true, false) {
		return false
	}
	s.withEngine(func(e *duplex.Engine) { e.SetMuted(false, "") })
	return true
}

// Open releases a Realtime session to the call it is for, and has it say its
// opening line, if there is one. It reports false for a session that is not a
// started Realtime session, or has been opened already.
func (s *Session) Open(greeting string) bool {
	if s.rt == nil {
		return false
	}
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started || s.rt.opened.Swap(true) {
		return false
	}
	s.rt.mu.Lock()
	if s.rt.timer != nil {
		s.rt.timer.Stop()
	}
	s.rt.mu.Unlock()
	s.release()
	if tel := s.tel; tel != nil {
		tel.released()
	}
	s.withEngine(func(e *duplex.Engine) { e.Say(greeting) })
	return true
}

// lastCall is a phone call as it was asked for: enough to ask for it again.
type lastCall struct {
	SavedAt      string `json:"saved_at"`
	Call         string `json:"call,omitempty"`
	Instructions string `json:"instructions"`
	Greeting     string `json:"greeting"`
	Voice        string `json:"voice,omitempty"`
	SHA          string `json:"instructions_sha"`
}

// shortSHA names a text without saying it: enough to tell whether two calls
// were given the same instructions.
func shortSHA(text string) string {
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:6])
}

// keepLastCall writes down what the call just answered was asked to be, when
// the server has been given somewhere to keep it (last_call_file). A call with
// no instructions of its own is not kept: it would replace a prompt worth
// redialling with nothing.
func (s *Server) keepLastCall(sess *Session, greeting, callID string) {
	path := s.cfg.LastCallFile
	if path == "" || sess == nil || sess.rt == nil || sess.rt.instructions == "" {
		return
	}
	rec := lastCall{
		SavedAt:      time.Now().Format(time.RFC3339),
		Call:         callID,
		Instructions: sess.rt.instructions,
		Greeting:     greeting,
		Voice:        sess.rt.voice,
		SHA:          shortSHA(sess.rt.instructions),
	}
	data, err := json.Marshal(rec)
	if err == nil {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		s.log.Warn("realtime: could not keep this call's prompt for redialling", "err", err)
		return
	}
	s.log.Info("realtime: kept this call's prompt for redialling",
		"instructions_chars", len([]rune(rec.Instructions)), "instructions_sha", rec.SHA,
		"greeting_chars", len([]rune(greeting)))
}

// CallID is the telephone call a Realtime session was opened for, if its
// client said.
func (s *Session) CallID() string {
	if s.rt == nil {
		return ""
	}
	return s.rt.opts.CallID
}

// Realtime reports whether this session's primary connection speaks the
// Realtime protocol, and whether it has been opened.
func (s *Session) Realtime() (is, opened bool) {
	if s.rt == nil {
		return false, false
	}
	return true, s.rt.opened.Load()
}

// --- the server's side: the endpoint, the opening, the pool ---

func (s *Server) handleRealtime(w http.ResponseWriter, r *http.Request) {
	opts := RealtimeOptions{
		Rate:        s.cfg.Realtime.SampleRate,
		Dialect:     s.cfg.Realtime.Dialect,
		Hold:        s.cfg.Realtime.HoldForOpening,
		HoldTimeout: time.Duration(s.cfg.Realtime.HoldTimeoutSeconds) * time.Second,
	}
	// A client that can say what it is — in the URL, which is the one thing
	// every Realtime client lets you set — is taken at its word.
	q := r.URL.Query()
	for _, key := range []string{"rate", "sample_rate"} {
		if v := q.Get(key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_rate", "hint": key + " must be a number"})
				return
			}
			opts.Rate = n
		}
	}
	if v := q.Get("dialect"); v == "ga" || v == "beta" {
		opts.Dialect = v
	}
	if v := q.Get("hold"); v != "" {
		opts.Hold = v == "1" || v == "true"
	}
	opts.CallID = cleanCallID(firstNonEmpty(q.Get("call"), q.Get("call_id"), q.Get("callId")))
	s.serveSession(w, r, nil, &opts)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// cleanCallID bounds a call id that arrived from outside: it is logged, and
// posted to a console.
func cleanCallID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) > 128 {
		return ""
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == ':':
		default:
			return ""
		}
	}
	return id
}

// openingState is one opening line being synthesized ahead of its call.
type openingState struct {
	ready bool
	bytes int
	err   error
}

// handleOpening is the two halves of starting a phone call well.
//
//	POST /v1/opening {"greeting": "…", "warm": true}
//
// is sent when a call is dialled. The line is synthesized into the phrase cache
// while the phone rings, and the reply says whether it is ready yet — callers
// poll.
//
//	POST /v1/opening {"greeting": "…", "warm": false}
//
// is sent when the call is answered. The session that has not been opened yet
// is released and says the line, from the cache, at once; the reply lists it
// under "kicked". An empty list means no session has attached yet, and the
// caller tries again.
//
// Which session: the one whose gateway connected with ?call=<id>, when the
// request names a call ("callId") and one matches. Otherwise every unopened
// Realtime session — the originator does not know which socket the gateway
// opened, and on a line that carries one call at a time there is only one.
func (s *Server) handleOpening(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method_not_allowed"})
		return
	}
	var body struct {
		Greeting string `json:"greeting"`
		Text     string `json:"text"`
		Warm     bool   `json:"warm"`
		// The call, under any of the names an originator might give it.
		CallID  string `json:"callId"`
		CallID2 string `json:"call_id"`
		Call    string `json:"call"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_json"})
		return
	}
	greeting := firstNonEmpty(body.Greeting, body.Text)
	callID := cleanCallID(firstNonEmpty(body.CallID, body.CallID2, body.Call,
		r.URL.Query().Get("call"), r.URL.Query().Get("callId")))
	if n := len([]rune(greeting)); n > 400 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "greeting_too_long"})
		return
	}

	if body.Warm {
		st := s.warmOpening(greeting)
		resp := map[string]any{
			"ok":     st.err == nil,
			"callId": callID,
			"warm":   true,
			"kicked": []string{},
			"cached": []map[string]any{{"ready": st.ready, "bytes": st.bytes, "callId": callID}},
		}
		if st.err != nil {
			resp["error"] = st.err.Error()
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	// The named call's session if there is one; otherwise whoever is waiting.
	if callID != "" {
		var named []*Session
		for _, sess := range sessions {
			if sess.CallID() == callID {
				named = append(named, sess)
			}
		}
		if len(named) > 0 {
			sessions = named
		}
	}
	kicked := []string{}
	var opened *Session
	for _, sess := range sessions {
		if sess.Open(greeting) {
			// Named as the originator knows it, when it gave the call a name.
			kicked = append(kicked, firstNonEmpty(sess.CallID(), sess.ID()))
			opened = sess
		}
	}
	if len(kicked) > 0 {
		s.log.Info("realtime: call answered; session opened",
			"call", callID, "sessions", kicked, "greeting", greeting != "")
		s.keepLastCall(opened, greeting, callID)
	} else {
		s.log.Debug("realtime: asked to open a call with no session waiting", "call", callID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "callId": callID, "warm": false, "kicked": kicked, "cached": []any{},
	})
}

// warmOpening starts synthesizing an opening line, once, and reports how far
// it has got.
func (s *Server) warmOpening(greeting string) openingState {
	if greeting == "" {
		// Nothing to say is ready at once.
		return openingState{ready: true}
	}
	s.openMu.Lock()
	defer s.openMu.Unlock()
	if st, ok := s.openings[greeting]; ok {
		if st.err != nil {
			// Reported once; the next poll tries again.
			delete(s.openings, greeting)
		}
		return *st
	}
	if len(s.openings) >= 64 {
		s.openings = map[string]*openingState{}
	}
	st := &openingState{}
	s.openings[greeting] = st
	go func() {
		started := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		bytes, err := s.synthesizeOpening(ctx, greeting)
		s.openMu.Lock()
		st.ready, st.bytes, st.err = err == nil, bytes, err
		s.openMu.Unlock()
		if err != nil {
			s.log.Warn("realtime: the opening line could not be synthesized ahead of the call", "err", err)
			return
		}
		s.log.Info("realtime: opening line ready",
			"bytes", bytes, "took_ms", time.Since(started).Milliseconds())
	}()
	return *st
}

func (s *Server) synthesizeOpening(ctx context.Context, greeting string) (int, error) {
	tts, err := provider.OpenTTS(s.cfg.TTS)
	if err != nil {
		return 0, err
	}
	// The same voice, language and rate a Realtime session will ask the cache
	// for: the server's own, since a gateway states none of them.
	return duplex.WarmSpeech(ctx, s.cfg, tts, greeting, "", s.cfg.Realtime.SampleRate, s.cfg.Language, s.log)
}

// handlePool reports the sessions in progress, which is how something that did
// not open a session — a monitor, a test — finds the one to attach to.
func (s *Server) handlePool(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		ID       string `json:"id"`
		Protocol string `json:"protocol"`
		Call     string `json:"call,omitempty"`
		Opened   bool   `json:"opened"`
		AgeMS    int64  `json:"age_ms"`
	}
	s.mu.Lock()
	list := make([]entry, 0, len(s.sessions))
	for _, sess := range s.sessions {
		e := entry{ID: sess.ID(), Protocol: "gpt-live-1", AgeMS: time.Since(sess.createdAt).Milliseconds()}
		if is, opened := sess.Realtime(); is {
			e.Protocol, e.Opened, e.Call = "openai-realtime", opened, sess.CallID()
		}
		list = append(list, e)
	}
	s.mu.Unlock()
	body := map[string]any{
		"ok":       true,
		"service":  "golive",
		"sessions": list,
		"in_use":   len(list),
		"capacity": s.cfg.MaxSessions,
		"realtime": map[string]any{
			"sample_rate":      s.cfg.Realtime.SampleRate,
			"dialect":          s.cfg.Realtime.Dialect,
			"hold_for_opening": s.cfg.Realtime.HoldForOpening,
		},
	}
	if s.tel != nil {
		body["telemetry"] = s.tel.stats()
	}
	writeJSON(w, http.StatusOK, body)
}
