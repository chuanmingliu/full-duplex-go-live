package audio

import (
	"math"
	"testing"
)

func TestTrimTrailingSilenceCutsTheRenderedPause(t *testing.T) {
	rate := 16000
	// 300 ms of tone, then 400 ms of near-silence.
	pcm := make([]byte, 0, rate*2*7/10)
	for i := 0; i < rate*3/10; i++ {
		v := int16(8000 * math.Sin(float64(i)*0.1))
		pcm = append(pcm, byte(uint16(v)), byte(uint16(v)>>8))
	}
	for i := 0; i < rate*4/10; i++ {
		pcm = append(pcm, 3, 0)
	}
	out := TrimTrailingSilence(pcm, rate, -40, 80)
	got := len(out) / 2 * 1000 / rate
	if got < 370 || got > 400 {
		t.Fatalf("kept %d ms; wanted ~380 (300 of tone + 80)", got)
	}
	if n := len(TrimTrailingSilence(make([]byte, 3200), rate, -40, 80)); n != 3200 {
		t.Fatalf("silence alone was cut to %d bytes", n)
	}
}
