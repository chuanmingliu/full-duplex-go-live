package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/duplex"
	"github.com/chuanmingliu/golive/internal/live"
)

// Turn-boundary telemetry for an external latency console.
//
// A phone call runs through more than this process — a PBX, a media gateway,
// whatever originated the call — and the place a call's latency is read is a
// console that hears from all of them. This is golive's contribution to it: one
// small event at each boundary of a turn, stamped when it happened and posted
// afterwards.
//
// The events are the console's own contract, so that golive can be compared
// like for like with the other agents it watches. In the "v1" format each is a
// self-describing record:
//
//	{"schemaVersion":1, "eventId":"…", "callId":"out-…", "turnId":2,
//	 "backend":"local", "chain":"turn", "point":"asr.final", "component":"asr",
//	 "sequence":41, "wallTimeMs":…, "monotonicNs":"…", "clockId":"boot:…",
//	 "extra":{…}}
//
// with these points, in the order a turn passes them:
//
//	vad.speech_started    the caller began to speak; opens turn n
//	vad.speech_stopped    the turn was judged over (extra.silence_ms: the pause
//	                      that judgement waited through)
//	asr.final             the final transcript
//	llm.first_token       the backend's first token for the turn
//	tts.first_audio       the first synthesized audio for the turn
//	phone.audio_sent      that audio written towards the phone
//
// and, before the caller's first turn, the opening line's
// opening.first_audio_generated and phone.audio_sent with no turn number.
//
// monotonicNs is CLOCK_MONOTONIC and clockId names the boot it belongs to, so a
// console on the same host can subtract golive's timestamps from its own and
// from the gateway's without trusting wall clocks to the millisecond.
//
// Nothing here is on the audio path. Events are stamped where they happen and
// handed to a bounded queue; a console that is down or slow costs events, which
// are counted, and nothing else.

// telPoint is one boundary, before it is put in a wire format.
type telPoint struct {
	wall      time.Time
	mono      int64
	hasMono   bool
	component string
	// event is the legacy envelope's name for it; point is the canonical one.
	event string
	point string
	chain string
	turn  int // 0 for none
	extra map[string]any
}

// telPoster delivers events to the console in the background.
type telPoster struct {
	url    string
	format string
	log    *slog.Logger
	ch     chan json.RawMessage
	client *http.Client

	clockID string
	mono    func() (int64, bool)
	seq     atomic.Int64

	sent    atomic.Int64
	failed  atomic.Int64
	dropped atomic.Int64
}

func newTelPoster(url, format string, log *slog.Logger) *telPoster {
	p := &telPoster{
		url:    url,
		format: format,
		log:    log,
		ch:     make(chan json.RawMessage, 512),
		client: &http.Client{Timeout: 2 * time.Second},
	}
	p.clockID, p.mono = hostClock()
	go p.run()
	return p
}

// post encodes a point for the console and queues it. It never blocks.
func (p *telPoster) post(pt telPoint, callID, sessionID string, t0 time.Time) {
	seq := p.seq.Add(1)
	extra := map[string]any{}
	for k, v := range pt.extra {
		extra[k] = v
	}
	var body map[string]any
	if p.format == "legacy" {
		extra["brain"] = "golive"
		extra["session"] = sessionID
		if callID != "" {
			extra["call"] = callID
		}
		component := pt.component
		if component == "phone" {
			// The older console knows the socket towards the phone by the name
			// of the agent that owned it.
			component = "s2s"
		}
		body = map[string]any{
			"at":        pt.wall.UnixMilli(),
			"tMs":       pt.wall.Sub(t0).Milliseconds(),
			"component": component,
			"event":     pt.event,
			"extra":     extra,
		}
	} else {
		if callID == "" {
			// The contract is keyed on the call. A session nobody attributed
			// to one — a browser, a test — has nothing to report under it.
			return
		}
		extra["agent"] = "golive"
		var turn any
		if pt.turn > 0 {
			turn = pt.turn
		}
		var mono any
		if pt.hasMono {
			mono = strconv.FormatInt(pt.mono, 10)
		}
		body = map[string]any{
			"schemaVersion": 1,
			"eventId":       p.clockID + ":golive-" + pt.component + ":" + strconv.FormatInt(seq, 10),
			"callId":        callID,
			"turnId":        turn,
			"backend":       "local",
			"chain":         pt.chain,
			"point":         pt.point,
			"component":     pt.component,
			"sequence":      seq,
			"wallTimeMs":    pt.wall.UnixMilli(),
			"monotonicNs":   mono,
			"clockId":       p.clockID,
			"extra":         extra,
		}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return
	}
	select {
	case p.ch <- data:
	default:
		p.dropped.Add(1)
	}
}

func (p *telPoster) run() {
	for ev := range p.ch {
		batch := []json.RawMessage{ev}
	drain:
		for len(batch) < 64 {
			select {
			case more := <-p.ch:
				batch = append(batch, more)
			default:
				break drain
			}
		}
		body, err := json.Marshal(batch)
		if err != nil {
			continue
		}
		n := int64(len(batch))
		resp, err := p.client.Post(p.url, "application/json", bytes.NewReader(body))
		if err != nil {
			if p.failed.Add(n) == n {
				// Once: a console that is not running is the normal state of
				// most deployments, and this is not worth a line per turn.
				p.log.Warn("telemetry: the latency console is not reachable; events are being discarded",
					"url", p.url, "err", err)
			}
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			if p.failed.Add(n) == n {
				p.log.Warn("telemetry: the latency console refused the events",
					"url", p.url, "status", resp.StatusCode, "format", p.format)
			}
			continue
		}
		p.sent.Add(n)
	}
}

func (p *telPoster) stats() map[string]any {
	return map[string]any{
		"url":     p.url,
		"format":  p.format,
		"clock":   p.clockID,
		"sent":    p.sent.Load(),
		"failed":  p.failed.Load(),
		"dropped": p.dropped.Load(),
	}
}

// telSession turns one session's events into the console's.
type telSession struct {
	poster   *telPoster
	id       string
	callID   string
	realtime bool
	t0       time.Time

	mu sync.Mutex
	// turn counts the caller's utterances; 0 until the first.
	turn int
	// Per turn: has the first token, and the first audio, been reported since
	// the caller last stopped speaking?
	sawToken bool
	sawAudio bool
}

func newTelSession(p *telPoster, id, callID string, realtime bool) *telSession {
	return &telSession{poster: p, id: id, callID: callID, realtime: realtime, t0: time.Now()}
}

func (t *telSession) emit(component, event, point, chain string, turn int, extra map[string]any) {
	pt := telPoint{
		wall: time.Now(), component: component, event: event,
		point: point, chain: chain, turn: turn, extra: extra,
	}
	pt.mono, pt.hasMono = t.poster.mono()
	t.poster.post(pt, t.callID, t.id, t.t0)
}

// released marks the moment a held session was opened to its call.
func (t *telSession) released() {
	t.emit("s2s", "release", "opening.released", "opening", 0, nil)
}

// observe is called with every event the session emits to all its connections.
func (t *telSession) observe(event any, cfg config.Config) {
	switch ev := event.(type) {
	case live.SessionStartedEvent:
		if ev.Type == live.ServerSessionStarted {
			protocol := "gpt-live-1"
			if t.realtime {
				protocol = "openai-realtime"
			}
			t.emit("s2s", "ws_open", "agent.connected", "lifecycle", 0, map[string]any{"protocol": protocol})
		}

	case live.SpeechEvent:
		switch ev.Type {
		case live.ExtSpeechStarted:
			t.mu.Lock()
			t.turn++
			turn := t.turn
			t.mu.Unlock()
			t.emit("vad", "speech_start", "vad.speech_started", "turn", turn, map[string]any{"barge_in": ev.BargeIn})
		case live.ExtSpeechStopped:
			t.mu.Lock()
			t.sawToken, t.sawAudio = false, false
			turn := t.turn
			t.mu.Unlock()
			// The judgement that the turn is over is made this long after the
			// caller's last sound. It is latency they feel and this event's
			// timestamp does not include, so it is stated.
			t.emit("vad", "speech_stop", "vad.speech_stopped", "turn", turn,
				map[string]any{"silence_ms": cfg.VAD.MinSilenceMS})
		}

	case live.TranscriptDelta:
		if ev.Type == live.ServerInputTranscriptDelta && ev.Final {
			t.mu.Lock()
			turn := t.turn
			t.mu.Unlock()
			t.emit("asr", "final", "asr.final", "turn", turn, nil)
		}

	case live.ResponseEventEnvelope:
		var inner struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(ev.Event, &inner) != nil || inner.Type != "response.output_text.delta" {
			return
		}
		t.mu.Lock()
		first := !t.sawToken
		t.sawToken = true
		turn := t.turn
		t.mu.Unlock()
		if first && turn > 0 {
			t.emit("llm", "first_token", "llm.first_token", "turn", turn, nil)
		}

	case live.OutputAudioDelta:
		if duplex.IsBackchannel(ev.ItemID) {
			return // an acknowledgement is not the answer
		}
		t.mu.Lock()
		first := !t.sawAudio
		t.sawAudio = true
		turn := t.turn
		t.mu.Unlock()
		if !first {
			return
		}
		// Synthesized audio is handed to the socket as it arrives — the player
		// adds pacing, not delay, to the first chunk — so the two boundaries
		// are the same instant here, reported separately because the console
		// has a row for each.
		sent := map[string]any{"coverage": "application"}
		if turn == 0 {
			// Before the caller has said anything: the opening line.
			t.emit("tts", "first_audio", "opening.first_audio_generated", "opening", 0, nil)
			t.emit("phone", "first_audio", "phone.audio_sent", "opening", 0, sent)
			return
		}
		t.emit("tts", "first_audio", "tts.first_audio", "turn", turn, nil)
		t.emit("phone", "first_audio", "phone.audio_sent", "turn", turn, sent)

	case live.SessionClosedEvent:
		t.emit("s2s", "ws_close", "agent.disconnected", "lifecycle", 0, map[string]any{"reason": ev.Reason})
	}
}
