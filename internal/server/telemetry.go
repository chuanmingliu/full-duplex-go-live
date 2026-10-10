package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
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
// The contract is the one that console already has with the other agents it
// watches, so they can be compared like for like:
//
//	{"at": <unix ms>, "tMs": <ms into the session>, "component": "vad",
//	 "event": "speech_stop", "extra": {"brain": "golive", "session": "sess_…"}}
//
//	vad   speech_start    the caller began to speak
//	vad   speech_stop     the turn was judged over (extra.silence_ms: the pause
//	                      that judgement waited through)
//	asr   final           the final transcript
//	llm   first_token     the backend's first token for the turn
//	tts   first_audio     the first synthesized audio for the turn
//	s2s   first_audio     that audio written towards the phone
//	s2s   ws_open / ws_close / release
//
// Nothing here is on the audio path. Events are stamped where they happen and
// handed to a bounded queue; a console that is down or slow costs events, which
// are counted, and nothing else.

type telEvent struct {
	At        int64          `json:"at"`
	TMs       int64          `json:"tMs"`
	Component string         `json:"component"`
	Event     string         `json:"event"`
	Extra     map[string]any `json:"extra"`
}

// telPoster delivers events to the console in the background.
type telPoster struct {
	url    string
	log    *slog.Logger
	ch     chan telEvent
	client *http.Client

	sent    atomic.Int64
	failed  atomic.Int64
	dropped atomic.Int64
}

func newTelPoster(url string, log *slog.Logger) *telPoster {
	p := &telPoster{
		url:    url,
		log:    log,
		ch:     make(chan telEvent, 512),
		client: &http.Client{Timeout: 2 * time.Second},
	}
	go p.run()
	return p
}

func (p *telPoster) post(ev telEvent) {
	select {
	case p.ch <- ev:
	default:
		p.dropped.Add(1)
	}
}

func (p *telPoster) run() {
	for ev := range p.ch {
		batch := []telEvent{ev}
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
		resp, err := p.client.Post(p.url, "application/json", bytes.NewReader(body))
		if err != nil {
			if p.failed.Add(int64(len(batch))) == int64(len(batch)) {
				// Once: a console that is not running is the normal state of
				// most deployments, and this is not worth a line per turn.
				p.log.Warn("telemetry: the latency console is not reachable; events are being discarded",
					"url", p.url, "err", err)
			}
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			p.failed.Add(int64(len(batch)))
			continue
		}
		p.sent.Add(int64(len(batch)))
	}
}

func (p *telPoster) stats() map[string]any {
	return map[string]any{
		"url":     p.url,
		"sent":    p.sent.Load(),
		"failed":  p.failed.Load(),
		"dropped": p.dropped.Load(),
	}
}

// telSession turns one session's events into the console's.
type telSession struct {
	poster   *telPoster
	id       string
	realtime bool
	t0       time.Time

	mu sync.Mutex
	// Per turn: has the first token, and the first audio, been reported since
	// the caller last stopped speaking?
	sawToken bool
	sawAudio bool
}

func newTelSession(p *telPoster, id string, realtime bool) *telSession {
	return &telSession{poster: p, id: id, realtime: realtime, t0: time.Now()}
}

func (t *telSession) emit(component, event string, extra map[string]any) {
	now := time.Now()
	if extra == nil {
		extra = map[string]any{}
	}
	extra["brain"] = "golive"
	extra["session"] = t.id
	t.poster.post(telEvent{
		At:        now.UnixMilli(),
		TMs:       now.Sub(t.t0).Milliseconds(),
		Component: component,
		Event:     event,
		Extra:     extra,
	})
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
			t.emit("s2s", "ws_open", map[string]any{"protocol": protocol})
		}

	case live.SpeechEvent:
		switch ev.Type {
		case live.ExtSpeechStarted:
			t.emit("vad", "speech_start", map[string]any{"barge_in": ev.BargeIn})
		case live.ExtSpeechStopped:
			t.mu.Lock()
			t.sawToken, t.sawAudio = false, false
			t.mu.Unlock()
			// The judgement that the turn is over is made this long after the
			// caller's last sound. It is latency they feel and this event's
			// timestamp does not include, so it is stated.
			t.emit("vad", "speech_stop", map[string]any{"silence_ms": cfg.VAD.MinSilenceMS})
		}

	case live.TranscriptDelta:
		if ev.Type == live.ServerInputTranscriptDelta && ev.Final {
			t.emit("asr", "final", nil)
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
		t.mu.Unlock()
		if first {
			t.emit("llm", "first_token", nil)
		}

	case live.OutputAudioDelta:
		if duplex.IsBackchannel(ev.ItemID) {
			return // an acknowledgement is not the answer
		}
		t.mu.Lock()
		first := !t.sawAudio
		t.sawAudio = true
		t.mu.Unlock()
		if first {
			// Synthesized audio is handed to the socket as it arrives — the
			// player adds pacing, not delay, to the first chunk — so the two
			// boundaries are the same instant here, reported separately
			// because the console has a row for each.
			t.emit("tts", "first_audio", map[string]any{"item": ev.ItemID})
			t.emit("s2s", "first_audio", map[string]any{"item": ev.ItemID})
		}

	case live.SessionClosedEvent:
		t.emit("s2s", "ws_close", map[string]any{"reason": ev.Reason})
	}
}
