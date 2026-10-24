package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/live"
)

// segKind labels what a caller segment was, which is what the metrics need
// to know to judge the engine's reaction to it.
type segKind string

const (
	kindTurn        segKind = "turn"         // a request that wants an answer
	kindBackchannel segKind = "backchannel"  // a murmur over the assistant
	kindTic         segKind = "vocal_tic"    // a hesitation, a cough
	kindNonDirected segKind = "non_directed" // said to someone else
)

// segment is a stretch of speech by one party, on the harness's clock.
type segment struct {
	Start, End time.Time
	Kind       segKind
	Text       string
	// OverAgent is whether the agent was audible when the segment began.
	OverAgent bool
	// PauseAt / PauseEnd bound an intra-utterance pause (a caller segment
	// with one), so takeovers inside it can be counted.
	PauseAt, PauseEnd time.Time
	HasPause          bool
	// Truncated marks an agent segment the engine cut short.
	Truncated bool
}

// timeline is everything that happened in one session, as the caller saw it.
type timeline struct {
	mu     sync.Mutex
	Origin time.Time
	User   []segment
	Agent  []segment
	// Events the engine reported, for cross-checks and the per-turn figures.
	SpeechStarted  []time.Time
	SpeechStopped  []time.Time
	Truncations    []time.Time
	Backchannels   []time.Time
	TurnMetrics    []live.TurnMetricsEvent
	FunctionCalls  []functionCall
	Replies        []string
	Errors         []string
	ToolPasses     int
	ToolAttempts   int
	agentOpen      bool
	agentLastAudio time.Time
	agentStart     time.Time
	// playhead is where a client's playback of what has arrived would end:
	// each chunk is queued behind the one before it, the way a player does.
	playhead time.Time
	rate     int
}

type functionCall struct {
	At        time.Time
	CallID    string
	Name      string
	Arguments string
}

// agentSpeaking is whether a client playing what has arrived would be
// making sound now.
func (tl *timeline) agentSpeaking() bool {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	return tl.agentOpen && time.Now().Before(tl.playhead)
}

// agentAudio notes a chunk of output audio arriving and queues it behind
// whatever is still playing.
func (tl *timeline) agentAudio(at time.Time, pcmBytes int) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	if !tl.agentOpen {
		tl.agentOpen = true
		tl.agentStart = at
		tl.playhead = at
	}
	if tl.playhead.Before(at) {
		tl.playhead = at
	}
	d := time.Duration(audio.PCM16(tl.rate).DurationMS(make([]byte, pcmBytes))*1e6) * time.Nanosecond
	tl.playhead = tl.playhead.Add(d)
	tl.agentLastAudio = at
}

// agentQuiet closes the open agent segment once playback has run dry for a
// beat, ending it where the last sound did.
func (tl *timeline) agentQuiet(now time.Time, chunkMS int) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	if tl.agentOpen && now.Sub(tl.playhead) > 200*time.Millisecond {
		tl.Agent = append(tl.Agent, segment{Start: tl.agentStart, End: tl.playhead})
		tl.agentOpen = false
	}
}

// agentTruncated closes the open agent segment at the truncation: the
// client's buffer is cleared there, as a real one's would be.
func (tl *timeline) agentTruncated(at time.Time) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	tl.Truncations = append(tl.Truncations, at)
	if tl.agentOpen {
		tl.Agent = append(tl.Agent, segment{Start: tl.agentStart, End: at, Truncated: true})
		tl.agentOpen = false
	}
}

func (tl *timeline) finish(now time.Time) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	if tl.agentOpen {
		end := tl.playhead
		if now.Before(end) {
			end = now
		}
		tl.Agent = append(tl.Agent, segment{Start: tl.agentStart, End: end})
		tl.agentOpen = false
	}
}

// session is one open conversation with the service, driven frame by frame.
type session struct {
	url     string
	token   string
	rate    int
	conn    *websocket.Conn
	tl      *timeline
	sendMu  sync.Mutex
	frameMS int
	closed  chan struct{}

	// pending audio the caller is saying, consumed one frame per tick.
	queueMu     sync.Mutex
	queue       []byte
	speak       *speaking
	toolDelayMS int
}

// speaking is the caller segment being sent.
type speaking struct {
	seg      segment
	voiced   int // bytes of voice sent so far
	total    int // bytes of voice in the utterance
	pauseAt  int // byte offset where the pause begins, or -1
	pauseLen int // bytes of pause
	done     chan struct{}
}

func dial(ctx context.Context, url, token string, rate int, tools []json.RawMessage, instructions string) (*session, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	conn, _, err := dialer.DialContext(ctx, url, header)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	s := &session{url: url, token: token, rate: rate, conn: conn, tl: &timeline{Origin: time.Now(), rate: rate}, frameMS: 20, closed: make(chan struct{})}
	quiet := ""
	start := live.SessionStartEvent{
		Envelope: live.Envelope{Type: live.ClientSessionStart, EventID: "fdb"},
		Session: live.SessionConfig{
			Instructions: instructions,
			Audio:        &live.AudioConfig{Format: &live.AudioFormat{Type: "audio/pcm", Rate: rate}},
			Delegation:   &live.DelegationConfig{Type: live.DelegationResponses, Responses: &live.ResponsesDelegation{Tools: tools}},
			Golive:       &live.GoliveConfig{Greeting: &quiet},
		},
	}
	if err := s.send(start); err != nil {
		conn.Close()
		return nil, err
	}
	ready := make(chan struct{})
	go s.read(ready)
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		conn.Close()
		return nil, fmt.Errorf("no session.started")
	case <-ctx.Done():
		conn.Close()
		return nil, ctx.Err()
	}
	return s, nil
}

func (s *session) send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *session) close() {
	_ = s.send(map[string]any{"type": live.ClientSessionClose})
	s.conn.Close()
	s.tl.finish(time.Now())
}

// read turns the service's events into timeline entries.
func (s *session) read(ready chan struct{}) {
	defer close(s.closed)
	var readyOnce sync.Once
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		at := time.Now()
		var env struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		tl := s.tl
		switch env.Type {
		case live.ServerSessionStarted:
			readyOnce.Do(func() { close(ready) })
		case live.ServerOutputAudioDelta:
			var ev struct {
				Delta string `json:"delta"`
			}
			_ = json.Unmarshal(data, &ev)
			tl.agentAudio(at, base64.StdEncoding.DecodedLen(len(ev.Delta)))
		case live.ExtAudioTruncated:
			tl.agentTruncated(at)
		case live.ExtSpeechStarted:
			tl.mu.Lock()
			tl.SpeechStarted = append(tl.SpeechStarted, at)
			tl.mu.Unlock()
		case live.ExtSpeechStopped:
			tl.mu.Lock()
			tl.SpeechStopped = append(tl.SpeechStopped, at)
			tl.mu.Unlock()
		case live.ExtInputBackchannel:
			tl.mu.Lock()
			tl.Backchannels = append(tl.Backchannels, at)
			tl.mu.Unlock()
		case live.ExtTurnMetrics:
			var ev live.TurnMetricsEvent
			if json.Unmarshal(data, &ev) == nil {
				tl.mu.Lock()
				tl.TurnMetrics = append(tl.TurnMetrics, ev)
				tl.mu.Unlock()
			}
		case live.ServerOutputTranscriptDelta:
			var ev struct {
				Delta string `json:"delta"`
			}
			_ = json.Unmarshal(data, &ev)
			tl.mu.Lock()
			tl.Replies = append(tl.Replies, ev.Delta)
			tl.mu.Unlock()
		case live.ServerResponseEvent:
			var ev struct {
				DelegationID string `json:"delegation_id"`
				Event        struct {
					Type string `json:"type"`
					Item struct {
						Type      string `json:"type"`
						CallID    string `json:"call_id"`
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"item"`
				} `json:"event"`
			}
			if json.Unmarshal(data, &ev) == nil && ev.Event.Type == "response.output_item.done" && ev.Event.Item.Type == "function_call" {
				fc := functionCall{At: at, CallID: ev.Event.Item.CallID, Name: ev.Event.Item.Name, Arguments: ev.Event.Item.Arguments}
				tl.mu.Lock()
				tl.FunctionCalls = append(tl.FunctionCalls, fc)
				tl.mu.Unlock()
				// The application answers the tool after a moment, as a
				// lead service would, then asks for the answer to continue.
				go s.answerTool(fc, ev.DelegationID)
			}
		case live.ServerError:
			tl.mu.Lock()
			tl.Errors = append(tl.Errors, trim(string(data), 200))
			tl.mu.Unlock()
		}
	}
}

func (s *session) answerTool(fc functionCall, delegation string) {
	delay := s.toolDelayMS
	if delay <= 0 {
		delay = 600
	}
	time.Sleep(time.Duration(delay) * time.Millisecond)
	_ = s.send(live.ResponseItemCreateEvent{
		Envelope:     live.Envelope{Type: live.ClientResponseItemCreate},
		Item:         live.ResponseItem{Type: "function_call_output", CallID: fc.CallID, Output: live.FlexString(`{"status":"ok","total":"三千二百元"}`)},
		DelegationID: delegation,
	})
	_ = s.send(live.ResponseCreateEvent{Envelope: live.Envelope{Type: live.ClientResponseCreate}, DelegationID: delegation})
}

// run is the caller's clock: one 20 ms frame to the service every 20 ms,
// voice when there is something to say and silence otherwise, until ctx
// ends. Sending silence matters: the detector closes a turn on silence it
// hears, not on audio it stops receiving.
func (s *session) run(ctx context.Context) {
	frame := audio.PCM16(s.rate).BytesForMS(s.frameMS)
	silence := make([]byte, frame)
	ticker := time.NewTicker(time.Duration(s.frameMS) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closed:
			return
		case now := <-ticker.C:
			s.tl.agentQuiet(now, 40)
			buf := silence
			s.queueMu.Lock()
			if sp := s.speak; sp != nil {
				inPause := sp.pauseAt >= 0 && sp.voiced >= sp.pauseAt && sp.pauseLen > 0
				switch {
				case inPause:
					// Inside the pause: silence goes out and the pause shrinks.
					if sp.seg.PauseAt.IsZero() {
						sp.seg.PauseAt = now
					}
					sp.pauseLen -= frame
					if sp.pauseLen <= 0 {
						sp.seg.PauseEnd = now.Add(time.Duration(s.frameMS) * time.Millisecond)
					}
				case len(s.queue) > 0:
					n := frame
					if n > len(s.queue) {
						n = len(s.queue)
					}
					buf = make([]byte, frame)
					copy(buf, s.queue[:n])
					s.queue = s.queue[n:]
					sp.voiced += n
					if sp.seg.Start.IsZero() {
						sp.seg.Start = now
						sp.seg.OverAgent = s.tl.agentSpeaking()
					}
					sp.seg.End = now.Add(time.Duration(s.frameMS) * time.Millisecond)
				}
				if len(s.queue) == 0 {
					s.tl.mu.Lock()
					s.tl.User = append(s.tl.User, sp.seg)
					s.tl.mu.Unlock()
					close(sp.done)
					s.speak = nil
				}
			}
			s.queueMu.Unlock()
			if err := s.send(map[string]any{
				"type":  live.ClientInputAudioAppend,
				"audio": base64.StdEncoding.EncodeToString(buf),
			}); err != nil {
				return
			}
		}
	}
}

// say queues phrase k for ms of voice, with an optional pause (of pauseMS)
// inserted after pauseAfterMS of voice, and returns a channel closed when the
// last voiced frame has been sent.
func (s *session) say(k int, kind segKind, ms, pauseAfterMS, pauseMS int) <-chan struct{} {
	pcm := voiceFor(k, ms, s.rate)
	sp := &speaking{seg: segment{Kind: kind, Text: phraseText(k)}, total: len(pcm), pauseAt: -1, done: make(chan struct{})}
	if pauseMS > 0 && pauseAfterMS > 0 && pauseAfterMS < ms {
		sp.pauseAt = audio.PCM16(s.rate).BytesForMS(pauseAfterMS)
		sp.pauseLen = audio.PCM16(s.rate).BytesForMS(pauseMS)
		sp.seg.HasPause = true
	}
	s.queueMu.Lock()
	s.queue = pcm
	s.speak = sp
	s.queueMu.Unlock()
	return sp.done
}

// sayTwo queues phrase a, a pause, then phrase b as one utterance: an opening
// that is not finished, a breath, and the rest of it.
func (s *session) sayTwo(a, b int, pauseMS int) <-chan struct{} {
	pa := voiceFor(a, naturalMS(a), s.rate)
	pb := voiceFor(b, naturalMS(b), s.rate)
	sp := &speaking{seg: segment{Kind: kindTurn, Text: phraseText(a) + "…" + phraseText(b), HasPause: true}, done: make(chan struct{})}
	sp.pauseAt = len(pa)
	sp.pauseLen = audio.PCM16(s.rate).BytesForMS(pauseMS)
	s.queueMu.Lock()
	s.queue = append(pa, pb...)
	s.speak = sp
	s.queueMu.Unlock()
	return sp.done
}

// waitAgentSpeaking blocks until the agent is audible, or the timeout.
func (s *session) waitAgentSpeaking(ctx context.Context, d time.Duration) bool {
	deadline := time.After(d)
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline:
			return false
		case <-t.C:
			if s.tl.agentSpeaking() {
				return true
			}
		}
	}
}

// waitAgentSilent blocks until the agent has been quiet for at least `for`,
// or the timeout. A caller who waits a second after the answer ends is
// τ-voice's default listener.
func (s *session) waitAgentSilent(ctx context.Context, quietFor, d time.Duration) bool {
	deadline := time.After(d)
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline:
			return false
		case <-t.C:
			s.tl.mu.Lock()
			open := s.tl.agentOpen
			last := s.tl.playhead
			s.tl.mu.Unlock()
			if !open && !last.IsZero() && time.Since(last) >= quietFor {
				return true
			}
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
