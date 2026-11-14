package provider

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type backgroundFake struct {
	fakeTTS
	bgOpens   atomic.Int32
	bgErr     error
	bgStarted chan struct{}
	bgRelease chan struct{}
}

func newBackgroundFake(t *testing.T) *backgroundFake {
	t.Helper()
	return &backgroundFake{fakeTTS: fakeTTS{name: fmt.Sprintf("background-%s-%d", t.Name(), time.Now().UnixNano()), accounts: 2, rpms: []int{100, 100}}}
}
func (f *backgroundFake) AccountKey(n int) string { return fmt.Sprintf("%s/%d", f.Name(), n) }
func (f *backgroundFake) OpenObserved(ctx context.Context, opts TTSOptions, observe func(int)) (TTSStream, error) {
	n, _ := Lane(ctx)
	observe(n % f.Accounts())
	return f.Open(ctx, opts)
}
func (f *backgroundFake) OpenBackground(ctx context.Context, opts TTSOptions, n int) (IdentifiedTTSStream, error) {
	f.bgOpens.Add(1)
	if f.bgStarted != nil {
		close(f.bgStarted)
		<-f.bgRelease
	}
	if f.bgErr != nil {
		return nil, f.bgErr
	}
	s, err := f.Open(WithLane(ctx, n), opts)
	if err != nil {
		return nil, err
	}
	return &identifiedFake{TTSStream: s, identity: AudioIdentity{Provider: f.Name(), Account: f.AccountKey(n), Voice: "voice", Rate: 16000}}, nil
}

func TestBackgroundCloseWhileOpeningClosesLateResult(t *testing.T) {
	f := newBackgroundFake(t)
	f.bgStarted = make(chan struct{})
	f.bgRelease = make(chan struct{})
	_, factory := backgroundFactory(t, f)
	b := factory.New(context.Background(), TTSOptions{})
	done := make(chan error, 1)
	go func() {
		_, attempted, err := b.TrySynthesize(context.Background(), "prefix")
		if !attempted {
			done <- errors.New("opening was not charged")
			return
		}
		done <- err
	}()
	<-f.bgStarted
	b.Close()
	close(f.bgRelease)
	if err := <-done; err == nil {
		t.Fatal("closed opening succeeded")
	}
	if !f.stream(0).closed.Load() {
		t.Fatal("late opening leaked")
	}
}

func TestBackgroundSkipsForegroundActivityAndSharedReservations(t *testing.T) {
	f := newBackgroundFake(t)
	h, factory := backgroundFactory(t, f)
	end0 := h.(*hedgedTTS).activity(0).beginForeground()
	end1 := h.(*hedgedTTS).activity(1).beginForeground()
	b := factory.New(context.Background(), TTSOptions{})
	defer b.Close()
	_, attempted, err := b.TrySynthesize(context.Background(), "blocked")
	if attempted || !errors.Is(err, ErrBackgroundUnavailable) || f.opened() != 0 {
		t.Fatal("foreground work did not exclude background")
	}
	end0()
	end1()
	ch, _, err := b.TrySynthesize(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	if err = readBackground(t, ch); err != nil {
		t.Fatal(err)
	}
	_, otherFactory := backgroundFactory(t, f)
	second := otherFactory.New(context.Background(), TTSOptions{})
	defer second.Close()
	ch, _, err = second.TrySynthesize(context.Background(), "second")
	if err != nil {
		t.Fatal(err)
	}
	if err = readBackground(t, ch); err != nil {
		t.Fatal(err)
	}
	id0, _ := b.EffectiveIdentity()
	id1, _ := second.EffectiveIdentity()
	if id0.Account == id1.Account {
		t.Fatal("background connections shared one account")
	}
	third := otherFactory.New(context.Background(), TTSOptions{})
	defer third.Close()
	_, attempted, err = third.TrySynthesize(context.Background(), "third")
	if attempted || !errors.Is(err, ErrBackgroundUnavailable) || f.bgOpens.Load() != 2 {
		t.Fatal("background reservation exceeded account bound")
	}
}

func TestBackgroundNeedsKnownFiniteAccountLimit(t *testing.T) {
	f := newBackgroundFake(t)
	f.rpms = nil
	h := HedgeTTSPool(f, 2, 100, 0, TTSPool{}, nil)
	factory, err := BackgroundTTSFor(h)
	if err != nil {
		t.Fatal(err)
	}
	b := factory.New(context.Background(), TTSOptions{})
	defer b.Close()
	_, attempted, err := b.TrySynthesize(context.Background(), "prefix")
	if attempted || !errors.Is(err, ErrBackgroundUnavailable) || f.opened() != 0 {
		t.Fatal("unknown allowance admitted background")
	}
}

type identifiedFake struct {
	TTSStream
	identity AudioIdentity
}

func (s *identifiedFake) EffectiveIdentity() AudioIdentity { return s.identity }

func backgroundFactory(t *testing.T, f *backgroundFake) (TTS, *BackgroundTTSFactory) {
	t.Helper()
	h := HedgeTTSPool(f, 2, 100, time.Second, TTSPool{AccountRPM: 100}, nil)
	b, err := BackgroundTTSFor(h)
	if err != nil {
		t.Fatal(err)
	}
	return h, b
}
func readBackground(t *testing.T, ch <-chan TTSChunk) error {
	t.Helper()
	var err error
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return err
			}
			if c.Err != nil {
				err = c.Err
			}
		case <-timer.C:
			t.Fatal("background did not finish")
			return nil
		}
	}
}

func TestBackgroundIsLazySingleRequestAndReusesConnection(t *testing.T) {
	f := newBackgroundFake(t)
	_, factory := backgroundFactory(t, f)
	b := factory.New(context.Background(), TTSOptions{})
	defer b.Close()
	if f.opened() != 0 {
		t.Fatal("constructor opened a connection")
	}
	for i := 0; i < 4; i++ {
		ch, attempted, err := b.TrySynthesize(context.Background(), "prefix")
		if err != nil || !attempted {
			t.Fatalf("attempt %d: %t %v", i, attempted, err)
		}
		if err = readBackground(t, ch); err != nil {
			t.Fatal(err)
		}
	}
	if f.bgOpens.Load() != 1 || f.stream(0).calls.Load() != 4 {
		t.Fatal("background amplified provider work")
	}
	if id, ok := b.EffectiveIdentity(); !ok || id.Voice != "voice" {
		t.Fatal("missing effective identity")
	}
}

func TestBackgroundSharesAccountBudgetAndCountsFailedOpen(t *testing.T) {
	f := newBackgroundFake(t)
	h, factory := backgroundFactory(t, f)
	f.rpms = []int{2, 2}
	f.bgErr = errors.New("refused")
	b := factory.New(context.Background(), TTSOptions{})
	defer b.Close()
	_, attempted, err := b.TrySynthesize(context.Background(), "prefix")
	if !attempted || err == nil || f.bgOpens.Load() != 1 {
		t.Fatal("failed opening not counted")
	}
	if !h.(*hedgedTTS).account(0).full() {
		t.Fatal("background did not spend shared ledger")
	}
	_, attempted, err = b.TrySynthesize(context.Background(), "retry")
	if attempted || err == nil || f.bgOpens.Load() != 1 {
		t.Fatal("failed background handle reopened")
	}
}

func TestBackgroundDeniesBusyOrExhaustedWithoutProviderWork(t *testing.T) {
	f := newBackgroundFake(t)
	h, factory := backgroundFactory(t, f)
	for n := 0; n < 2; n++ {
		h.(*hedgedTTS).account(n).fill()
	}
	b := factory.New(context.Background(), TTSOptions{})
	defer b.Close()
	_, attempted, err := b.TrySynthesize(context.Background(), "prefix")
	if attempted || !errors.Is(err, ErrBackgroundUnavailable) || f.opened() != 0 {
		t.Fatal("exhausted budget sent work")
	}
}

func TestBackgroundForegroundRetiresOnlyActiveWork(t *testing.T) {
	f := newBackgroundFake(t)
	h, factory := backgroundFactory(t, f)
	b := factory.New(context.Background(), TTSOptions{})
	defer b.Close()
	ch, _, err := b.TrySynthesize(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	if err = readBackground(t, ch); err != nil {
		t.Fatal(err)
	}
	fg, err := h.Open(context.Background(), TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer fg.Close()
	// An idle background connection survives foreground opening and synthesis.
	fc, err := fg.Synthesize(context.Background(), "foreground")
	if err != nil {
		t.Fatal(err)
	}
	drainTTS(fc)
	ch, attempted, err := b.TrySynthesize(context.Background(), "second")
	if err != nil || !attempted {
		t.Fatalf("idle handle was retired: %v", err)
	}
	if err = readBackground(t, ch); err != nil {
		t.Fatal(err)
	}
	// Keep the next background request active until foreground preempts it.
	f.stream(0).tts.synthStart = nil
	f.delays = [][]time.Duration{{time.Millisecond, time.Millisecond, time.Hour}}
	ch, _, err = b.TrySynthesize(context.Background(), "third")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	fc, err = fg.Synthesize(context.Background(), "preempt")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 50*time.Millisecond {
		t.Fatal("foreground waited for background drain")
	}
	drainTTS(fc)
	if err = readBackground(t, ch); err == nil {
		t.Fatal("cancelled audio looked complete")
	}
	if _, attempted, err = b.TrySynthesize(context.Background(), "fourth"); attempted || err == nil {
		t.Fatal("retired handle reopened")
	}
}

func TestBackgroundCloseCancelsUnconsumedOutput(t *testing.T) {
	f := newBackgroundFake(t)
	_, factory := backgroundFactory(t, f)
	b := factory.New(context.Background(), TTSOptions{})
	ch, _, err := b.TrySynthesize(context.Background(), "prefix")
	if err != nil {
		t.Fatal(err)
	}
	if _, attempted, err := b.TrySynthesize(context.Background(), "overlap"); attempted || !errors.Is(err, ErrBackgroundUnavailable) {
		t.Fatal("concurrent synthesis admitted")
	}
	b.Close()
	if err = readBackground(t, ch); err == nil {
		t.Fatal("closed audio looked complete")
	}
}
