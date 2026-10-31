package provider

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type observedAccountFake struct {
	*backgroundFake
	scripts  [][]int
	fail     bool
	mu       sync.Mutex
	opened   int
	attempts []int
}

func (f *observedAccountFake) Open(ctx context.Context, opts TTSOptions) (TTSStream, error) {
	return f.OpenObserved(ctx, opts, nil)
}
func (f *observedAccountFake) OpenObserved(ctx context.Context, opts TTSOptions, observe func(int)) (TTSStream, error) {
	f.mu.Lock()
	n := f.opened
	f.opened++
	script := f.scripts[n]
	for _, account := range script {
		f.attempts[account]++
		if observe != nil {
			observe(account)
		}
	}
	f.mu.Unlock()
	if f.fail {
		return nil, errors.New("all accounts refused")
	}
	return f.fakeTTS.Open(WithLane(ctx, script[len(script)-1]), opts)
}
func observedFixture(t *testing.T, scripts [][]int, limits []int) *observedAccountFake {
	f := &observedAccountFake{backgroundFake: newBackgroundFake(t), scripts: scripts, attempts: make([]int, len(limits))}
	f.fakeTTS.accounts = len(limits)
	f.fakeTTS.rpms = limits
	return f
}
func waitAccountSpare(t *testing.T, stream TTSStream) {
	t.Helper()
	s := stream.(*hedgedTTSStream)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		s.mu.Lock()
		ready := s.spare != nil
		s.mu.Unlock()
		if ready {
			return
		}
		select {
		case <-timer.C:
			t.Fatal("spare not opened")
		case <-time.After(time.Millisecond):
		}
	}
}
func accountCount(h *hedgedTTS, n int) int {
	b := h.account(n)
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.stamps)
}

func TestBackgroundAdmissionCountsSpareFailoverAccount(t *testing.T) {
	f := observedFixture(t, [][]int{{0}, {1, 0}}, []int{3, 1})
	h := HedgeTTSPool(f, 2, 100, 0, TTSPool{}, nil).(*hedgedTTS)
	s, err := h.Open(context.Background(), TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitAccountSpare(t, s)
	if got := accountCount(h, 0); got != 2 {
		t.Errorf("actual account A setups=%d; want2", got)
	}
	if got := accountCount(h, 1); got != 1 {
		t.Errorf("failed account B setups=%d; want1", got)
	}
	factory, err := BackgroundTTSFor(h)
	if err != nil {
		t.Fatal(err)
	}
	b := factory.New(context.Background(), TTSOptions{})
	defer b.Close()
	_, attempted, err := b.TrySynthesize(context.Background(), "prefix")
	if attempted || !errors.Is(err, ErrBackgroundUnavailable) {
		t.Fatal("background overfilled actual failover account")
	}
}

func TestConnectionAccountingIncludesEveryFailedDestination(t *testing.T) {
	f := observedFixture(t, [][]int{{0, 1, 2}, {0, 1, 2}}, []int{2, 2, 2})
	h := HedgeTTSPool(f, 2, 100, 0, TTSPool{}, nil).(*hedgedTTS)
	s, err := h.Open(context.Background(), TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitAccountSpare(t, s)
	for n := 0; n < 3; n++ {
		if got := accountCount(h, n); got != 2 {
			t.Errorf("account%d recorded%d physical2", n, got)
		}
	}
}

func TestConnectionAccountingRetainsCompletelyFailedOpen(t *testing.T) {
	f := observedFixture(t, [][]int{{0, 1, 2}}, []int{3, 3, 3})
	f.fail = true
	h := HedgeTTSPool(f, 2, 100, 0, TTSPool{}, nil).(*hedgedTTS)
	if _, err := h.Open(context.Background(), TTSOptions{}); err == nil {
		t.Fatal("open should fail")
	}
	for n := 0; n < 3; n++ {
		if got := accountCount(h, n); got != 1 {
			t.Errorf("failed account%d recorded%d physical1", n, got)
		}
	}
}

func TestConnectionAccountingKeepsSkippedSpareReservation(t *testing.T) {
	// B was already resting, so its prepaid setup is skipped by the provider;
	// its reservation stays charged and the actual replacement A is also charged.
	f := observedFixture(t, [][]int{{0}, {0}}, []int{3, 1})
	h := HedgeTTSPool(f, 2, 100, 0, TTSPool{}, nil).(*hedgedTTS)
	s, err := h.Open(context.Background(), TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitAccountSpare(t, s)
	if accountCount(h, 0) != 2 || accountCount(h, 1) != 1 {
		t.Fatal("skipped reservation hid replacement setup")
	}
}

type shortConsumerTimeout struct{ TTSStream }

func (shortConsumerTimeout) ConsumerTimeout() time.Duration { return 5 * time.Millisecond }

func TestForegroundProxyRecoversFromConsumerAbandonment(t *testing.T) {
	f := newBackgroundFake(t)
	raw, err := f.Open(context.Background(), TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	a := &accountActivity{}
	s := &foregroundTTSStream{TTSStream: shortConsumerTimeout{raw}, activity: a}
	ch, err := s.Synthesize(context.Background(), "nobody reads this")
	if err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	for {
		a.mu.Lock()
		active := a.foreground
		a.mu.Unlock()
		if active == 0 {
			break
		}
		select {
		case <-timer.C:
			t.Fatal("abandoned consumer held foreground activity forever")
		case <-time.After(time.Millisecond):
		}
	}
	if f.stream(0).cancelled.Load() == 0 {
		t.Fatal("proxy did not cancel abandoned raw synthesis")
	}
	if err := readBackground(t, ch); err == nil {
		t.Fatal("abandoned output looked complete")
	}
}
