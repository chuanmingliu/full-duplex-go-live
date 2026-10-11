package provider

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTTS opens connections that answer after the delays scripted for them:
// delays[i][j] is how long connection i takes to its first audio on its jth
// synthesis. Each chunk is tagged with the connection that produced it.
type fakeTTS struct {
	delays   [][]time.Duration
	openErr  []error // per connection
	synthErr []error // per connection, on its first synthesis

	mu      sync.Mutex
	streams []*fakeTTSStream
}

func (f *fakeTTS) Name() string { return "fake" }

func (f *fakeTTS) Open(ctx context.Context, _ TTSOptions) (TTSStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.streams)
	if n < len(f.openErr) && f.openErr[n] != nil {
		f.streams = append(f.streams, nil)
		return nil, f.openErr[n]
	}
	s := &fakeTTSStream{tts: f, n: n}
	f.streams = append(f.streams, s)
	return s, nil
}

func (f *fakeTTS) stream(n int) *fakeTTSStream {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n >= len(f.streams) {
		return nil
	}
	return f.streams[n]
}

func (f *fakeTTS) opened() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.streams)
}

type fakeTTSStream struct {
	tts *fakeTTS
	n   int

	lock      sync.Mutex // one synthesis at a time, as a real connection
	active    atomic.Int32
	overlaps  atomic.Int32
	calls     atomic.Int32
	cancelled atomic.Int32
	closed    atomic.Bool
}

func (s *fakeTTSStream) SampleRate() int { return 16000 }
func (s *fakeTTSStream) Close() error    { s.closed.Store(true); return nil }

func (s *fakeTTSStream) Synthesize(ctx context.Context, text string) (<-chan TTSChunk, error) {
	if s.active.Add(1) > 1 {
		s.overlaps.Add(1)
	}
	call := int(s.calls.Add(1)) - 1
	if call == 0 && s.n < len(s.tts.synthErr) && s.tts.synthErr[s.n] != nil {
		s.active.Add(-1)
		return nil, s.tts.synthErr[s.n]
	}
	s.lock.Lock()
	delay := 10 * time.Millisecond
	if s.n < len(s.tts.delays) && call < len(s.tts.delays[s.n]) {
		delay = s.tts.delays[s.n][call]
	}
	out := make(chan TTSChunk)
	go func() {
		defer s.lock.Unlock()
		defer s.active.Add(-1)
		defer close(out)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			s.cancelled.Add(1)
			// A connection takes a moment to put itself back in order.
			time.Sleep(80 * time.Millisecond)
			return
		}
		for i := 0; i < 3; i++ {
			select {
			case out <- TTSChunk{PCM: []byte{byte(s.n), byte(i)}}:
			case <-ctx.Done():
				s.cancelled.Add(1)
				return
			}
		}
	}()
	return out, nil
}

// heard returns which connection produced the audio and how long the first
// chunk took.
func heard(t *testing.T, ch <-chan TTSChunk) (from []byte, first time.Duration, err error) {
	t.Helper()
	start := time.Now()
	first = -1
	for c := range ch {
		if c.Err != nil {
			err = c.Err
			continue
		}
		if first < 0 {
			first = time.Since(start)
		}
		from = append(from, c.PCM[0])
	}
	return from, first, err
}

func openHedged(t *testing.T, f *fakeTTS) TTSStream {
	t.Helper()
	s, err := HedgeTTS(f, 2, nil).Open(context.Background(), TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	deadline := time.Now().Add(time.Second)
	for f.opened() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)
	return s
}

// The point of it: the first segment is heard when the faster connection has
// it, the audio is that connection's alone, and what follows goes to it.
func TestHedgedFirstSegmentIsTheConnectionThatSpokeFirst(t *testing.T) {
	f := &fakeTTS{delays: [][]time.Duration{{400 * time.Millisecond}, {20 * time.Millisecond}}}
	s := openHedged(t, f)

	ch, err := s.Synthesize(WithUrgent(context.Background()), "您点开链接，")
	if err != nil {
		t.Fatal(err)
	}
	from, first, serr := heard(t, ch)
	if serr != nil {
		t.Fatalf("stream error: %v", serr)
	}
	if len(from) != 3 || from[0] != 1 || from[1] != 1 || from[2] != 1 {
		t.Fatalf("audio came from connections %v; want all of it from the faster one, unmixed", from)
	}
	if first > 200*time.Millisecond {
		t.Errorf("first audio after %v; the faster connection had it at 20 ms", first)
	}
	if f.stream(0).cancelled.Load() != 1 {
		t.Error("the connection that lost was not told to stop")
	}

	// The rest of the answer follows on the connection that is free.
	ch, _ = s.Synthesize(context.Background(), "按提示输入车牌号。")
	from, _, _ = heard(t, ch)
	if len(from) == 0 || from[0] != 1 {
		t.Errorf("the next segment went to connection %v; the one that won is the session's now", from)
	}
	for i := 0; i < 2; i++ {
		if n := f.stream(i).overlaps.Load(); n != 0 {
			t.Errorf("connection %d was asked for two syntheses at once", i)
		}
	}
}

func TestOnlyUrgentSegmentsAreHedged(t *testing.T) {
	f := &fakeTTS{}
	s := openHedged(t, f)
	ch, _ := s.Synthesize(context.Background(), "后面的句子。")
	if from, _, _ := heard(t, ch); len(from) != 3 || from[0] != 0 {
		t.Fatalf("got audio from %v", from)
	}
	if n := f.stream(1).calls.Load(); n != 0 {
		t.Errorf("the second connection was used %d time(s) for a segment nobody was waiting on", n)
	}
}

// A connection that lost a race is still putting itself in order when the next
// first segment comes due. That one is not raced; it is not made to wait either.
func TestHedgeLeavesARecoveringConnectionAlone(t *testing.T) {
	f := &fakeTTS{delays: [][]time.Duration{{15 * time.Millisecond, 15 * time.Millisecond}, {300 * time.Millisecond, 15 * time.Millisecond}}}
	s := openHedged(t, f)
	ch, _ := s.Synthesize(WithUrgent(context.Background()), "第一句，")
	if from, _, _ := heard(t, ch); len(from) == 0 || from[0] != 0 {
		t.Fatalf("got audio from %v", from)
	}
	// At once, while the loser is still recovering (80 ms in this fake).
	ch, _ = s.Synthesize(WithUrgent(context.Background()), "第二句，")
	from, first, _ := heard(t, ch)
	if len(from) == 0 || from[0] != 0 || first > 60*time.Millisecond {
		t.Errorf("got audio from %v after %v; want the free connection's, promptly", from, first)
	}
	if n := f.stream(1).overlaps.Load(); n != 0 {
		t.Error("the recovering connection was asked for another synthesis before it had finished")
	}
	// And once it has, it is raced again.
	time.Sleep(150 * time.Millisecond)
	ch, _ = s.Synthesize(WithUrgent(context.Background()), "第三句，")
	heard(t, ch)
	if n := f.stream(1).calls.Load(); n != 2 {
		t.Errorf("the second connection has been used %d time(s); want 2: raced, left alone, raced", n)
	}
}

func TestHedgeSurvivesAConnectionThatFails(t *testing.T) {
	refused := errors.New("rate limited")
	t.Run("no second connection to be had", func(t *testing.T) {
		f := &fakeTTS{openErr: []error{nil, refused}}
		s, err := HedgeTTS(f, 2, nil).Open(context.Background(), TTSOptions{})
		if err != nil {
			t.Fatalf("a session without a spare is a session without hedging, not a failed one: %v", err)
		}
		defer s.Close()
		time.Sleep(30 * time.Millisecond)
		ch, err := s.Synthesize(WithUrgent(context.Background()), "您好，")
		if err != nil {
			t.Fatal(err)
		}
		if from, _, serr := heard(t, ch); serr != nil || len(from) != 3 || from[0] != 0 {
			t.Errorf("got %v, err %v", from, serr)
		}
	})
	t.Run("the spare refuses the synthesis", func(t *testing.T) {
		f := &fakeTTS{synthErr: []error{nil, refused}, delays: [][]time.Duration{{60 * time.Millisecond}}}
		s := openHedged(t, f)
		ch, _ := s.Synthesize(WithUrgent(context.Background()), "您好，")
		if from, _, serr := heard(t, ch); serr != nil || len(from) != 3 || from[0] != 0 {
			t.Errorf("got %v, err %v; want the first connection's audio", from, serr)
		}
		time.Sleep(50 * time.Millisecond)
		if !f.stream(1).closed.Load() {
			t.Error("the connection that failed was kept")
		}
		if f.opened() < 3 {
			t.Error("no connection was dialled to replace the one that failed")
		}
	})
	t.Run("the first refuses and the spare answers", func(t *testing.T) {
		f := &fakeTTS{synthErr: []error{refused, nil}}
		s := openHedged(t, f)
		ch, _ := s.Synthesize(WithUrgent(context.Background()), "您好，")
		if from, _, serr := heard(t, ch); serr != nil || len(from) != 3 || from[0] != 1 {
			t.Errorf("got %v, err %v; want the spare's audio", from, serr)
		}
	})
	t.Run("both refuse", func(t *testing.T) {
		f := &fakeTTS{synthErr: []error{refused, refused}}
		s := openHedged(t, f)
		ch, _ := s.Synthesize(WithUrgent(context.Background()), "您好，")
		if _, _, serr := heard(t, ch); !errors.Is(serr, refused) {
			t.Errorf("both connections refused and the synthesis reported %v", serr)
		}
	})
}

func TestHedgedSynthesisStopsWhenTheCallerDoes(t *testing.T) {
	f := &fakeTTS{delays: [][]time.Duration{{5 * time.Second}, {5 * time.Second}}}
	s := openHedged(t, f)
	ctx, cancel := context.WithCancel(WithUrgent(context.Background()))
	ch, _ := s.Synthesize(ctx, "您好，")
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("audio after the caller cancelled")
		}
	case <-time.After(time.Second):
		t.Fatal("the synthesis did not end when the caller cancelled")
	}
	time.Sleep(150 * time.Millisecond)
	for i := 0; i < 2; i++ {
		if f.stream(i).cancelled.Load() != 1 {
			t.Errorf("connection %d was left synthesizing", i)
		}
	}
}

func TestHedgeTTSOfOneIsTheSynthesizerItself(t *testing.T) {
	f := &fakeTTS{}
	if got := HedgeTTS(f, 1, nil); got != TTS(f) {
		t.Error("a hedge of one copy should be the synthesizer, unwrapped")
	}
}
