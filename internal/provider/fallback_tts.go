package provider

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// FallbackTTS returns a TTS that speaks on primary and, when primary has
// produced no audio for a segment after a wait, asks second — another vendor
// — for the same segment and plays whichever answers first.
//
// It is for the thing a second connection to the same vendor cannot fix: the
// vendor stalling. Measured on three runs of the primary synthesizer, once in
// each, both of its connections had nothing for three to eight seconds at
// once, while the chat model and the recognizer were as usual: a stall of the
// vendor, not the path. A second vendor is at the end of a different path and
// in a different data centre, and its own first audio (drama-3 on Fish Audio
// from the box: 424 ms at the median, 748 at worst in 20 paced turns, none
// refused) is what a stalled turn then costs, plus the wait.
//
// The second vendor is slower than the first by about its network round trip
// (200 ms from the box), so it is not raced on every segment: asked at once
// it would lose nearly always and cost a request each time. It is asked only
// after wait with nothing from primary, which — set just past where primary
// usually answers — is one segment in twenty, and the ones that matter. A
// later segment of an answer gets three times the wait, as the same-vendor
// hedge gives it: it is synthesized under cover of audio already playing.
//
// Primary's own hedging (HedgeTTSPool) is inside it and unchanged: by the time
// this asks the second vendor, primary has already asked both of its
// connections. The second vendor's session is opened with the primary's and
// kept for the call; a session that cannot be opened on it is a session
// without the fallback, not a failed one. The sample rate is primary's; the
// second vendor is opened at the same rate.
//
// A primary that fails a segment outright (an error before any audio) is
// followed by the second vendor at once, whatever the wait.
//
// second nil, or wait <= 0, returns primary unchanged.
func FallbackTTS(primary, second TTS, wait time.Duration, log *slog.Logger) TTS {
	if primary == nil || second == nil || wait <= 0 {
		return primary
	}
	if log == nil {
		log = slog.Default()
	}
	return &fallbackTTS{primary: primary, second: second, wait: wait, log: log}
}

type fallbackTTS struct {
	primary, second TTS
	wait            time.Duration
	log             *slog.Logger
}

func (f *fallbackTTS) Name() string { return f.primary.Name() + "+" + f.second.Name() }

func (f *fallbackTTS) Open(ctx context.Context, opts TTSOptions) (TTSStream, error) {
	p, err := f.primary.Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	s := &fallbackStream{tts: f, ctx: ctx, opts: opts, primary: p}
	s.opts.SampleRate = p.SampleRate()
	s.openSecond()
	return s, nil
}

type fallbackStream struct {
	tts  *fallbackTTS
	ctx  context.Context
	opts TTSOptions

	mu      sync.Mutex
	primary TTSStream
	second  TTSStream
	busy    bool // the second vendor is still on a segment it lost
	opening bool
	closed  bool
	// spoke counts segments the second vendor spoke, for the log at close.
	asked, spoke int
}

func (s *fallbackStream) openSecond() {
	s.mu.Lock()
	if s.closed || s.opening || s.second != nil {
		s.mu.Unlock()
		return
	}
	s.opening = true
	s.mu.Unlock()
	go func() {
		st, err := s.tts.second.Open(s.ctx, s.opts)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.opening = false
		if err != nil || s.closed {
			if err != nil {
				s.tts.log.Warn("tts fallback: the second vendor could not be opened; this session has no fallback",
					"vendor", s.tts.second.Name(), "err", err)
			} else {
				_ = st.Close()
			}
			return
		}
		if st.SampleRate() != s.opts.SampleRate {
			s.tts.log.Warn("tts fallback: the second vendor speaks at another rate; this session has no fallback",
				"vendor", s.tts.second.Name(), "rate", st.SampleRate(), "want", s.opts.SampleRate)
			_ = st.Close()
			return
		}
		s.second = st
	}()
}

func (s *fallbackStream) SampleRate() int { return s.primary.SampleRate() }

func (s *fallbackStream) Close() error {
	s.mu.Lock()
	s.closed = true
	second := s.second
	s.second = nil
	asked, spoke := s.asked, s.spoke
	s.mu.Unlock()
	if second != nil {
		_ = second.Close()
	}
	if asked > 0 {
		s.tts.log.Info("tts fallback: the second vendor this session", "vendor", s.tts.second.Name(), "asked", asked, "spoke", spoke)
	}
	return s.primary.Close()
}

// Prewarm implements Prewarmer when the connections do.
func (s *fallbackStream) Prewarm(ctx context.Context) error {
	var err error
	if p, ok := s.primary.(Prewarmer); ok {
		err = p.Prewarm(ctx)
	}
	s.mu.Lock()
	second, busy := s.second, s.busy
	s.mu.Unlock()
	if second == nil {
		s.openSecond()
	} else if p, ok := second.(Prewarmer); ok && !busy {
		_ = p.Prewarm(ctx)
	}
	return err
}

// takeSecond claims the second vendor's connection for a segment, or reports
// that there is none free.
func (s *fallbackStream) takeSecond() TTSStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.second == nil || s.busy {
		return nil
	}
	s.busy = true
	s.asked++
	return s.second
}

func (s *fallbackStream) Synthesize(ctx context.Context, text string) (<-chan TTSChunk, error) {
	wait := s.tts.wait
	if !IsUrgent(ctx) {
		wait *= laterSegmentPatience
	}
	s.mu.Lock()
	hasSecond := s.second != nil && !s.busy
	s.mu.Unlock()
	if !hasSecond {
		// No second vendor this segment (none yet, or it is still on a
		// segment it lost): the primary alone, as before.
		s.openSecond()
		return s.primary.Synthesize(ctx, text)
	}
	pctx, pcancel := context.WithCancel(ctx)
	pch, err := s.primary.Synthesize(pctx, text)
	if err != nil {
		pcancel()
		// Failed before it began: the second vendor is the request itself.
		second := s.takeSecond()
		if second == nil {
			return nil, err
		}
		s.tts.log.Warn("tts fallback: the primary failed the segment; asking the second vendor", "err", err)
		return s.speakOn(ctx, second, text, time.Now()), nil
	}
	out := make(chan TTSChunk, 64)
	began := time.Now()
	go func() {
		defer close(out)
		timer := time.NewTimer(wait)
		defer timer.Stop()
		var sch <-chan TTSChunk
		var sstart chan ttsStarted // the second vendor's Synthesize, when asked
		var scancel context.CancelFunc
		var second TTSStream
		// ask puts the segment to the second vendor, once, in the background:
		// its Synthesize returns when the vendor has answered the request,
		// which is a round trip, and the primary's audio must not wait on it.
		ask := func(why string) {
			if second != nil {
				return
			}
			second = s.takeSecond()
			if second == nil {
				return
			}
			var sctx context.Context
			sctx, scancel = context.WithCancel(ctx)
			sstart = make(chan ttsStarted, 1)
			go func(st TTSStream, res chan<- ttsStarted) {
				ch, err := st.Synthesize(sctx, text)
				res <- ttsStarted{ch: ch, err: err}
			}(second, sstart)
			s.tts.log.Info("tts fallback: asking the second vendor too", "why", why,
				"vendor", s.tts.second.Name(), "waited_ms", time.Since(began).Milliseconds(), "chars", len([]rune(text)))
		}
		// finish drains and frees the second vendor's connection once the
		// race is over, in the background, waiting for its Synthesize to
		// return if it has not yet.
		finish := func() {
			if second == nil {
				return
			}
			scancel()
			ch, start := sch, sstart
			go func() {
				if ch == nil && start != nil {
					st := <-start
					ch = st.ch
				}
				if ch != nil {
					drainTTS(ch)
				}
				s.release()
			}()
		}
		var pErr error
		for {
			select {
			case <-ctx.Done():
				pcancel()
				finish()
				return
			case <-timer.C:
				ask("no audio from the primary yet")
			case st := <-sstart:
				sstart = nil
				if st.err != nil {
					if ctx.Err() == nil {
						s.tts.log.Warn("tts fallback: the second vendor refused the segment", "err", st.err)
					}
					s.release()
					second, scancel = nil, nil
					if pch == nil {
						pcancel()
						if pErr != nil {
							select {
							case out <- TTSChunk{Err: pErr}:
							case <-ctx.Done():
							}
						}
						return
					}
					continue
				}
				sch = st.ch
			case c, ok := <-pch:
				if !ok {
					// The primary finished without a single chunk: nothing to
					// say, or an error already passed on. If the second
					// vendor was asked, its audio is still worth playing.
					if second == nil {
						pcancel()
						if pErr != nil {
							select {
							case out <- TTSChunk{Err: pErr}:
							case <-ctx.Done():
							}
						}
						return
					}
					pch = nil
					continue
				}
				if c.Err != nil {
					pErr = c.Err
					if second == nil {
						ask("the primary failed the segment")
						if second == nil {
							pcancel()
							select {
							case out <- c:
							case <-ctx.Done():
							}
							return
						}
					}
					pch = nil
					continue
				}
				// The primary spoke. The second vendor, if asked, is dropped.
				finish()
				s.relay(ctx, out, c, pch, pcancel, began, 0)
				return
			case c, ok := <-sch:
				if !ok || c.Err != nil {
					if c.Err != nil {
						s.tts.log.Warn("tts fallback: the second vendor failed the segment", "err", c.Err)
					}
					s.release()
					second, sch = nil, nil
					if pch == nil {
						// Both are gone.
						pcancel()
						if pErr != nil {
							select {
							case out <- TTSChunk{Err: pErr}:
							case <-ctx.Done():
							}
						}
						return
					}
					continue
				}
				// The second vendor spoke first. The primary is abandoned for
				// this segment; it resynchronizes itself.
				pcancel()
				go drainTTS(pch)
				s.mu.Lock()
				s.spoke++
				s.mu.Unlock()
				s.tts.log.Info("tts fallback: the second vendor spoke first", "vendor", s.tts.second.Name(),
					"ms", time.Since(began).Milliseconds(), "chars", len([]rune(text)))
				s.relay(ctx, out, c, sch, func() { scancel(); s.release() }, began, 1)
				return
			}
		}
	}()
	return out, nil
}

// speakOn is the segment on the second vendor alone.
func (s *fallbackStream) speakOn(ctx context.Context, second TTSStream, text string, began time.Time) <-chan TTSChunk {
	sctx, cancel := context.WithCancel(ctx)
	ch, err := second.Synthesize(sctx, text)
	out := make(chan TTSChunk, 64)
	if err != nil {
		cancel()
		s.release()
		out <- TTSChunk{Err: err}
		close(out)
		return out
	}
	go func() {
		defer close(out)
		for c := range ch {
			select {
			case out <- c:
			case <-ctx.Done():
				cancel()
				go drainTTS(ch)
				s.release()
				return
			}
		}
		cancel()
		s.release()
	}()
	return out
}

// relay passes first and the rest of ch to out, and runs done at the end.
func (s *fallbackStream) relay(ctx context.Context, out chan<- TTSChunk, first TTSChunk, ch <-chan TTSChunk, done func(), began time.Time, who int) {
	defer done()
	for c, ok := first, true; ok; c, ok = <-ch {
		select {
		case out <- c:
		case <-ctx.Done():
			go drainTTS(ch)
			return
		}
	}
}

func (s *fallbackStream) release() {
	s.mu.Lock()
	s.busy = false
	s.mu.Unlock()
}
