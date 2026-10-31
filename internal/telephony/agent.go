package telephony

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/gorilla/websocket"
)

var errAgentCompleted = errors.New("agent completed call")

func agentHTTP(ctx context.Context, c Config, path string, body any, out any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.GoliveURL, "/")+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.GoliveToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.GoliveToken)
	}
	client := http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return errors.New("golive HTTP connection failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("golive HTTP request rejected")
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
	if err != nil && ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return err
}

// Check verifies the dedicated experimental agent without creating a session or
// dialing. Capacity one prevents accidental mixing of call sessions.
func Check(ctx context.Context, c Config) error {
	if err := c.validate(); err != nil {
		return err
	}
	var pool struct {
		OK       bool
		Service  string
		InUse    int `json:"in_use"`
		Capacity int
		Realtime struct {
			Hold bool `json:"hold_for_opening"`
		}
	}
	if err := agentHTTP(ctx, c, "/v1/pool", nil, &pool); err != nil {
		return err
	}
	if !pool.OK || pool.Service != "golive" || pool.InUse != 0 || pool.Capacity != 1 || !pool.Realtime.Hold {
		return errors.New("requires an idle dedicated golive instance with max_sessions=1 and hold_for_opening=true")
	}
	return nil
}

type Call struct {
	ID           string `json:"call_id"`
	Instructions string `json:"instructions"`
	Greeting     string `json:"greeting"`
	Voice        string `json:"voice,omitempty"`
}

func connectAgent(ctx context.Context, c Config, call Call) (*websocket.Conn, error) {
	u, _ := url.Parse(c.GoliveURL)
	if u.Scheme == "http" {
		u.Scheme = "ws"
	} else {
		u.Scheme = "wss"
	}
	u.Path = "/v1/realtime"
	q := url.Values{"call": {call.ID}}
	u.RawQuery = q.Encode()
	header := http.Header{}
	if c.GoliveToken != "" {
		header.Set("Authorization", "Bearer "+c.GoliveToken)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	ws, _, err := dialer.DialContext(ctx, u.String(), header)
	if err != nil {
		return nil, errors.New("golive WebSocket connection failed")
	}
	// DialContext stops covering cancellation once the upgrade completes.
	// Interrupt the session.updated read too, rather than waiting ten seconds.
	stopCancel := context.AfterFunc(ctx, func() { ws.Close() })
	defer stopCancel()
	ws.SetReadLimit(1 << 20)
	_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err = ws.WriteJSON(map[string]any{"type": "session.update", "session": map[string]any{"instructions": call.Instructions, "voice": call.Voice, "input_audio_format": c.wireFormat(), "output_audio_format": c.wireFormat()}})
	if err == nil {
		for i := 0; i < 16; i++ {
			var msg struct {
				Type    string
				Session struct {
					Input  string `json:"input_audio_format"`
					Output string `json:"output_audio_format"`
				}
			}
			if err = ws.ReadJSON(&msg); err != nil {
				break
			}
			if msg.Type == "session.updated" {
				if msg.Session.Input != c.wireFormat() || msg.Session.Output != c.wireFormat() {
					err = errors.New("golive did not accept the G.711 format")
					break
				}
				_ = ws.SetReadDeadline(time.Time{})
				return ws, nil
			}
			if msg.Type == "error" {
				err = errors.New("golive session rejected")
				break
			}
		}
	}
	ws.Close()
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	return nil, errors.New("golive session setup failed")
}

func readAgent(ws *websocket.Conn, m *media, answered *atomic.Bool, r *recorder) error {
	for {
		var ev struct {
			Type       string `json:"type"`
			Delta      string `json:"delta"`
			Transcript string `json:"transcript"`
			ResponseID string `json:"response_id"`
			ItemID     string `json:"item_id"`
			Response   struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"response"`
		}
		if err := ws.ReadJSON(&ev); err != nil {
			var close *websocket.CloseError
			if errors.As(err, &close) && close.Code == websocket.CloseNormalClosure && answered.Load() {
				switch close.Text {
				case live.CloseAssistantHangup, live.CloseRequested, live.CloseRemoteHangup:
					r.event("agent_hangup", map[string]any{"reason": close.Text})
					return errAgentCompleted
				}
			}
			return errors.New("agent connection closed")
		}
		switch ev.Type {
		case "response.created":
			m.startResponse(ev.Response.ID)
		case "response.audio.delta", "response.output_audio.delta":
			if !answered.Load() {
				return errors.New("agent emitted audio before SIP answer")
			}
			b, err := base64.StdEncoding.DecodeString(ev.Delta)
			if err != nil {
				return errors.New("invalid agent audio")
			}
			if err = m.enqueue(ev.ResponseID, b, false); err != nil {
				return err
			}
		case "response.audio.done", "response.output_audio.done":
			if err := m.enqueue(ev.ResponseID, nil, true); err != nil {
				return err
			}
		case "input_audio_buffer.speech_started", "output_audio_buffer.cleared", "output_audio_buffer.clear":
			m.clear()
		case "response.done":
			if ev.Response.Status == "cancelled" || ev.Response.Status == "failed" {
				m.cancelResponse(ev.Response.ID)
			}
		case "conversation.item.input_audio_transcription.completed", "response.audio_transcript.done", "response.output_audio_transcript.done":
			r.event("transcript", map[string]any{"event": ev.Type, "item_id": ev.ItemID, "response_id": ev.ResponseID, "text": ev.Transcript})
		case "conversation.item.input_audio_transcription.delta", "response.audio_transcript.delta", "response.output_audio_transcript.delta":
			r.event("transcript_delta", map[string]any{"event": ev.Type, "item_id": ev.ItemID, "response_id": ev.ResponseID, "text": ev.Delta})
		case "error":
			return errors.New("agent reported an error")
		}
	}
}
