package provider

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// A second-vendor fake whose connections number from 10, so its audio reads
// apart from the primary's (PCM[0] is the connection number).
func secondVendor(delays ...time.Duration) *fakeTTS {
	f := &fakeTTS{name: "second"}
	f.streams = make([]*fakeTTSStream, 10) // connections 0–9 are "taken"
	f.delays = make([][]time.Duration, 11)
	f.delays[10] = delays
	return f
}

func openFallback(t *testing.T, primary TTS, second *fakeTTS, wait time.Duration) TTSStream {
	t.Helper()
	s, err := FallbackTTS(primary, second, wait, nil).Open(context.Background(), TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		f := s.(*fallbackStream)
		f.mu.Lock()
		ready := f.second != nil
		f.mu.Unlock()
		if ready {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("fallback connection did not become ready")
	return s
}

// Startup models a reconnect or a lock that cannot return until released.
// Its eventual audio must be drained even when cancellation won meanwhile.
type blockedStartupTTS struct {
	TTS
	started chan context.Context
	unblock chan struct{}
	drained chan struct{}
}

type blockedStartupStream struct {
	TTSStream
	tts *blockedStartupTTS
}

func blockedStartup(t *testing.T) (*blockedStartupTTS, func()) {
	t.Helper()
	f := &blockedStartupTTS{TTS: &fakeTTS{name: "primary"}, started: make(chan context.Context, 1), unblock: make(chan struct{}), drained: make(chan struct{})}
	unblock := sync.OnceFunc(func() { close(f.unblock) })
	t.Cleanup(unblock)
	return f, unblock
}

func (f *blockedStartupTTS) Open(ctx context.Context, opts TTSOptions) (TTSStream, error) {
	s, err := f.TTS.Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &blockedStartupStream{TTSStream: s, tts: f}, nil
}

func (s *blockedStartupStream) Synthesize(ctx context.Context, _ string) (<-chan TTSChunk, error) {
	s.tts.started <- ctx
	<-s.tts.unblock
	ch := make(chan TTSChunk)
	go func() {
		defer close(ch)
		defer close(s.tts.drained)
		ch <- TTSChunk{PCM: []byte{0, 0}}
	}()
	return ch, nil
}

// An ordinary segment: the primary answers inside the wait, the second vendor
// is never asked, the audio is the primary's.
func TestFallbackIsNotAskedWhenThePrimaryIsPrompt(t *testing.T) {
	primary := &fakeTTS{name: "primary", delays: [][]time.Duration{{30 * time.Millisecond}}}
	second := secondVendor(10 * time.Millisecond)
	s := openFallback(t, primary, second, 200*time.Millisecond)
	ch, err := s.Synthesize(WithUrgent(context.Background()), "好的")
	if err != nil {
		t.Fatal(err)
	}
	from, _, err := heard(t, ch)
	if err != nil || len(from) != 3 || from[0] != 0 {
		t.Fatalf("heard %v, %v", from, err)
	}
	if n := second.stream(10).calls.Load(); n != 0 {
		t.Fatalf("the second vendor was asked %d times", n)
	}
}

// A stall: the primary has nothing for far longer than the wait; the second
// vendor is asked at the wait and its audio is what is heard, at about the
// wait plus its own time; the primary's segment is cancelled.
func TestFallbackSpeaksWhenThePrimaryStalls(t *testing.T) {
	primary := &fakeTTS{name: "primary", delays: [][]time.Duration{{2 * time.Second}}}
	second := secondVendor(40 * time.Millisecond)
	s := openFallback(t, primary, second, 100*time.Millisecond)
	ch, err := s.Synthesize(WithUrgent(context.Background()), "明天下午两点到四点是空的。")
	if err != nil {
		t.Fatal(err)
	}
	from, first, err := heard(t, ch)
	if err != nil || len(from) != 3 || from[0] != 10 {
		t.Fatalf("heard %v, %v", from, err)
	}
	if first < 120*time.Millisecond || first > 400*time.Millisecond {
		t.Fatalf("first audio after %v; wanted the wait plus the second vendor's ~40 ms", first)
	}
	time.Sleep(50 * time.Millisecond)
	if primary.stream(0).cancelled.Load() == 0 {
		t.Fatal("the primary's stalled segment was not cancelled")
	}
}

// A reconnect or a previous segment's cleanup can block Synthesize itself.
// That time belongs to the fallback deadline, just like waiting for audio.
func TestFallbackDeadlineIncludesPrimaryStartup(t *testing.T) {
	primary, unblock := blockedStartup(t)
	second := secondVendor(10 * time.Millisecond)
	s := openFallback(t, primary, second, 40*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result := make(chan TTSChunk, 1)
	started := time.Now()
	go func() {
		ch, err := s.Synthesize(WithUrgent(ctx), "好的")
		if err != nil {
			result <- TTSChunk{Err: err}
			return
		}
		result <- <-ch
		drainTTS(ch)
	}()
	select {
	case c := <-result:
		if c.Err != nil || len(c.PCM) == 0 || c.PCM[0] != 10 {
			t.Fatalf("wanted fallback audio while primary startup is blocked, got %v", c)
		}
		if time.Since(started) < 40*time.Millisecond {
			t.Fatal("fallback was asked before its deadline")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("fallback deadline waited for the primary's Synthesize to return")
	}
	select {
	case pctx := <-primary.started:
		if pctx.Err() == nil {
			t.Fatal("the losing primary startup was not cancelled")
		}
	case <-time.After(time.Second):
		t.Fatal("primary synthesis never started")
	}
	unblock()
	select {
	case <-primary.drained:
	case <-time.After(time.Second):
		t.Fatal("the losing primary's late audio was not drained")
	}
}

func TestFallbackCancellationWhilePrimaryIsStarting(t *testing.T) {
	primary, unblock := blockedStartup(t)
	s := openFallback(t, primary, secondVendor(), time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan ttsStarted, 1)
	go func() {
		ch, err := s.Synthesize(WithUrgent(ctx), "好的")
		returned <- ttsStarted{ch: ch, err: err}
	}()
	var result ttsStarted
	select {
	case result = <-returned:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Synthesize blocked on primary startup")
	}
	cancel()
	select {
	case _, ok := <-result.ch:
		if ok {
			t.Fatal("audio after cancellation")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancellation waited for primary startup")
	}
	unblock()
	select {
	case <-primary.drained:
	case <-time.After(time.Second):
		t.Fatal("cancelled primary's late audio was not drained")
	}
}

func TestFallbackFailureDoesNotDiscardAStartingPrimary(t *testing.T) {
	primary, unblock := blockedStartup(t)
	second := secondVendor()
	second.synthErr = make([]error, 11)
	second.synthErr[10] = errors.New("fallback refused")
	s := openFallback(t, primary, second, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := s.Synthesize(WithUrgent(ctx), "好的")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	failed := false
	for time.Now().Before(deadline) {
		f := s.(*fallbackStream)
		f.mu.Lock()
		failed = f.asked == 1 && !f.busy
		f.mu.Unlock()
		if failed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !failed {
		t.Fatal("the fallback did not finish failing during primary startup")
	}
	unblock()
	select {
	case c, ok := <-ch:
		if !ok || c.Err != nil || len(c.PCM) == 0 || c.PCM[0] != 0 {
			t.Fatalf("starting primary was lost after fallback failure: %+v, open=%v", c, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("primary audio was not delivered")
	}
	drainTTS(ch)
}

// Both asked, the primary answers first after all: its audio, the second
// vendor's dropped, and the second vendor is free again for the next stall.
func TestFallbackLosesToALatePrimaryAndIsFreeAgain(t *testing.T) {
	primary := &fakeTTS{name: "primary", delays: [][]time.Duration{{150 * time.Millisecond, 2 * time.Second}}}
	second := secondVendor(300*time.Millisecond, 20*time.Millisecond)
	s := openFallback(t, primary, second, 100*time.Millisecond)
	ch, _ := s.Synthesize(WithUrgent(context.Background()), "一")
	from, _, _ := heard(t, ch)
	if len(from) != 3 || from[0] != 0 {
		t.Fatalf("first segment heard from %v", from)
	}
	if second.stream(10).calls.Load() != 1 {
		t.Fatal("the second vendor was not asked for the slow first segment")
	}
	// the second vendor's losing synthesis is cancelled and drained; it is
	// free again by the time the next stall comes
	time.Sleep(150 * time.Millisecond)
	ch, _ = s.Synthesize(WithUrgent(context.Background()), "二")
	from, _, _ = heard(t, ch)
	if len(from) != 3 || from[0] != 10 {
		t.Fatalf("second segment heard from %v; wanted the second vendor", from)
	}
}

// A later segment gets three times the wait before the second vendor is asked.
func TestFallbackIsPatientWithALaterSegment(t *testing.T) {
	primary := &fakeTTS{name: "primary", delays: [][]time.Duration{{200 * time.Millisecond}}}
	second := secondVendor(10 * time.Millisecond)
	s := openFallback(t, primary, second, 100*time.Millisecond)
	ch, _ := s.Synthesize(context.Background(), "后半句")
	from, _, _ := heard(t, ch)
	if len(from) != 3 || from[0] != 0 {
		t.Fatalf("heard %v", from)
	}
	if n := second.stream(10).calls.Load(); n != 0 {
		t.Fatalf("the second vendor was asked %d times for a later segment that came at 200 ms", n)
	}
}

// The primary fails the segment outright: the second vendor is the request.
func TestFallbackTakesASegmentThePrimaryFails(t *testing.T) {
	primary := &fakeTTS{name: "primary", synthErr: []error{errors.New("refused")}}
	second := secondVendor(10 * time.Millisecond)
	s := openFallback(t, primary, second, time.Second)
	ch, err := s.Synthesize(WithUrgent(context.Background()), "好的")
	if err != nil {
		t.Fatalf("the failure should have gone to the second vendor, not the caller: %v", err)
	}
	from, first, err := heard(t, ch)
	if err != nil || len(from) != 3 || from[0] != 10 {
		t.Fatalf("heard %v, %v", from, err)
	}
	if first > 500*time.Millisecond {
		t.Fatalf("primary startup failure waited for the fallback deadline: %v", first)
	}
}

func TestFallbackReportsFailureWhenBothStartupsFail(t *testing.T) {
	want := errors.New("primary refused")
	primary := &fakeTTS{name: "primary", synthErr: []error{want}}
	second := secondVendor()
	second.synthErr = make([]error, 11)
	second.synthErr[10] = errors.New("fallback refused")
	s := openFallback(t, primary, second, time.Second)
	ch, err := s.Synthesize(WithUrgent(context.Background()), "好的")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = heard(t, ch)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want primary startup failure", err)
	}
	if calls := second.stream(10).calls.Load(); calls != 1 {
		t.Fatalf("fallback asked %d times for the failed segment, want 1", calls)
	}
}

// With no second vendor reachable, the session is the primary's alone.
func TestFallbackWithoutASecondVendorIsThePrimary(t *testing.T) {
	primary := &fakeTTS{name: "primary", delays: [][]time.Duration{{10 * time.Millisecond}}}
	second := &fakeTTS{name: "second", openErr: []error{errors.New("no key")}}
	s, err := FallbackTTS(primary, second, 100*time.Millisecond, nil).Open(context.Background(), TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	time.Sleep(20 * time.Millisecond)
	ch, err := s.Synthesize(WithUrgent(context.Background()), "好的")
	if err != nil {
		t.Fatal(err)
	}
	from, _, _ := heard(t, ch)
	if len(from) != 3 || from[0] != 0 {
		t.Fatalf("heard %v", from)
	}
}

// The phrase cache keys on the voice heard, so a fallback's name is its
// primary's for that purpose.
func TestVoiceNameIsThePrimarys(t *testing.T) {
	primary := &fakeTTS{name: "minimax"}
	f := FallbackTTS(primary, &fakeTTS{name: "fish"}, 320*time.Millisecond, nil)
	if f.Name() != "minimax+fish" || VoiceName(f) != "minimax" {
		t.Fatalf("Name %q, VoiceName %q", f.Name(), VoiceName(f))
	}
	if VoiceName(primary) != "minimax" {
		t.Fatal("a bare provider is its own voice")
	}
}
