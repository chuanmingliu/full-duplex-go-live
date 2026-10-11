package duplex

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

// trailingASR behaves the way the recognizer on the phone path was measured
// to: its hypotheses trail the audio by partialLag, and its final comes
// finalDelay after it is told the audio has ended — much the sooner of the
// two. Each connection hears the phrase from where the one before it stopped,
// as successive recognitions of one utterance do.
type trailingASR struct {
	phrase     []rune
	msPerRune  float64
	partialLag time.Duration
	finalDelay time.Duration

	// fullStop closes every final with "。", as a recognizer does with whatever
	// audio it is told has ended.
	fullStop bool

	mu     sync.Mutex
	offset int
	opens  int
}

func (a *trailingASR) Name() string { return "trailing" }

func (a *trailingASR) opened() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opens
}

func (a *trailingASR) Open(ctx context.Context, opts provider.ASROptions) (provider.ASRStream, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.opens++
	return &trailingStream{asr: a, rate: opts.SampleRate, from: a.offset,
		results: make(chan provider.ASRResult, 256)}, nil
}

type trailingStream struct {
	asr     *trailingASR
	rate    int
	from    int
	results chan provider.ASRResult

	mu       sync.Mutex
	voicedMS float64
	last     string
	sendDone bool
	closed   bool
}

// heard is the part of the phrase this connection's audio amounts to.
func (s *trailingStream) heard() string {
	n := int(s.voicedMS / s.asr.msPerRune)
	if s.from+n > len(s.asr.phrase) {
		n = len(s.asr.phrase) - s.from
	}
	return string(s.asr.phrase[s.from : s.from+n])
}

func (s *trailingStream) Write(pcm []byte) error {
	samples, err := audio.DecodePCM16(pcm)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendDone || s.closed {
		return nil
	}
	frame := s.rate / 50
	for off := 0; off+frame <= len(samples); off += frame {
		if audio.DBFS(audio.RMS(samples[off:off+frame])) > -40 {
			s.voicedMS += 20
		}
	}
	if text := s.heard(); text != s.last {
		s.last = text
		time.AfterFunc(s.asr.partialLag, func() { s.send(provider.ASRResult{Text: text}, false) })
	}
	return nil
}

func (s *trailingStream) send(r provider.ASRResult, final bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.results <- r
	if final {
		s.closed = true
		close(s.results)
	}
}

func (s *trailingStream) Results() <-chan provider.ASRResult { return s.results }

func (s *trailingStream) CloseSend() error {
	s.mu.Lock()
	if s.sendDone || s.closed {
		s.mu.Unlock()
		return nil
	}
	s.sendDone = true
	text := s.heard()
	s.mu.Unlock()

	s.asr.mu.Lock()
	s.asr.offset = s.from + len([]rune(text))
	s.asr.mu.Unlock()
	if s.asr.fullStop && text != "" {
		text += "。"
	}
	time.AfterFunc(s.asr.finalDelay, func() { s.send(provider.ASRResult{Text: text, Final: true}, true) })
	return nil
}

func (s *trailingStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.results)
	}
	return nil
}

func newTrailingASR(phrase string) *trailingASR {
	return &trailingASR{phrase: []rune(phrase), msPerRune: 150,
		partialLag: 450 * time.Millisecond, finalDelay: 60 * time.Millisecond}
}

func earlyFinalConfig(earlyMS int) (cfg struct {
	opts Options
}) {
	c := parityConfig()
	c.VAD.MinSilenceMS = 400
	c.Duplex.EarlyFinalMS = earlyMS
	cfg.opts = Options{Cfg: c, Delegation: live.DelegationResponses, Speculative: true}
	return cfg
}

func finalInputTranscripts(c *collector) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, ev := range c.events {
		if t, ok := ev.(live.InputTranscriptEvent); ok && t.Final {
			out = append(out, t.Text)
		}
	}
	return out
}

// The head start a cascade has is the pause: begin the answer in it, on the
// words so far. Against a recognizer whose hypotheses trail the audio that
// head start was never had — three hundred milliseconds into the pause the
// words so far were still short of the sentence, and the answer was begun from
// nothing after the turn had closed. Asking for the final early is what puts
// the whole sentence in hand while the pause is still running.
func TestAnswerIsBegunInThePauseOnAFinalAskedForEarly(t *testing.T) {
	const phrase = "今天天气怎么样"
	run := func(t *testing.T, earlyMS int) (*collector, *trailingASR, *Engine, int) {
		cfg := earlyFinalConfig(earlyMS)
		rate := cfg.opts.Cfg.ClientRate
		c := newCollector(rate)
		asr := newTrailingASR(phrase)
		e := startEngine(t, cfg.opts, Deps{ASR: asr, LLM: mock.NewLLM(), Emit: c.emit})
		speak(e, rate, 1200)
		pause(e, rate, 300) // a hundred short of ending the turn
		return c, asr, e, rate
	}
	whole := func(c *collector) bool {
		for _, h := range c.speculativeHints() {
			if normalizeForCompare(h) == phrase {
				return true
			}
		}
		return false
	}

	t.Run("left to the hypotheses, the sentence is not there to begin on", func(t *testing.T) {
		c, _, _, _ := run(t, 0)
		if whole(c) {
			t.Fatal("the whole sentence was guessed on inside the pause without an early final; this recognizer does not trail enough to prove anything")
		}
	})

	t.Run("with the final asked for early, it is", func(t *testing.T) {
		c, asr, e, rate := run(t, 120)
		if !whole(c) {
			t.Fatalf("300 ms into the pause no answer had been begun on the whole sentence; guesses so far: %q", c.speculativeHints())
		}
		if n := c.count(live.ExtSpeechStopped); n != 0 {
			t.Fatalf("the turn was taken %d time(s) before the pause had run out", n)
		}
		if ms := c.audioMS(); ms != 0 {
			t.Fatalf("%v ms of the answer was heard before the turn was the assistant's", ms)
		}

		pause(e, rate, 500)
		waitFor(t, "the turn to be confirmed", 2*time.Second, func() bool {
			return c.count(live.ServerDelegationCreated) == 1
		})
		ds := c.delegations()
		if got := normalizeForCompare(ds[0].Delegation.Transcript); got != phrase {
			t.Errorf("the turn answered is %q; the caller said %q", got, phrase)
		}
		if ds[0].Delegation.Reason != "speculative" {
			t.Errorf("the turn was answered from %q; the answer begun in the pause should have stood", ds[0].Delegation.Reason)
		}
		waitFor(t, "the answer", 3*time.Second, func() bool { return c.audioMS() > 0 })
		if finals := finalInputTranscripts(c); len(finals) != 1 || normalizeForCompare(finals[0]) != phrase {
			t.Errorf("final transcripts of the caller's turn: %q; want the sentence, once", finals)
		}
		if got := normalizeForCompare(c.inputTranscript()); got != phrase {
			t.Errorf("the append-only transcript reads %q", got)
		}
		if c.count(live.ServerError) != 0 {
			t.Errorf("errors: %v", c.errorCodes())
		}
		// One connection for the utterance, one listening in case the caller
		// carried on. Not one per frame of the pause.
		if n := asr.opened(); n > 3 {
			t.Errorf("the recognizer was dialled %d times for one utterance with one pause", n)
		}
	})
}

// The pause was not the end: the caller carried on. What they said before it
// was settled by a connection that has finished, and what they say after it is
// heard by another. The turn is both, once.
func TestWordsAfterAnEarlyFinalAreJoinedToTheWordsBeforeIt(t *testing.T) {
	const phrase = "你好帮我查一下明天的天气"
	cfg := earlyFinalConfig(120)
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR(phrase)
	e := startEngine(t, cfg.opts, Deps{ASR: asr, LLM: mock.NewLLM(), Emit: c.emit})

	speak(e, rate, 900)
	pause(e, rate, 220) // long enough for the early final, too short to end the turn
	speak(e, rate, 900)
	pause(e, rate, 800)

	waitFor(t, "the turn to be confirmed", 3*time.Second, func() bool {
		return c.count(live.ServerDelegationCreated) >= 1
	})
	time.Sleep(300 * time.Millisecond)
	if n := c.count(live.ExtSpeechStopped); n != 1 {
		t.Fatalf("the caller's one sentence was taken as %d turns", n)
	}
	ds := c.delegations()
	if len(ds) != 1 {
		t.Fatalf("%d turns were answered; the caller spoke one sentence with a pause in it", len(ds))
	}
	if got := normalizeForCompare(ds[0].Delegation.Transcript); got != phrase {
		t.Errorf("the turn answered is %q; the caller said %q", got, phrase)
	}
	if finals := finalInputTranscripts(c); len(finals) != 1 || normalizeForCompare(finals[0]) != phrase {
		t.Errorf("final transcripts of the caller's turn: %q; want the whole sentence, once", finals)
	}
	if got := normalizeForCompare(c.inputTranscript()); got != phrase {
		t.Errorf("the append-only transcript reads %q; want %q", got, phrase)
	}
	history := e.History()
	assertWellFormed(t, history)
	if len(history) == 0 || history[0].Role != provider.RoleUser || normalizeForCompare(history[0].Content) != phrase {
		t.Errorf("the conversation should open with the caller's whole sentence:\n%s", dump(history))
	}
	if asr.opened() < 2 {
		t.Errorf("the recognizer was dialled %d time(s); nothing was listening after the early final", asr.opened())
	}
	if c.count(live.ServerError) != 0 {
		t.Errorf("errors: %v", c.errorCodes())
	}
}

// A murmur after the pause — voice, and no word the recognizer makes anything
// of. The continuation heard something, so its own final is waited for; it
// comes back empty and the turn is what was said before the pause.
func TestAMurmurAfterAnEarlyFinalChangesNothing(t *testing.T) {
	const phrase = "今天天气怎么样"
	cfg := earlyFinalConfig(120)
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	e := startEngine(t, cfg.opts, Deps{ASR: newTrailingASR(phrase), LLM: mock.NewLLM(), Emit: c.emit})

	speak(e, rate, 1200)
	pause(e, rate, 200)
	speak(e, rate, 100)
	pause(e, rate, 800)

	waitFor(t, "the turn to be confirmed", 3*time.Second, func() bool {
		return c.count(live.ServerDelegationCreated) >= 1
	})
	time.Sleep(200 * time.Millisecond)
	ds := c.delegations()
	if len(ds) != 1 || normalizeForCompare(ds[0].Delegation.Transcript) != phrase {
		t.Fatalf("turns answered: %+v; want the one sentence", ds)
	}
	waitFor(t, "the answer", 3*time.Second, func() bool { return c.audioMS() > 0 })
	if c.count(live.ServerError) != 0 {
		t.Errorf("errors: %v", c.errorCodes())
	}
}

// The pause ends the turn before the early final has come back. It is then
// simply the turn's final, and the turn is answered on it — once.
func TestTurnClosingBeforeTheEarlyFinalArrivesIsStillAnswered(t *testing.T) {
	const phrase = "今天天气怎么样"
	cfg := earlyFinalConfig(120)
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR(phrase)
	asr.finalDelay = 600 * time.Millisecond
	e := startEngine(t, cfg.opts, Deps{ASR: asr, LLM: mock.NewLLM(), Emit: c.emit})

	speak(e, rate, 1200)
	pause(e, rate, 700)
	if n := c.count(live.ExtSpeechStopped); n != 1 {
		t.Fatalf("the utterance closed %d time(s) in a 700 ms pause", n)
	}
	waitFor(t, "the turn to be answered", 3*time.Second, func() bool {
		return c.count(live.ServerDelegationCreated) >= 1 && c.audioMS() > 0
	})
	time.Sleep(200 * time.Millisecond)
	ds := c.delegations()
	if len(ds) != 1 || normalizeForCompare(ds[0].Delegation.Transcript) != phrase {
		t.Fatalf("turns answered: %+v; want the one sentence", ds)
	}
	if finals := finalInputTranscripts(c); len(finals) != 1 || normalizeForCompare(finals[0]) != phrase {
		t.Errorf("final transcripts of the caller's turn: %q; want the sentence, once", finals)
	}
	if c.count(live.ServerError) != 0 {
		t.Errorf("errors: %v", c.errorCodes())
	}
}

func TestJoinTranscript(t *testing.T) {
	for _, tc := range []struct{ prefix, text, want string }{
		{"", "你好", "你好"},
		{"你好。", "", "你好。"},
		{"帮我查一下。", "明天的天气怎么样？", "帮我查一下，明天的天气怎么样？"},
		{"是吗？", "那明天呢", "是吗？那明天呢"},
		{"Check the weather.", "And the flights.", "Check the weather, And the flights."},
		{"帮我查一下", "明天", "帮我查一下明天"},
	} {
		if got := joinTranscript(tc.prefix, tc.text); got != tc.want {
			t.Errorf("joinTranscript(%q, %q) = %q; want %q", tc.prefix, tc.text, got, tc.want)
		}
	}
}

// Between asking for the final and getting it, the words in hand are the
// hypothesis that was trailing the audio. On the real recognizer a guess was
// made on them — "你好，今天天气" — fifty milliseconds before the final said
// "你好，今天天气怎么样？", and thrown away: a completion bought for nothing on
// every turn.
func TestNoGuessIsMadeOnTrailingWordsWhileTheFinalIsOnItsWay(t *testing.T) {
	const phrase = "今天天气怎么样"
	cfg := earlyFinalConfig(120)
	// The window for a guess on a hypothesis falls inside the final's round
	// trip, as it does with the phone profile and the real recognizer.
	cfg.opts.Cfg.Duplex.SpeculativeStableMS = 180
	cfg.opts.Cfg.Duplex.SpeculativeEagerMS = 160
	cfg.opts.Cfg.Duplex.SpeculativeSettleMS = 20
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR(phrase)
	asr.partialLag = 300 * time.Millisecond
	asr.finalDelay = 150 * time.Millisecond
	e := startEngine(t, cfg.opts, Deps{ASR: asr, LLM: mock.NewLLM(), Emit: c.emit})

	speak(e, rate, 1200)
	pause(e, rate, 900)
	waitFor(t, "the turn to be answered", 3*time.Second, func() bool {
		return c.count(live.ServerDelegationCreated) >= 1
	})
	for _, h := range c.speculativeHints() {
		if normalizeForCompare(h) != phrase {
			t.Errorf("a guess was made on %q, with the final for the whole sentence already asked for", h)
		}
	}
}

// A recognizer told the audio has ended puts a full stop on whatever it was
// given. Asked in a pause, that is punctuation by default, not a judgement —
// and read as one it made "就是。" a finished turn: on a real call it was
// answered, as were "呃。" and "我想问你一下啊。".
func TestTheFullStopOnAnEarlyFinalDoesNotEndTheSentence(t *testing.T) {
	const phrase = "就是那个保险多少钱"
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.SemanticEndpointing = true
	cfg.opts.Cfg.Duplex.PatientSilenceMS = 900
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR(phrase)
	asr.fullStop = true
	e := startEngine(t, cfg.opts, Deps{ASR: asr, LLM: mock.NewLLM(), Emit: c.emit})

	speak(e, rate, 340)  // 就是
	pause(e, rate, 550)  // longer than the 400 ms that ends a finished turn
	speak(e, rate, 1100) // 那个保险多少钱
	pause(e, rate, 1200)

	waitFor(t, "the turn to be answered", 3*time.Second, func() bool {
		return c.count(live.ServerDelegationCreated) >= 1
	})
	time.Sleep(200 * time.Millisecond)
	for _, h := range c.speculativeHints() {
		if normalizeForCompare(h) == "就是" {
			t.Errorf("an answer was begun on %q", h)
		}
	}
	if n := c.count(live.ExtSpeechStopped); n != 1 {
		t.Fatalf("the caller's one sentence was taken as %d turns", n)
	}
	ds := c.delegations()
	if len(ds) != 1 || normalizeForCompare(ds[0].Delegation.Transcript) != phrase {
		t.Fatalf("turns answered: %+v; want the one sentence", ds)
	}
}

// Talking over the assistant: whether that is an interruption is decided on
// words, and on a real call the words were a hypothesis 340 ms behind the
// audio — three syllables, a stop, and the turn had all but closed before
// anything said the caller had spoken. The final asked for early is those
// words, in time to give the floor up and begin the answer inside the pause.
func TestAnInterruptionIsJudgedOnAFinalAskedForEarly(t *testing.T) {
	const phrase = "你先停一停"
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.UserBackchannelHoldMS = 900
	cfg.opts.Greeting = "您好，我是车险管家小翠儿，看到您的爱车快到报价期了给您来电，想跟您确认几件事情"
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR(phrase)
	asr.partialLag = 2 * time.Second // nothing arrives before the pause is over
	e := startEngine(t, cfg.opts, Deps{ASR: asr, LLM: mock.NewLLM(), Emit: c.emit})
	e.Greet()
	waitFor(t, "the greeting to hold the floor", 5*time.Second, func() bool { return c.audioMS() > 200 })

	speak(e, rate, 800)
	pause(e, rate, 330) // inside the 400 ms that ends the turn
	if got := c.speculativeHints(); len(got) == 0 || normalizeForCompare(got[len(got)-1]) != phrase {
		t.Fatalf("330 ms after the caller stopped, no answer had been begun on what they said; guesses: %q", got)
	}
	if n := c.count(live.ExtSpeechStopped); n != 0 {
		t.Fatalf("the turn had already closed; this proves nothing about the pause")
	}
	pause(e, rate, 600)
	waitFor(t, "the turn to be answered", 3*time.Second, func() bool {
		return c.count(live.ServerDelegationCreated) == 1
	})
	if c.count(live.ServerError) != 0 {
		t.Errorf("errors: %v", c.errorCodes())
	}
}

// An afterthought — one syllable said after a pause — is speech too. The bar
// a continuation had to clear before it could be ended early was set above it,
// and on a real call "…多少钱？" + "行吗" had its answer begun only when the
// turn closed.
func TestAnAfterthoughtGetsItsOwnEarlyFinal(t *testing.T) {
	const phrase = "你直接说多少钱吗"
	cfg := earlyFinalConfig(120)
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR(phrase)
	asr.msPerRune = 120
	e := startEngine(t, cfg.opts, Deps{ASR: asr, LLM: mock.NewLLM(), Emit: c.emit})

	speak(e, rate, 900) // 你直接说多少钱
	pause(e, rate, 220) // the first early final
	speak(e, rate, 140) // 吗
	pause(e, rate, 300) // a hundred short of ending the turn
	whole := false
	for _, h := range c.speculativeHints() {
		whole = whole || normalizeForCompare(h) == phrase
	}
	if !whole {
		t.Fatalf("300 ms after the afterthought no answer had been begun on the whole of it; guesses: %q", c.speculativeHints())
	}
	if n := c.count(live.ExtSpeechStopped); n != 0 {
		t.Fatalf("the turn had already closed; this proves nothing about the pause")
	}
}

// "呃。" and then nothing. The caller drew breath to speak and has not yet;
// on a real call the answer to it was cut off 200 ms in by the sentence the
// hesitation was the start of.
func TestABareHesitationIsNotAnswered(t *testing.T) {
	run := func(t *testing.T, ignore bool) *collector {
		cfg := earlyFinalConfig(120)
		cfg.opts.Cfg.Duplex.IgnoreHesitations = ignore
		cfg.opts.Cfg.Duplex.SemanticEndpointing = true
		rate := cfg.opts.Cfg.ClientRate
		c := newCollector(rate)
		asr := newTrailingASR("呃")
		asr.msPerRune = 250
		asr.fullStop = true
		e := startEngine(t, cfg.opts, Deps{ASR: asr, LLM: mock.NewLLM(), Emit: c.emit})
		speak(e, rate, 400)
		pause(e, rate, 1500)
		time.Sleep(300 * time.Millisecond)
		return c
	}
	t.Run("answered when hesitations count as turns", func(t *testing.T) {
		if c := run(t, false); c.count(live.ServerDelegationCreated) == 0 {
			t.Fatal("the hesitation was not answered even with the setting off; this proves nothing")
		}
	})
	t.Run("left with the caller when they do not", func(t *testing.T) {
		c := run(t, true)
		if n := c.count(live.ExtSpeechStopped); n != 1 {
			t.Fatalf("the utterance closed %d time(s)", n)
		}
		if n := c.count(live.ServerDelegationCreated); n != 0 {
			t.Errorf("a hesitation was answered: %+v", c.delegations())
		}
		if ms := c.audioMS(); ms != 0 {
			t.Errorf("%v ms of audio was played in answer to a hesitation", ms)
		}
		if finals := finalInputTranscripts(c); len(finals) != 1 {
			t.Errorf("the caller's hesitation should still be on the transcript, once: %q", finals)
		}
	})
}

// The first completion of a call is the slow one — 1100 ms to a first token on
// a call where every later one was near 450 — and a call spends seconds
// ringing before anyone can be waiting on it.
func TestBackendIsWarmedWhenTheSessionOpens(t *testing.T) {
	const persona = "你是电话客服小翠儿。"
	run := func(t *testing.T, warm bool) []provider.LLMRequest {
		cfg := parityConfig()
		cfg.Duplex.BackendPrefixWarmStart = warm
		c := newCollector(cfg.ClientRate)
		llm := &recordingLLM{inner: mock.NewLLM()}
		startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationResponses, Speculative: true, Instructions: persona},
			Deps{ASR: voicedASR("你好"), LLM: llm, Emit: c.emit})
		time.Sleep(300 * time.Millisecond)
		return llm.requests()
	}
	if reqs := run(t, false); len(reqs) != 0 {
		t.Fatalf("%d request(s) were made to the backend before anyone spoke, unasked", len(reqs))
	}
	reqs := run(t, true)
	if len(reqs) != 1 {
		t.Fatalf("%d request(s) at session open; want the one that warms the backend", len(reqs))
	}
	if reqs[0].MaxTokens != 1 {
		t.Errorf("the warming request asks for %d tokens; it should ask for one", reqs[0].MaxTokens)
	}
	found := false
	for _, m := range reqs[0].Messages {
		found = found || strings.Contains(m.Content, persona)
	}
	if !found {
		t.Errorf("the warming request does not carry the session's instructions:\n%s", dump(reqs[0].Messages))
	}
}
