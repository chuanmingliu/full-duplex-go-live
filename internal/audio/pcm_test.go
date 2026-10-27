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

func TestTrimLeadingSilenceCutsTheOnsetPause(t *testing.T) {
	rate := 16000
	// 200 ms of near-silence, then 300 ms of tone.
	pcm := make([]byte, 0, rate*2/2)
	for i := 0; i < rate*2/10; i++ {
		pcm = append(pcm, 3, 0)
	}
	for i := 0; i < rate*3/10; i++ {
		v := int16(8000 * math.Sin(float64(i)*0.1))
		pcm = append(pcm, byte(uint16(v)), byte(uint16(v)>>8))
	}
	out := TrimLeadingSilence(pcm, rate, -40, 20)
	got := len(out) / 2 * 1000 / rate
	if got < 310 || got > 340 {
		t.Fatalf("kept %d ms; wanted ~320 (20 of the head + 300 of tone)", got)
	}
	// The sound itself is intact: the first kept samples are the quiet head.
	if out[0] != 3 || out[1] != 0 {
		t.Fatalf("the kept head is not the original audio: % x", out[:4])
	}
	if n := len(TrimLeadingSilence(make([]byte, 3200), rate, -40, 20)); n != 3200 {
		t.Fatalf("silence alone was cut to %d bytes", n)
	}
	// Audio that opens with sound is left alone.
	if n := len(TrimLeadingSilence(pcm[rate*2/10*2:], rate, -40, 20)); n != rate*3/10*2 {
		t.Fatalf("audio with no quiet head was cut to %d bytes", n)
	}
}
