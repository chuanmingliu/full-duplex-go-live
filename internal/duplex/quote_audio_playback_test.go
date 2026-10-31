package duplex

import (
	"context"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/provider/mock"
)

func TestPreparedQuoteAudioUsesExactPCMAndActualOutputTimestamp(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.PlaybackPaced = false
	cfg.Duplex.PhraseCache = false
	tts := &urgencyTTS{inner: mock.NewTTS()}
	c := newCollector(cfg.ClientRate)
	e := New(Options{Cfg: cfg}, Deps{TTS: tts, Emit: c.emit})
	e.ctx = context.Background()
	t.Cleanup(func() { e.quoteAudio = nil; e.Close() })
	q := cacheFixture()
	q.prepare = func(context.Context, string) (preparedQuoteAudio, bool, error) {
		pcm := make([]byte, 3200)
		for i := 0; i < len(pcm); i += 2 {
			pcm[i+1] = 16
		}
		return preparedQuoteAudio{PhraseAudio{PCM: pcm, Rate: cfg.ClientRate}, "voice-A"}, true, nil
	}
	const text = "Specific quoted amount 123."
	q.consume([]quoteAudioResult{ready(text)})
	e.quoteAudio = q
	turn := e.tracker.Begin("", false)
	e.commitTurn(turn)
	turn.Readout = text
	turn.QuoteResult = true
	pipe := e.newSpeechPipe(turn, false)
	pipe.PushFixed(text, true)
	pipe.Close()
	awaitHandoffSignal(t, pipe.done)
	tts.mu.Lock()
	n := len(tts.marks)
	tts.mu.Unlock()
	if n != 0 {
		t.Fatalf("ready exact quote was synthesized again (%d calls)", n)
	}
	if !turn.Timings().FirstReadoutOut.IsZero() {
		t.Fatal("lookup stamped readout before PCM output")
	}
	seen := false
	for _, seg := range e.player.queue {
		if seg.Kind != SegAudio {
			continue
		}
		if !seg.readout || seg.owner != turn {
			t.Fatal("prepared audio lost exact readout owner")
		}
		e.player.playAudio(&e.gen, seg)
		seen = true
	}
	if !seen || turn.Timings().FirstReadoutOut.IsZero() {
		t.Fatal("actual prepared PCM output did not stamp readout")
	}
}

func TestPreparedQuoteAudioMissesChangedOrExpiredText(t *testing.T) {
	for _, expired := range []bool{false, true} {
		cfg := testConfig()
		cfg.Duplex.PhraseCache = false
		cfg.Duplex.PlaybackPaced = false
		tts := &urgencyTTS{inner: mock.NewTTS()}
		e := New(Options{Cfg: cfg}, Deps{TTS: tts, Emit: func(any) {}})
		e.ctx = context.Background()
		q := cacheFixture()
		q.consume([]quoteAudioResult{ready("Original quote 123.")})
		e.quoteAudio = q
		text := "Changed quote 456."
		if expired {
			text = "Original quote 123."
			entry := q.entries[text]
			entry.expires = time.Now().Add(-time.Second)
			q.entries[text] = entry
		}
		turn := e.tracker.Begin("", false)
		e.commitTurn(turn)
		pipe := e.newSpeechPipe(turn, false)
		pipe.PushFixed(text, true)
		pipe.Close()
		awaitHandoffSignal(t, pipe.done)
		tts.mu.Lock()
		n := len(tts.marks)
		tts.mu.Unlock()
		e.quoteAudio = nil
		e.Close()
		if n == 0 {
			t.Fatal("unvalidated or expired text hit the cache")
		}
	}
}

func TestQuoteReadoutDoesNotLearnIntoGlobalPhraseCache(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.PhraseCache = true
	cfg.Duplex.PhraseCacheLearnChars = 120
	cfg.Duplex.PlaybackPaced = false
	e := New(Options{Cfg: cfg}, Deps{TTS: mock.NewTTS(), Emit: func(any) {}})
	e.ctx = context.Background()
	defer e.Close()
	const text = "Private quote cache regression 92863."
	turn := e.tracker.Begin("", false)
	e.commitTurn(turn)
	pipe := e.newSpeechPipe(turn, false)
	pipe.PushFixed(text, true)
	pipe.Close()
	awaitHandoffSignal(t, pipe.done)
	if _, ok := Phrases().Get(e.phraseKey(text)); ok {
		t.Fatal("private quote audio entered process-wide cache")
	}
}

func TestPreparedQuoteAudioEmptyResampleFallsBack(t *testing.T) {
	cfg := testConfig()
	cfg.ClientRate = 8000
	cfg.Duplex.PlaybackPaced = false
	cfg.Duplex.PhraseCache = false
	tts := &urgencyTTS{inner: mock.NewTTS()}
	e := New(Options{Cfg: cfg}, Deps{TTS: tts, Emit: func(any) {}})
	e.ctx = context.Background()
	q := cacheFixture()
	const text = "Quoted amount 123."
	q.entries[text] = quoteAudioEntry{preparedQuoteAudio: preparedQuoteAudio{audio: PhraseAudio{PCM: []byte{1, 2}, Rate: 48000}, identity: "voice-A"}, expires: time.Now().Add(time.Minute)}
	e.quoteAudio = q
	turn := e.tracker.Begin("", false)
	e.commitTurn(turn)
	pipe := e.newSpeechPipe(turn, false)
	pipe.PushFixed(text, true)
	pipe.Close()
	awaitHandoffSignal(t, pipe.done)
	tts.mu.Lock()
	n := len(tts.marks)
	tts.mu.Unlock()
	e.quoteAudio = nil
	e.Close()
	if n != 1 {
		t.Fatal("unplayable cache entry dropped the quotation instead of synthesizing it")
	}
}
