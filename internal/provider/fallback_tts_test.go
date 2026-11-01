package provider

import (
	"context"
	"errors"
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

func openFallback(t *testing.T, primary, second *fakeTTS, wait time.Duration) TTSStream {
	t.Helper()
	s, err := FallbackTTS(primary, second, wait, nil).Open(context.Background(), TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	deadline := time.Now().Add(time.Second)
	for second.stream(10) == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return s
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
	s := openFallback(t, primary, second, 100*time.Millisecond)
	ch, err := s.Synthesize(WithUrgent(context.Background()), "好的")
	if err != nil {
		t.Fatalf("the failure should have gone to the second vendor, not the caller: %v", err)
	}
	from, _, err := heard(t, ch)
	if err != nil || len(from) != 3 || from[0] != 10 {
		t.Fatalf("heard %v, %v", from, err)
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
