package provider

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
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
// rpm is how many requests a minute the hedging may add to what the sessions
// of this process would have made anyway — second requests, and the second
// connections they are made on; 0 is no limit. A vendor's rate limit is the one
// thing a second request can make worse: past it the vendor refuses the first
// request too, and the caller hears nothing at all where they would have heard
// an answer a tenth of a second late. So what hedging adds is bounded, and a
// segment is asked for twice only while there is room under the bound.
//
// It bounds the additions and not the total because the total is not hedging's
// to bound. It was first written as "bring the account to no more than this
// many requests a minute", set to 18 for an account thought to allow about 20
// — and a real call ran at 22 a minute on its own, unrefused, so there was
// never room and all four of its slow syntheses went unhedged, two of them
// the only turns of the call over target.
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
	return HedgeTTSPool(inner, copies, rpm, after, TTSPool{}, log)
}

// Pooled is a provider that holds more than one account, and can be asked for
// a session on a particular one with WithLane: lane n is account n.
type Pooled interface {
	Accounts() int
}

// TTSPool is how two connections are used when they are on different
// accounts, which is a different thing from two on one.
//
// On one account a second request is drawn from the allowance the first needs,
// so it is rationed (rpm) and held back (after). On two accounts it is drawn
// from an allowance of its own, and what bounds it is that account's own
// limit. So:
//
//   - each account's requests are counted, every one of them, over the last
//     minute, and AccountRPM is what one account is trusted with;
//   - the first segment of an answer is asked of both accounts at once (or
//     after After, if that is set) whenever the second has room — the fastest
//     of two draws on every turn rather than insurance on one in ten;
//   - a segment whose own account is at its limit goes to the other when that
//     one has room: the pool carries what one account cannot.
//
// A request the session has to make is made whatever the count says; only the
// second, optional one waits for room. AccountRPM 0 is no limit.
//
// Accounts are not alike. One of the two this was built for carries thirty
// requests a minute and the other refuses the fifteenth in half a minute
// (measured: 38 turns paced at 30 a minute on the second account alone, 42 of
// 76 requests refused from the 28th second on). So AccountRPM is only what an
// account is trusted with when nothing better is known: a provider that knows
// (AccountLimiter) says so per account, and where both accounts have had
// their minute the session goes to the roomier one — the one that will
// actually take it — rather than staying where it happens to be. And a
// request an account refuses is asked of the other at once, room or no room:
// that request is no longer optional.
type TTSPool struct {
	AccountRPM int
	After      time.Duration
}

// AccountLimiter is a Pooled provider that knows how many requests a minute a
// particular account allows. 0 is "not known": TTSPool.AccountRPM applies.
type AccountLimiter interface {
	AccountRPM(account int) int
}

// HedgeTTSPool is HedgeTTS for a provider that may hold several accounts. With
// one account, or a provider that is not Pooled, pool is unused.
func HedgeTTSPool(inner TTS, copies, rpm int, after time.Duration, pool TTSPool, log *slog.Logger) TTS {
	if copies < 2 || inner == nil {
		return inner
	}
	if log == nil {
		log = slog.Default()
	}
	if after < 0 {
		after = 0
	}
	h := &hedgedTTS{inner: inner, log: log, after: after, budget: ttsBudgetFor(inner.Name(), rpm)}
	if p, ok := inner.(Pooled); ok && p.Accounts() > 0 {
		h.accounts, h.pool = p.Accounts(), pool
		if h.pool.After < 0 {
			h.pool.After = 0
		}
	}
	return h
}

type hedgedTTS struct {
	inner  TTS
	log    *slog.Logger
	after  time.Duration
	budget *rpmBudget
	// accounts is how many accounts inner holds when that is more than one,
	// and pool how they are used.
	accounts int
	pool     TTSPool
}

// accountOf is which account a connection is on.
func accountOf(slot *ttsSlot) int {
	if slot == nil {
		return 0
	}
	if a, ok := slot.stream.(interface{ Account() int }); ok {
		return a.Account()
	}
	return 0
}

// apart reports whether two connections are on different accounts.
func (h *hedgedTTS) apart(a, b *ttsSlot) bool {
	return h.accounts > 1 && a != nil && b != nil && accountOf(a) != accountOf(b)
}

// account is the count of one account's requests over the last minute. A nil
// count means no known limit; optional background synthesis then declines.
func (h *hedgedTTS) account(n int) *rpmBudget {
	if h.accounts < 1 {
		return nil
	}
	limit := h.limitOf(n)
	if limit <= 0 {
		return nil
	}
	return ttsBudgetFor(h.accountKey(n), limit)
}

func accountNumber(n int) string { return strconv.Itoa(n) }

// limitOf is how many requests a minute account n is trusted with; 0 is no
// limit.
func (h *hedgedTTS) limitOf(n int) int {
	if l, ok := h.inner.(AccountLimiter); ok {
		if rpm := l.AccountRPM(n); rpm > 0 {
			return rpm
		}
	}
	return h.pool.AccountRPM
}

// roomier reports whether account a is trusted with more than account b.
func (h *hedgedTTS) roomier(a, b int) bool {
	la, lb := h.limitOf(a), h.limitOf(b)
	return lb > 0 && (la <= 0 || la > lb)
}

// made records a request the session had to make on a connection.
func (h *hedgedTTS) made(slot *ttsSlot) {
	h.account(accountOf(slot)).spend(1, true)
}

// room asks for a second, optional request on spare, and records it if there
// is room: under the other account's own limit when the two are apart, under
// what hedging may add to the one account when they are not.
func (h *hedgedTTS) room(primary, spare *ttsSlot) bool {
	if h.apart(primary, spare) {
		return h.account(accountOf(spare)).spend(1, false)
	}
	if !h.budget.spend(1, false) {
		return false
	}
	// Same-account hedge admission retains its existing additions budget, but
	// background work also needs the account's complete usage count.
	h.account(accountOf(spare)).spend(1, true)
	return true
}

// laterSegmentPatience is how many times longer than an urgent segment a
// later one is given before the second connection is asked for it.
const laterSegmentPatience = 3

// hedgeDelay says whether a segment is hedged and how long the first
// connection has to itself: none, for an urgent segment when both are asked
// at once.
func (h *hedgedTTS) hedgeDelay(urgent, apart bool) (wait time.Duration, hedged bool) {
	switch {
	case apart && urgent:
		return h.pool.After, true
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

// spend records n requests that hedging is adding, if the minute has room for
// them, which is what it reports. With must set they are recorded regardless.
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

// fill marks the minute as spent: the vendor has just refused a request, which
// says more about the account's room than any count kept here.
func (b *rpmBudget) fill() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	for len(b.stamps) < b.limit {
		b.stamps = append(b.stamps, now)
	}
}

// full reports whether the last minute already holds as many as the limit.
func (b *rpmBudget) full() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	n := 0
	for _, t := range b.stamps {
		if now.Sub(t) < time.Minute {
			n++
		}
	}
	return n >= b.limit
}

func (h *hedgedTTS) Name() string { return h.inner.Name() }

func (h *hedgedTTS) Open(ctx context.Context, opts TTSOptions) (TTSStream, error) {
	first, err := h.openForeground(ctx, opts, -1)
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
	// With several accounts the second connection is asked for on the one
	// after the first's; the provider gives another if that one is resting.
	octx, want := s.ctx, accountOf(s.primary)
	if s.tts.accounts > 1 {
		want = (want + 1) % s.tts.accounts
		octx = WithLane(s.ctx, want)
		if !s.tts.account(want).spend(1, false) {
			s.mu.Unlock()
			return
		}
	} else if !s.tts.budget.spend(1, false) {
		// No room this minute for a connection nobody is waiting on. It is
		// asked for again when the next first segment comes due.
		s.mu.Unlock()
		return
	} else {
		s.tts.account(want).spend(1, true)
	}
	s.opening = true
	s.mu.Unlock()
	go func() {
		stream, err := s.tts.openForeground(octx, s.opts, want)
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
	apart := s.tts.apart(primary, spare)
	if apart && !spare.busy.Load() && s.tts.account(accountOf(primary)).full() &&
		(!s.tts.account(accountOf(spare)).full() || s.tts.roomier(accountOf(spare), accountOf(primary))) {
		// This account has had its minute's worth. The segment goes to the
		// other if that one has not — or, if both have, when the other is the
		// one that can take more: a count kept here is a belief, and the
		// account believed to allow thirty a minute is a better place to be
		// wrong than the one known to allow ten.
		s.primary, s.spare = spare, primary
		primary, spare = spare, primary
		s.tts.log.Debug("tts pool: this account is at its limit; the session moves to the other",
			"to_account", accountOf(primary)+1)
	}
	s.mu.Unlock()
	wait, hedged := s.tts.hedgeDelay(IsUrgent(ctx), apart)
	if hedged && spare == nil {
		s.openSpare()
	}
	if !hedged || spare == nil {
		s.tts.made(primary)
		return primary.stream.Synthesize(ctx, text)
	}
	// fallbackOnly is a segment that is not being raced but still has
	// somewhere else to go if its own account refuses it.
	fallbackOnly := false
	if wait == 0 {
		race := spare.busy.CompareAndSwap(false, true)
		if race && !s.tts.room(primary, spare) {
			// No room this minute for a second request. One is made, as it
			// would have been anyway.
			spare.busy.Store(false)
			race = false
		}
		if !race {
			if !apart {
				s.tts.made(primary)
				return primary.stream.Synthesize(ctx, text)
			}
			fallbackOnly = true
		} else {
			s.tts.log.Debug("tts hedge: asking both connections at once",
				"accounts", [2]int{accountOf(primary) + 1, accountOf(spare) + 1})
		}
	}
	s.tts.made(primary)
	// With a wait, one request now: the second is asked for, and accounted
	// for, only if this one is slow.

	primary.busy.Store(true)
	slots := [2]*ttsSlot{primary, spare}
	var contexts [2]context.Context
	var cancels [2]context.CancelFunc
	var started [2]chan ttsStarted
	// ask puts the segment to one connection. After the first call it is only
	// ever called from the goroutine below.
	ask := func(n int) {
		cctx, cancel := context.WithCancel(ctx)
		contexts[n] = cctx
		cancels[n] = cancel
		res := make(chan ttsStarted, 1)
		started[n] = res
		go func(slot *ttsSlot) {
			ch, err := slot.stream.Synthesize(cctx, text)
			res <- ttsStarted{ch: ch, err: err}
		}(slots[n])
	}
	ask(0)
	both := wait == 0 && !fallbackOnly
	if both {
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
		out2 := [2]bool{false, !both}
		var lastErr error
		// A queued synthesis can return its cancellation before sending any
		// request. That leaves the connection healthy. An unrelated transport
		// error still breaks it, even if cancellation happened at the same time.
		connectionFailed := func(n int, err error) bool {
			ctxErr := contexts[n].Err()
			return err != nil && (ctxErr == nil || !errors.Is(err, ctxErr))
		}

		var patience <-chan time.Time
		if wait > 0 {
			t := time.NewTimer(wait)
			defer t.Stop()
			patience = t.C
		}
		asked := both
		tried := both
		// second asks the spare connection as well, once per segment, if it is
		// free. When the first is merely slow that is an optional request and
		// wants room in the minute. When the first has failed and the spare is
		// on another account it is the request itself, and is made regardless:
		// the caller is owed the sentence.
		second := func(why string, failed bool) {
			if tried {
				return
			}
			tried, patience = true, nil
			if !slots[1].busy.CompareAndSwap(false, true) {
				return
			}
			if failed && apart {
				s.tts.made(slots[1])
			} else if !s.tts.room(slots[0], slots[1]) {
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
					ch, bad = st.ch, bad || connectionFailed(n, st.err)
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
				second("no audio yet", false)
			case st := <-waiting[0]:
				waiting[0] = nil
				if st.err != nil {
					lastErr, broken[0] = st.err, connectionFailed(0, st.err)
					if broken[0] {
						s.refused(slots[0], apart)
					}
					leave(0)
					if broken[0] {
						second("the first connection failed", true)
					}
				} else {
					audio[0] = st.ch
				}
			case st := <-waiting[1]:
				waiting[1] = nil
				if st.err != nil {
					lastErr, broken[1] = st.err, connectionFailed(1, st.err)
					if broken[1] {
						s.refused(slots[1], apart)
					}
					leave(1)
				} else {
					audio[1] = st.ch
				}
			case c, ok := <-audio[0]:
				if ok && c.Err == nil {
					winner, first = 0, c
				} else {
					if c.Err != nil {
						lastErr, broken[0] = c.Err, connectionFailed(0, c.Err)
						if broken[0] {
							s.refused(slots[0], apart)
						}
					}
					leave(0)
					if broken[0] {
						second("the first connection failed", true)
					}
				}
			case c, ok := <-audio[1]:
				if ok && c.Err == nil {
					winner, first = 1, c
				} else {
					if c.Err != nil {
						lastErr, broken[1] = c.Err, connectionFailed(1, c.Err)
						if broken[1] {
							s.refused(slots[1], apart)
						}
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
				"copy", winner+1, "account", accountOf(slots[winner])+1, "ms", time.Since(began).Milliseconds())
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

// refused notes that an account has just failed a request. With accounts
// apart that is taken as the account having no room left this minute, so that
// nothing optional is sent its way and the session does not settle there.
func (s *hedgedTTSStream) refused(slot *ttsSlot, apart bool) {
	if apart {
		s.tts.account(accountOf(slot)).fill()
	}
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
