package duplex

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
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
	cfg.opts.Cfg.Duplex.OpenerQuestionPhrases = []string{"好的，"}
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
	// The opener's audio is capped: the answer's first word waits for its
	// end, so its length is on the caller's path (the 10:46 call of 10-26).
	m := regexp.MustCompile(`opened the answer from the phrase cache at the pause.*audio_ms=(\d+)`).FindStringSubmatch(logs.String())
	if m == nil {
		t.Fatal("the opener's length was not logged")
	}
	if n, _ := strconv.Atoi(m[1]); n > openerMaxMS {
		t.Fatalf("the opener ran %d ms, over the %d cap", n, openerMaxMS)
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

// The caller pauses inside a sentence, the opener fires at the pause, and the
// caller goes on within a breath: the opener finishes as an 嗯 of listening,
// the half-sentence's answer is dropped silently (no truncation), and the
// continuation gets no second opener — one answer, to the whole sentence.
func TestAnOpenerBecomesABackchannelWhenTheCallerGoesOn(t *testing.T) {
	Phrases().Reset()
	t.Cleanup(Phrases().Reset)
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Opener = true
	cfg.opts.Cfg.Duplex.OpenerPhrases = []string{"嗯，"}
	cfg.opts.Cfg.Duplex.OpenerQuestionPhrases = []string{"嗯，"}
	cfg.opts.Cfg.Duplex.PhraseCache = true
	cfg.opts.Cfg.Duplex.AllowBargeIn = true
	cfg.opts.Cfg.Duplex.OnNewQuery = "cut"
	rate := cfg.opts.Cfg.ClientRate
	tts := mock.NewTTS()
	if _, err := WarmPhrases(context.Background(), cfg.opts.Cfg, tts, nil); err != nil {
		t.Fatal(err)
	}
	c := newCollector(rate)
	asr := newTrailingASR("我想问一下保险多少钱")
	asr.msPerRune = 150
	llm := mock.NewLLM()
	llm.FirstTokenDelay = 600 * time.Millisecond
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := startEngine(t, Options{Cfg: cfg.opts.Cfg, Delegation: cfg.opts.Delegation, Speculative: true, Language: cfg.opts.Cfg.Language, Voice: ResolveVoice(cfg.opts.Cfg, "")},
		Deps{ASR: asr, LLM: llm, TTS: tts, Emit: c.emit, Log: logger})
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	speak(e, rate, 600) // 我想问一下
	pause(e, rate, 560) // the turn closes at 400; the opener plays
	speak(e, rate, 700) // …保险多少钱 — the caller went on, a short fragment
	pause(e, rate, 1500)
	waitFor(t, "the answer", 6*time.Second, func() bool { return strings.Contains(c.allOutput(), "让我确认一下") })
	if !strings.Contains(logs.String(), "the opener became a backchannel") {
		t.Fatalf("the caller going on was not read as continuing the sentence")
	}
	if cut := c.count(live.ExtAudioTruncated); cut != 0 {
		t.Fatalf("the opener was cut %d time(s); wanted it to finish as a backchannel", cut)
	}
	if n := strings.Count(c.allOutput(), "嗯"); n > 1 {
		t.Fatalf("the opener was said %d times across the sentence: %q", n, c.allOutput())
	}
}

// The model is told which opener the engine says, so its first clause
// continues from it.
func TestTheModelIsToldTheOpener(t *testing.T) {
	Phrases().Reset()
	t.Cleanup(Phrases().Reset)
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Opener = true
	cfg.opts.Cfg.Duplex.OpenerPhrases = []string{"好的，"}
	cfg.opts.Cfg.Duplex.OpenerQuestionPhrases = []string{"这个嘛，"}
	cfg.opts.Cfg.Duplex.PhraseCache = true
	rate := cfg.opts.Cfg.ClientRate
	tts := mock.NewTTS()
	WarmPhrases(context.Background(), cfg.opts.Cfg, tts, nil)
	c := newCollector(rate)
	asr := newTrailingASR("保险多少钱")
	asr.msPerRune = 150
	llm := &quoteLLM{}
	e := startEngine(t, Options{Cfg: cfg.opts.Cfg, Delegation: cfg.opts.Delegation, Speculative: true, Language: cfg.opts.Cfg.Language, Voice: ResolveVoice(cfg.opts.Cfg, "")},
		Deps{ASR: asr, LLM: llm, TTS: tts, Emit: c.emit})
	speak(e, rate, 750)
	pause(e, rate, 1200)
	waitFor(t, "a request", 4*time.Second, func() bool { llm.mu.Lock(); defer llm.mu.Unlock(); return len(llm.requests) > 0 })
	llm.mu.Lock()
	last := llm.requests[len(llm.requests)-1].Messages
	llm.mu.Unlock()
	note := last[len(last)-1]
	if note.Role != provider.RoleSystem || !strings.Contains(note.Content, "这个嘛，") || !strings.Contains(note.Content, "已由系统替你说出") {
		t.Fatalf("the model was not told the question's opener: %+v", note)
	}
}

// The model's opening 嗯嗯，好的， after the engine's 嗯， leaves nothing of
// either; a clause that opens with its own words is untouched.
func TestStripOpenersTakesEveryLeadingOpener(t *testing.T) {
	pools := [][]string{{"好的，", "嗯，", "嗯嗯，"}, {"对，"}}
	for _, tc := range []struct {
		text, opened, want string
		n                  int
	}{
		{"嗯嗯，好的，三者险是一千五。", "嗯，", "三者险是一千五。", 2},
		{"嗯，三者险是一千五。", "嗯嗯，", "三者险是一千五。", 1},
		{"好的，对，没问题。", "嗯，", "没问题。", 2},
		{"三者险是一千五。", "嗯，", "三者险是一千五。", 0},
	} {
		got, n := stripOpeners(tc.text, tc.opened, pools...)
		if got != tc.want || n != tc.n {
			t.Errorf("stripOpeners(%q, %q) = %q, %d; want %q, %d", tc.text, tc.opened, got, n, tc.want, tc.n)
		}
	}
}

// A turn given longer because its words read as unfinished still gets its
// opener at the base pause — a listener's 嗯 — while the answer itself waits
// for the turn to be judged over. The two turns over 1000 on the 09:31 call
// of 10-26 were 600 ms patience waits.
func TestTheOpenerIsSaidAtTheBasePauseDuringAPatienceWait(t *testing.T) {
	Phrases().Reset()
	t.Cleanup(Phrases().Reset)
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Opener = true
	cfg.opts.Cfg.Duplex.OpenerPhrases = []string{"嗯，"}
	cfg.opts.Cfg.Duplex.OpenerQuestionPhrases = []string{"嗯，"}
	cfg.opts.Cfg.Duplex.PhraseCache = true
	cfg.opts.Cfg.Duplex.SemanticEndpointing = true
	cfg.opts.Cfg.Duplex.PatientSilenceMS = 1000
	cfg.opts.Cfg.Duplex.SpeculateWhilePatient = true
	rate := cfg.opts.Cfg.ClientRate
	tts := mock.NewTTS()
	WarmPhrases(context.Background(), cfg.opts.Cfg, tts, nil)
	c := newCollector(rate)
	asr := newTrailingASR("就是") // reads as unfinished: the turn waits 1000 ms
	asr.fullStop = true
	llm := mock.NewLLM()
	e := startEngine(t, Options{Cfg: cfg.opts.Cfg, Delegation: cfg.opts.Delegation, Speculative: true, Language: cfg.opts.Cfg.Language, Voice: ResolveVoice(cfg.opts.Cfg, "")},
		Deps{ASR: asr, LLM: llm, TTS: tts, Emit: c.emit})
	speak(e, rate, 600)
	stopped := time.Now()
	// 650 ms of silence: past the base pause (400), inside the patience wait.
	pause(e, rate, 650)
	first := c.firstAudioAt()
	if first.IsZero() || first.Sub(stopped) > 620*time.Millisecond {
		t.Fatalf("the opener was not said at the base pause during the patience wait (first audio at %v)", first.Sub(stopped))
	}
	if strings.Contains(c.allOutput(), "我听到你说") {
		t.Fatalf("the answer began before the turn was judged over: %q", c.allOutput())
	}
	pause(e, rate, 900)
	waitFor(t, "the answer", 4*time.Second, func() bool { return strings.Contains(c.allOutput(), "我听到你说") })
	if n := strings.Count(c.allOutput(), "嗯"); n != 1 {
		t.Fatalf("the opener was said %d times: %q", n, c.allOutput())
	}
}
