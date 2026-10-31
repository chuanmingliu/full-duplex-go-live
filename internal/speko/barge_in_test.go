package speko

import (
	"bytes"
	"github.com/chuanmingliu/golive/internal/audio"
	"testing"
	"time"
)

func TestBargeGateRejectsClicksAndRearmsAfterQuiet(t *testing.T) {
	var b bargeDetector
	now := time.Now()
	loud := bytes.Repeat([]byte{0x33}, 160)
	quiet := bytes.Repeat([]byte{0xd5}, 160)
	for i := 0; i < 5; i++ {
		if b.observe(loud, audio.CodecALaw, now.Add(time.Duration(i)*20*time.Millisecond)) {
			t.Fatal("short burst interrupted")
		}
	}
	b.observe(quiet, audio.CodecALaw, now.Add(100*time.Millisecond))
	if b.talking(now.Add(100 * time.Millisecond)) {
		t.Fatal("click was treated as speech")
	}
	for i := 0; i < 6; i++ {
		got := b.observe(loud, audio.CodecALaw, now.Add(time.Duration(i+10)*20*time.Millisecond))
		if got != (i == 5) {
			t.Fatalf("trigger at frame %d: %v", i, got)
		}
	}
	if !b.talking(now.Add(400 * time.Millisecond)) {
		t.Fatal("released during a brief gap")
	}
	if b.talking(now.Add(600 * time.Millisecond)) {
		t.Fatal("did not release after silence")
	}
}
