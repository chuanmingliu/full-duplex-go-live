package telephony

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/sipgo"
)

// Run places one real call. The record-directory lock serializes invocations;
// keeping completed call directories prevents accidental redial with the same ID.
func Run(parent context.Context, c Config, call Call) (stats MediaStats, err error) {
	if !safeID.MatchString(call.ID) {
		return stats, errors.New("invalid call ID")
	}
	if len(call.Instructions) > 64<<10 || len(call.Greeting) > 4096 || len(call.Voice) > 256 {
		return stats, errors.New("call settings too large")
	}
	if err = c.validate(); err != nil {
		return stats, err
	}
	lock := filepath.Join(c.RecordDir, ".active-call")
	if err = os.Mkdir(lock, 0700); err != nil {
		return stats, errors.New("record directory is locked; confirm no call is active before removing .active-call")
	}
	defer os.Remove(lock)
	if err = Check(parent, c); err != nil {
		return stats, err
	}
	dir := filepath.Join(c.RecordDir, call.ID)
	if err = os.Mkdir(dir, 0700); err != nil {
		return stats, errors.New("call ID already exists or record directory is not writable")
	}
	r, err := newRecorder(dir, c.Codec, c.MaxDuration+c.RingTimeout)
	if err != nil {
		return stats, err
	}
	defer func() {
		reason := "completed"
		if err != nil {
			reason = err.Error()
		}
		r.event("call_end", map[string]any{"failed": err != nil, "reason": reason, "media": stats})
		err = errors.Join(err, r.close())
		summary, _ := json.MarshalIndent(map[string]any{"call_id": call.ID, "media": stats, "failed": err != nil, "measurement": "adapter boundary; not handset latency"}, "", "  ")
		err = errors.Join(err, os.WriteFile(filepath.Join(dir, "summary.json"), summary, 0600))
	}()
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	fail := func(reason string) { cancel(errors.New(reason)) }
	m, err := newMedia(c, r, fail)
	if err != nil {
		return stats, err
	}
	defer func() { stats = m.snapshot() }()
	ws, err := connectAgent(ctx, c, call)
	if err != nil {
		m.conn.Close()
		return stats, err
	}
	endpoint, err := newSIPEndpoint(c)
	if err != nil {
		ws.Close()
		m.conn.Close()
		return stats, err
	}
	defer endpoint.close()
	var answered atomic.Bool
	var wg sync.WaitGroup
	spawn := func(fn func()) { wg.Add(1); go func() { defer wg.Done(); fn() }() }
	spawn(func() {
		if e := readAgent(ws, m, &answered, r); e != nil {
			cancel(e)
		}
	})
	spawn(func() { m.send(ctx) })
	spawn(func() {
		var pcm pcmUpsampler
		m.receive(ctx, func(b []byte) error {
			if c.GoliveAudioFormat == "pcm16" {
				b = pcm.convert(b, c.Codec)
			}
			_ = ws.SetWriteDeadline(time.Now().Add(2 * time.Second))
			return ws.WriteJSON(map[string]any{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(b)})
		})
	})
	// Closing sockets is what unblocks network readers on any failure or signal.
	spawn(func() { <-ctx.Done(); ws.Close(); m.conn.Close() })
	var d *sipgo.DialogClientSession
	defer func() {
		cancel(nil)
		ws.Close()
		m.conn.Close()
		wg.Wait()
		if d != nil {
			err = errors.Join(err, endDialog(d, r))
		}
	}()
	// Translate a recognized normal agent close before the cleanup above adds
	// any SIP BYE failure. A rejected/unacknowledged BYE must still fail the call.
	defer func() {
		if err == errAgentCompleted {
			err = nil
		}
	}()
	r.event("dial", map[string]any{"call_id": call.ID, "codec": c.Codec})
	if err = r.failure(); err != nil {
		return stats, err
	}
	ringCtx, stopRing := context.WithTimeout(ctx, c.RingTimeout)
	d, err = endpoint.invite(ringCtx, c, call.ID, m.conn.LocalAddr().(*net.UDPAddr).Port, r, m.setEarlyRemote)
	ringErr := ringCtx.Err()
	stopRing()
	if err != nil {
		return stats, err
	}
	// The timer may have expired while the final answer was in flight.
	if ctx.Err() != nil {
		return stats, context.Cause(ctx)
	}
	// Ring cancellation is handled in invite; an answer received after its deadline
	// must not release the opening, even though its ACK has already been sent.
	if ringErr != nil {
		return stats, errors.New("answer arrived after cancellation")
	}
	remote, err := parseAnswer(d.InviteResponse.Body(), c.payloadType(), c.MediaNetworks)
	if err != nil {
		return stats, err
	}
	r.markAnswered(time.Now())
	m.mu.Lock()
	m.remote = remote
	m.mu.Unlock()
	answered.Store(true)
	m.notify()
	r.event("answered", nil)
	var opened struct {
		OK     bool
		Kicked []string
	}
	if err = agentHTTP(ctx, c, "/v1/opening", map[string]any{"callId": call.ID, "greeting": call.Greeting}, &opened); err != nil {
		return stats, err
	}
	if !opened.OK || len(opened.Kicked) != 1 || opened.Kicked[0] != call.ID {
		return stats, errors.New("agent did not release the named opening")
	}
	deadline := time.NewTimer(c.MaxDuration)
	defer deadline.Stop()
	health := time.NewTicker(time.Second)
	defer health.Stop()
	for {
		select {
		case <-ctx.Done():
			return stats, context.Cause(ctx)
		case <-d.Context().Done():
			r.event("remote_hangup", nil)
			return stats, nil
		case <-deadline.C:
			r.event("duration_limit", nil)
			return stats, nil
		case <-health.C:
			if e := r.failure(); e != nil {
				return stats, e
			}
		}
	}
}
