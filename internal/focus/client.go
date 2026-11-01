package focus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client is a Scorer on a diarization service over a WebSocket (the edge
// gateway's /v1/audio/diarize).
//
// Audio is sent as it is pushed; a push that cannot be sent at once is
// dropped rather than queued, since labels for audio a second old are of no
// use to anyone. The connection failing closes Frames: the engine carries on
// without focus, as it would without the service at all.
type Client struct {
	url      string
	token    string
	log      *slog.Logger
	lag      time.Duration
	frames   chan Frame
	enrolled chan Enrolment

	mu     sync.Mutex
	conn   *websocket.Conn
	closed bool
	// dropped counts pushes that could not be sent.
	dropped int
}

// Dial connects and waits for the service's ready message.
func Dial(ctx context.Context, url, token string, log *slog.Logger) (*Client, error) {
	if log == nil {
		log = slog.Default()
	}
	hdr := http.Header{}
	if token != "" {
		hdr.Set("Authorization", "Bearer "+token)
	}
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.DefaultDialer.DialContext(dctx, url, hdr)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("focus: dial %s: HTTP %d", url, resp.StatusCode)
		}
		return nil, fmt.Errorf("focus: dial %s: %w", url, err)
	}
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("focus: no ready message: %w", err)
	}
	ready, _, err := parse(data)
	if err != nil || ready == nil || ready.Type != "ready" {
		conn.Close()
		return nil, errors.New("focus: the service did not say it was ready")
	}
	conn.SetReadDeadline(time.Time{})
	c := &Client{url: url, token: token, log: log, lag: time.Duration(ready.LagMS) * time.Millisecond,
		frames: make(chan Frame, 1024), enrolled: make(chan Enrolment, 8), conn: conn}
	go c.read()
	return c, nil
}

func (c *Client) read() {
	defer close(c.frames)
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if !closed {
				c.log.Warn("focus: the diarization connection ended; this session goes on without focus", "err", err)
			}
			return
		}
		_, msg, err := parse(data)
		if err != nil || msg == nil {
			continue
		}
		if msg.Type == "error" {
			c.log.Warn("focus: the diarization service reported an error", "message", msg.Message)
			continue
		}
		if msg.Type == "enrolled" {
			en := Enrolment{Profile: msg.Profile, Found: msg.Found == nil || *msg.Found, VoicedMS: msg.VoicedMS, Saved: msg.Saved,
				Candidates: msg.Candidates, Agreeing: msg.Agreeing, Tentative: msg.Tentative, Rejected: msg.Rejected}
			c.log.Info("focus: the service answered an enrolment", "profile", msg.Profile, "found", en.Found, "voiced_ms", msg.VoicedMS,
				"candidates", msg.Candidates, "agreeing", msg.Agreeing, "tentative", msg.Tentative, "rejected", msg.Rejected, "saved", msg.Saved)
			select {
			case c.enrolled <- en:
			default:
			}
			continue
		}
		for _, f := range msg.Expand() {
			select {
			case c.frames <- f:
			default:
				// The engine is not reading; nothing waits on labels.
			}
		}
	}
}

// Push sends audio.
func (c *Client) Push(pcm []byte, atMS int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("focus: closed")
	}
	c.conn.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
	if err := c.conn.WriteMessage(websocket.BinaryMessage, EncodeAudio(pcm, atMS)); err != nil {
		c.dropped++
		if c.dropped == 1 || c.dropped%100 == 0 {
			c.log.Warn("focus: audio could not be sent to the diarizer", "dropped", c.dropped, "err", err)
		}
		return err
	}
	return nil
}

func (c *Client) Frames() <-chan Frame { return c.frames }

// Enrol tells the service the caller spoke from fromMS to toMS: a
// target-speaker service takes the caller's voice from it (see EnrolMsg),
// and keeps it under saveAs for the next call when that is not empty.
func (c *Client) Enrol(fromMS, toMS int64, saveAs string) error {
	return c.send(EnrolMsg{Type: "enrol", FromMS: fromMS, ToMS: toMS, SaveAs: saveAs})
}

// EnrolProfile asks the service to enrol the caller from a kept profile.
// Whether one was found comes back on Enrolled.
func (c *Client) EnrolProfile(name string) error {
	return c.send(EnrolMsg{Type: "enrol", Profile: name})
}

// Enrolled is where the service's answers to enrolments come out.
func (c *Client) Enrolled() <-chan Enrolment { return c.enrolled }

func (c *Client) send(m EnrolMsg) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("focus: closed")
	}
	data, _ := json.Marshal(m)
	c.conn.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

func (c *Client) Lag() time.Duration { return c.lag }

// Close ends the session.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	conn := c.conn
	c.mu.Unlock()
	conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	return conn.Close()
}
