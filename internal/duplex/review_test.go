package duplex

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

// --- a recognizer that never says "final" ---

// noFinalASR forwards a recognizer's hypotheses and withholds its final
// transcript. With hang set it then says nothing more and never closes, like a
// connection that has silently died; otherwise it closes, like one that hit a
// read error after the audio was all sent.
type noFinalASR struct {
	inner provider.ASR
	hang  bool
}

func (a *noFinalASR) Name() string { return "no-final" }

func (a *noFinalASR) Open(ctx context.Context, opts provider.ASROptions) (provider.ASRStream, error) {
	s, err := a.inner.Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := &noFinalStream{ASRStream: s, results: make(chan provider.ASRResult, 32), stop: make(chan struct{})}
	go func() {
		defer close(out.results)
		for r := range s.Results() {
			if r.Final {
				if a.hang {
					<-out.stop
				}
				return
			}
			out.results <- r
		}
	}()
	return out, nil
}

type noFinalStream struct {
	provider.ASRStream
	results chan provider.ASRResult
	once    sync.Once
	stop    chan struct{}
}

func (s *noFinalStream) Results() <-chan provider.ASRResult { return s.results }

func (s *noFinalStream) Close() error {
	s.once.Do(func() { close(s.stop) })
	return s.ASRStream.Close()
}

// TestUtteranceIsAnsweredWhenTheRecognizerNeverSendsAFinal: recognizers end a
// stream without a final transcript often enough — a read error after the last
// audio, their own timeout, a dropped connection — and the caller said what
// they said either way.
func TestUtteranceIsAnsweredWhenTheRecognizerNeverSendsAFinal(t *testing.T) {
	for _, tc := range []struct {
		name string
		hang bool
		wait time.Duration
	}{
		{"it closes the stream", false, 3 * time.Second},
		{"it goes silent and never closes", true, finalWaitMax + 3*time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, speculative := range []bool{false, true} {
				cfg := parityConfig()
				cfg.Duplex.Speculative = speculative
				c := newCollector(cfg.ClientRate)
				llm := mock.NewLLM()
				llm.DelayPerRune = 2 * time.Millisecond
				e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationResponses, Speculative: speculative},
					Deps{ASR: &noFinalASR{inner: voicedASR("帮我查一下明天的天气"), hang: tc.hang}, LLM: llm, Emit: c.emit})

				speak(e, cfg.ClientRate, 2000)
				pause(e, cfg.ClientRate, 700)
				waitFor(t, "the turn to be announced", tc.wait, func() bool {
					return c.count(live.ServerDelegationCreated) == 1
				})
				waitFor(t, "an answer", 3*time.Second, func() bool { return c.audioMS() > 0 })
				if got := c.delegations()[0].Delegation.Transcript; got != "帮我查一下明天的天气" {
					t.Errorf("speculative=%v: answered %q", speculative, got)
				}
				if got := c.inputTranscript(); got != "帮我查一下明天的天气" {
					t.Errorf("speculative=%v: the caller's transcript reads %q", speculative, got)
				}
				history := e.History()
				if len(history) == 0 || history[0].Role != provider.RoleUser {
					t.Errorf("speculative=%v: the question is missing from the conversation:\n%s", speculative, dump(history))
				}
			}
		})
	}
}

// --- the gap before a backend's first token ---

// dialSlowLLM takes a while to return from Stream, the way an HTTP round trip
// to a real backend does. The mock returns at once, which hides everything
// that goes wrong in that gap.
type dialSlowLLM struct {
	inner     provider.LLM
	delay     time.Duration
	started   atomic.Int32
	cancelled atomic.Int32
}

func (d *dialSlowLLM) Name() string { return "dial-slow" }

func (d *dialSlowLLM) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	d.started.Add(1)
	select {
	case <-time.After(d.delay):
	case <-ctx.Done():
		d.cancelled.Add(1)
		return nil, ctx.Err()
	}
	return d.inner.Stream(ctx, req)
}

// TestSpeakingAgainBeforeTheAnswerStartsCancelsIt: the caller adds to what they
// said while the backend is still thinking about the first half. The answer to
// the first half must not then arrive on top of them.
func TestSpeakingAgainBeforeTheAnswerStartsCancelsIt(t *testing.T) {
	cfg := parityConfig()
	cfg.Duplex.Speculative = false
	c := newCollector(cfg.ClientRate)
	inner := mock.NewLLM()
	inner.DelayPerRune = 2 * time.Millisecond
	slow := &dialSlowLLM{inner: inner, delay: 1500 * time.Millisecond}
	llm := &recordingLLM{inner: slow}
	e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationResponses}, Deps{
		ASR: &sequenceASR{phrases: []string{"我想查一下保单", "就是去年买的那个"}},
		LLM: llm, Emit: c.emit,
	})

	speak(e, cfg.ClientRate, 1300)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "the first request to the backend", 2*time.Second, func() bool { return slow.started.Load() == 1 })
	if !e.assistantHasFloor() {
		t.Fatal("with a request out to the backend and no word back, an answer is in progress")
	}

	speak(e, cfg.ClientRate, 1500)
	if c.audioMS() != 0 {
		t.Fatalf("the answer to the first half was played over the caller: %v ms", c.audioMS())
	}
	if slow.cancelled.Load() != 1 {
		t.Errorf("the request for the first half was left running while the caller went on")
	}
	pause(e, cfg.ClientRate, 700)

	waitFor(t, "the request for the whole of it", 3*time.Second, func() bool { return len(llm.requests()) == 2 })
	msgs := llm.requests()[1].Messages
	if last := msgs[len(msgs)-1]; !strings.Contains(last.Content, "我想查一下保单") || !strings.Contains(last.Content, "就是去年买的那个") {
		t.Errorf("the backend was asked %q", last.Content)
	}
	waitFor(t, "the answer", 5*time.Second, func() bool { return c.audioMS() > 0 })
	if said := c.allOutput(); strings.Count(said, "我听到你说") != 1 {
		t.Errorf("want one answer; heard %q", said)
	}
}

// TestSomethingWaitingForTheFloorDoesNotTakeItFromAnAnswerOnItsWay: an
// application's update arrives just after the caller's turn, while the
// conversational model is still composing its reply. It used to start at once,
// and was cut off mid-word when the reply turned up.
func TestSomethingWaitingForTheFloorDoesNotTakeItFromAnAnswerOnItsWay(t *testing.T) {
	cfg := parityConfig()
	cfg.Duplex.Speculative = false
	c := newCollector(cfg.ClientRate)
	slow := &dialSlowLLM{inner: &slowBackend{}, delay: 800 * time.Millisecond}
	e := startEngine(t, Options{
		Cfg: cfg, Delegation: live.DelegationClient, ClientDelegation: "auto",
	}, Deps{ASR: voicedASR("你好呀"), LLM: slow, Emit: c.emit})

	speak(e, cfg.ClientRate, 600)
	pause(e, cfg.ClientRate, 700)
	waitFor(t, "the conversational model to be asked", 2*time.Second, func() bool { return slow.started.Load() == 1 })

	e.AppendCommentary("", "另外，您的快递到了。", "push")
	waitFor(t, "both to be said", 8*time.Second, func() bool {
		return strings.Contains(c.allOutput(), "快递到了")
	})
	said := c.allOutput()
	reply, update := strings.Index(said, "已经查到了"), strings.Index(said, "快递到了")
	if reply < 0 || update < reply {
		t.Errorf("the reply should be said, whole, before the update that was waiting: %q", said)
	}
	if n := c.count(live.ExtAudioTruncated); n != 0 {
		t.Errorf("%d thing(s) were cut off mid-sentence", n)
	}
}

// --- history ---

// TestHistoryTrimNeverDropsTheExchangeBeingAnswered: a function result larger
// than the whole size limit used to take the entire conversation with it, and
// then itself.
func TestHistoryTrimNeverDropsTheExchangeBeingAnswered(t *testing.T) {
	cfg := parityConfig()
	cfg.HistoryTurns = 0
	cfg.HistoryMaxChars = 2000
	e := New(Options{Cfg: cfg, SessionID: "trim"}, Deps{Emit: func(any) {}})

	e.history = []provider.Message{
		{Role: provider.RoleUser, Content: "你好"},
		{Role: provider.RoleAssistant, Content: "你好，请讲。"},
		{Role: provider.RoleUser, Content: "帮我查一下保单"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "lookup"}}},
		{Role: provider.RoleTool, ToolCallID: "c1", Content: strings.Repeat("保", 5000)},
	}
	e.trimHistory()

	assertWellFormed(t, e.history)
	if n := len(e.history); n != 3 || e.history[0].Content != "帮我查一下保单" ||
		len(e.history[1].ToolCalls) != 1 || e.history[2].ToolCallID != "c1" {
		t.Fatalf("want the question, the call and its result; the trim left:\n%s", dump(e.history))
	}

	// Once it has been answered and the caller has moved on, it goes like
	// anything else.
	e.history = append(e.history,
		provider.Message{Role: provider.RoleAssistant, Content: "查到了。"},
		provider.Message{Role: provider.RoleUser, Content: "谢谢"})
	e.trimHistory()
	assertWellFormed(t, e.history)
	if len(e.history) != 1 || e.history[0].Content != "谢谢" {
		t.Fatalf("the oversized exchange should be gone once it is no longer the newest:\n%s", dump(e.history))
	}
}

// TestHistoryStartsAtAnExchangeAfterATrim: trimmed history opens with the
// caller, not with an answer to a question that is no longer there.
func TestHistoryStartsAtAnExchangeAfterATrim(t *testing.T) {
	cfg := parityConfig()
	cfg.HistoryTurns = 2 // four messages, trimmed back to three
	cfg.HistoryMaxChars = 0
	e := New(Options{Cfg: cfg, SessionID: "trim"}, Deps{Emit: func(any) {}})
	for i := 0; i < 3; i++ {
		e.history = append(e.history,
			provider.Message{Role: provider.RoleUser, Content: "问"},
			provider.Message{Role: provider.RoleAssistant, Content: "答"})
	}
	e.trimHistory()
	assertWellFormed(t, e.history)
	if len(e.history) == 0 || e.history[0].Role != provider.RoleUser {
		t.Fatalf("history opens on %q:\n%s", e.history[0].Role, dump(e.history))
	}
}

// TestHistoryCanBeReadAfterTheEngineHasStopped: a session that reaches its time
// limit is stopped by the deadline that ends the call, and a stored one was
// saving an empty conversation.
func TestHistoryCanBeReadAfterTheEngineHasStopped(t *testing.T) {
	cfg := parityConfig()
	seeded := []provider.Message{
		{Role: provider.RoleUser, Content: "你好"},
		{Role: provider.RoleAssistant, Content: "你好，请讲。"},
	}
	e := New(Options{Cfg: cfg, SessionID: "stopped", History: seeded},
		Deps{ASR: voicedASR("你好"), LLM: mock.NewLLM(), TTS: mock.NewTTS(), Emit: func(any) {}})
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)
	t.Cleanup(e.Close)

	cancel() // the session's deadline
	started := time.Now()
	got := e.History()
	if len(got) != 2 || got[0].Content != "你好" {
		t.Fatalf("history of a stopped engine: %+v", got)
	}
	if took := time.Since(started); took > time.Second {
		t.Errorf("reading it took %v", took)
	}
}

// --- what a client can leave queued ---

func TestQueuedInputIsBounded(t *testing.T) {
	cfg := parityConfig()
	c := newCollector(cfg.ClientRate)
	e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationClient},
		Deps{ASR: voicedASR("你好"), LLM: mock.NewLLM(), Emit: c.emit})

	saw := func(code string) func() bool {
		return func() bool {
			for _, got := range c.errorCodes() {
				if got == code {
					return true
				}
			}
			return false
		}
	}

	// Typed input with no response.create behind it.
	for i := 0; i < maxQueuedItems+1; i++ {
		e.SubmitItem(live.ResponseItem{Type: "message", Role: "user", Content: live.TextContent("input_text", "还有一件事")}, "")
	}
	waitFor(t, "typed input to be refused", 2*time.Second, saw("content_too_long"))

	// Commentary faster than it can be said.
	for i := 0; i < maxQueuedItems+2; i++ {
		e.AppendCommentary("", "这是一条需要一点时间才能说完的通知。", "")
	}
	waitFor(t, "commentary to be refused", 2*time.Second, saw("commentary_queue_full"))

	// Instructions without end.
	before := len(c.errorCodes())
	chunk := strings.Repeat("规", cfg.Duplex.MaxAppendChars)
	for i := 0; i < maxInstructionAppends+1; i++ {
		e.AppendInstructions("", chunk, "")
	}
	waitFor(t, "instructions to be refused", 2*time.Second, func() bool {
		codes := c.errorCodes()
		return len(codes) > before && codes[len(codes)-1] == "content_too_long"
	})
	if got := len([]rune(e.Instructions())); got > (maxInstructionAppends+1)*cfg.Duplex.MaxAppendChars+len([]rune(cfg.Instructions)) {
		t.Errorf("instructions grew to %d characters", got)
	}
}

// The splitter can cut a closing quote or a dash off as a segment of its own.
// It has nothing in it to pronounce; sending it cost a synthesizer round trip
// and the connection, which was dropped when no audio came back.
func TestSpeakableTextHasSomethingToPronounce(t *testing.T) {
	for text, want := range map[string]bool{
		"”":      false,
		"……":     false,
		"——":     false,
		" 。":     false,
		"\"":     false,
		"好":      true,
		"“好的。”":  true,
		"3":      true,
		"ok.":    true,
		"— 那就这样": true,
	} {
		if got := speakable(text); got != want {
			t.Errorf("speakable(%q) = %v, want %v", text, got, want)
		}
	}
}

// A line that is never quiet never gives the detector the silence it ends a
// turn on. A real call asked "今天天气怎么样啊？", stopped, and was held open for
// 3.3 seconds by the noise on the line — the transcript finished and unchanged
// the whole time, a guess at the answer made and thrown away eight times. The
// recognizer is the witness that does not depend on the line: words that have
// stood still through sound that is mostly not voice are a finished turn.
func TestTurnEndsWhenTheWordsSettleOnALineThatIsNeverQuiet(t *testing.T) {
	// Loud enough for the detector to call voiced, too faint for the mock
	// recognizer to count as speech: what line noise is to a real one.
	noise := func(e *Engine, rate int) {
		for i := 0; i < 12; i++ {
			pushPaced(e, voiced(rate, 120, 0.007), rate)
			pause(e, rate, 180)
		}
	}
	run := func(t *testing.T, settledMS int) *collector {
		cfg := parityConfig()
		cfg.Duplex.SettledCloseMS = settledMS
		c := newCollector(cfg.ClientRate)
		e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationResponses, Speculative: true},
			Deps{ASR: voicedASR("今天天气怎么样"), LLM: mock.NewLLM(), Emit: c.emit})
		speak(e, cfg.ClientRate, 1500)
		noise(e, cfg.ClientRate)
		return c
	}

	t.Run("waiting for silence alone, the turn is still open", func(t *testing.T) {
		c := run(t, 0)
		if n := c.count(live.ExtSpeechStopped); n != 0 {
			t.Fatalf("the detector closed the utterance %d time(s) in the noise; this line is not noisy enough to prove anything", n)
		}
		if n := c.count(live.ServerDelegationCreated); n != 0 {
			t.Fatalf("the turn was answered without ever ending")
		}
	})

	t.Run("with the words as witness, it ends and is answered", func(t *testing.T) {
		c := run(t, 600)
		waitFor(t, "the turn to be taken", 2*time.Second, func() bool {
			return c.count(live.ExtSpeechStopped) == 1 && c.count(live.ServerDelegationCreated) == 1
		})
		ds := c.delegations()
		if got := ds[0].Delegation.Transcript; normalizeForCompare(got) != "今天天气怎么样" {
			t.Errorf("the turn answered is %q; the caller said 今天天气怎么样", got)
		}
		waitFor(t, "the answer", 3*time.Second, func() bool { return c.audioMS() > 0 })
		if c.count(live.ServerError) != 0 {
			t.Errorf("errors: %v", c.errorCodes())
		}
	})
}

// The other half of that: a transcript also stands still when the recognizer
// has fallen behind someone who is still talking. They are, audibly, still
// talking, and their turn is not over.
func TestSettledWordsDoNotEndTheTurnOfSomeoneStillTalking(t *testing.T) {
	cfg := parityConfig()
	cfg.Duplex.SettledCloseMS = 600
	c := newCollector(cfg.ClientRate)
	// A recognizer that has heard all it is going to: seven characters, then
	// nothing, however long the caller goes on.
	e := startEngine(t, Options{Cfg: cfg, Delegation: live.DelegationResponses, Speculative: true},
		Deps{ASR: voicedASR("今天天气怎么样"), LLM: mock.NewLLM(), Emit: c.emit})
	speak(e, cfg.ClientRate, 3600)
	if n := c.count(live.ExtSpeechStopped); n != 0 {
		t.Fatalf("the turn was taken from a caller who was still speaking")
	}
	if n := c.count(live.ServerDelegationCreated); n != 0 {
		t.Fatalf("a turn was answered while the caller was still speaking")
	}
}
