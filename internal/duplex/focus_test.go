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

// A room that never goes quiet. The caller says their piece and a second
// source — a television, a bystander — keeps the line loud, so the
// detector's pause never comes and the recognizer, still making words of
// the noise, keeps the settled close from seeing them stand still. The
// labels say the caller stopped; the turn is taken on that, long before
// the noise ends.
func TestTheTurnIsTakenWhenTheCallerGoesQuietThoughTheLineDoesNot(t *testing.T) {
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Focus = "veto"
	cfg.opts.Cfg.Duplex.FocusQuietCloseMS = 420
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	// A recognizer that never stops finding words while there is sound.
	asr := newTrailingASR("多少钱啊明天有空吗这个那个这个那个这个那个这个那个这个那个这个")
	asr.msPerRune = 150
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	// The caller for the first 800 ms of sound; someone else after.
	scorer := newFakeScorer(func(atMS int64) int {
		if atMS < 800 {
			return 0
		}
		return 1
	}, 60*time.Millisecond)
	e := startFocusedEngine(t, cfg.opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, scorer)

	speak(e, rate, 800)
	// The other source carries straight on, 3 s without a gap; the answer
	// should begin while it is still going.
	began := time.Now()
	var answeredAt time.Duration
	// (45 × 100 ms: under a full test run the answer's first audio can be
	// a second late; the close itself is the thing under test.)
	for i := 0; i < 45; i++ {
		speak(e, rate, 100)
		if answeredAt == 0 && c.audioMS() > 0 {
			answeredAt = time.Since(began)
		}
	}
	if answeredAt == 0 {
		t.Fatalf("not answered while the other source went on for 4.5 s (quiet closes %d)", e.focus.quietCloses)
	}
	if answeredAt > 3*time.Second {
		t.Fatalf("answered %v after the caller stopped; the turn should have been taken on the caller's quiet, not the line's", answeredAt)
	}
	if e.focus.quietCloses != 1 {
		t.Fatalf("quiet closes %d; wanted 1", e.focus.quietCloses)
	}
	pause(e, rate, 1000)
}

// A caller who interrupts and keeps talking is still a caller: the part of
// the interruption after the assistant's voice and its echo tail is offered
// as their voice. Before, a barge-in was never offered, and a fast talker
// could go a whole call unenrolled.
func TestTheCleanTailOfAnInterruptionIsOfferedForEnrolment(t *testing.T) {
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Focus = "veto"
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("多少钱啊明天有空吗这个那个这个那个这个那个")
	asr.msPerRune = 150
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	scorer := newFakeScorer(func(int64) int { return 0 }, 60*time.Millisecond)
	e := startFocusedEngine(t, cfg.opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, scorer)

	speak(e, rate, 700)
	pause(e, rate, 600)
	waitFor(t, "the caller to be answered", 5*time.Second, func() bool { return c.audioMS() > 300 })
	first := len(scorer.enrolments())
	// Interrupt, and keep talking well past the assistant's last word.
	speak(e, rate, 3000)
	pause(e, rate, 800)
	waitFor(t, "the interruption to be offered", 5*time.Second, func() bool { return len(scorer.enrolments()) > first })
	en := scorer.enrolments()
	last := en[len(en)-1]
	if span := last[1] - last[0]; span >= 3000 || span < 600 {
		t.Fatalf("offered span %d ms; wanted the clean tail of the 3000 ms interruption (600–3000)", span)
	}
	if e.focus.tracker.MaskedWithin(last[0], last[1]) {
		t.Fatalf("the offered span %v overlaps the assistant's voice", last)
	}
}

// The quiet close is for a line that never goes quiet. On an ordinary
// quiet line it must not act at all: a caller who pauses mid-thought is
// given patient_silence_ms by semantic endpointing, and the quiet close
// was ending their turn at focus_quiet_close_ms (420) regardless — on the
// full-duplex bench of 10-21 every "我想问一下", 600 ms pause, "车险一年多少钱"
// was answered on its first half and cut by its second; in the archive, 7
// of a call's 8 turns had closed that way, with others_ms=0 on every one.
func TestTheQuietCloseLeavesAQuietLineAlone(t *testing.T) {
	// The recognizer reveals one phrase across the utterances, by voice
	// heard: the first 600 ms are "多少钱啊", answered, and enrol the caller;
	// the second utterance is the one under test.
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Focus = "veto"
	cfg.opts.Cfg.Duplex.FocusQuietCloseMS = 420
	cfg.opts.Cfg.Duplex.SemanticEndpointing = true
	cfg.opts.Cfg.Duplex.PatientSilenceMS = 900
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("多少钱啊就是那个保险多少钱")
	asr.fullStop = true
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	// Every voiced frame is the caller's.
	scorer := newFakeScorer(func(atMS int64) int { return 0 }, 60*time.Millisecond)
	e := startFocusedEngine(t, cfg.opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, scorer)

	// The caller's first turn: answered, and their voice enrolled.
	speak(e, rate, 600)
	pause(e, rate, 1200)
	waitFor(t, "the caller to be answered", 5*time.Second, func() bool { return c.audioMS() > 100 })
	listenThrough(t, e, rate)
	if e.focus.tracker.Primary() != 0 {
		t.Fatalf("primary speaker %d; wanted the caller, 0", e.focus.tracker.Primary())
	}
	turnsBefore := c.count(live.ExtSpeechStopped)
	closesBefore := e.focus.quietCloses

	speak(e, rate, 340)  // 就是
	pause(e, rate, 550)  // a thinking pause: over 420, under the 900 the words earn
	speak(e, rate, 1100) // 那个保险多少钱
	pause(e, rate, 1200)

	waitFor(t, "the second turn to be answered", 4*time.Second, func() bool {
		return c.count(live.ExtSpeechStopped) > turnsBefore
	})
	time.Sleep(300 * time.Millisecond)
	if n := e.focus.quietCloses - closesBefore; n != 0 {
		t.Errorf("quiet closes %d on a quiet line; wanted none", n)
	}
	if n := c.count(live.ExtSpeechStopped) - turnsBefore; n != 1 {
		t.Fatalf("the caller's one sentence was taken as %d turns", n)
	}
}

// A sound over the answer that the labels call mostly another voice — with
// less of the caller in it than counts as the caller speaking — keeps the
// floor when the hold runs out, instead of yielding to the words as the
// pre-label engine did. The 09:43 call of 10-23: a station announcement over
// the answer, 92% another voice and 100 ms the caller's, 3 points short of
// the veto share; the hold waited out and the answer was cut (ISS-052).
func TestAnUndecidedHoldThatIsMostlyAnotherVoiceKeepsTheFloor(t *testing.T) {
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Focus = "veto"
	cfg.opts.Cfg.Duplex.AllowBargeIn = true
	cfg.opts.Cfg.Duplex.OnNewQuery = "cut"
	cfg.opts.Cfg.Duplex.UserBackchannelHoldMS = 600
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("多少钱啊本次列车开往")
	asr.msPerRune = 150
	llm := mock.NewLLM()
	llm.DelayPerRune = 40 * time.Millisecond
	llm.FirstTokenDelay = 300 * time.Millisecond
	// The caller's enrolment utterance; then the announcement over the
	// answer with 100 ms of the caller in it.
	scorer := newFakeScorer(func(atMS int64) int {
		switch {
		case atMS < 1000:
			return 0
		case atMS >= 1300 && atMS < 1400:
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
	speak(e, rate, 1200)
	pause(e, rate, 1500)
	if cut := c.count(live.ExtAudioTruncated); cut != 0 {
		t.Fatalf("the answer was cut %d time(s) by a sound that was mostly another voice", cut)
	}
	if !strings.Contains(logs.String(), "waited out, mostly another voice") {
		t.Fatalf("the hold was not kept on the waited-out verdict")
	}
}

// An utterance another voice opened and the caller joined for a moment is
// not answered at the quiet close: its words are mostly the other voice's.
// The 09:43 call of 10-23: 200 ms of the caller in 2.6 s of a station
// announcement, taken as a turn, seven garbled characters read by the model
// as a refusal (ISS-053).
func TestTheQuietCloseDoesNotAnswerAnUtteranceThatIsMostlyAnotherVoice(t *testing.T) {
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Focus = "veto"
	cfg.opts.Cfg.Duplex.FocusQuietCloseMS = 420
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("多少钱啊本次列车开往这个那个这个那个这个那个这个那个这个那个这个那个")
	asr.msPerRune = 150
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	// The caller for 800 ms (enrolment); silence; then another voice for
	// 3.5 s with the caller joining for 200 ms a second in.
	scorer := newFakeScorer(func(atMS int64) int {
		switch {
		case atMS < 800:
			return 0
		case atMS >= 4000 && atMS < 4200:
			return 0
		}
		return 1
	}, 60*time.Millisecond)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e := startFocusedEngine(t, cfg.opts, Deps{ASR: asr, LLM: llm, Emit: c.emit, Log: logger}, scorer)
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	speak(e, rate, 800)
	pause(e, rate, 700)
	waitFor(t, "the caller to be answered", 5*time.Second, func() bool { return c.audioMS() > 0 })
	pause(e, rate, 1500) // the short answer plays out; audio now at 3000 ms
	turns := c.count(live.ServerDelegationCreated)
	for i := 0; i < 35; i++ { // 3.5 s of the announcement, the caller inside it at 4000–4200
		speak(e, rate, 100)
	}
	pause(e, rate, 800)
	if got := c.count(live.ServerDelegationCreated); got != turns {
		t.Fatalf("a turn was made of an utterance that was mostly another voice (%d → %d)", turns, got)
	}
	// Let go either at the quiet close, or never taken up at all now that a
	// join over the assistant's floor needs the caller to be most of the
	// sound (ISS-055).
	if l := logs.String(); !strings.Contains(l, "mostly another voice at the quiet close") && !strings.Contains(l, `why="another voice"`) {
		t.Fatalf("the utterance's voice was not judged")
	}
	if e.focus.quietCloses != 0 {
		t.Fatalf("quiet closes %d; wanted 0", e.focus.quietCloses)
	}
}

// An utterance another voice opened over the answer, with 200 ms of the
// caller in a second of it, is not taken up on the caller's 160 ms alone:
// over the assistant's floor the caller has to be at least as present as the
// rest of the sound (or speak for 400 ms). The 12:45 call of 10-25: taken up
// on 200 of 980 ms, the answer cut, the utterance let go 40 ms later as
// mostly another voice's, and the caller asked whether anyone was there
// (ISS-055).
func TestAJoinOverTheAnswerNeedsTheCallerToBeMostOfTheSound(t *testing.T) {
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Focus = "veto"
	cfg.opts.Cfg.Duplex.AllowBargeIn = true
	cfg.opts.Cfg.Duplex.OnNewQuery = "cut"
	cfg.opts.Cfg.Duplex.UserBackchannelHoldMS = 600
	cfg.opts.Cfg.Duplex.FocusQuietCloseMS = 420
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("多少钱啊本次列车开往")
	asr.msPerRune = 150
	llm := mock.NewLLM()
	llm.DelayPerRune = 40 * time.Millisecond
	llm.FirstTokenDelay = 300 * time.Millisecond
	// The caller's enrolment utterance; then the announcement over the
	// answer with 200 ms of the caller inside it.
	scorer := newFakeScorer(func(atMS int64) int {
		switch {
		case atMS < 1000:
			return 0
		case atMS >= 1500 && atMS < 1700:
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
	speak(e, rate, 1200)
	pause(e, rate, 1500)
	if cut := c.count(live.ExtAudioTruncated); cut != 0 {
		t.Fatalf("the answer was cut %d time(s) by a sound that was mostly another voice with 200 ms of the caller in it", cut)
	}
	if strings.Contains(logs.String(), "the caller's voice joined the utterance") {
		t.Fatalf("200 ms of the caller in a second of another voice was taken up as the caller joining")
	}
}

// The quiet close needs the other voice to fill half its window; a 100 ms
// flicker of another label inside the caller's own thinking pause is not a
// line that is not quiet. The 12:45 call of 10-25: a question closed in the
// pause before its second half on 100 ms of other labels, the half answered,
// the answer cut by the rest, the rest answered on its own (ISS-056).
func TestTheQuietCloseNeedsMoreThanAFlickerOfAnotherVoice(t *testing.T) {
	cfg := earlyFinalConfig(120)
	cfg.opts.Cfg.Duplex.Focus = "veto"
	cfg.opts.Cfg.Duplex.FocusQuietCloseMS = 420
	cfg.opts.Cfg.Duplex.SemanticEndpointing = true
	cfg.opts.Cfg.Duplex.PatientSilenceMS = 900
	rate := cfg.opts.Cfg.ClientRate
	c := newCollector(rate)
	asr := newTrailingASR("多少钱啊就是那个保险多少钱")
	asr.fullStop = true
	llm := mock.NewLLM()
	llm.DelayPerRune = 2 * time.Millisecond
	// The caller throughout, except a 100 ms flicker of another voice in
	// their thinking pause (scheduled once the clock is known).
	var flickerAt atomic.Int64
	scorer := newFakeScorer(func(atMS int64) int {
		if f := flickerAt.Load(); f > 0 && atMS >= f && atMS < f+100 {
			return 1
		}
		return 0
	}, 60*time.Millisecond)
	e := startFocusedEngine(t, cfg.opts, Deps{ASR: asr, LLM: llm, Emit: c.emit}, scorer)

	speak(e, rate, 600)
	pause(e, rate, 1200)
	waitFor(t, "the caller to be answered", 5*time.Second, func() bool { return c.audioMS() > 100 })
	listenThrough(t, e, rate)
	turnsBefore := c.count(live.ExtSpeechStopped)
	closesBefore := e.focus.quietCloses

	speak(e, rate, 340) // 就是
	pause(e, rate, 60)
	flickerAt.Store(e.inputMS.Load())
	speak(e, rate, 100)  // the flicker
	pause(e, rate, 440)  // the rest of the thinking pause
	speak(e, rate, 1100) // 那个保险多少钱
	pause(e, rate, 1200)

	waitFor(t, "the second turn to be answered", 4*time.Second, func() bool {
		return c.count(live.ExtSpeechStopped) > turnsBefore
	})
	time.Sleep(300 * time.Millisecond)
	if n := e.focus.quietCloses - closesBefore; n != 0 {
		t.Errorf("quiet closes %d on a flicker of another voice; wanted none", n)
	}
	if n := c.count(live.ExtSpeechStopped) - turnsBefore; n != 1 {
		t.Fatalf("the caller's one sentence was taken as %d turns", n)
	}
}
