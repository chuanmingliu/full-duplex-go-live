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
// copies < 2 returns inner unchanged.
func HedgeTTS(inner TTS, copies int, log *slog.Logger) TTS {
	if copies < 2 || inner == nil {
		return inner
	}
	if log == nil {
		log = slog.Default()
	}
	return &hedgedTTS{inner: inner, log: log}
}

type hedgedTTS struct {
	inner TTS
	log   *slog.Logger
}

func (h *hedgedTTS) Name() string { return h.inner.Name() }

func (h *hedgedTTS) Open(ctx context.Context, opts TTSOptions) (TTSStream, error) {
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
	if !IsUrgent(ctx) || spare == nil || !spare.busy.CompareAndSwap(false, true) {
		return primary.stream.Synthesize(ctx, text)
	}

	primary.busy.Store(true)
	slots := [2]*ttsSlot{primary, spare}
	var cancels [2]context.CancelFunc
	var started [2]chan ttsStarted
	for i, slot := range slots {
		cctx, cancel := context.WithCancel(ctx)
		cancels[i] = cancel
		started[i] = make(chan ttsStarted, 1)
		go func(slot *ttsSlot, res chan<- ttsStarted) {
			ch, err := slot.stream.Synthesize(cctx, text)
			res <- ttsStarted{ch: ch, err: err}
		}(slot, started[i])
	}

	out := make(chan TTSChunk, 64)
	began := time.Now()
	go func() {
		defer close(out)
		// For each copy: the channel its Synthesize will report on (nil once it
		// has), its audio (nil until then, and again once it is out of the
		// race), and whether its connection is fit to use again.
		waiting := [2]<-chan ttsStarted{started[0], started[1]}
		var audio [2]<-chan TTSChunk
		var out2, broken [2]bool
		var lastErr error

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
			case st := <-waiting[0]:
				waiting[0] = nil
				if st.err != nil {
					lastErr, broken[0] = st.err, true
					leave(0)
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
			// Neither produced audio. An error is the caller's to act on; two
			// empty syntheses are an empty synthesis.
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
		s.tts.log.Debug("tts hedge: a copy produced audio first",
			"copy", winner+1, "ms", time.Since(began).Milliseconds())

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
