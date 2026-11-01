package provider

import (
	"context"
	"errors"
	"strconv"
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
	name     string
	synthErr []error // per connection, on its first synthesis
	// accounts, above one, makes this a pool: a connection is opened on the
	// account its context's lane names, the first when it names none.
	accounts int
	// rpms is what each account allows, when the provider knows.
	rpms []int

	mu      sync.Mutex
	streams []*fakeTTSStream
}

func (f *fakeTTS) Name() string {
	if f.name != "" {
		return f.name
	}
	return "fake"
}

func (f *fakeTTS) Open(ctx context.Context, _ TTSOptions) (TTSStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.streams)
	if n < len(f.openErr) && f.openErr[n] != nil {
		f.streams = append(f.streams, nil)
		return nil, f.openErr[n]
	}
	s := &fakeTTSStream{tts: f, n: n}
	if lane, ok := Lane(ctx); ok && f.accounts > 1 {
		s.account = lane % f.accounts
	}
	f.streams = append(f.streams, s)
	return s, nil
}

func (f *fakeTTS) AccountRPM(n int) int {
	if n < len(f.rpms) {
		return f.rpms[n]
	}
	return 0
}

func (f *fakeTTS) Accounts() int {
	if f.accounts < 1 {
		return 1
	}
	return f.accounts
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
	tts     *fakeTTS
	n       int
	account int

	lock      sync.Mutex // one synthesis at a time, as a real connection
	active    atomic.Int32
	overlaps  atomic.Int32
	calls     atomic.Int32
	cancelled atomic.Int32
	closed    atomic.Bool
}

func (s *fakeTTSStream) SampleRate() int { return 16000 }
func (s *fakeTTSStream) Account() int    { return s.account }
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
	s, err := HedgeTTS(f, 2, 0, 0, nil).Open(context.Background(), TTSOptions{})
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
		s, err := HedgeTTS(f, 2, 0, 0, nil).Open(context.Background(), TTSOptions{})
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
	if got := HedgeTTS(f, 1, 0, 0, nil); got != TTS(f) {
		t.Error("a hedge of one copy should be the synthesizer, unwrapped")
	}
}

// Past a vendor's rate limit it refuses the request that was going to be made
// anyway. A first segment is raced only while the minute has room for the
// extra request; when it has not, the segment is synthesized once, as before.
func TestHedgeStaysInsideItsRequestBudget(t *testing.T) {
	// The budget is the account's, so it is shared by name across the process.
	f := &fakeTTS{name: "budgeted-" + strconv.FormatInt(time.Now().UnixNano(), 36)}
	// Room for what hedging adds: the spare connection, and one second request.
	s, err := HedgeTTS(f, 2, 2, 0, nil).Open(context.Background(), TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	deadline := time.Now().Add(time.Second)
	for f.opened() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)

	ch, _ := s.Synthesize(WithUrgent(context.Background()), "第一句，")
	heard(t, ch)
	time.Sleep(120 * time.Millisecond) // the loser is free again
	raced := f.stream(0).calls.Load() + f.stream(1).calls.Load()
	if raced != 2 {
		t.Fatalf("%d syntheses for the first raced segment; want 2", raced)
	}
	for i := 0; i < 3; i++ {
		ch, err := s.Synthesize(WithUrgent(context.Background()), "下一句，")
		if err != nil {
			t.Fatal(err)
		}
		if from, _, serr := heard(t, ch); serr != nil || len(from) != 3 {
			t.Fatalf("a segment over budget was not synthesized at all: %v, %v", from, serr)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := f.stream(0).calls.Load() + f.stream(1).calls.Load(); got != raced+3 {
		t.Errorf("%d syntheses for three segments with the budget spent; want one each", got-raced)
	}
}

func openHedgedAfter(t *testing.T, f *fakeTTS, rpm int, after time.Duration) TTSStream {
	t.Helper()
	s, err := HedgeTTS(f, 2, rpm, after, nil).Open(context.Background(), TTSOptions{})
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

// The frugal form, for an account with no room for two requests a turn: a
// synthesis that answers in its usual time is the only request made.
func TestDelayedHedgeMakesOneRequestWhenTheFirstIsPrompt(t *testing.T) {
	f := &fakeTTS{}
	s := openHedgedAfter(t, f, 0, 60*time.Millisecond)

	for i := 0; i < 3; i++ {
		ch, err := s.Synthesize(WithUrgent(context.Background()), "您点开链接，")
		if err != nil {
			t.Fatal(err)
		}
		from, _, serr := heard(t, ch)
		if serr != nil || len(from) != 3 || from[0] != 0 {
			t.Fatalf("segment %d: audio %v, err %v; want three chunks from the first connection", i, from, serr)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if n := f.stream(1).calls.Load(); n != 0 {
		t.Errorf("the second connection was asked %d times for syntheses that answered at once; want 0", n)
	}
}

// And one that does not answer is not waited out: the second connection is
// asked once the first has had its time, and the caller hears whichever
// speaks. This is the 1284 and 1763 ms first audio of the 22:21 call.
func TestDelayedHedgeAsksTheSecondConnectionWhenTheFirstIsSlow(t *testing.T) {
	f := &fakeTTS{delays: [][]time.Duration{{600 * time.Millisecond}, {20 * time.Millisecond}}}
	s := openHedgedAfter(t, f, 0, 60*time.Millisecond)

	ch, err := s.Synthesize(WithUrgent(context.Background()), "加上之后点链接，")
	if err != nil {
		t.Fatal(err)
	}
	from, first, serr := heard(t, ch)
	if serr != nil || len(from) != 3 || from[0] != 1 {
		t.Fatalf("audio %v, err %v; want three chunks from the second connection", from, serr)
	}
	if first < 60*time.Millisecond || first > 250*time.Millisecond {
		t.Errorf("first audio after %s; want the wait (60 ms) plus the second connection's 20, not the first connection's 600", first)
	}
	time.Sleep(150 * time.Millisecond)
	if f.stream(0).cancelled.Load() != 1 {
		t.Error("the connection that lost was not cancelled")
	}
	// The connection that spoke is the session's now.
	ch, _ = s.Synthesize(context.Background(), "就能看到报价了。")
	if from, _, _ := heard(t, ch); len(from) == 0 || from[0] != 1 {
		t.Errorf("the next segment came from %v; want the connection that answered", from)
	}
	if f.stream(0).overlaps.Load()+f.stream(1).overlaps.Load() != 0 {
		t.Error("a connection was given two syntheses at once")
	}
}

// A later segment plays behind audio already going out, so it is given three
// times as long — but not for ever: on that call one never arrived in 3.3 s.
func TestDelayedHedgeCoversLaterSegmentsWithMorePatience(t *testing.T) {
	f := &fakeTTS{delays: [][]time.Duration{
		{100 * time.Millisecond, 700 * time.Millisecond},
		{20 * time.Millisecond},
	}}
	s := openHedgedAfter(t, f, 0, 50*time.Millisecond)

	// 100 ms is past an urgent segment's 50 and inside a later one's 150.
	ch, _ := s.Synthesize(context.Background(), "按提示输入车牌号，")
	if from, _, serr := heard(t, ch); serr != nil || len(from) != 3 || from[0] != 0 {
		t.Fatalf("audio %v, err %v; want the first connection's", from, serr)
	}
	if n := f.stream(1).calls.Load(); n != 0 {
		t.Fatalf("the second connection was asked for a later segment that took 100 ms; it has 150")
	}
	ch, _ = s.Synthesize(context.Background(), "就能实时看到报价了。")
	from, first, serr := heard(t, ch)
	if serr != nil || len(from) != 3 || from[0] != 1 {
		t.Fatalf("audio %v, err %v; want the second connection's", from, serr)
	}
	if first < 150*time.Millisecond || first > 400*time.Millisecond {
		t.Errorf("first audio after %s; want about 170 ms", first)
	}
}

// The second request is the one thing that can run the account into its rate
// limit, so it is not made when the minute has no room for it.
func TestDelayedHedgeStaysInsideItsRequestBudget(t *testing.T) {
	f := &fakeTTS{
		name:   "delayed-budget-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		delays: [][]time.Duration{{10 * time.Millisecond, 10 * time.Millisecond, 200 * time.Millisecond, 200 * time.Millisecond}},
	}
	// Room for what hedging adds: the spare connection, and one second request.
	// The session's own connection and its own segments are not counted: a
	// real call made 22 requests a minute by itself, and a budget that counted
	// them never had room for the second request that two slow turns needed.
	s := openHedgedAfter(t, f, 2, 50*time.Millisecond)
	for i := 0; i < 2; i++ {
		ch, _ := s.Synthesize(WithUrgent(context.Background()), "好的，")
		heard(t, ch)
	}
	// Slow, and there is room: the second connection is asked, and wins.
	ch, _ := s.Synthesize(WithUrgent(context.Background()), "您稍等，")
	if from, _, serr := heard(t, ch); serr != nil || len(from) != 3 || from[0] != 1 {
		t.Fatalf("audio %v, err %v; want the second connection's", from, serr)
	}
	time.Sleep(150 * time.Millisecond)
	// Slow again, and the minute is spent. The connections changed places, so
	// the slow one is the fake's second; script it.
	f.delays = [][]time.Duration{nil, {0, 200 * time.Millisecond}}
	asked := f.stream(0).calls.Load()
	ch, _ = s.Synthesize(WithUrgent(context.Background()), "我看一下，")
	from, first, serr := heard(t, ch)
	if serr != nil || len(from) != 3 || from[0] != 1 {
		t.Fatalf("audio %v, err %v; want it synthesized, once, by the session's connection", from, serr)
	}
	if first < 180*time.Millisecond {
		t.Errorf("first audio after %s from a connection that takes 200 ms", first)
	}
	if n := f.stream(0).calls.Load(); n != asked {
		t.Error("a second request was made with no room in the minute for it")
	}
}

// A connection that fails outright is not waited on at all.
func TestDelayedHedgeGoesToTheSecondConnectionWhenTheFirstFails(t *testing.T) {
	f := &fakeTTS{synthErr: []error{errors.New("connection reset")}}
	s := openHedgedAfter(t, f, 0, 300*time.Millisecond)

	ch, err := s.Synthesize(WithUrgent(context.Background()), "您点开链接，")
	if err != nil {
		t.Fatal(err)
	}
	from, first, serr := heard(t, ch)
	if serr != nil || len(from) != 3 || from[0] != 1 {
		t.Fatalf("audio %v, err %v; want the second connection's, and no error", from, serr)
	}
	if first > 200*time.Millisecond {
		t.Errorf("first audio after %s; the second connection should have been asked at once, not after the 300 ms wait", first)
	}
}

// The bound is on what hedging adds. A call's own syntheses — 22 a minute on
// the call this is taken from — must not use it up, or the second request is
// never there for the slow one: four of four went unasked, and two of those
// turns were the call's only ones over target.
func TestTheSessionsOwnRequestsDoNotUseUpTheHedgingBudget(t *testing.T) {
	delays := make([]time.Duration, 0, 26)
	for i := 0; i < 25; i++ {
		delays = append(delays, 5*time.Millisecond)
	}
	delays = append(delays, 600*time.Millisecond)
	f := &fakeTTS{
		name:   "own-requests-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		delays: [][]time.Duration{delays, {20 * time.Millisecond}},
	}
	// Room for the spare connection and two second requests in a minute.
	s := openHedgedAfter(t, f, 3, 60*time.Millisecond)
	for i := 0; i < 25; i++ {
		ch, err := s.Synthesize(WithUrgent(context.Background()), "好的，")
		if err != nil {
			t.Fatal(err)
		}
		if from, _, serr := heard(t, ch); serr != nil || len(from) != 3 || from[0] != 0 {
			t.Fatalf("segment %d: audio %v, err %v", i, from, serr)
		}
	}
	ch, _ := s.Synthesize(WithUrgent(context.Background()), "您稍等，")
	from, first, serr := heard(t, ch)
	if serr != nil || len(from) != 3 || from[0] != 1 {
		t.Fatalf("after 25 prompt syntheses the slow one was left to the first connection: audio %v, err %v", from, serr)
	}
	if first > 300*time.Millisecond {
		t.Errorf("first audio after %s; the second connection should have had it in about 80 ms", first)
	}
}

// --- two accounts ---

var uniqN atomic.Int64

// uniq makes a provider name no other test, and no earlier run of this one,
// has used: an account's count of requests is kept per provider for the life
// of the process.
func uniq(name string) string {
	return name + "-" + strconv.FormatInt(uniqN.Add(1), 10) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func openPool(t *testing.T, f *fakeTTS, after time.Duration, pool TTSPool) TTSStream {
	t.Helper()
	s, err := HedgeTTSPool(f, 2, 1, after, pool, nil).Open(context.Background(), TTSOptions{})
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

// A second account is a second place for a request to go: the session's own
// connection is on the first account, as it has always been, and the one
// beside it on the next.
func TestPoolPutsTheSecondConnectionOnAnotherAccount(t *testing.T) {
	f := &fakeTTS{name: uniq("pool-accounts"), accounts: 2}
	openPool(t, f, 0, TTSPool{})
	if f.opened() != 2 {
		t.Fatalf("%d connections opened; want 2", f.opened())
	}
	if a, b := f.stream(0).account, f.stream(1).account; a != 0 || b != 1 {
		t.Errorf("connections on accounts %d and %d; want the first and the second", a+1, b+1)
	}
}

// On one account the second request is held back until the first has been
// slow for a while, because the account cannot afford two a turn (the one a
// minute this test allows it would be spent opening the connection). The
// second account has an allowance of its own, so there the first segment is
// asked of both at once and the caller hears the faster.
func TestPoolAsksBothAccountsAtOnceForTheFirstSegment(t *testing.T) {
	f := &fakeTTS{name: uniq("pool-race"), accounts: 2,
		delays: [][]time.Duration{{300 * time.Millisecond}, {20 * time.Millisecond}}}
	s := openPool(t, f, 200*time.Millisecond, TTSPool{AccountRPM: 10})

	ch, err := s.Synthesize(WithUrgent(context.Background()), "好的，")
	if err != nil {
		t.Fatal(err)
	}
	from, first, serr := heard(t, ch)
	if serr != nil || len(from) != 3 || from[0] != 1 {
		t.Fatalf("audio %v, err %v; want three chunks from the second account", from, serr)
	}
	if first > 150*time.Millisecond {
		t.Errorf("first audio after %s; want the second account's 20 ms, not the 200 ms wait before it was asked", first)
	}
}

// What bounds the second request is the other account's own limit: every
// request to an account is counted, and when its minute is full it is not
// asked for anything it does not have to do.
func TestPoolDoesNotAskAnAccountThatHasHadItsMinute(t *testing.T) {
	f := &fakeTTS{name: uniq("pool-room"), accounts: 2,
		delays: [][]time.Duration{nil, {time.Second, time.Second, time.Second, time.Second, time.Second}}}
	s := openPool(t, f, 0, TTSPool{AccountRPM: 3})

	for i := 0; i < 5; i++ {
		ch, err := s.Synthesize(WithUrgent(context.Background()), "好的，")
		if err != nil {
			t.Fatal(err)
		}
		if from, _, serr := heard(t, ch); serr != nil || len(from) != 3 || from[0] != 0 {
			t.Fatalf("segment %d: audio %v, err %v; want the first account's", i, from, serr)
		}
		time.Sleep(120 * time.Millisecond) // the copy that lost puts itself in order
	}
	// Three a minute: the connection it was opened on, and two segments.
	if n := f.stream(1).calls.Load(); n != 2 {
		t.Errorf("the second account was asked for %d of five first segments; want 2, which with its connection is its three a minute", n)
	}
	if n := f.stream(0).calls.Load(); n != 5 {
		t.Errorf("the first account made %d of the five requests the session had to make; want all 5", n)
	}
}

// And the pool carries what one account cannot: a session whose account has
// had its minute moves to the one that has not.
func TestPoolMovesASessionOffAnAccountAtItsLimit(t *testing.T) {
	f := &fakeTTS{name: uniq("pool-move"), accounts: 2}
	s := openPool(t, f, 0, TTSPool{AccountRPM: 4})

	var got []byte
	for i := 0; i < 7; i++ {
		ch, err := s.Synthesize(context.Background(), "按提示输入车牌号。")
		if err != nil {
			t.Fatal(err)
		}
		from, _, serr := heard(t, ch)
		if serr != nil || len(from) != 3 {
			t.Fatalf("segment %d: audio %v, err %v", i, from, serr)
		}
		got = append(got, from[0])
	}
	// Four a minute each: a connection and three segments, then the other
	// account's three; the seventh has nowhere better to go and is made anyway.
	want := []byte{0, 0, 0, 1, 1, 1, 1}
	if string(got) != string(want) {
		t.Errorf("segments came from accounts %v; want %v", got, want)
	}
}

// One account with two connections is what it was: the pool's rules are for
// connections that are apart.
func TestPoolRulesDoNotApplyToOneAccount(t *testing.T) {
	f := &fakeTTS{name: uniq("pool-one"), delays: [][]time.Duration{{150 * time.Millisecond}, {10 * time.Millisecond}}}
	s, err := HedgeTTSPool(f, 2, 0, 100*time.Millisecond, TTSPool{AccountRPM: 1}, nil).Open(context.Background(), TTSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for f.opened() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)
	ch, err := s.Synthesize(WithUrgent(context.Background()), "好的，")
	if err != nil {
		t.Fatal(err)
	}
	from, first, serr := heard(t, ch)
	if serr != nil || len(from) != 3 || from[0] != 1 {
		t.Fatalf("audio %v, err %v; want the second connection's", from, serr)
	}
	if first < 100*time.Millisecond {
		t.Errorf("first audio after %s; on one account the second connection is asked only after the 100 ms wait", first)
	}
}

// Accounts are on different tiers, and a provider that knows says so. The
// roomy one carries the call; the small one is asked only for what fits under
// its own limit, whatever the pool's default says.
func TestPoolCountsEachAccountAgainstItsOwnLimit(t *testing.T) {
	f := &fakeTTS{name: uniq("pool-own-limit"), accounts: 2, rpms: []int{30, 3},
		delays: [][]time.Duration{nil, {time.Second, time.Second, time.Second, time.Second, time.Second, time.Second}}}
	s := openPool(t, f, 0, TTSPool{AccountRPM: 10})

	for i := 0; i < 6; i++ {
		ch, err := s.Synthesize(WithUrgent(context.Background()), "好的，")
		if err != nil {
			t.Fatal(err)
		}
		if from, _, serr := heard(t, ch); serr != nil || len(from) != 3 || from[0] != 0 {
			t.Fatalf("segment %d: audio %v, err %v; want the first account's", i, from, serr)
		}
		time.Sleep(120 * time.Millisecond)
	}
	// Three a minute on the second account: its connection and two segments —
	// not the ten the pool would otherwise have trusted it with.
	if n := f.stream(1).calls.Load(); n != 2 {
		t.Errorf("the second account, which allows 3 a minute, was asked for %d of six first segments; want 2", n)
	}
}

// When both accounts have had their minute, the session belongs on the one
// that can take more. It was staying wherever it happened to be — and a busy
// call parked on the account that refuses past ten a minute loses sentences.
func TestPoolSettlesOnTheRoomierAccountWhenBothAreFull(t *testing.T) {
	f := &fakeTTS{name: uniq("pool-roomier"), accounts: 2, rpms: []int{4, 3}}
	s := openPool(t, f, 0, TTSPool{})

	var got []byte
	for i := 0; i < 9; i++ {
		ch, err := s.Synthesize(context.Background(), "按提示输入车牌号。")
		if err != nil {
			t.Fatal(err)
		}
		from, _, serr := heard(t, ch)
		if serr != nil || len(from) != 3 {
			t.Fatalf("segment %d: audio %v, err %v", i, from, serr)
		}
		got = append(got, from[0])
	}
	// The roomy account: its connection and three segments, its four. Then
	// the small one, which has room: two segments on top of its connection,
	// its three. Now both are full — and every segment after that belongs on
	// the roomy account, not on the one it happens to be on.
	want := []byte{0, 0, 0, 1, 1, 0, 0, 0, 0}
	if string(got) != string(want) {
		t.Errorf("segments came from accounts %v; want %v", got, want)
	}
}

// A request an account refuses is asked of the other at once, room or no
// room: by then it is not an optional request, it is the sentence.
func TestPoolAsksTheOtherAccountWhenOneRefusesEvenWithNoRoom(t *testing.T) {
	refusal := errors.New("status 1002: rate limit exceeded(RPM)")
	f := &fakeTTS{name: uniq("pool-refused"), accounts: 2, rpms: []int{30, 1},
		synthErr: []error{refusal, nil}}
	// One a minute on the second account, spent on its connection: no room.
	s := openPool(t, f, 0, TTSPool{})

	ch, err := s.Synthesize(WithUrgent(context.Background()), "好的，")
	if err != nil {
		t.Fatal(err)
	}
	from, _, serr := heard(t, ch)
	if serr != nil || len(from) != 3 || from[0] != 1 {
		t.Fatalf("audio %v, err %v; the first account refused the segment, so the second should have said it", from, serr)
	}
}
