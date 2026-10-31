package telephony

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// This opt-in wall-clock soak complements the fast controlled-clock regression.
// It exercises SIP, WebSocket input, real timers, RTP and WAV recording together.
func TestSimSustainedPlayback(t *testing.T) {
	if os.Getenv("GOLIVE_SUSTAINED_SIM") != "1" {
		t.Skip("set GOLIVE_SUSTAINED_SIM=1 for the 30-second playback soak")
	}
	for _, codec := range []string{"pcma", "pcmu"} {
		for _, kind := range []string{"response.audio", "response.output_audio"} {
			t.Run(codec+"/"+kind, func(t *testing.T) {
				t.Parallel()
				s := &simCall{c: testConfig(t), pbx: udpSocket(t), rtp: udpSocket(t), done: make(chan simResult, 1)}
				s.c.Codec, s.c.QueueMS, s.c.MaxDuration = codec, 500, 40*time.Second
				s.c.PBX = s.pbx.LocalAddr().String()
				s.agent = newFakeAgent(t, s.c.wireFormat())
				s.c.GoliveURL = s.agent.server.URL
				ctx, cancel := context.WithCancel(context.Background())
				s.cancel = cancel
				defer cancel()
				go func() {
					stats, err := Run(ctx, s.c, Call{ID: "sustained"})
					s.done <- simResult{stats, err}
				}()
				s.invite, s.peer = expectMethod(t, s.pbx, "INVITE")
				s.answer(t)
				const frames = 1500
				payload := bytes.Repeat([]byte{0x72}, frameBytes)
				producerCtx, stop := context.WithCancel(ctx)
				producerDone := make(chan struct{})
				go func() {
					defer close(producerDone)
					start := time.Now()
					sent := 0
					for deadline := start; sent < frames; deadline = deadline.Add(40 * time.Millisecond) {
						timer := time.NewTimer(time.Until(deadline))
						select {
						case <-producerCtx.Done():
							timer.Stop()
							return
						case <-timer.C:
						}
						n := min(2, frames-sent)
						if sent == 0 {
							n = 15 // 300 ms lead, then 40 ms chunks like the box profile.
						}
						select {
						case s.agent.send <- delta(kind+".delta", "long-response", bytes.Repeat(payload, n)):
						case <-producerCtx.Done():
							return
						}
						sent += n
					}
				}()
				defer func() { stop(); <-producerDone }()
				var first, last time.Time
				var previous uint32
				for i := 0; i < frames; i++ {
					packet, _ := readUDP(t, s.rtp)
					_, stamp, _, audio, ok := parseRTP(packet, s.c.payloadType())
					if !ok || !bytes.Equal(audio, payload) || (i > 0 && stamp-previous != frameBytes) {
						t.Fatalf("corrupt, lost or discontinuous RTP at frame %d", i)
					}
					previous, last = stamp, time.Now()
					if i == 0 {
						first = last
					}
				}
				s.cancel()
				result := s.finish(t, true)
				if !errors.Is(result.err, context.Canceled) || result.stats.Sent != frames || result.stats.MaxQueueMS > 380 {
					t.Fatalf("unexpected hangup or growing queue: stats=%+v err=%v", result.stats, result.err)
				}
				if elapsed := last.Sub(first); elapsed < 29*time.Second || elapsed > 31*time.Second {
					t.Fatalf("playback clock drift: %s", elapsed)
				}
				t.Logf("frames=%d max_queue=%dms playback=%s", frames, result.stats.MaxQueueMS, last.Sub(first))
			})
		}
	}
}
