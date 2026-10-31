package telephony

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"time"
)

const frameBytes = 160 // G.711, 8 kHz, 20 ms.

type MediaStats struct {
	Received   uint64 `json:"received_packets"`
	Sent       uint64 `json:"sent_packets"`
	Rejected   uint64 `json:"rejected_packets"`
	Late       uint64 `json:"late_or_duplicate_packets"`
	Lost       uint64 `json:"lost_packets"`
	Cleared    uint64 `json:"cleared_frames"`
	Stale      uint64 `json:"stale_audio_events"`
	MaxQueueMS int    `json:"max_queue_ms"`
}

type frame struct {
	data     []byte
	response string
	received time.Time
}
type partial struct {
	data     []byte
	received time.Time
}

type media struct {
	conn        *net.UDPConn
	pt, silence byte
	limit       int
	mu          sync.Mutex
	remote      *net.UDPAddr
	queue       []frame
	partial     map[string]partial
	cancelled   map[string]bool
	current     string
	stats       MediaStats
	wake        chan struct{}
	fail        func(string)
	record      *recorder
}

func newMedia(c Config, r *recorder, fail func(string)) (*media, error) {
	host, _, _ := net.SplitHostPort(c.SIPListen)
	for port := c.RTPMin; port <= c.RTPMax; port++ {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(host), Port: port})
		if err == nil {
			return &media{conn: conn, pt: c.payloadType(), silence: c.silence(), limit: c.QueueMS * 8, partial: map[string]partial{}, cancelled: map[string]bool{}, wake: make(chan struct{}, 1), record: r, fail: fail}, nil
		}
	}
	return nil, errors.New("no RTP port available")
}

func (m *media) snapshot() MediaStats { m.mu.Lock(); defer m.mu.Unlock(); return m.stats }
func (m *media) notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
func (m *media) startResponse(id string) { m.mu.Lock(); m.current = id; m.mu.Unlock() }

func (m *media) enqueue(id string, data []byte, done bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == "" {
		return errors.New("audio event lacks response identity")
	}
	if m.cancelled[id] {
		m.stats.Stale++
		return nil
	}
	n := len(m.queue)*frameBytes + len(data)
	for _, p := range m.partial {
		n += len(p.data)
	}
	if done {
		if rem := (len(m.partial[id].data) + len(data)) % frameBytes; rem != 0 {
			n += frameBytes - rem
		}
	}
	if n > m.limit {
		return errors.New("outgoing audio queue exceeded limit")
	}
	p := m.partial[id]
	if len(p.data) == 0 {
		p.received = time.Now()
	}
	p.data = append(p.data, data...)
	for len(p.data) >= frameBytes {
		m.queue = append(m.queue, frame{data: append([]byte(nil), p.data[:frameBytes]...), response: id, received: p.received})
		p.data = p.data[frameBytes:]
	}
	if done && len(p.data) > 0 {
		buf := make([]byte, frameBytes)
		for i := range buf {
			buf[i] = m.silence
		}
		copy(buf, p.data)
		m.queue = append(m.queue, frame{data: buf, response: id, received: p.received})
		p.data = nil
	}
	if len(p.data) > 0 {
		m.partial[id] = p
	} else {
		delete(m.partial, id)
	}
	if n/8 > m.stats.MaxQueueMS {
		m.stats.MaxQueueMS = n / 8
	}
	m.notify()
	return nil
}

// clear serializes with the actual UDP write. After it returns, no queued
// frame from an invalidated response can leave this sender.
func (m *media) clear() {
	m.mu.Lock()
	if m.current != "" {
		m.cancelled[m.current] = true
	}
	for _, f := range m.queue {
		m.cancelled[f.response] = true
	}
	for id := range m.partial {
		m.cancelled[id] = true
	}
	m.stats.Cleared += uint64(len(m.queue))
	m.queue = nil
	m.partial = map[string]partial{}
	over := len(m.cancelled) > 4096
	m.mu.Unlock()
	m.record.event("playback_cleared", nil)
	if over {
		m.fail("too_many_cancelled_responses")
	}
	m.notify()
}

// A delayed completion for an old response must not cancel its successor.
func (m *media) cancelResponse(id string) {
	if id == "" {
		return
	}
	m.mu.Lock()
	m.cancelled[id] = true
	delete(m.partial, id)
	kept := m.queue[:0]
	for _, f := range m.queue {
		if f.response == id {
			m.stats.Cleared++
		} else {
			kept = append(kept, f)
		}
	}
	m.queue = kept
	over := len(m.cancelled) > 4096
	m.mu.Unlock()
	if over {
		m.fail("too_many_cancelled_responses")
	}
	m.notify()
}

func (m *media) send(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	m.sendPaced(ctx, time.Now, func(delay time.Duration) bool {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
		select {
		case <-ctx.Done():
			return false
		case <-m.wake:
		case <-timer.C:
		}
		return true
	})
}

// sendPaced keeps the RTP sender testable with a controlled scheduler. Socket
// deadlines use the real clock; now and wait govern only the media timeline.
func (m *media) sendPaced(ctx context.Context, nowFunc func() time.Time, waitFunc func(time.Duration) bool) {
	var seed [10]byte
	_, _ = rand.Read(seed[:])
	seq := binary.BigEndian.Uint16(seed[:2])
	ts := binary.BigEndian.Uint32(seed[2:6])
	ssrc := binary.BigEndian.Uint32(seed[6:])
	var next, last time.Time
	first := map[string]bool{}
	for {
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		now := nowFunc()
		if len(m.queue) > 0 && m.remote != nil && !now.Before(next) {
			f := m.queue[0]
			m.queue = m.queue[1:]
			marker := last.IsZero() || now.Sub(last) > 40*time.Millisecond
			if !last.IsZero() && marker {
				ts += uint32(now.Sub(last).Milliseconds()*8) - frameBytes
			}
			buf := make([]byte, 12+frameBytes)
			buf[0] = 0x80
			buf[1] = m.pt
			if marker {
				buf[1] |= 0x80
			}
			binary.BigEndian.PutUint16(buf[2:], seq)
			binary.BigEndian.PutUint32(buf[4:], ts)
			binary.BigEndian.PutUint32(buf[8:], ssrc)
			copy(buf[12:], f.data)
			_ = m.conn.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
			_, err := m.conn.WriteToUDP(buf, m.remote)
			if err == nil {
				m.stats.Sent++
			}
			m.mu.Unlock()
			if err != nil {
				m.fail("rtp_write_failed")
				return
			}
			m.record.audio(false, f.data, now)
			if !first[f.response] {
				first[f.response] = true
				m.record.event("first_rtp", map[string]any{"response_id": f.response, "enqueue_to_send_us": now.Sub(f.received).Microseconds()})
			}
			seq++
			ts += frameBytes
			last = now
			// Preserve the media clock across ordinary scheduler jitter. Adding
			// 20 ms to the actual wake time on every frame accumulates lateness,
			// eventually overflowing the queue even with a real-time producer.
			next = next.Add(20 * time.Millisecond)
			// Start immediately after idle, but don't dump missed ticks after a
			// stall. Rebase if recovery would put packets less than 10 ms apart.
			if next.Before(now.Add(10 * time.Millisecond)) {
				next = now.Add(20 * time.Millisecond)
			}
			continue
		}
		wait := time.Hour
		if len(m.queue) > 0 && m.remote != nil {
			wait = next.Sub(nowFunc())
			if wait < 0 {
				wait = 0
			}
		}
		m.mu.Unlock()
		if !waitFunc(wait) {
			return
		}
	}
}

// parseRTP validates variable headers, extensions and padding before exposing
// the payload. RTCP and unexpected payload types are not audio.
func parseRTP(b []byte, pt byte) (seq uint16, stamp, ssrc uint32, payload []byte, ok bool) {
	if len(b) < 12 || b[0]>>6 != 2 || b[1]&0x7f != pt {
		return
	}
	off := 12 + int(b[0]&15)*4
	if off > len(b) {
		return
	}
	if b[0]&0x10 != 0 {
		if off+4 > len(b) {
			return
		}
		n := int(binary.BigEndian.Uint16(b[off+2:])) * 4
		off += 4 + n
		if off > len(b) {
			return
		}
	}
	end := len(b)
	if b[0]&0x20 != 0 {
		n := int(b[end-1])
		if n == 0 || n > end-off {
			return
		}
		end -= n
	}
	if end <= off || end-off > 1600 {
		return
	}
	return binary.BigEndian.Uint16(b[2:]), binary.BigEndian.Uint32(b[4:]), binary.BigEndian.Uint32(b[8:]), b[off:end], true
}

// receive uses no added jitter look-ahead. Late/duplicate packets are dropped;
// timestamp gaps up to 200 ms are represented by G.711 silence. The gateway/PBX
// remains responsible for network jitter adaptation in this first version.
func (m *media) receive(ctx context.Context, input func([]byte) error) {
	buf := make([]byte, 2048)
	var have bool
	var lastSeq uint16
	var nextStamp, source uint32
	for {
		n, addr, err := m.conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() == nil {
				m.fail("rtp_read_failed")
			}
			return
		}
		seq, stamp, ssrc, data, ok := parseRTP(buf[:n], m.pt)
		m.mu.Lock()
		peer := m.remote
		if !ok || peer == nil || !peer.IP.Equal(addr.IP) || peer.Port != addr.Port || (have && ssrc != source) {
			m.stats.Rejected++
			m.mu.Unlock()
			continue
		}
		if have && int16(seq-lastSeq) <= 0 {
			m.stats.Late++
			m.mu.Unlock()
			continue
		}
		if have {
			m.stats.Lost += uint64(uint16(seq-lastSeq) - 1)
		}
		m.stats.Received++
		m.mu.Unlock()
		if have {
			gap := int32(stamp - nextStamp)
			if gap > 0 && gap <= 1600 {
				sil := make([]byte, int(gap))
				for i := range sil {
					sil[i] = m.silence
				}
				if input(sil) != nil {
					m.fail("agent_audio_write_failed")
					return
				}
			}
		}
		have = true
		lastSeq = seq
		nextStamp = stamp + uint32(len(data))
		source = ssrc
		m.record.audio(true, data, time.Now())
		if input(data) != nil {
			m.fail("agent_audio_write_failed")
			return
		}
	}
}
