package duplex

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
	"github.com/chuanmingliu/golive/internal/tools"
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

// The opener fits the caller's words when the lead service has a scenario
// for them: a question about the price opens with the price scenario's
// phrase, from the cache, at the pause; the phrases are cached at the
// session's start (the owner, 11:27 on 10-27, after ConvFill).
func TestTheOpenerFitsTheCallersWordsFromTheLeadServicesScenarios(t *testing.T) {
	Phrases().Reset()
	t.Cleanup(Phrases().Reset)
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/configs/openers" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"config":{"name":"openers","version":3,"body":{"scenarios":[{"id":"price","keywords":["多少钱","多少","价格"],"phrases":["价格的话，"]}]}}}`))
	}))
	t.Cleanup(svc.Close)
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Opener = true
	cfg.opts.Cfg.Duplex.OpenerPhrases = []string{"好的，"}
	cfg.opts.Cfg.Duplex.OpenerQuestionPhrases = []string{"嗯，"}
	cfg.opts.Cfg.Duplex.PhraseCache = true
	cfg.opts.Cfg.ToolsBaseURL = svc.URL
	rate := cfg.opts.Cfg.ClientRate
	tts := mock.NewTTS()
	if _, err := WarmPhrases(context.Background(), cfg.opts.Cfg, tts, nil); err != nil {
		t.Fatal(err)
	}
	c := newCollector(rate)
	asr := newTrailingASR("这个一共多少钱")
	asr.msPerRune = 150
	llm := mock.NewLLM()
	llm.FirstTokenDelay = 700 * time.Millisecond
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := startEngine(t, Options{Cfg: cfg.opts.Cfg, Delegation: cfg.opts.Delegation, Speculative: true, Language: cfg.opts.Cfg.Language, Voice: ResolveVoice(cfg.opts.Cfg, "")},
		Deps{ASR: asr, LLM: llm, TTS: tts, Emit: c.emit, Log: logger})
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	waitFor(t, "the openers cached", 3*time.Second, func() bool { return strings.Contains(logs.String(), "openers cached") })

	speak(e, rate, 1000)
	closed := time.Now()
	pause(e, rate, 1200)
	waitFor(t, "the answer", 5*time.Second, func() bool { return strings.Contains(c.allOutput(), "让我确认一下") })
	said := strings.TrimSpace(c.allOutput())
	if !strings.HasPrefix(said, "价格的话，") {
		t.Fatalf("the answer did not open with the price scenario's phrase: %q", said)
	}
	if strings.Contains(said, "好的，") || strings.Contains(said, "嗯，") {
		t.Fatalf("the profile's pool was used beside the scenario's: %q", said)
	}
	if d := c.firstAudioAt().Sub(closed); d > 800*time.Millisecond {
		t.Fatalf("first audio %v after the caller stopped: the scenario's phrase was not in the cache", d)
	}
	if !strings.Contains(logs.String(), "scenario=price") {
		t.Fatal("the scenario was not logged")
	}
}

// The content figure is when the answer's own first word is heard — the
// synthesizer's first audio for the first segment — not when that segment was
// handed to the synthesizer. Until 10-27 it was the latter, and on a turn the
// model's clause binds (the opener ends before the clause is in) the figure
// was short by the synthesizer's first-audio time, ~200 ms on a call.
func TestTheContentFigureIsTheAnswersFirstAudioNotItsRequest(t *testing.T) {
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
	// From here on the synthesizer is slow to its first audio.
	tts.FirstByteDelay = 1500 * time.Millisecond
	c := newCollector(rate)
	asr := newTrailingASR("今天天气怎么样")
	asr.msPerRune = 150
	llm := mock.NewLLM()
	llm.FirstTokenDelay = 700 * time.Millisecond // the clause, not the opener, binds
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := startEngine(t, Options{Cfg: cfg.opts.Cfg, Delegation: cfg.opts.Delegation, Speculative: true, Language: cfg.opts.Cfg.Language, Voice: ResolveVoice(cfg.opts.Cfg, "")},
		Deps{ASR: asr, LLM: llm, TTS: tts, Emit: c.emit, Log: logger})
	speak(e, rate, 1000)
	pause(e, rate, 1800)
	waitFor(t, "the answer", 6*time.Second, func() bool { return strings.Contains(c.allOutput(), "让我确认一下") })
	turn := e.tracker.Current()
	if turn == nil {
		t.Fatal("no current turn")
	}
	tm := turn.Timings()
	if tm.FirstContent.IsZero() || tm.FirstToken.IsZero() {
		t.Fatalf("timings not stamped: %+v", tm)
	}
	if d := tm.FirstContent.Sub(tm.FirstToken); d < 1400*time.Millisecond {
		t.Fatalf("the content stamp is only %v after the first token; the synthesizer took 1500 ms to its first audio, so the stamp is the request, not the audio", d)
	}
}

// A rendering opens with a quiet head before its first syllable. The opener
// is said at the pause and the answer's first segment follows it, so both
// heads are dead air the caller waits through — cut to 20 ms, and the amount
// is logged so a call shows what it was worth.
func TestTheQuietHeadOfTheOpenerAndTheFirstSegmentIsCut(t *testing.T) {
	Phrases().Reset()
	t.Cleanup(Phrases().Reset)
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Opener = true
	cfg.opts.Cfg.Duplex.OpenerPhrases = []string{"好的，"}
	cfg.opts.Cfg.Duplex.OpenerQuestionPhrases = []string{"好的，"}
	cfg.opts.Cfg.Duplex.PhraseCache = true
	rate := cfg.opts.Cfg.ClientRate
	tts := mock.NewTTS()
	tts.LeadSilence = 200 * time.Millisecond
	if added, err := WarmPhrases(context.Background(), cfg.opts.Cfg, tts, nil); err != nil || added == 0 {
		t.Fatalf("warming added %d, err %v", added, err)
	}
	c := newCollector(rate)
	asr := newTrailingASR("今天天气怎么样")
	asr.msPerRune = 150
	llm := mock.NewLLM()
	llm.FirstTokenDelay = 300 * time.Millisecond
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := startEngine(t, Options{Cfg: cfg.opts.Cfg, Delegation: cfg.opts.Delegation, Speculative: true, Language: cfg.opts.Cfg.Language, Voice: ResolveVoice(cfg.opts.Cfg, "")},
		Deps{ASR: asr, LLM: llm, TTS: tts, Emit: c.emit, Log: logger})
	speak(e, rate, 1000)
	pause(e, rate, 1500)
	waitFor(t, "the answer", 6*time.Second, func() bool { return strings.Contains(c.allOutput(), "让我确认一下") })
	lead := func(line string) int {
		m := regexp.MustCompile(line + `.*\blead_ms=(\d+)`).FindStringSubmatch(logs.String())
		if m == nil {
			t.Fatalf("no lead_ms on %q:\n%s", line, logs.String())
		}
		n, _ := strconv.Atoi(m[1])
		return n
	}
	if n := lead("opened the answer from the phrase cache at the pause"); n < 160 || n > 200 {
		t.Fatalf("the opener's quiet head: %d ms cut; wanted ~180 of the 200 rendered", n)
	}
	if n := lead("speak: first audio for segment"); n < 160 || n > 200 {
		t.Fatalf("the first segment's quiet head: %d ms cut; wanted ~180 of the 200 rendered", n)
	}
	// The opener's own audio is the word, not the head: 好的， is 330 ms of
	// voice in this synthesizer.
	m := regexp.MustCompile(`opened the answer from the phrase cache at the pause.*audio_ms=(\d+)`).FindStringSubmatch(logs.String())
	if n, _ := strconv.Atoi(m[1]); n < 330 || n > 360 {
		t.Fatalf("the opener ran %d ms; wanted 330 of voice + 20 of head", n)
	}
}

// The model opens with the same words as the engine's opener; they are
// stripped, and the answer's first word must still be treated as a first
// segment: short, allowed to end on a comma, bounded by the deadline. Until
// 10-27 the stripped opener counted as the first segment spoken and the
// first word waited for a strong boundary at stream_min_chunk_chars (24):
// 640 and 840 ms after the first token on two turns of the 21:31 call.
func TestTheFirstWordAfterAStrippedOpenerIsStillAFirstSegment(t *testing.T) {
	Phrases().Reset()
	t.Cleanup(Phrases().Reset)
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Opener = true
	cfg.opts.Cfg.Duplex.OpenerPhrases = []string{"好的，"}
	cfg.opts.Cfg.Duplex.OpenerQuestionPhrases = []string{"好的，"}
	cfg.opts.Cfg.Duplex.PhraseCache = true
	cfg.opts.Cfg.Duplex.StreamFirstChunkChars = 4
	cfg.opts.Cfg.Duplex.StreamMinChunkChars = 24
	rate := cfg.opts.Cfg.ClientRate
	tts := mock.NewTTS()
	if added, err := WarmPhrases(context.Background(), cfg.opts.Cfg, tts, nil); err != nil || added == 0 {
		t.Fatalf("warming added %d, err %v", added, err)
	}
	c := newCollector(rate)
	asr := newTrailingASR("今天天气怎么样")
	asr.msPerRune = 150
	// The mock's answer is 好的，我听到你说今天天气怎么样。让我确认一下细节，然后给你一个完整的答复。
	// With 好的， stripped, the first word's clause is 13 characters to a 。;
	// the strong boundary at ≥24 is 20 characters further on.
	llm := mock.NewLLM()
	llm.FirstTokenDelay = 300 * time.Millisecond
	llm.DelayPerRune = 20 * time.Millisecond
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
	pause(e, rate, 1500)
	waitFor(t, "the answer", 6*time.Second, func() bool { return strings.Contains(c.allOutput(), "让我确认一下") })
	if !strings.Contains(logs.String(), "the model's opener was already said at the pause") {
		t.Fatal("the model's opener was not stripped; the test does not exercise the case")
	}
	turn := e.tracker.Current()
	if turn == nil {
		t.Fatal("no current turn")
	}
	tm := turn.Timings()
	// 13 characters at 20 ms is ~260 ms; the whole sentence to the strong
	// boundary is ~660.
	if d := tm.FirstContent.Sub(tm.FirstToken); d > 450*time.Millisecond {
		t.Fatalf("the answer's first word came %v after the first token; it waited for a later segment's boundary instead of the first segment's", d)
	}
	if n := strings.Count(c.allOutput(), "好的"); n != 1 {
		t.Fatalf("the opener was said %d times: %q", n, c.allOutput())
	}
}

// With opener_on_fragments, the short fragment after a backchanneled opener
// gets an opener of another kind: 嗯，… then 好的，…, never 嗯，嗯. Its first
// audio is at the pause again instead of at the model's.
func TestAFragmentAfterABackchannelOpensWithAPhraseOfAnotherKind(t *testing.T) {
	Phrases().Reset()
	t.Cleanup(Phrases().Reset)
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Opener = true
	cfg.opts.Cfg.Duplex.OpenerOnFragments = true
	cfg.opts.Cfg.Duplex.OpenerPhrases = []string{"嗯，", "好的，"}
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
	llm.FirstTokenDelay = 900 * time.Millisecond // the answer itself is late; the opener must not wait for it
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := startEngine(t, Options{Cfg: cfg.opts.Cfg, Delegation: cfg.opts.Delegation, Speculative: true, Language: cfg.opts.Cfg.Language, Voice: ResolveVoice(cfg.opts.Cfg, "")},
		Deps{ASR: asr, LLM: llm, TTS: tts, Emit: c.emit, Log: logger})
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	speak(e, rate, 600) // 我想问一下 — reads as a question: 嗯，
	pause(e, rate, 560) // the turn closes at 400; the opener plays
	speak(e, rate, 700) // …保险多少钱 — a short fragment
	stopped := time.Now()
	pause(e, rate, 1800)
	waitFor(t, "the answer", 6*time.Second, func() bool { return strings.Contains(c.allOutput(), "让我确认一下") })
	if !strings.Contains(logs.String(), "the opener became a backchannel") {
		t.Fatalf("the caller going on was not read as continuing the sentence")
	}
	if !strings.Contains(logs.String(), "a fragment after a backchannel opens with a phrase of another kind") {
		t.Fatalf("the fragment got no opener of another kind:\n%s", logs.String())
	}
	said := c.allOutput()
	if strings.Count(said, "嗯") != 1 || strings.Count(said, "好的") != 1 {
		t.Fatalf("wanted 嗯 once (the backchannel) and 好的 once (the fragment's opener): %q", said)
	}
	// The fragment's opener was said at its pause, not at the model's 900 ms.
	m := regexp.MustCompile(`a fragment after a backchannel opens.*`).FindStringIndex(logs.String())
	if m == nil {
		t.Fatal("no log line")
	}
	if d := time.Since(stopped); d < 0 {
		t.Fatal("clock")
	}
	turn := e.tracker.Current()
	if turn == nil {
		t.Fatal("no current turn")
	}
	tm := turn.Timings()
	if tm.FirstAudio.IsZero() || tm.FirstToken.IsZero() || !tm.FirstAudio.Before(tm.FirstToken) {
		t.Fatalf("the fragment's first audio (%v) was not before the model's first token (%v)", tm.FirstAudio, tm.FirstToken)
	}
}

// oneDeltaLLM delivers the whole reply as a single delta.
type oneDeltaLLM struct {
	text  string
	delay time.Duration
}

func (l *oneDeltaLLM) Name() string { return "onedelta" }
func (l *oneDeltaLLM) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	out := make(chan provider.LLMDelta, 4)
	go func() {
		defer close(out)
		select {
		case <-ctx.Done():
			return
		case <-time.After(l.delay):
		}
		out <- provider.LLMDelta{Text: l.text}
		out <- provider.LLMDelta{Usage: &provider.TokenUsage{InputTokens: 1, OutputTokens: len([]rune(l.text))}}
	}()
	return out, nil
}

// One delta of the model's can carry several segments at once (a reply
// that arrives whole). When the first of them is the opener, stripped, the
// restart must wait until the other segments cut from that delta are
// spoken — found by review on 10-27: the restart re-pushed the buffer and
// put the model's later "好的，再见" before its first sentence.
func TestSegmentsStayInOrderWhenOneDeltaCarriesSeveralAndTheFirstIsStripped(t *testing.T) {
	Phrases().Reset()
	t.Cleanup(Phrases().Reset)
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Opener = true
	cfg.opts.Cfg.Duplex.OpenerPhrases = []string{"好的，"}
	cfg.opts.Cfg.Duplex.OpenerQuestionPhrases = []string{"好的，"}
	cfg.opts.Cfg.Duplex.PhraseCache = true
	cfg.opts.Cfg.Duplex.StreamFirstChunkChars = 3
	cfg.opts.Cfg.Duplex.StreamMinChunkChars = 24
	rate := cfg.opts.Cfg.ClientRate
	tts := mock.NewTTS()
	if added, err := WarmPhrases(context.Background(), cfg.opts.Cfg, tts, nil); err != nil || added == 0 {
		t.Fatalf("warming added %d, err %v", added, err)
	}
	c := newCollector(rate)
	asr := newTrailingASR("今天天气怎么样")
	asr.msPerRune = 150
	llm := &oneDeltaLLM{text: "好的，我听到你说今天天气怎么样。让我确认一下细节，然后给你一个完整的答复。好的，再见。", delay: 300 * time.Millisecond}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := startEngine(t, Options{Cfg: cfg.opts.Cfg, Delegation: cfg.opts.Delegation, Speculative: true, Language: cfg.opts.Cfg.Language, Voice: ResolveVoice(cfg.opts.Cfg, "")},
		Deps{ASR: asr, LLM: llm, TTS: tts, Emit: c.emit, Log: logger})
	speak(e, rate, 1000)
	pause(e, rate, 1500)
	waitFor(t, "the answer", 8*time.Second, func() bool { return strings.Contains(c.allOutput(), "再见") })
	said := c.allOutput()
	if !strings.Contains(logs.String(), "the model's opener was already said at the pause") {
		t.Fatalf("the model's opener was not stripped; the test does not exercise the case:\n%s", logs.String())
	}
	want := "好的，我听到你说今天天气怎么样。让我确认一下细节，然后给你一个完整的答复。好的，再见。"
	if strings.ReplaceAll(said, " ", "") != want {
		t.Fatalf("words out of order or doubled:\n got %q\nwant %q", said, want)
	}
}

// quietTTS renders one marked text as near-silence (every sample = 1), as a
// vendor would a rendering with nothing above -40 dBFS; everything else goes
// to the mock.
type quietTTS struct {
	inner *mock.TTS
	quiet string
}

func (q *quietTTS) Name() string { return q.inner.Name() }
func (q *quietTTS) Open(ctx context.Context, opts provider.TTSOptions) (provider.TTSStream, error) {
	s, err := q.inner.Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &quietStream{TTSStream: s, quiet: q.quiet}, nil
}

type quietStream struct {
	provider.TTSStream
	quiet string
}

func (s *quietStream) Synthesize(ctx context.Context, text string) (<-chan provider.TTSChunk, error) {
	if !strings.Contains(text, s.quiet) {
		return s.TTSStream.Synthesize(ctx, text)
	}
	out := make(chan provider.TTSChunk, 4)
	go func() {
		defer close(out)
		n := s.SampleRate() * 300 / 1000 // 300 ms
		pcm := make([]byte, n*2)
		for i := 0; i < n; i++ {
			pcm[2*i] = 1
		}
		for _, part := range [][]byte{pcm[:len(pcm)/2], pcm[len(pcm)/2:]} {
			select {
			case <-ctx.Done():
				return
			case out <- provider.TTSChunk{PCM: part}:
			}
		}
	}()
	return out, nil
}

// A first segment whose whole rendering is under the silence floor is still
// audio: it is played (what was kept of it), the segment is recorded as
// spoken and the synthesis session is kept. Found by review on 10-27: the
// lead trim held every chunk back, the pipe took it for "no audio", dropped
// the session (two reconnects) and left the sentence out of the transcript.
func TestAnAllQuietFirstSegmentStillCounts(t *testing.T) {
	Phrases().Reset()
	t.Cleanup(Phrases().Reset)
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Opener = true
	cfg.opts.Cfg.Duplex.OpenerPhrases = []string{"好的，"}
	cfg.opts.Cfg.Duplex.OpenerQuestionPhrases = []string{"好的，"}
	cfg.opts.Cfg.Duplex.PhraseCache = true
	rate := cfg.opts.Cfg.ClientRate
	inner := mock.NewTTS()
	if added, err := WarmPhrases(context.Background(), cfg.opts.Cfg, inner, nil); err != nil || added == 0 {
		t.Fatalf("warming added %d, err %v", added, err)
	}
	tts := &quietTTS{inner: inner, quiet: "我听到你说"}
	c := newCollector(rate)
	asr := newTrailingASR("今天天气怎么样")
	asr.msPerRune = 150
	llm := mock.NewLLM()
	llm.FirstTokenDelay = 300 * time.Millisecond
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
	pause(e, rate, 1500)
	waitFor(t, "the answer", 8*time.Second, func() bool { return strings.Contains(c.allOutput(), "让我确认一下") })
	opens := inner.Opens()
	if strings.Contains(logs.String(), "produced no audio") {
		t.Fatalf("the quiet segment was taken for no audio")
	}
	if !strings.Contains(logs.String(), "all quiet; playing what was kept") {
		t.Fatalf("the quiet segment was not played from what was kept")
	}
	if !strings.Contains(c.allOutput(), "我听到你说") {
		t.Fatalf("the quiet sentence is missing from what was said: %q", c.allOutput())
	}
	// One open for the warm, one for the session; a third is the reconnect
	// after a dropped session.
	if opens != 2 {
		t.Fatalf("the synthesis session was reopened: %d opens, wanted 2", opens)
	}
}

// When an opener becomes a backchannel, the player drops the turn's queue and
// lets the paced audio already in the air (up to playback_lead_ms) play out;
// through that drain the player is still Active, but the floor is free for
// the next turn's opener — on the 22:05 simulated call of 10-27 five
// fragments closed inside that drain and got no opener (first audio
// 742–1131 ms). The new turn's opener is accepted and played at once.
func TestTheFloorIsFreeForTheNextOpenerWhileABackchannelDrains(t *testing.T) {
	var mu sync.Mutex
	heard := map[string]int{}
	p := NewPlayer(PlayerConfig{Rate: 24000, ChunkMS: 40, Paced: true, LeadMS: 300},
		func(id string, pcm []byte, _ time.Time) {
			mu.Lock()
			heard[id] += len(pcm)
			mu.Unlock()
		})
	bytesFor := func(id string) int {
		mu.Lock()
		defer mu.Unlock()
		return heard[id]
	}
	var gen Generation
	go p.Run(&gen)
	defer p.Close()

	// The opener, then the guessed answer behind it: two seconds of audio.
	twoSeconds := make([]byte, 2*2*24000)
	p.Enqueue(Segment{Kind: SegBegin, Gen: gen.Current(), TurnID: "item_1", Text: "嗯，"})
	p.Enqueue(Segment{Kind: SegAudio, Gen: gen.Current(), TurnID: "item_1", PCM: twoSeconds})
	waitFor(t, "the opener to be heard", 2*time.Second, func() bool { return bytesFor("item_1") > 24000/2 })

	// The caller went on: the opener stands, the rest is dropped.
	p.FinishOpenerAsBackchannel("item_1")
	if p.PendingMS() <= 0 {
		t.Fatal("nothing in the air after the backchannel; the drain is not exercised")
	}
	if !p.Active() {
		t.Fatal("the player should still be active while its paced audio drains")
	}
	if !p.FreeForOpener() {
		t.Fatal("the floor is not free for the next opener during the drain")
	}
	// The fragment's turn opens now, not after the drain.
	p.Enqueue(Segment{Kind: SegBegin, Gen: gen.Current(), TurnID: "item_2", Text: "好的，"})
	p.Enqueue(Segment{Kind: SegAudio, Gen: gen.Current(), TurnID: "item_2", PCM: make([]byte, 2*24000/2)})
	waitFor(t, "the next opener to be heard", 2*time.Second, func() bool { return bytesFor("item_2") > 0 })
	if p.FreeForOpener() {
		t.Fatal("the floor reads free while the next turn's opener is playing")
	}
	p.Enqueue(Segment{Kind: SegMark, Gen: gen.Current(), TurnID: "item_2", Text: "好的，"})
	p.Enqueue(Segment{Kind: SegEnd, Gen: gen.Current(), TurnID: "item_2"})
	waitFor(t, "the floor to be released", 3*time.Second, func() bool { return !p.Active() })
}

// A revised turn inherits the opener said under the earlier revision with
// the stamps of its audio, so its first audio is the opener's, not its
// content's.
func TestARevisedTurnInheritsTheOpenersStamps(t *testing.T) {
	var gen Generation
	tr := NewTracker(&gen)
	spec := tr.Begin("今天天气", true)
	spec.Opened, spec.OpenedMS, spec.OpenedAt = "好的，", 430, time.Now()
	spec.MarkFirstSegment()
	spec.MarkFirstAudio()
	spec.MarkFirstAudioOut()
	before := spec.Timings()
	time.Sleep(20 * time.Millisecond)
	revised := tr.Revise(spec.ID, "今天天气怎么样", true)
	if revised == nil {
		t.Fatal("no revision")
	}
	revised.InheritOpener(spec)
	tm := revised.Timings()
	if revised.Opened != "好的，" || revised.OpenedMS != 430 || revised.OpenerPlanned != "好的，" {
		t.Fatalf("the opener was not inherited: %+v", revised)
	}
	if !tm.FirstAudioOut.Equal(before.FirstAudioOut) || !tm.FirstAudio.Equal(before.FirstAudio) || !tm.FirstSegment.Equal(before.FirstSegment) {
		t.Fatalf("the opener's stamps were not inherited: %v vs %v", tm.FirstAudioOut, before.FirstAudioOut)
	}
	// A later stamp on the revised turn does not move them.
	revised.MarkFirstAudioOut()
	if !revised.Timings().FirstAudioOut.Equal(before.FirstAudioOut) {
		t.Fatal("the inherited first audio out was overwritten")
	}
}

// Openers rotate: the least recently said phrase of the pool is chosen,
// never the one said last, and once a small scenario pool has been used up
// the profile's generic pool joins in — so five price questions in a row do
// not hear 保费呢， twice while 费用上， waits (the owner, 10:07 on 10-28).
func TestOpenersRotateThroughThePoolBeforeRepeating(t *testing.T) {
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Opener = true
	cfg.opts.Cfg.Duplex.OpenerPhrases = []string{"好的，", "嗯，"}
	cfg.opts.Cfg.Duplex.OpenerQuestionPhrases = []string{"嗯，", "好的，"}
	e := &Engine{opts: Options{Cfg: cfg.opts.Cfg}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	e.openers = tools.Openers{Scenarios: []tools.OpenerScenario{{ID: "price", Keywords: []string{"多少钱"}, Phrases: []string{"价格嘛，", "保费呢，", "费用上，"}}}}
	var said []string
	for i := 0; i < 7; i++ {
		turn := &Turn{ID: fmt.Sprintf("item_%d", i), Transcript: "这个多少钱"}
		said = append(said, e.planOpener(turn))
	}
	// The first three are the scenario's three, each once.
	first := map[string]bool{}
	for _, p := range said[:3] {
		first[p] = true
	}
	if len(first) != 3 || first["嗯，"] || first["好的，"] {
		t.Fatalf("the scenario's phrases were not each said once first: %v", said)
	}
	// Then the generic pool joins while all three are within the last three
	// turns: the fourth is 嗯，/好的，; by the fifth the first scenario phrase
	// has left the window and comes back.
	if said[3] != "嗯，" && said[3] != "好的，" {
		t.Fatalf("the generic pool did not join once the scenario's was all recent: %v", said)
	}
	if said[4] != said[0] {
		t.Fatalf("the scenario's phrase did not come back once out of the window: %v", said)
	}
	// Never the same twice running.
	for i := 1; i < len(said); i++ {
		if said[i] == said[i-1] {
			t.Fatalf("an opener repeated at once: %v", said)
		}
	}
}
