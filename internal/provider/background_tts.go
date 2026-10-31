package provider

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// AudioIdentity describes the effective settings, not merely requested options.
// Account and Settings are opaque fingerprints; neither contains credentials.
// An identity is stable for the life of its synthesis connection.
type AudioIdentity struct {
	Provider, Account, Model, Voice, Language, Settings string
	Speed                                               float64
	Rate                                                int
}

type IdentifiedTTSStream interface {
	TTSStream
	EffectiveIdentity() AudioIdentity
}

// BackgroundAccountTTS is deliberately stricter than TTS.Open. It opens exactly
// the named account once, never fails over, and never reconnects that stream.
// Close must safely interrupt concurrent opening/reading without waiting for
// provider timeouts. AccountKey identifies the actual account, independent of
// pool position or endpoint paths that share its rate allowance.
type BackgroundAccountTTS interface {
	ConnectionObservedTTS
	Pooled
	AccountKey(int) string
	OpenBackground(context.Context, TTSOptions, int) (IdentifiedTTSStream, error)
}

var (
	ErrBackgroundUnavailable = errors.New("tts background: no optional capacity")
	ErrBackgroundClosed      = errors.New("tts background: stream retired")
	ErrBackgroundUnsupported = errors.New("tts background: provider lacks strict account support")
)

// BackgroundTTSFactory retains the very same raw provider and account policy as
// the foreground hedge wrapper. It is not another registry/factory lookup.
type BackgroundTTSFactory struct {
	hedge *hedgedTTS
	raw   BackgroundAccountTTS
}

// BackgroundTTSFor must be called on the result of HedgeTTSPool, before adding a
// vendor fallback. Unsupported wrappers fail closed rather than bypass admission.
func BackgroundTTSFor(tts TTS) (*BackgroundTTSFactory, error) {
	h, ok := tts.(*hedgedTTS)
	if !ok {
		return nil, ErrBackgroundUnsupported
	}
	raw, ok := h.inner.(BackgroundAccountTTS)
	if !ok {
		return nil, ErrBackgroundUnsupported
	}
	return &BackgroundTTSFactory{hedge: h, raw: raw}, nil
}

// New creates a lazy call-owned handle. It neither opens nor starts a goroutine
// per candidate. The owner must Close it, also when no candidate was admitted.
func (f *BackgroundTTSFactory) New(ctx context.Context, opts TTSOptions) *BackgroundTTSStream {
	life, cancel := context.WithCancel(ctx)
	b := &BackgroundTTSStream{factory: f, opts: opts, ctx: life, cancel: cancel}
	context.AfterFunc(life, func() { b.Close() })
	return b
}

// accountActivity is shared across wrappers by actual account identity. Keeping
// one background handle per account also bounds idle optional connections.
type accountActivity struct {
	mu         sync.Mutex
	foreground int
	background *BackgroundTTSStream
	active     bool
}

var ttsActivities sync.Map // account key -> *accountActivity

func (h *hedgedTTS) accountKey(n int) string {
	if p, ok := h.inner.(interface{ AccountKey(int) string }); ok {
		return h.inner.Name() + "#" + p.AccountKey(n)
	}
	return h.inner.Name() + "#" + accountNumber(n)
}

func (h *hedgedTTS) activity(n int) *accountActivity {
	v, _ := ttsActivities.LoadOrStore(h.accountKey(n), &accountActivity{})
	return v.(*accountActivity)
}

// beginForeground never waits for background completion. Only active optional
// provider work is retired; an idle persistent background connection survives.
func (a *accountActivity) beginForeground() func() {
	a.mu.Lock()
	a.foreground++
	var b *BackgroundTTSStream
	if a.active {
		b = a.background
	}
	a.mu.Unlock()
	if b != nil {
		b.retire(ErrBackgroundClosed)
	}
	return func() { a.mu.Lock(); a.foreground--; a.mu.Unlock() }
}

func (h *hedgedTTS) openForeground(ctx context.Context, opts TTSOptions, prepaid int) (TTSStream, error) {
	_, track := h.inner.(BackgroundAccountTTS)
	// Open may fail over internally, so protect every possible destination.
	ends := make([]func(), 0, h.accounts)
	if track {
		for n := 0; n < h.accounts; n++ {
			ends = append(ends, h.activity(n).beginForeground())
		}
	}
	defer func() {
		for _, end := range ends {
			end()
		}
	}()
	var s TTSStream
	var err error
	observed, reportsAttempts := h.inner.(ConnectionObservedTTS)
	if reportsAttempts {
		s, err = observed.OpenObserved(ctx, opts, h.connectionAttempt(prepaid))
	} else {
		s, err = h.inner.Open(ctx, opts)
	}
	if err != nil {
		return nil, err
	}
	n := 0
	if p, ok := s.(interface{ Account() int }); ok {
		n = p.Account()
	}
	if !reportsAttempts && n != prepaid {
		h.account(n).spend(1, true)
	}
	if !track {
		return s, nil
	}
	return &foregroundTTSStream{TTSStream: s, activity: h.activity(n), account: n}, nil
}

type foregroundTTSStream struct {
	TTSStream
	activity *accountActivity
	account  int
}

func (s *foregroundTTSStream) Account() int { return s.account }
func (s *foregroundTTSStream) Prewarm(ctx context.Context) error {
	p, ok := s.TTSStream.(Prewarmer)
	if !ok {
		return nil
	}
	end := s.activity.beginForeground()
	defer end()
	return p.Prewarm(ctx)
}
func (s *foregroundTTSStream) Synthesize(ctx context.Context, text string) (<-chan TTSChunk, error) {
	end := s.activity.beginForeground()
	ctx, cancel := context.WithCancel(ctx)
	ch, err := s.TTSStream.Synthesize(ctx, text)
	if err != nil || ch == nil {
		cancel()
		end()
		return ch, err
	}
	out := make(chan TTSChunk, 1)
	go func() {
		defer close(out)
		defer end()
		defer cancel()
		stall := consumerTimeout(s.TTSStream)
		timer := time.NewTimer(stall)
		timer.Stop()
		defer timer.Stop()
		for c := range ch {
			timer.Reset(stall)
			select {
			case out <- c:
				timer.Stop()
			case <-ctx.Done():
				cancel()
				drainTTS(ch)
				return
			case <-timer.C:
				cancel()
				drainTTS(ch)
				// As with background failures, discard a queued partial chunk
				// when needed to leave a terminal failure for a resumed reader.
				for {
					select {
					case out <- TTSChunk{Err: errors.New("tts: output consumer stopped reading")}:
						return
					case <-out:
					}
				}
			}
		}
	}()
	return out, nil
}

// BackgroundTTSStream permits only sequential optional requests. It retires on
// any failed/abandoned provider attempt, without reopening. A caller enforcing a
// per-call budget charges every TrySynthesize with attempted=true, including an
// opening failure. Admission failures and concurrent calls are not attempts.
type BackgroundTTSStream struct {
	factory      *BackgroundTTSFactory
	opts         TTSOptions
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	closed, busy bool
	closing      bool
	err          error
	stream       IdentifiedTTSStream
	identity     AudioIdentity
	account      int
	activity     *accountActivity
}

func (b *BackgroundTTSStream) EffectiveIdentity() (AudioIdentity, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.identity, b.stream != nil
}

// SampleRate is zero until the first successful lazy connection.
func (b *BackgroundTTSStream) SampleRate() int { id, _ := b.EffectiveIdentity(); return id.Rate }

func (b *BackgroundTTSStream) Close() error { b.retire(ErrBackgroundClosed); return nil }

func (b *BackgroundTTSStream) retire(err error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed, b.err = true, err
	b.cancel()
	s, a, busy := b.stream, b.activity, b.busy
	b.closing = s != nil
	b.mu.Unlock()
	// The foreground path calls retire; no provider Close can delay it.
	if s != nil {
		go func() {
			_ = s.Close()
			b.mu.Lock()
			b.closing = false
			idle := !b.busy
			b.mu.Unlock()
			if a != nil && idle {
				b.release(a, true)
			}
		}()
	}
	if s == nil && a != nil && !busy {
		b.release(a, true)
	}
}

func (b *BackgroundTTSStream) release(a *accountActivity, retired bool) {
	a.mu.Lock()
	if a.background == b {
		a.active = false
		if retired {
			a.background = nil
		}
	}
	a.mu.Unlock()
}

func (b *BackgroundTTSStream) finish() {
	b.mu.Lock()
	b.busy = false
	a, retired := b.activity, b.closed && !b.closing
	b.mu.Unlock()
	if a != nil {
		b.release(a, retired)
	}
}

// admit charges connection setup and synthesis separately on the same account
// ledger used by HedgeTTSPool. No known finite limit means no optional work.
func (b *BackgroundTTSStream) admit() bool {
	b.mu.Lock()
	old, account := b.activity, b.account
	b.mu.Unlock()
	for n := 0; n < b.factory.raw.Accounts(); n++ {
		if old != nil && n != account {
			continue
		}
		a := b.factory.hedge.activity(n)
		budget := b.factory.hedge.account(n)
		if budget == nil {
			continue
		}
		a.mu.Lock()
		if a.foreground > 0 || a.active || (a.background != nil && a.background != b) {
			a.mu.Unlock()
			continue
		}
		cost := 1
		if old == nil {
			cost++
		}
		if !budget.spend(cost, false) {
			a.mu.Unlock()
			continue
		}
		a.background, a.active = b, true
		a.mu.Unlock()
		b.mu.Lock()
		b.activity, b.account = a, n
		b.mu.Unlock()
		return true
	}
	return false
}

// TrySynthesize returns a complete segment stream, never a cache entry. The
// caller must drain it fully and reject any error/cancelled owner before caching.
// On retirement a terminal error is guaranteed, even after partial audio.
func (b *BackgroundTTSStream) TrySynthesize(ctx context.Context, text string) (ch <-chan TTSChunk, attempted bool, err error) {
	if strings.TrimSpace(text) == "" {
		return nil, false, errors.New("tts background: empty text")
	}
	if err = ctx.Err(); err != nil {
		return nil, false, err
	}
	b.mu.Lock()
	if b.closed || b.ctx.Err() != nil {
		b.mu.Unlock()
		return nil, false, ErrBackgroundClosed
	}
	if b.busy {
		b.mu.Unlock()
		return nil, false, ErrBackgroundUnavailable
	}
	b.busy = true
	b.mu.Unlock()
	if !b.admit() {
		b.finish()
		return nil, false, ErrBackgroundUnavailable
	}
	opCtx, cancel := context.WithCancel(b.ctx)
	stop := context.AfterFunc(ctx, cancel)
	stopRetire := context.AfterFunc(opCtx, func() { b.retire(opCtx.Err()) })
	cleanup := func() { stopRetire(); stop(); cancel(); b.finish() }
	b.mu.Lock()
	s, n := b.stream, b.account
	b.mu.Unlock()
	if err = opCtx.Err(); err != nil {
		cleanup()
		return nil, false, err
	}
	if s == nil {
		attempted = true
		s, err = b.factory.raw.OpenBackground(opCtx, b.opts, n)
		if err == nil && opCtx.Err() != nil {
			err = opCtx.Err()
		}
		if err != nil {
			if s != nil {
				_ = s.Close()
			}
			b.retire(err)
			cleanup()
			return nil, true, err
		}
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			_ = s.Close()
			cleanup()
			return nil, true, ErrBackgroundClosed
		}
		b.stream, b.identity = s, s.EffectiveIdentity()
		b.mu.Unlock()
	}
	attempted = true
	input, err := s.Synthesize(opCtx, text)
	if err != nil || input == nil {
		if err == nil {
			err = errors.New("tts background: missing audio stream")
		}
		b.retire(err)
		cleanup()
		return nil, true, err
	}
	out := make(chan TTSChunk, 1)
	go func() {
		defer close(out)
		defer cleanup()
		var failed error
		for c := range input {
			if c.Err != nil {
				failed = c.Err
				b.retire(failed)
			}
			if failed != nil || opCtx.Err() != nil {
				continue
			}
			select {
			case out <- c:
			case <-opCtx.Done():
			}
		}
		if failed == nil {
			failed = opCtx.Err()
		}
		if failed != nil {
			b.retire(failed)
			// Make room for the terminal failure if the consumer stopped reading.
			// Partial PCM is unusable after an error and can safely be discarded.
			for {
				select {
				case out <- TTSChunk{Err: failed}:
					return
				case <-out:
				}
			}
		}
	}()
	return out, true, nil
}
