package speko

import (
	"encoding/binary"
	"github.com/chuanmingliu/golive/internal/audio"
	"time"
)

// bargeDetector is an energy gate for the isolated telephone receive leg,
// not semantic VAD or echo cancellation. Require 120 ms of sustained signal;
// short clicks and low-level line noise must not seize the conversation.
type bargeDetector struct {
	samples   int
	lastVoice time.Time
	active    bool
}

func (b *bargeDetector) talking(now time.Time) bool {
	if now.Sub(b.lastVoice) >= 240*time.Millisecond {
		b.active = false
		b.samples = 0
	}
	return b.active
}
func (b *bargeDetector) observe(wire []byte, codec audio.Codec, now time.Time) bool {
	b.talking(now)
	pcm := codec.ToPCM16(wire)
	var sum int64
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int64(int16(binary.LittleEndian.Uint16(pcm[i:])))
		sum += v * v
	}
	if len(wire) == 0 || sum < int64(len(wire))*400*400 {
		b.samples = 0
		return false
	}
	if now.Sub(b.lastVoice) > 80*time.Millisecond {
		b.samples = 0
	}
	b.lastVoice = now
	b.samples += len(wire)
	if !b.active && b.samples >= 960 {
		b.active = true
		return true
	}
	return false
}
