package telephony

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

// Exercise the real queue, packet builder and UDP sender with a scheduler that
// wakes late. The producer stays on its own 8 kHz timeline, like paced GoLive.
// Short first-packet tests cannot expose this cumulative clock drift.
func TestSustainedPlaybackSchedulerJitter(t *testing.T) {
	for _, codec := range []string{"pcma", "pcmu"} {
		for _, jitter := range [][]time.Duration{{0}, {500 * time.Microsecond}, {2 * time.Millisecond}, {0, 2 * time.Millisecond, 500 * time.Microsecond, time.Millisecond}} {
			t.Run(fmt.Sprintf("%s/%v", codec, jitter), func(t *testing.T) {
				c := testConfig(t)
				c.Codec, c.QueueMS = codec, 500
				m, err := newMedia(c, nil, func(s string) { t.Fatal(s) })
				if err != nil {
					t.Fatal(err)
				}
				defer m.conn.Close()
				sink := udpSocket(t)
				m.remote = sink.LocalAddr().(*net.UDPAddr)
				start := time.Now()
				now := start
				payload := bytes.Repeat([]byte{c.silence()}, frameBytes)
				if err := m.enqueue("sustained", bytes.Repeat(payload, 15), false); err != nil {
					t.Fatal(err)
				}
				produced := 15
				const totalFrames = 3000 // 60 seconds, accelerated by the test clock.
				nextInput := start.Add(40 * time.Millisecond)
				var received uint64
				var stamp uint32
				var last time.Time
				waits := 0
				m.sendPaced(context.Background(), func() time.Time { return now }, func(delay time.Duration) bool {
					for received < m.snapshot().Sent {
						packet, _ := readUDP(t, sink)
						_, ts, _, got, ok := parseRTP(packet, c.payloadType())
						if !ok || !bytes.Equal(got, payload) {
							t.Fatal("corrupt paced audio")
						}
						if received > 0 && (ts-stamp != frameBytes || now.Sub(last) < 18*time.Millisecond || now.Sub(last) > 22*time.Millisecond) {
							t.Fatalf("bad RTP pacing: timestamp step=%d interval=%s", ts-stamp, now.Sub(last))
						}
						stamp, last = ts, now
						received++
					}
					if received == totalFrames {
						return false
					}
					now = now.Add(delay + jitter[waits%len(jitter)])
					waits++
					// Match the deployed producer's 40 ms chunks and 300 ms lead.
					for produced < totalFrames && !nextInput.After(now) {
						n := min(2, totalFrames-produced)
						if err := m.enqueue("sustained", bytes.Repeat(payload, n), false); err != nil {
							t.Fatalf("call would hang up at %s with %v scheduler jitter: %v", now.Sub(start), jitter, err)
						}
						produced += n
						nextInput = nextInput.Add(40 * time.Millisecond)
					}
					return true
				})
				if received != totalFrames {
					t.Fatalf("sent %d/%d frames", received, totalFrames)
				}
				if m.snapshot().MaxQueueMS > 320 || now.Sub(start) > 60*time.Second {
					t.Fatalf("accumulating playback delay: queue=%dms elapsed=%s", m.snapshot().MaxQueueMS, now.Sub(start))
				}
				t.Logf("frames=%d max_queue=%dms elapsed=%s", received, m.snapshot().MaxQueueMS, now.Sub(start))
			})
		}
	}
}

func TestPlaybackSchedulerStallAndIdle(t *testing.T) {
	for _, stall := range []time.Duration{15 * time.Millisecond, 120 * time.Millisecond} {
		t.Run(stall.String(), func(t *testing.T) {
			c := testConfig(t)
			m, err := newMedia(c, nil, func(s string) { t.Fatal(s) })
			if err != nil {
				t.Fatal(err)
			}
			defer m.conn.Close()
			sink := udpSocket(t)
			m.remote = sink.LocalAddr().(*net.UDPAddr)
			now := time.Now()
			if err := m.enqueue("before-idle", make([]byte, 4*frameBytes), false); err != nil {
				t.Fatal(err)
			}
			var sent int
			var last time.Time
			var previousStamp uint32
			m.sendPaced(context.Background(), func() time.Time { return now }, func(delay time.Duration) bool {
				packet, _ := readUDP(t, sink)
				_, stamp, _, _, ok := parseRTP(packet, c.payloadType())
				if !ok {
					t.Fatal("bad RTP")
				}
				sent++
				gap := now.Sub(last)
				if sent == 3 && gap != 20*time.Millisecond {
					t.Fatalf("catch-up burst after scheduler stall: %s", gap)
				}
				if sent == 5 && (gap != 10*time.Second || stamp-previousStamp != 80000 || packet[1]&0x80 == 0) {
					t.Fatalf("idle resumption lost timeline/marker: gap=%s stamp step=%d", gap, stamp-previousStamp)
				}
				last, previousStamp = now, stamp
				switch sent {
				case 1:
					now = now.Add(delay + stall)
				case 4:
					// Idle wakeup should send immediately, without replaying missed ticks.
					now = now.Add(10 * time.Second)
					if err := m.enqueue("after-idle", make([]byte, 2*frameBytes), false); err != nil {
						t.Fatal(err)
					}
				case 6:
					return false
				default:
					if delay != 20*time.Millisecond {
						t.Fatalf("unexpected post-stall/idle delay: %s", delay)
					}
					now = now.Add(delay)
				}
				return true
			})
		})
	}
}
