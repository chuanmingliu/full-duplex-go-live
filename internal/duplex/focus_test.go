package duplex

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/focus"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

// fakeScorer labels audio by a schedule: who is speaking at each moment of
// the session's audio clock, voiced or not. Labels come out after lag.
type fakeScorer struct {
	who        func(atMS int64) int // speaker index, or -1 for nobody
	lag        time.Duration
	frames     chan focus.Frame
	mu         sync.Mutex
	pushed     int64
	closed     bool
	enrolled   [][2]int64
	saveAs     string
	asked      string
	hasProfile bool
	answers    chan focus.Enrolment
}

func newFakeScorer(who func(int64) int, lag time.Duration) *fakeScorer {
	return &fakeScorer{who: who, lag: lag, frames: make(chan focus.Frame, 4096), answers: make(chan focus.Enrolment, 4)}
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

// Enrol records what the engine would tell a target-speaker service.
func (f *fakeScorer) Enrol(fromMS, toMS int64, saveAs string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enrolled = append(f.enrolled, [2]int64{fromMS, toMS})
	f.saveAs = saveAs
	// Every offered utterance agrees, as a real caller's would.
	n := len(f.enrolled)
	select {
	case f.answers <- focus.Enrolment{Candidates: n, Agreeing: n, Tentative: n < 2, VoicedMS: int(toMS - fromMS), Saved: saveAs != "" && n >= 2}:
	default:
	}
	return nil
}

// EnrolProfile answers at once: found when the test said the profile
// exists.
func (f *fakeScorer) EnrolProfile(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = name
	select {
	case f.answers <- focus.Enrolment{Profile: name, Found: f.hasProfile}:
	default:
	}
	return nil
}

func (f *fakeScorer) Enrolled() <-chan focus.Enrolment { return f.answers }

func (f *fakeScorer) enrolments() [][2]int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]int64(nil), f.enrolled...)
}
func (f *fakeScorer) Lag() time.Duration { return f.lag }
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
	if n := len(c.delegationReasons()); n != 1 && n != 2 {
		t.Fatalf("delegations %d; the second voice's should have been let go by, or begun and then dropped", n)
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

// A television over the answer. The recognizer makes words of it, which used
// to cut the answer as any interruption does; the labels say the voice is
// not the caller's, and in veto mode the answer carries on.
func TestAnotherVoiceOverTheAnswerDoesNotCutIt(t *testing.T) {
	for _, mode := range []string{"veto", "observe"} {
		t.Run(mode, func(t *testing.T) {
			cfg := earlyFinalConfig(120)
			cfg.opts.Cfg.Duplex.Focus = mode
			cfg.opts.Cfg.Duplex.AllowBargeIn = true
			cfg.opts.Cfg.Duplex.OnNewQuery = "cut"
			cfg.opts.Cfg.Duplex.UserBackchannelHoldMS = 600
			rate := cfg.opts.Cfg.ClientRate
			c := newCollector(rate)
			asr := newTrailingASR("多少钱啊今天天气怎么样")
			asr.msPerRune = 150
			llm := mock.NewLLM()
			llm.DelayPerRune = 40 * time.Millisecond // a long answer to talk over
			llm.FirstTokenDelay = 300 * time.Millisecond
			scorer := newFakeScorer(func(atMS int64) int {
				if atMS < 1000 {
					return 0
				}
				return 1
			}, 150*time.Millisecond)
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			e := startFocusedEngine(t, cfg.opts, Deps{ASR: asr, LLM: llm, Emit: c.emit, Log: logger}, scorer)
			t.Cleanup(func() {
				if t.Failed() {
					t.Log(logs.String())
				}
			})

			speak(e, rate, 600)
			pause(e, rate, 700)
			waitFor(t, "the caller to be answered", 5*time.Second, func() bool { return c.audioMS() > 250 })
			if e.focus.tracker.Primary() != 0 {
				t.Fatalf("primary speaker %d; wanted the caller, 0", e.focus.tracker.Primary())
			}
			// The television, 1000 ms, over the answer; then the line as it is.
			speak(e, rate, 1000)
			pause(e, rate, 1500)
			cut := c.count(live.ExtAudioTruncated)
			var judged []live.FocusEvent
			for _, ev := range c.focusEvents() {
				if ev.BargeIn || ev.Utterance {
					judged = append(judged, ev)
				}
			}
			if len(judged) == 0 {
				t.Fatalf("the sound over the answer was never judged by the labels")
			}
			if !judged[0].AnotherVoice {
				t.Fatalf("the television was not read as another voice: %+v", judged[0])
			}
			if mode == "veto" && cut != 0 {
				t.Fatalf("the answer was cut %d time(s) by another voice", cut)
			}
			if mode == "observe" && cut == 0 {
				t.Fatalf("observe mode must still cut on the words; it did not")
			}
		})
	}
}

// The target-speaker path: the caller's first utterance alone is what the
// service is told to take their voice from; a background voice's utterance,
// while the assistant is silent, is let go by with no turn made of it; the
// caller's next one is answered as usual.
func TestABackgroundVoicesUtteranceIsLetGoBy(t *testing.T) {
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Focus = "veto"
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("多少钱啊今天天气怎么样明天有空吗")
	asr.msPerRune = 150
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	llm.FirstTokenDelay = 300 * time.Millisecond
	// Speaker 0 is the caller; the television (1) talks in the window set
	// below, once the first answer is over; the caller again after.
	var tvFrom, tvTo atomic.Int64
	tvFrom.Store(1 << 40)
	scorer := newFakeScorer(func(atMS int64) int {
		if atMS >= tvFrom.Load() && atMS < tvTo.Load() {
			return 1
		}
		return 0
	}, 150*time.Millisecond)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := startFocusedEngine(t, cfg.opts, Deps{ASR: asr, LLM: llm, Emit: c.emit, Log: logger}, scorer)
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	})

	speak(e, rate, 700)
	pause(e, rate, 700)
	waitFor(t, "the caller to be answered", 5*time.Second, func() bool { return c.audioMS() > 100 })
	if en := scorer.enrolments(); len(en) != 1 || en[0][0] != 0 || en[0][1] < 600 {
		t.Fatalf("offered %v; wanted the first utterance", en)
	}
	listenThrough(t, e, rate)
	delegated := len(c.delegationReasons())
	// The television for 1.5 s, then quiet.
	tvFrom.Store(e.inputMS.Load() - 300)
	tvTo.Store(e.inputMS.Load() + 1800)
	speak(e, rate, 1500)
	pause(e, rate, 1500)
	var judged []live.FocusEvent
	for _, ev := range c.focusEvents() {
		if ev.Utterance {
			judged = append(judged, ev)
		}
	}
	if len(judged) == 0 || !judged[0].AnotherVoice || !judged[0].Vetoed {
		t.Fatalf("the television's utterance was not let go by: %+v", judged)
	}
	if n := len(c.delegationReasons()); n != delegated {
		t.Fatalf("delegations went from %d to %d on the television's words", delegated, n)
	}
	if en := scorer.enrolments(); len(en) != 1 {
		t.Fatalf("offered the television's utterance: %v", en)
	}
	// The caller again: answered.
	heard := c.audioMS()
	speak(e, rate, 800)
	pause(e, rate, 700)
	waitFor(t, "the caller's next utterance to be answered", 6*time.Second, func() bool { return c.audioMS() > heard+100 })
}

// An enrolment the labels then contradict — everything after it "not the
// caller" — is the wrong voice, and acting on it would ignore the caller.
// After eight seconds of such voice the channel only observes, and the
// caller's next utterance is answered.
func TestAnEnrolmentContradictedByWhatFollowsIsNotActedOn(t *testing.T) {
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Focus = "veto"
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("多少钱啊" + strings.Repeat("今天天气怎么样", 12) + "明天有空吗")
	asr.msPerRune = 150
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	llm.FirstTokenDelay = 300 * time.Millisecond
	var after atomic.Int64
	after.Store(1 << 40)
	scorer := newFakeScorer(func(atMS int64) int {
		if atMS >= after.Load() {
			return 1 // the service calls everyone "not the caller" from here on
		}
		return 0
	}, 150*time.Millisecond)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := startFocusedEngine(t, cfg.opts, Deps{ASR: asr, LLM: llm, Emit: c.emit, Log: logger}, scorer)
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	})

	speak(e, rate, 700)
	pause(e, rate, 700)
	waitFor(t, "the caller to be answered", 5*time.Second, func() bool { return c.audioMS() > 100 })
	listenThrough(t, e, rate)
	after.Store(e.inputMS.Load())
	// Nine seconds of "another voice" that is in fact the caller: let go by
	// at first, then the check trips and the channel stands down.
	for i := 0; i < 6; i++ {
		speak(e, rate, 1500)
		pause(e, rate, 300)
	}
	pause(e, rate, 1500)
	if !strings.Contains(logs.String(), "the enrolment does not match what followed") {
		t.Fatalf("the enrolment was never found wanting")
	}
	heard := c.audioMS()
	speak(e, rate, 800)
	pause(e, rate, 700)
	waitFor(t, "the caller to be answered though the labels say otherwise", 6*time.Second, func() bool { return c.audioMS() > heard+100 })
}

// A kept profile enrols the caller before they have spoken, and the first
// utterance is not enrolled again; without one, the first utterance is
// saved under the profile's name for next time.
func TestAKeptProfileEnrolsTheCallerAtOnce(t *testing.T) {
	for _, kept := range []bool{true, false} {
		t.Run(map[bool]string{true: "kept", false: "none yet"}[kept], func(t *testing.T) {
			cfg := earlyFinalConfig(120)
			cfg.opts.Cfg.Duplex.Focus = "veto"
			cfg.opts.Cfg.Duplex.FocusProfile = "owner"
			rate := cfg.opts.Cfg.ClientRate
			c := newCollector(rate)
			asr := newTrailingASR("多少钱啊今天天气怎么样")
			asr.msPerRune = 150
			llm := mock.NewLLM()
			llm.DelayPerRune = 2 * time.Millisecond
			scorer := newFakeScorer(func(int64) int { return 0 }, 150*time.Millisecond)
			scorer.hasProfile = kept
			e := startFocusedEngine(t, cfg.opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, scorer)
			speak(e, rate, 700)
			pause(e, rate, 700)
			waitFor(t, "the caller to be answered", 5*time.Second, func() bool { return c.audioMS() > 100 })
			listenThrough(t, e, rate)
			scorer.mu.Lock()
			asked, saveAs, n := scorer.asked, scorer.saveAs, len(scorer.enrolled)
			scorer.mu.Unlock()
			if asked != "owner" {
				t.Fatalf("the profile was not asked for: %q", asked)
			}
			if n != 1 || saveAs != "owner" {
				t.Fatalf("offered %d utterance(s) save_as %q; wanted the first, under the profile's name", n, saveAs)
			}
			if e.focus.tracker.Primary() != 0 {
				t.Fatalf("primary %d", e.focus.tracker.Primary())
			}
		})
	}
}
