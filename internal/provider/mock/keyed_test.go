package mock

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/provider"
)

// tone is ms of a sine at hz, the shape cmd/golivefdb speaks in.
func tone(rate int, hz float64, ms int) []byte {
	n := rate * ms / 1000
	buf := make([]float32, n)
	for i := range buf {
		t := float64(i) / float64(rate)
		env := 0.6 + 0.4*math.Sin(2*math.Pi*4*t)
		buf[i] = float32(0.3 * env * math.Sin(2*math.Pi*hz*t))
	}
	return audio.EncodePCM16(buf)
}

func hear(t *testing.T, a *ASR, pcm []byte, rate int) string {
	t.Helper()
	s, err := a.Open(context.Background(), provider.ASROptions{SampleRate: rate})
	if err != nil {
		t.Fatal(err)
	}
	frame := audio.PCM16(rate).BytesForMS(20)
	for off := 0; off < len(pcm); off += frame {
		end := off + frame
		if end > len(pcm) {
			end = len(pcm)
		}
		if err := s.Write(pcm[off:end]); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CloseSend(); err != nil {
		t.Fatal(err)
	}
	var final string
	deadline := time.After(2 * time.Second)
	for {
		select {
		case r, ok := <-s.Results():
			if !ok {
				return final
			}
			if r.Final {
				final = r.Text
			}
		case <-deadline:
			t.Fatal("no final transcript")
		}
	}
}

func TestKeyedPhrasesAreChosenByPitch(t *testing.T) {
	phrases := []string{"帮我查一下车险多少钱", "我想问一下", "嗯", "等一下我接个电话"}
	for rate := range map[int]bool{16000: true, 24000: true} {
		for k, want := range phrases {
			a := NewASR("")
			a.Phrases = phrases
			a.VoicedOnly = true
			// Enough audio for the whole phrase at 180 ms a rune, preceded by
			// silence the way the detector's pre-roll arrives.
			ms := len([]rune(want))*180 + 40
			pcm := append(make([]byte, audio.PCM16(rate).BytesForMS(240)), tone(rate, PhraseHz(k), ms)...)
			if got := hear(t, a, pcm, rate); got != want {
				t.Errorf("rate %d, tone %.0f Hz: heard %q, want %q", rate, PhraseHz(k), got, want)
			}
		}
	}
}

func TestKeyedShortMurmurIsHeardWhole(t *testing.T) {
	a := NewASR("")
	a.Phrases = []string{"帮我查一下", "嗯"}
	a.VoicedOnly = true
	// A 400 ms murmur: longer than its one rune needs; the phrase does not
	// grow past its end.
	if got := hear(t, a, tone(16000, PhraseHz(1), 400), 16000); got != "嗯" {
		t.Errorf("heard %q, want 嗯", got)
	}
}

func TestUnkeyedRecognizerIsUnchanged(t *testing.T) {
	a := NewASR("你好")
	a.VoicedOnly = true
	if got := hear(t, a, tone(16000, 500, 400), 16000); got != "你好" {
		t.Errorf("heard %q, want 你好", got)
	}
}
