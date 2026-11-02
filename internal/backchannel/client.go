// Package backchannel provides a bounded, optional client for the local audio
// classifier. Predictions are evidence, never commands to stop playback.
package backchannel

import (
	"context"
	"encoding/base64"
	"log/slog"
	"math"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type Prediction struct {
	EndMS, FloorFromMS int64
	Generation         uint64
	FloorFrames        int
	Floor, Backchannel float64
	SentAt             time.Time
}
type packet struct {
	PCM        []byte
	Bot        [2][8]float64
	EndMS      int64
	Generation uint64
	At         time.Time
	Mixed      bool
}

// Client has one Push caller (the engine loop); lifecycle and reads are safe
// from other goroutines. One failed/gapped stream disables this call's helper.
type Client struct {
	Updates       chan struct{}
	queue         chan packet
	cancel        context.CancelFunc
	done          chan struct{}
	ready, failed atomic.Bool
	latest        atomic.Pointer[Prediction]
	processed     atomic.Int64
	log           *slog.Logger
	// Push-owned framing and bot-state clock, in 20 ms input frames.
	pending            []byte
	partial            packet
	halves             int
	nextMS             int64
	clockSet           bool
	speaking           bool
	onset, stop, pause int64
	pauseMarked        bool
}

func New(ctx context.Context, url string, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(ctx)
	c := &Client{Updates: make(chan struct{}, 1), queue: make(chan packet, 32), cancel: cancel, done: make(chan struct{}), log: log, onset: -5000, stop: -5000, pause: -5000, pauseMarked: true}
	go c.run(ctx, url)
	return c
}
func (c *Client) Disable(reason string) {
	if c.failed.CompareAndSwap(false, true) {
		c.ready.Store(false)
		c.latest.Store(nil)
		c.cancel()
		c.log.Info("backchannel: legacy fallback", "reason", reason)
		c.notify()
	}
}
func (c *Client) Close() { c.cancel(); <-c.done }
func (c *Client) Snapshot() *Prediction {
	if c.failed.Load() {
		return nil
	}
	return c.latest.Load()
}
func (c *Client) Ready() bool { return c.ready.Load() && !c.failed.Load() }
func (c *Client) notify() {
	select {
	case c.Updates <- struct{}{}:
	default:
	}
}

// The queue absorbs batched WebSocket input (at most 40 KiB of PCM). Its
// capacity is not a latency allowance: queued chunks still expire at 120 ms.
// Push never waits for a socket or model. Backpressure invalidates the entire
// prediction stream instead of silently continuing with broken model history.
func (c *Client) Push(pcm []byte, startMS int64, gen uint64, speaking, filler bool) {
	if !c.Ready() {
		return
	}
	if len(pcm)%2 != 0 {
		c.Disable("invalid_pcm")
		return
	}
	if !c.clockSet {
		c.nextMS = startMS
		c.clockSet = true
	}
	expected := c.nextMS + int64(len(c.pending)/32)
	if startMS != expected {
		c.Disable("audio_gap")
		return
	}
	c.pending = append(c.pending, pcm...)
	for len(c.pending) >= 640 {
		at := c.nextMS
		if speaking && !c.speaking {
			c.onset = at
		}
		if !speaking && c.speaking {
			c.stop = at
			c.pauseMarked = false
		}
		if !speaking && !c.pauseMarked && at-c.stop >= 200 {
			c.pause = at
			c.pauseMarked = true
		}
		c.speaking = speaking
		b := [8]float64{}
		if speaking {
			b[0] = 1
		}
		if filler {
			b[4] = 1
		}
		for i, t := range []int64{c.onset, c.pause, c.stop} {
			b[i+1] = math.Min(1, math.Max(0, float64(at-t)/5000))
		}
		if c.halves == 0 {
			c.partial = packet{Generation: gen, At: time.Now()}
		}
		if c.partial.Generation != gen {
			c.partial.Mixed = true
		}
		c.partial.PCM = append(c.partial.PCM, c.pending[:640]...)
		c.partial.Bot[c.halves] = b
		c.pending = c.pending[640:]
		c.halves++
		c.nextMS += 20
		if c.halves == 2 {
			c.partial.EndMS = c.nextMS
			select {
			case c.queue <- c.partial:
			default:
				c.Disable("queue_overflow")
				return
			}
			c.halves = 0
		}
	}
}

func (c *Client) run(ctx context.Context, url string) {
	defer close(c.done)
	defer c.ready.Store(false)
	defer func() { c.log.Info("backchannel: stream closed", "processed_chunks", c.processed.Load()) }()
	conn, resp, err := (&websocket.Dialer{HandshakeTimeout: time.Second}).DialContext(ctx, url, nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		c.Disable("connect_failed")
		return
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	fail := func() {
		if ctx.Err() == nil {
			c.Disable("stream_unavailable")
		}
	}
	conn.SetReadLimit(16384)
	conn.SetWriteDeadline(time.Now().Add(150 * time.Millisecond))
	if err = conn.WriteJSON(map[string]any{"type": "start", "sample_rate": 16000, "language": "zh", "aec": false}); err != nil {
		fail()
		return
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	var hello struct {
		Type, Mode, Fallback string
		Control              bool `json:"control_allowed"`
	}
	if err = conn.ReadJSON(&hello); err != nil || hello.Type != "ready" || hello.Mode != "shadow" || hello.Fallback != "legacy" || hello.Control {
		fail()
		return
	}
	c.ready.Store(true)
	c.log.Info("backchannel: connected", "language", "zh", "policy", "asr_assist_or_shadow")
	c.notify()
	seq, streak := 0, 0
	var from int64
	var lastGen uint64
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-c.queue:
			if time.Since(p.At) > 120*time.Millisecond {
				c.Disable("stale_queue")
				return
			}
			conn.SetWriteDeadline(time.Now().Add(120 * time.Millisecond))
			if err = conn.WriteJSON(map[string]any{"type": "audio", "seq": seq, "pcm_s16le": base64.StdEncoding.EncodeToString(p.PCM), "bot_state": p.Bot}); err != nil {
				fail()
				return
			}
			conn.SetReadDeadline(time.Now().Add(120 * time.Millisecond))
			var reply struct {
				Type, Fallback string
				Seq            int
				Control        bool `json:"control_allowed"`
				Frames         []struct {
					Floor       float64 `json:"p_floor"`
					Backchannel float64 `json:"p_backchannel"`
				}
			}
			if err = conn.ReadJSON(&reply); err != nil || reply.Type != "prediction" || reply.Seq != seq || len(reply.Frames) != 2 || reply.Control || reply.Fallback != "legacy" {
				fail()
				return
			}
			if p.Generation != lastGen {
				streak = 0
				lastGen = p.Generation
			}
			latest := &Prediction{EndMS: p.EndMS, Generation: p.Generation, SentAt: p.At}
			for i, f := range reply.Frames {
				if !probability(f.Floor) || !probability(f.Backchannel) {
					c.Disable("invalid_prediction")
					return
				}
				if f.Floor >= .7 {
					if streak == 0 {
						from = p.EndMS - 40 + int64(i)*20
					}
					streak++
				} else {
					streak = 0
				}
				latest.Floor = f.Floor
				latest.Backchannel = f.Backchannel
			}
			if p.Mixed {
				streak = 0
			}
			latest.FloorFrames = streak
			latest.FloorFromMS = from
			if ctx.Err() != nil || c.failed.Load() {
				return
			}
			c.latest.Store(latest)
			c.processed.Add(1)
			c.notify()
			seq++
		}
	}
}
func probability(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1 }

// Eligible excludes observations from another utterance, generation or stale
// queue. The engine must additionally apply speaker, transcript and pause gates.
func (p *Prediction) Eligible(now time.Time, inputMS, startMS int64, gen uint64) bool {
	return p != nil && p.Generation == gen && p.FloorFrames >= 3 && p.FloorFromMS >= startMS && p.EndMS <= inputMS && inputMS-p.EndMS <= 120 && now.Sub(p.SentAt) >= 0 && now.Sub(p.SentAt) <= 150*time.Millisecond
}
