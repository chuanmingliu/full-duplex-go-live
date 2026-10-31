package telephony

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestSDPRestrictions(t *testing.T) {
	allowed := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	for _, pt := range []byte{0, 8} {
		b := offerSDP("127.0.0.1", 32000, pt)
		p, e := parseAnswer(b, pt, allowed)
		if e != nil || p.Port != 32000 {
			t.Fatalf("valid answer: %v", e)
		}
	}
	good := string(offerSDP("127.0.0.1", 32000, 8))
	for name, body := range map[string]string{
		"wrong codec":               strings.ReplaceAll(good, "RTP/AVP 8", "RTP/AVP 0"),
		"remapped codec":            strings.ReplaceAll(good, "PCMA/8000", "opus/48000"),
		"address outside allowlist": strings.ReplaceAll(good, "127.0.0.1", "192.0.2.1"),
		"zero port":                 strings.ReplaceAll(good, "32000", "0"),
		"one way":                   strings.ReplaceAll(good, "sendrecv", "recvonly"),
		"encrypted":                 strings.ReplaceAll(good, "RTP/AVP", "RTP/SAVP"),
		"two audio":                 good + "m=audio 32002 RTP/AVP 8\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := parseAnswer([]byte(body), 8, allowed); e == nil {
				t.Fatal("accepted unsupported SDP")
			}
		})
	}
}

func TestRTPHeaders(t *testing.T) {
	p := testRTP(8, 65535, 99, []byte{1, 2, 3})
	seq, ts, ssrc, data, ok := parseRTP(p, 8)
	if !ok || seq != 65535 || ts != 99 || ssrc != 42 || !bytes.Equal(data, []byte{1, 2, 3}) {
		t.Fatal("basic parse")
	}
	// One CSRC, one extension word, payload, then four padding bytes.
	extended := append([]byte(nil), p[:12]...)
	extended[0] = 0xb1
	extended = append(extended, 0, 0, 0, 7, 0xbe, 0xde, 0, 1, 9, 8, 7, 6, 1, 2, 3, 0, 0, 0, 4)
	_, _, _, data, ok = parseRTP(extended, 8)
	if !ok || !bytes.Equal(data, []byte{1, 2, 3}) {
		t.Fatal("extensions or padding")
	}
	for _, b := range [][]byte{nil, p[:11], {0x81, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, append([]byte{0x90}, p[1:]...), append([]byte{0xa0}, p[1:]...)} {
		if _, _, _, _, ok := parseRTP(b, 8); ok {
			t.Fatal("accepted invalid header")
		}
	}
	if _, _, _, _, ok := parseRTP(p, 0); ok {
		t.Fatal("accepted wrong payload type")
	}
}

func TestQueueAndCancellation(t *testing.T) {
	c := testConfig(t)
	c.QueueMS = 40
	m, e := newMedia(c, nil, func(s string) { t.Error(s) })
	if e != nil {
		t.Fatal(e)
	}
	defer m.conn.Close()
	if e = m.enqueue("old", make([]byte, 161), true); e != nil {
		t.Fatal(e)
	}
	if m.snapshot().MaxQueueMS != 40 || len(m.queue) != 2 {
		t.Fatal("tail padding not included in queue size")
	}
	if e = m.enqueue("overflow", []byte{1}, true); e == nil {
		t.Fatal("queue overflow was accepted")
	}
	m.startResponse("old")
	m.clear()
	if e = m.enqueue("old", make([]byte, 160), true); e != nil {
		t.Fatal(e)
	}
	if len(m.queue) != 0 || m.snapshot().Stale != 1 {
		t.Fatal("stale audio survived barge-in")
	}
	m.startResponse("new")
	_ = m.enqueue("new", make([]byte, 160), false)
	m.cancelResponse("old")
	if len(m.queue) != 1 || m.queue[0].response != "new" {
		t.Fatal("old cancellation killed new response")
	}
}

func TestReceiveValidationAndLoss(t *testing.T) {
	c := testConfig(t)
	m, e := newMedia(c, nil, func(s string) { t.Error(s) })
	if e != nil {
		t.Fatal(e)
	}
	peer, wrong := udpSocket(t), udpSocket(t)
	m.remote = peer.LocalAddr().(*net.UDPAddr)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	got := make(chan []byte, 8)
	go func() {
		defer close(done)
		m.receive(ctx, func(b []byte) error { got <- append([]byte(nil), b...); return nil })
	}()
	defer func() { cancel(); m.conn.Close(); <-done }()
	dst := m.conn.LocalAddr().(*net.UDPAddr)
	payload := bytes.Repeat([]byte{9}, 160)
	send := func(conn *net.UDPConn, pt byte, seq uint16, ts uint32) {
		_, _ = conn.WriteToUDP(testRTP(pt, seq, ts, payload), dst)
	}
	send(wrong, 8, 1, 0)
	send(peer, 0, 1, 0)
	send(peer, 8, 65535, 0)
	send(peer, 8, 65535, 0)
	send(peer, 8, 1, 320)
	for i := 0; i < 3; i++ {
		select {
		case b := <-got:
			want := payload
			if i == 1 {
				want = bytes.Repeat([]byte{c.silence()}, 160)
			}
			if !bytes.Equal(b, want) {
				t.Fatal("bad gap concealment")
			}
		case <-time.After(time.Second):
			t.Fatal("missing received audio")
		}
	}
	s := m.snapshot()
	if s.Received != 2 || s.Lost != 1 || s.Late != 1 || s.Rejected != 2 {
		t.Fatalf("stats: %+v", s)
	}
}

func FuzzRTPParser(f *testing.F) {
	f.Add(testRTP(8, 1, 160, []byte{1, 2, 3}))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _, _, p, ok := parseRTP(b, 8)
		if ok && (len(p) == 0 || len(p) > 1600) {
			t.Fatal("invalid payload size")
		}
	})
}

// This compares only the first complete frame's scheduling delay on loopback.
// The baseline models the current patch's free-running 20 ms pacing timer; it
// does not execute Active Call or estimate mobile-network/ASR/LLM/TTS latency.
func TestPacingExperiment(t *testing.T) {
	if os.Getenv("GOLIVE_PACING_EXPERIMENT") != "1" {
		t.Skip("set GOLIVE_PACING_EXPERIMENT=1 for synthetic timing report")
	}
	sink, base := udpSocket(t), udpSocket(t)
	var baseline, candidate []float64
	for i := 0; i < 60; i++ {
		tick := time.NewTicker(20 * time.Millisecond)
		time.Sleep(time.Duration((i*7)%20) * time.Millisecond)
		start := time.Now()
		<-tick.C
		_, _ = base.WriteToUDP(testRTP(8, uint16(i), uint32(i*160), make([]byte, 160)), sink.LocalAddr().(*net.UDPAddr))
		readUDP(t, sink)
		baseline = append(baseline, float64(time.Since(start).Microseconds())/1000)
		tick.Stop()
		c := testConfig(t)
		m, e := newMedia(c, nil, func(s string) { t.Error(s) })
		if e != nil {
			t.Fatal(e)
		}
		m.remote = sink.LocalAddr().(*net.UDPAddr)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); m.send(ctx) }()
		start = time.Now()
		_ = m.enqueue(fmt.Sprint(i), make([]byte, 160), false)
		readUDP(t, sink)
		candidate = append(candidate, float64(time.Since(start).Microseconds())/1000)
		cancel()
		m.conn.Close()
		<-done
	}
	for name, samples := range map[string][]float64{"modeled_tick_baseline": baseline, "new_adapter": candidate} {
		sort.Float64s(samples)
		t.Logf("%s samples=%d p50=%.3fms p95=%.3fms max=%.3fms", name, len(samples), samples[29], samples[56], samples[59])
	}
}

func TestRecorderTimeline(t *testing.T) {
	dir := t.TempDir()
	r, e := newRecorder(dir, "pcma", time.Second)
	if e != nil {
		t.Fatal(e)
	}
	r.audio(true, bytes.Repeat([]byte{0xd5}, 160), r.start.Add(100*time.Millisecond))
	r.audio(false, bytes.Repeat([]byte{0xd5}, 160), r.start.Add(300*time.Millisecond))
	if e = r.close(); e != nil {
		t.Fatal(e)
	}
	for name, want := range map[string]uint32{"inbound.wav": 1920, "outbound.wav": 5120} {
		b, e := os.ReadFile(dir + "/" + name)
		if e != nil {
			t.Fatal(e)
		}
		if binary.LittleEndian.Uint32(b[40:44]) != want || len(b) != int(want)+44 {
			t.Fatalf("%s is not aligned", name)
		}
	}
}
