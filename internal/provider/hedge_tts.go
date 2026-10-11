package provider

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

type urgentKey struct{}

// WithUrgent marks a synthesis as one a caller is waiting on in silence: the
// first segment of an answer. Everything after it is synthesized while that
// one plays.
func WithUrgent(ctx context.Context) context.Context {
	return context.WithValue(ctx, urgentKey{}, true)
}

// IsUrgent reports whether ctx was marked by WithUrgent.
func IsUrgent(ctx context.Context) bool {
	v, _ := ctx.Value(urgentKey{}).(bool)
	return v
}

// HedgeTTS returns a TTS whose sessions hold two connections and, for a
// segment marked urgent, ask both and play whichever produces audio first.
//
// A synthesizer's time to first audio has a tail like a chat model's: measured
// on a 70-turn call, 180 ms at the median and over 300 one turn in ten, 589 at
// worst — and on a turn's first segment all of it is silence the caller is
// listening to. The second request costs the segment's characters again and
// nothing else, since only that first, short segment is raced: every later one
// goes to one connection, as before, and is synthesized under cover of the
// audio already playing.
//
// The copy that loses is cancelled, and its connection is left alone until it
// has finished putting itself back in order; a segment that comes due before
// then is simply not raced. A session whose second connection cannot be opened
// is a session without hedging, not a failed one.
//
// rpm is how many requests a minute the hedging may bring the provider's
// account to, across every session of this process; 0 is no limit. A vendor's
// rate limit is the one thing a second request can make worse — past it the
// vendor refuses the first request too, and the caller hears nothing at all
// where they would have heard an answer a tenth of a second late. So a segment
// is raced only while there is room for it, and the second connection is only
// dialled while there is room for that.
//
// after, when it is more than zero, is the frugal form, for an account whose
// rate limit has no room for a second request on every turn. The first
// connection is asked alone, and the second only when after has passed with no
// audio from the first — which, set just past where the synthesizer usually
// answers, is one segment in ten or twenty, and exactly the ones worth a
// second request. It is what a slow first audio costs that changes: after
// plus an ordinary synthesis, rather than however long the first connection
// takes. Measured on two calls, 93 turns: 180 ms at the median, and 350, 402,
// 491, 613, 1284 and 1763 ms on the six worst.
//
// In that form every segment is covered, not only the urgent one, with three
// times the wait for the rest: a later segment is synthesized under cover of
// audio already playing, so it has that long before its lateness is heard —
// but on one of those calls a second segment did not arrive in 3.3 s, and the
// caller sat in silence in the middle of an answer. And a first connection
// that fails outright is followed by the second at once.
//
// copies < 2 returns inner unchanged.
func HedgeTTS(inner TTS, copies, rpm int, after time.Duration, log *slog.Logger) TTS {
	if copies < 2 || inner == nil {
		return inner
	}
	if log == nil {
		log = slog.Default()
	}
	if after < 0 {
		after = 0
	}
	return &hedgedTTS{inner: inner, log: log, after: after, budget: ttsBudgetFor(inner.Name(), rpm)}
}

type hedgedTTS struct {
	inner  TTS
	log    *slog.Logger
	after  time.Duration
	budget *rpmBudget
}

// laterSegmentPatience is how many times longer than an urgent segment a
// later one is given before the second connection is asked for it.
const laterSegmentPatience = 3

// hedgeDelay says whether a segment is hedged and how long the first
// connection has to itself: none, for an urgent segment when both are asked
// at once.
func (h *hedgedTTS) hedgeDelay(urgent bool) (wait time.Duration, hedged bool) {
	switch {
	case h.after <= 0:
		return 0, urgent
	case urgent:
		return h.after, true
	default:
		return h.after * laterSegmentPatience, true
	}
}

// rpmBudget counts requests over the last minute.
type rpmBudget struct {
	mu     sync.Mutex
	limit  int
	stamps []time.Time
}

var ttsBudgets sync.Map // provider name → *rpmBudget

func ttsBudgetFor(name string, rpm int) *rpmBudget {
	if rpm <= 0 {
		return nil
	}
	b, _ := ttsBudgets.LoadOrStore(name, &rpmBudget{limit: rpm})
	budget := b.(*rpmBudget)
	budget.mu.Lock()
	budget.limit = rpm
	budget.mu.Unlock()
	return budget
}

// spend records n requests. With must set they are recorded regardless — they
// are being made either way — and otherwise only if the minute has room for
// them, which is what it reports.
func (b *rpmBudget) spend(n int, must bool) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	cut := 0
	for cut < len(b.stamps) && now.Sub(b.stamps[cut]) >= time.Minute {
		cut++
	}
	b.stamps = b.stamps[cut:]
	if !must && len(b.stamps)+n > b.limit {
		return false
	}
	for i := 0; i < n; i++ {
		b.stamps = append(b.stamps, now)
	}
	return true
}

func (h *hedgedTTS) Name() string { return h.inner.Name() }

func (h *hedgedTTS) Open(ctx context.Context, opts TTSOptions) (TTSStream, error) {
	h.budget.spend(1, true)
	first, err := h.inner.Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	s := &hedgedTTSStream{tts: h, ctx: ctx, opts: opts, primary: &ttsSlot{stream: first}}
	s.openSpare()
	return s, nil
}

// ttsSlot is one connection and whether a synthesis is still using it.
type ttsSlot struct {
	stream TTSStream
	busy   atomic.Bool
}

type hedgedTTSStream struct {
	tts  *hedgedTTS
	ctx  context.Context
	opts TTSOptions

	mu      sync.Mutex
	primary *ttsSlot
	spare   *ttsSlot
	opening bool
	closed  bool
}

// openSpare dials the second connection in the background.
func (s *hedgedTTSStream) openSpare() {
	s.mu.Lock()
	if s.closed || s.opening || s.spare != nil {
		s.mu.Unlock()
		return
	}
	if !s.tts.budget.spend(1, false) {
		// No room this minute for a connection nobody is waiting on. It is
		// asked for again when the next first segment comes due.
		s.mu.Unlock()
		return
	}
	s.opening = true
	s.mu.Unlock()
	go func() {
		stream, err := s.tts.inner.Open(s.ctx, s.opts)
		s.mu.Lock()
		s.opening = false
		if err != nil || s.closed {
			s.mu.Unlock()
			if err != nil {
				s.tts.log.Debug("tts hedge: no second connection; the next first segment goes unhedged", "err", err)
			} else {
				_ = stream.Close()
			}
			return
		}
		s.spare = &ttsSlot{stream: stream}
		s.mu.Unlock()
	}()
}

func (s *hedgedTTSStream) SampleRate() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.primary.stream.SampleRate()
}

func (s *hedgedTTSStream) Close() error {
	s.mu.Lock()
	s.closed = true
	primary, spare := s.primary, s.spare
	s.spare = nil
	s.mu.Unlock()
	err := primary.stream.Close()
	if spare != nil {
		_ = spare.stream.Close()
	}
	return err
}

// Prewarm implements Prewarmer when the connections do.
func (s *hedgedTTSStream) Prewarm(ctx context.Context) error {
	s.mu.Lock()
	primary, spare := s.primary, s.spare
	s.mu.Unlock()
	var err error
	if p, ok := primary.stream.(Prewarmer); ok {
		err = p.Prewarm(ctx)
	}
	if spare == nil {
		s.openSpare()
	} else if p, ok := spare.stream.(Prewarmer); ok && !spare.busy.Load() {
		_ = p.Prewarm(ctx)
	}
	return err
}

type ttsStarted struct {
	ch  <-chan TTSChunk
	err error
}

func drainTTS(ch <-chan TTSChunk) {
	for range ch {
	}
}

func (s *hedgedTTSStream) Synthesize(ctx context.Context, text string) (<-chan TTSChunk, error) {
	s.mu.Lock()
	primary, spare := s.primary, s.spare
	s.mu.Unlock()
	wait, hedged := s.tts.hedgeDelay(IsUrgent(ctx))
	if hedged && spare == nil {
		s.openSpare()
	}
	if !hedged || spare == nil {
		s.tts.budget.spend(1, true)
		return primary.stream.Synthesize(ctx, text)
	}
	if wait > 0 {
		// One request now. The second is asked for, and accounted for, only
		// if this one is slow.
		s.tts.budget.spend(1, true)
	} else {
		race := spare.busy.CompareAndSwap(false, true)
		if race && !s.tts.budget.spend(2, false) {
			// No room this minute for a second request. One is made, as it
			// would have been anyway.
			spare.busy.Store(false)
			race = false
		}
		if !race {
			s.tts.budget.spend(1, true)
			return primary.stream.Synthesize(ctx, text)
		}
	}

	primary.busy.Store(true)
	slots := [2]*ttsSlot{primary, spare}
	var cancels [2]context.CancelFunc
	var started [2]chan ttsStarted
	// ask puts the segment to one connection. After the first call it is only
	// ever called from the goroutine below.
	ask := func(n int) {
		cctx, cancel := context.WithCancel(ctx)
		cancels[n] = cancel
		res := make(chan ttsStarted, 1)
		started[n] = res
		go func(slot *ttsSlot) {
			ch, err := slot.stream.Synthesize(cctx, text)
			res <- ttsStarted{ch: ch, err: err}
		}(slots[n])
	}
	ask(0)
	if wait == 0 {
		ask(1)
	}

	out := make(chan TTSChunk, 64)
	began := time.Now()
	go func() {
		defer close(out)
		// For each copy: the channel its Synthesize will report on (nil once it
		// has), its audio (nil until then, and again once it is out of the
		// race), whether it is out of the race — as the second is until it is
		// asked — and whether its connection is fit to use again.
		waiting := [2]<-chan ttsStarted{started[0], started[1]}
		var audio [2]<-chan TTSChunk
		var broken [2]bool
		out2 := [2]bool{false, wait > 0}
		var lastErr error

		var patience <-chan time.Time
		if wait > 0 {
			t := time.NewTimer(wait)
			defer t.Stop()
			patience = t.C
		}
		asked := wait == 0
		// second asks the spare connection as well, if it is free and the
		// minute has room for the request. It is tried once per segment.
		second := func(why string) {
			if patience == nil {
				return
			}
			patience = nil
			if !slots[1].busy.CompareAndSwap(false, true) {
				return
			}
			if !s.tts.budget.spend(1, false) {
				slots[1].busy.Store(false)
				s.tts.log.Debug("tts hedge: no room this minute for a second request",
					"waited_ms", time.Since(began).Milliseconds())
				return
			}
			ask(1)
			waiting[1], out2[1], asked = started[1], false, true
			s.tts.log.Debug("tts hedge: asking the second connection too",
				"why", why, "waited_ms", time.Since(began).Milliseconds())
		}

		// leave takes a copy out of the race: it is cancelled, whatever it
		// still has to say is read and dropped, and its connection is free
		// again only when the provider has finished with it.
		leave := func(n int) {
			out2[n] = true
			cancels[n]()
			slot, wait, ch, bad := slots[n], waiting[n], audio[n], broken[n]
			waiting[n], audio[n] = nil, nil
			go func() {
				if wait != nil {
					st := <-wait
					ch, bad = st.ch, bad || st.err != nil
				}
				if ch != nil {
					drainTTS(ch)
				}
				slot.busy.Store(false)
				if bad {
					s.replaceIfSpare(slot)
				}
			}()
		}

		winner := -1
		var first TTSChunk
		for winner < 0 && !(out2[0] && out2[1]) {
			select {
			case <-ctx.Done():
				for i := range slots {
					if !out2[i] {
						leave(i)
					}
				}
				return
			case <-patience:
				second("no audio yet")
			case st := <-waiting[0]:
				waiting[0] = nil
				if st.err != nil {
					lastErr, broken[0] = st.err, true
					leave(0)
					second("the first connection failed")
				} else {
					audio[0] = st.ch
				}
			case st := <-waiting[1]:
				waiting[1] = nil
				if st.err != nil {
					lastErr, broken[1] = st.err, true
					leave(1)
				} else {
					audio[1] = st.ch
				}
			case c, ok := <-audio[0]:
				if ok && c.Err == nil {
					winner, first = 0, c
				} else {
					if c.Err != nil {
						lastErr, broken[0] = c.Err, true
					}
					leave(0)
					if c.Err != nil {
						second("the first connection failed")
					}
				}
			case c, ok := <-audio[1]:
				if ok && c.Err == nil {
					winner, first = 1, c
				} else {
					if c.Err != nil {
						lastErr, broken[1] = c.Err, true
					}
					leave(1)
				}
			}
		}
		if winner < 0 {
			// Nothing produced audio. An error is the caller's to act on; an
			// empty synthesis is an empty synthesis.
			if lastErr != nil {
				select {
				case out <- TTSChunk{Err: lastErr}:
				case <-ctx.Done():
				}
			}
			return
		}

		// This copy spoke first. The other is abandoned, and this one's
		// connection is the session's from here on.
		if other := 1 - winner; !out2[other] {
			leave(other)
		}
		s.mu.Lock()
		if s.spare == slots[winner] {
			s.primary, s.spare = s.spare, s.primary
		}
		s.mu.Unlock()
		if broken[1-winner] {
			s.replaceIfSpare(slots[1-winner])
		}
		if asked {
			s.tts.log.Debug("tts hedge: a copy produced audio first",
				"copy", winner+1, "ms", time.Since(began).Milliseconds())
		}

		ch := audio[winner]
		defer slots[winner].busy.Store(false)
		defer cancels[winner]()
		for c, ok := first, true; ok; c, ok = <-ch {
			select {
			case out <- c:
			case <-ctx.Done():
				go drainTTS(ch)
				return
			}
		}
	}()
	return out, nil
}

// replaceIfSpare closes a connection that failed and dials another in its
// place, when it is the spare. A failed primary is the caller's to drop: it is
// told by the error chunk, and closes the whole session.
func (s *hedgedTTSStream) replaceIfSpare(slot *ttsSlot) {
	s.mu.Lock()
	isSpare := s.spare == slot
	if isSpare {
		s.spare = nil
	}
	s.mu.Unlock()
	if isSpare {
		_ = slot.stream.Close()
		s.openSpare()
	}
}
