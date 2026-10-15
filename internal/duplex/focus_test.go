package duplex

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/focus"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

// fakeScorer labels audio by a schedule: who is speaking at each moment of
// the session's audio clock, voiced or not. Labels come out after lag.
type fakeScorer struct {
	who    func(atMS int64) int // speaker index, or -1 for nobody
	lag    time.Duration
	frames chan focus.Frame
	mu     sync.Mutex
	pushed int64
	closed bool
}

func newFakeScorer(who func(int64) int, lag time.Duration) *fakeScorer {
	return &fakeScorer{who: who, lag: lag, frames: make(chan focus.Frame, 4096)}
}

func (f *fakeScorer) Push(pcm []byte, atMS int64) error {
	f.mu.Lock()
	f.pushed += int64(len(pcm)) / 32
	f.mu.Unlock()
	dur := int64(len(pcm)) / 32 // 16 kHz PCM16
	// Only energy counts as voice: the schedule says whose it is.
	voiced := false
	for i := 0; i+1 < len(pcm); i += 2 {
		if v := int16(pcm[i]) | int16(pcm[i+1])<<8; v > 800 || v < -800 {
			voiced = true
			break
		}
	}
	p := make([]float32, 2)
	if voiced {
		if s := f.who(atMS); s >= 0 {
			p[s] = 0.95
		}
	}
	fr := focus.Frame{StartMS: atMS, EndMS: atMS + dur, P: p}
	deliver := func() {
		f.mu.Lock()
		closed := f.closed
		f.mu.Unlock()
		if closed {
			return
		}
		select {
		case f.frames <- fr:
		default:
		}
	}
	if f.lag > 0 {
		time.AfterFunc(f.lag, deliver)
	} else {
		deliver()
	}
	return nil
}

func (f *fakeScorer) Frames() <-chan focus.Frame { return f.frames }
func (f *fakeScorer) Lag() time.Duration         { return f.lag }
func (f *fakeScorer) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.frames)
	}
	return nil
}

// listenThrough streams the caller's silence while the assistant answers, as
// a line does, and for the echo tail after: the audio clock the focus masks
// are kept on must keep running.
func listenThrough(t *testing.T, e *Engine, rate int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for e.assistantHasFloor() && time.Now().Before(deadline) {
		pause(e, rate, 100)
	}
	pause(e, rate, focusEchoTailMS+200)
}

func (c *collector) focusEvents() []live.FocusEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []live.FocusEvent
	for _, ev := range c.events {
		if f, ok := ev.(live.FocusEvent); ok {
			out = append(out, f)
		}
	}
	return out
}

func startFocusedEngine(t *testing.T, opts Options, deps Deps, scorer focus.Scorer) *Engine {
	t.Helper()
	if deps.TTS == nil {
		deps.TTS = mock.NewTTS()
	}
	opts.ClientRate = opts.Cfg.ClientRate
	opts.SessionID = "test"
	e := New(opts, deps)
	e.focusDial = func(context.Context) (focus.Scorer, error) { return scorer, nil }
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)
	t.Cleanup(func() { e.Close(); cancel() })
	return e
}

// The point of it: the caller asks something and is answered; then a second
// voice says something, the engine begins an answer on the words, and the
// answer is dropped before it is heard, because the labels say the voice was
// not the caller's.
func TestAnotherVoicesUtteranceIsNotAnswered(t *testing.T) {
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Focus = "veto"
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("多少钱啊明天有空吗")
	asr.msPerRune = 150
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	// The chat model takes what it takes on a real stack: the window the
	// labels have to land in.
	llm.FirstTokenDelay = 400 * time.Millisecond
	// Speaker 0 (the caller) for the first utterance; speaker 1 for anything
	// after the answer (the audio clock runs on through it, see listenThrough).
	scorer := newFakeScorer(func(atMS int64) int {
		if atMS < 1500 {
			return 0
		}
		return 1
	}, 150*time.Millisecond)
	e := startFocusedEngine(t, cfg.opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, scorer)

	// The caller's turn: 600 ms of speech, a pause; answered.
	speak(e, rate, 600)
	pause(e, rate, 700)
	waitFor(t, "the caller to be answered", 5*time.Second, func() bool { return c.audioMS() > 100 })
	listenThrough(t, e, rate)
	heard := c.audioMS()
	if e.focus.tracker.Primary() != 0 {
		t.Fatalf("primary speaker %d; wanted the caller, 0", e.focus.tracker.Primary())
	}

	// Another voice, 700 ms, then quiet.
	speak(e, rate, 700)
	pause(e, rate, 1500)
	waitFor(t, "the utterance to be judged", 5*time.Second, func() bool { return len(c.focusEvents()) >= 2 })
	evs := c.focusEvents()
	last := evs[len(evs)-1]
	if !last.AnotherVoice || !last.Vetoed || last.OthersShare < 0.7 {
		t.Fatalf("judgment %+v; wanted another voice, vetoed", last)
	}
	time.Sleep(1200 * time.Millisecond)
	if got := c.audioMS(); got > heard+40 {
		t.Fatalf("the other voice was answered: %.0f ms of audio after the caller's %.0f", got-heard, heard)
	}
	if n := len(c.delegationReasons()); n != 2 {
		t.Fatalf("delegations %d; the second should have been begun and then dropped", n)
	}
}

// Labels for the caller's own words change nothing: the answer plays.
func TestTheCallersOwnUtteranceIsAnswered(t *testing.T) {
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Focus = "veto"
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("多少钱啊明天有空吗")
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	scorer := newFakeScorer(func(int64) int { return 0 }, 150*time.Millisecond)
	e := startFocusedEngine(t, cfg.opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, scorer)
	speak(e, rate, 600)
	pause(e, rate, 700)
	waitFor(t, "the caller to be answered", 5*time.Second, func() bool { return c.audioMS() > 100 })
	listenThrough(t, e, rate)
	speak(e, rate, 600)
	pause(e, rate, 1500)
	waitFor(t, "the second utterance to be judged", 5*time.Second, func() bool { return len(c.focusEvents()) >= 2 })
	for _, ev := range c.focusEvents() {
		if ev.AnotherVoice || ev.Vetoed {
			t.Fatalf("the caller's words were taken for another voice: %+v", ev)
		}
	}
	if e.focus.vetoed != 0 {
		t.Fatalf("vetoed %d", e.focus.vetoed)
	}
}

// "observe" judges and logs but never drops an answer.
func TestObserveModeDoesNotVeto(t *testing.T) {
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Focus = "observe"
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("多少钱啊明天有空吗")
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	scorer := newFakeScorer(func(atMS int64) int {
		if atMS < 1500 {
			return 0
		}
		return 1
	}, 150*time.Millisecond)
	e := startFocusedEngine(t, cfg.opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, scorer)
	speak(e, rate, 600)
	pause(e, rate, 700)
	waitFor(t, "the caller to be answered", 5*time.Second, func() bool { return c.audioMS() > 100 })
	listenThrough(t, e, rate)
	heard := c.audioMS()
	speak(e, rate, 700)
	pause(e, rate, 700)
	waitFor(t, "the other voice to be answered anyway", 6*time.Second, func() bool { return c.audioMS() > heard+100 })
	evs := c.focusEvents()
	if len(evs) < 2 || !evs[len(evs)-1].AnotherVoice || evs[len(evs)-1].Vetoed {
		t.Fatalf("judgment %+v; wanted another voice noted, not vetoed", evs)
	}
}
