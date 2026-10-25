package duplex

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

// The opener: when the caller's turn is judged over and the model's first
// clause is not in yet, the engine says a warmed opener from the phrase cache
// at the pause; the model's own opening, if it is the same words, is said
// once. The caller hears a word ~at the pause rather than ~500 ms after it.
func TestTheAnswerOpensFromTheCacheAtThePause(t *testing.T) {
	Phrases().Reset()
	t.Cleanup(Phrases().Reset)
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Opener = true
	cfg.opts.Cfg.Duplex.OpenerPhrases = []string{"好的，"}
	cfg.opts.Cfg.Duplex.PhraseCache = true
	rate := cfg.opts.Cfg.ClientRate
	tts := mock.NewTTS()
	if added, err := WarmPhrases(context.Background(), cfg.opts.Cfg, tts, nil); err != nil || added == 0 {
		t.Fatalf("warming added %d, err %v", added, err)
	}
	c := newCollector(rate)
	asr := newTrailingASR("今天天气怎么样")
	asr.msPerRune = 150
	llm := mock.NewLLM()
	llm.FirstTokenDelay = 700 * time.Millisecond // a slow turn: the model's clause is well after the pause
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := startEngine(t, Options{Cfg: cfg.opts.Cfg, Delegation: cfg.opts.Delegation, Speculative: true, Language: cfg.opts.Cfg.Language, Voice: ResolveVoice(cfg.opts.Cfg, "")},
		Deps{ASR: asr, LLM: llm, TTS: tts, Emit: c.emit, Log: logger})
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	})

	speak(e, rate, 1000)
	closed := time.Now()
	pause(e, rate, 1200)
	waitFor(t, "the answer", 5*time.Second, func() bool { return strings.Contains(c.allOutput(), "让我确认一下") })
	said := c.allOutput()
	// Opened once, not "好的，好的，".
	if n := strings.Count(said, "好的"); n != 1 {
		t.Fatalf("the opener was said %d times: %q", n, said)
	}
	if !strings.HasPrefix(strings.TrimSpace(said), "好的，") {
		t.Fatalf("the answer did not open with the opener: %q", said)
	}
	// And it was said at the pause, not after the model's 700 ms.
	first := c.firstAudioAt()
	if first.IsZero() {
		t.Fatal("no audio")
	}
	if d := first.Sub(closed); d > 800*time.Millisecond {
		t.Fatalf("first audio %v after the caller stopped; wanted it at the pause (~400 ms), before the model's 700 ms", d)
	}
}

// withoutOpener: the model's own opener, punctuation aside, is dropped; a
// different opening is left alone.
func TestWithoutOpener(t *testing.T) {
	for _, tc := range []struct {
		text, opener, want string
		ok                 bool
	}{
		{"好的，没问题。", "好的，", "没问题。", true},
		{"好的。", "好的，", "", true},
		{"好的没问题", "好的，", "没问题", true},
		{"明白，没问题。", "好的，", "明白，没问题。", false},
	} {
		got, ok := withoutOpener(tc.text, tc.opener)
		if got != tc.want || ok != tc.ok {
			t.Errorf("withoutOpener(%q, %q) = %q, %v; want %q, %v", tc.text, tc.opener, got, ok, tc.want, tc.ok)
		}
	}
}

// firstAudioAt is when the first audio delta was emitted.
func (c *collector) firstAudioAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, ev := range c.events {
		if _, ok := ev.(live.OutputAudioDelta); ok {
			return c.at[i]
		}
	}
	return time.Time{}
}
