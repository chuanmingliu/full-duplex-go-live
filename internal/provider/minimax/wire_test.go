package minimax

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/provider"
)

// fakeT2A is enough of MiniMax's T2A WebSocket to answer the only question that
// matters here: which events does the client actually put on the wire?
//
// It deliberately does not implement task_flush or task_cancel, because the
// standard endpoint does not either — it records whatever arrives and answers
// nothing, so a client that wrongly sends one against /ws/v1/t2a_v2 hangs until
// the read deadline, exactly as it would in production.
type fakeT2A struct {
	mu   sync.Mutex
	sent []string // events received from the client, in order

	// bidi decides whether flush and cancel are answered.
	bidi bool
	// streaming models the thing that makes a bidirectional stream
	// bidirectional: text keeps arriving, audio keeps coming back, and there
	// is no per-task_continue boundary. is_final belongs to the task, not to
	// each piece of text handed to it, so nothing terminates a segment except
	// an explicit task_flush.
	streaming bool
	// lateFlushAck answers task_flush only after the segment has already been
	// finalised, so the acknowledgement is left in the socket for whoever reads
	// next. A live /ws/v1/t2a_v2_bidi trace of a flushed segment ends on
	// is_final, so any task_flushed it sends arrives behind that.
	lateFlushAck bool
	pendingFlush bool
	// beforeContinue can hold a reply while a test establishes that the
	// synthesis is active; it runs before any audio or final frame is sent.
	beforeContinue func()
}

func (f *fakeT2A) record(event string) {
	f.mu.Lock()
	f.sent = append(f.sent, event)
	f.mu.Unlock()
}

func (f *fakeT2A) events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func (f *fakeT2A) saw(event string) bool {
	for _, e := range f.events() {
		if e == event {
			return true
		}
	}
	return false
}

func (f *fakeT2A) handler() http.HandlerFunc {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	// One 20 ms frame of silence is plenty: the test is about control events,
	// not audio.
	audio := hex.EncodeToString(make([]byte, 2*24000*20/1000))

	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		write := func(v any) error {
			data, _ := json.Marshal(v)
			return conn.WriteMessage(websocket.TextMessage, data)
		}
		if err := write(map[string]any{"event": "connected_success"}); err != nil {
			return
		}
		for {
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var in struct {
				Event string `json:"event"`
			}
			if json.Unmarshal(data, &in) != nil {
				return
			}
			f.record(in.Event)

			switch in.Event {
			case "task_start":
				_ = write(map[string]any{"event": "task_started"})
			case "task_continue":
				if f.beforeContinue != nil {
					f.beforeContinue()
				}
				_ = write(map[string]any{
					"event": "task_continued",
					"data":  map[string]any{"audio": audio, "status": 1},
				})
				if !f.streaming {
					_ = write(map[string]any{"event": "task_continued", "is_final": true})
				}
				if f.pendingFlush {
					// Behind the terminator, which is what makes it stale.
					f.pendingFlush = false
					_ = write(map[string]any{"event": "task_flushed"})
				}
			case "task_flush":
				if !f.bidi {
					break
				}
				if f.lateFlushAck {
					f.pendingFlush = true
					break
				}
				_ = write(map[string]any{"event": "task_flushed"})
			case "task_cancel":
				if f.bidi {
					_ = write(map[string]any{"event": "task_canceled"})
				}
			case "task_finish":
				return
			}
		}
	}
}

func newFakeT2A(t *testing.T, bidi bool) (*fakeT2A, string) {
	return newFakeT2AMode(t, bidi, false)
}

func newFakeT2AMode(t *testing.T, bidi, streaming bool) (*fakeT2A, string) {
	return newFakeT2AFull(t, bidi, streaming, false)
}

func newFakeT2AFull(t *testing.T, bidi, streaming, lateFlushAck bool) (*fakeT2A, string) {
	t.Helper()
	f := &fakeT2A{bidi: bidi, streaming: streaming, lateFlushAck: lateFlushAck}
	return f, serveFakeT2A(t, f)
}

func serveFakeT2A(t *testing.T, f *fakeT2A) string {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	base := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/v1/t2a_v2"
	if f.bidi {
		base += "_bidi"
	}
	return base
}

func testClient(endpoint string) *TTS {
	return &TTS{
		Endpoint:             endpoint,
		APIKey:               "test",
		Model:                "speech-2.8-turbo",
		VoiceID:              "v1",
		Speed:                1,
		LanguageBoost:        "auto",
		FlushPartialSegments: true,
		CancelOnAbandon:      true,
		OpenTimeout:          3 * time.Second,
		ReceiveTimeout:       3 * time.Second,
		warnOnce:             &sync.Once{},
	}
}

// TestStandardEndpointNeverSeesBidiEvents is the wire-level proof of the fix.
//
// task_flush and task_cancel exist only on /ws/v1/t2a_v2_bidi. Sending either
// to /ws/v1/t2a_v2 returns 2202 illegal event, and because both are sent
// mid-turn it surfaces as a broken answer rather than as a URL one word short.
// The string check elsewhere tests the predicate; this tests that the predicate
// is actually consulted before anything reaches the socket.
func TestStandardEndpointNeverSeesBidiEvents(t *testing.T) {
	fake, endpoint := newFakeT2A(t, false)
	tts := testClient(endpoint)
	if tts.Bidi() {
		t.Fatalf("Bidi() is true for %q", endpoint)
	}

	st, err := tts.Open(context.Background(), provider.TTSOptions{SampleRate: 24000, Voice: "v1", Speed: 1})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	// "好的" has no sentence-ending punctuation, so FlushPartialSegments wants
	// to flush it. The endpoint must win that argument.
	chunks, err := st.Synthesize(context.Background(), "好的")
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	got := 0
	for c := range chunks {
		if c.Err != nil {
			t.Fatalf("synthesis failed on the standard endpoint: %v", c.Err)
		}
		got += len(c.PCM)
	}
	if got == 0 {
		t.Fatal("no audio came back")
	}
	if fake.saw("task_flush") {
		t.Error("task_flush was sent to the standard endpoint, which answers 2202 illegal event")
	}
	if fake.saw("task_cancel") {
		t.Error("task_cancel was sent to the standard endpoint")
	}
}

// TestBidiEndpointUsesTheEventsItHas is the other half: the guard must not be
// so cautious that the feature never runs where it is supported.
func TestBidiEndpointUsesTheEventsItHas(t *testing.T) {
	fake, endpoint := newFakeT2A(t, true)
	tts := testClient(endpoint)
	if !tts.Bidi() {
		t.Fatalf("Bidi() is false for %q", endpoint)
	}

	st, err := tts.Open(context.Background(), provider.TTSOptions{SampleRate: 24000, Voice: "v1", Speed: 1})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	chunks, err := st.Synthesize(context.Background(), "好的")
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	for c := range chunks {
		if c.Err != nil {
			t.Fatalf("synthesis failed: %v", c.Err)
		}
	}
	// The server records an event only after it has finished answering the one
	// before, so under load it can still be reading task_flush when the client
	// has already drained the audio. Wait for it rather than racing it.
	deadline := time.Now().Add(2 * time.Second)
	for !fake.saw("task_flush") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !fake.saw("task_flush") {
		t.Errorf("no task_flush on the bidirectional endpoint; events were %v", fake.events())
	}

	// A complete sentence needs no flush: the server synthesizes it on the
	// punctuation, and flushing it would be a round trip for nothing.
	before := len(fake.events())
	chunks, _ = st.Synthesize(context.Background(), "明天下午两点到四点是空的。")
	for c := range chunks {
		if c.Err != nil {
			t.Fatalf("synthesis failed: %v", c.Err)
		}
	}
	for _, e := range fake.events()[before:] {
		if e == "task_flush" {
			t.Error("a sentence ending in punctuation was flushed unnecessarily")
		}
	}
}

// chattyT2A behaves like the real thing on a normal sentence: dozens of small
// audio frames, not two. The count matters — the deadlock this reproduces needs
// more chunks than the delivery channel can buffer.
func chattyT2A(t *testing.T, frames int) string {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	audio := hex.EncodeToString(make([]byte, 2*24000*40/1000)) // 40 ms

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		write := func(v any) error {
			b, _ := json.Marshal(v)
			return conn.WriteMessage(websocket.TextMessage, b)
		}
		_ = write(map[string]any{"event": "connected_success"})
		for {
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var in struct {
				Event string `json:"event"`
			}
			_ = json.Unmarshal(data, &in)
			switch in.Event {
			case "task_start":
				_ = write(map[string]any{"event": "task_started"})
			case "task_continue":
				for i := 0; i < frames; i++ {
					if write(map[string]any{"event": "task_continued",
						"data": map[string]any{"audio": audio, "status": 1}}) != nil {
						return
					}
				}
				_ = write(map[string]any{"event": "task_continued", "is_final": true})
			case "task_finish":
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/v1/t2a_v2"
}

// TestAbandonedChannelDoesNotWedgeTheConnection is the regression test for the
// defect behind "only part of each answer is played".
//
// A caller that stops reading a Synthesize channel without cancelling used to
// block this stream's reader forever, with the lock that serializes synthesis
// still held. Every later segment on the connection then deadlocked — and the
// connection is shared across turns, so the session produced nothing but
// fragments from that point on: first segment audible, rest silent, for the
// rest of the call.
//
// The caller is at fault and is fixed separately. A provider that answers a
// caller's mistake with a permanent outage of itself is its own bug.
func TestAbandonedChannelDoesNotWedgeTheConnection(t *testing.T) {
	tts := testClient(chattyT2A(t, 60))
	// Short, so the test does not sit through the production default.
	tts.ReceiveTimeout = 700 * time.Millisecond
	st, err := tts.Open(context.Background(), provider.TTSOptions{SampleRate: 24000, Voice: "v1", Speed: 1})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	// Deliberately alive: this is the case a per-turn context creates when one
	// segment of that turn is superseded.
	ctx := context.Background()
	chunks, err := st.Synthesize(ctx, "这是第一段比较长的话。")
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	<-chunks // take one chunk, then walk away

	done := make(chan error, 1)
	go func() {
		_, err := st.Synthesize(ctx, "第二段。")
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection is wedged: a later segment never got to synthesize, " +
			"which is heard as every answer being cut off after its first sentence")
	}
}

func TestCancelledSynthesisWaitingForDrainSendsNoTask(t *testing.T) {
	active, release := make(chan struct{}), make(chan struct{})
	var first, released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	fake := &fakeT2A{beforeContinue: func() {
		first.Do(func() { close(active); <-release })
	}}
	tts := testClient(serveFakeT2A(t, fake))
	t.Cleanup(unblock)
	st, err := tts.Open(context.Background(), provider.TTSOptions{SampleRate: 24000, Voice: "v1", Speed: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	oldCtx, cancelOld := context.WithCancel(context.Background())
	defer cancelOld()
	oldChunks, err := st.Synthesize(oldCtx, "这句话会被打断。")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-active:
	case <-time.After(2 * time.Second):
		t.Fatal("the first synthesis did not reach the server")
	}
	cancelOld()

	// The old task cannot finish draining until the server is released. A
	// losing hedge can be queued on that lock and cancelled in the meantime.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		chunks <-chan provider.TTSChunk
		err    error
	}
	started, done := make(chan struct{}), make(chan result, 1)
	go func() {
		close(started)
		chunks, err := st.Synthesize(ctx, "这句话已经不需要了。")
		done <- result{chunks, err}
	}()
	<-started
	cancel()
	unblock()
	for range oldChunks {
	}
	select {
	case got := <-done:
		if got.chunks != nil {
			for range got.chunks {
			}
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Errorf("queued synthesis error = %v, want context.Canceled", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled synthesis did not return after the drain")
	}

	chunks, err := st.Synthesize(context.Background(), "这是下一句。")
	if err != nil {
		t.Fatalf("synthesize after cancellation: %v", err)
	}
	bytes := 0
	for chunk := range chunks {
		if chunk.Err != nil {
			t.Fatalf("synthesis after cancellation: %v", chunk.Err)
		}
		bytes += len(chunk.PCM)
	}
	if want := 2 * 24000 * 20 / 1000; bytes != want {
		t.Errorf("next synthesis audio = %d bytes, want %d", bytes, want)
	}
	starts, continues := 0, 0
	for _, event := range fake.events() {
		switch event {
		case "task_start":
			starts++
		case "task_continue":
			continues++
		}
	}
	if starts != 1 || continues != 2 {
		t.Errorf("got %d starts and %d syntheses, want one reused connection and only two live requests: %v", starts, continues, fake.events())
	}
}

// TestAbandonedTurnIsCancelledOnBidiAndDrainedOtherwise covers the barge-in
// path, where the endpoint difference decides between one round trip and
// reading out however much audio the vendor had already queued.
func TestAbandonedTurnIsCancelledOnBidiAndDrainedOtherwise(t *testing.T) {
	for _, bidi := range []bool{false, true} {
		name := "legacy"
		if bidi {
			name = "bidi"
		}
		t.Run(name, func(t *testing.T) {
			active, release := make(chan struct{}), make(chan struct{})
			var first, released sync.Once
			unblock := func() { released.Do(func() { close(release) }) }
			fake := &fakeT2A{bidi: bidi, streaming: bidi, beforeContinue: func() {
				first.Do(func() { close(active); <-release })
			}}
			endpoint := serveFakeT2A(t, fake)
			t.Cleanup(unblock)
			tts := testClient(endpoint)
			st, err := tts.Open(context.Background(), provider.TTSOptions{SampleRate: 24000, Voice: "v1", Speed: 1})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer st.Close()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			chunks, err := st.Synthesize(ctx, "这是一个会被打断的长句子。")
			if err != nil {
				t.Fatalf("synthesize: %v", err)
			}
			select {
			case <-active:
			case <-time.After(2 * time.Second):
				t.Fatal("the fake server did not receive the active synthesis")
			}
			// Cancellation precedes every response frame. The non-final audio
			// wakes a reader already in recv; it must then cancel or drain.
			// Bidi stays active until task_cancel; legacy releases its final.
			cancel()
			unblock()
			for range chunks {
			}
			if got := fake.saw("task_cancel"); got != bidi {
				t.Fatalf("task_cancel sent = %v, want %v (events: %v)", got, bidi, fake.events())
			}

			// Both paths must leave the same connection ready for the next
			// segment, with no stale audio or final frame ahead of its reply.
			chunks, err = st.Synthesize(context.Background(), "第二段")
			if err != nil {
				t.Fatalf("synthesize after abandonment: %v", err)
			}
			got := 0
			for c := range chunks {
				if c.Err != nil {
					t.Fatalf("synthesis after abandonment: %v", c.Err)
				}
				got += len(c.PCM)
			}
			if want := 2 * 24000 * 20 / 1000; got != want {
				t.Fatalf("audio after abandonment = %d bytes, want %d", got, want)
			}
			starts := 0
			for _, event := range fake.events() {
				if event == "task_start" {
					starts++
				}
			}
			if starts != 1 {
				t.Fatalf("the next synthesis reconnected instead of reusing the drained stream: %v", fake.events())
			}
		})
	}
}

// TestBidiToleratesAStreamWithNoPerChunkIsFinal records a theory that was
// tested against a live account and found false, and keeps the adapter honest
// about the part of it that was worth keeping.
//
// The theory: a bidirectional stream would not finalise each task_continue —
// text keeps arriving, audio keeps coming back, so is_final would describe the
// task — and a sentence-ending segment would therefore have nothing to
// terminate it and would sit until the read deadline. That would explain an
// answer whose opening fragment plays and whose rest never does.
//
// `ttsprobe -trace` says otherwise. Both endpoints finalise every
// task_continue: is_final in all four cases, 322–420 ms, full audio. So the
// flush was left alone.
//
// What remains true is that a server behaving this way would wedge the adapter
// for ReceiveTimeout per segment with no diagnosis, which is the worst way to
// fail. This drives the stand-in in that mode and asserts the adapter reports
// rather than hangs: a caller gets an error promptly, and the log names the
// cause instead of an i/o timeout.
func TestBidiToleratesAStreamWithNoPerChunkIsFinal(t *testing.T) {
	_, endpoint := newFakeT2AMode(t, true, true)
	c := testClient(endpoint)
	c.ReceiveTimeout = 700 * time.Millisecond

	stream, err := c.Open(context.Background(), provider.TTSOptions{SampleRate: 24000})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer stream.Close()

	started := time.Now()
	chunks, err := stream.Synthesize(context.Background(), "众安保险的车险管家小翠儿。")
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	var sawErr bool
	for chunk := range chunks {
		if chunk.Err != nil {
			sawErr = true
		}
	}
	if !sawErr {
		t.Error("a segment nothing terminated closed without an error; the caller would hear " +
			"the answer stop with nothing to go on")
	}
	// Bounded by the read deadline rather than by the caller giving up.
	if elapsed := time.Since(started); elapsed > 4*c.ReceiveTimeout {
		t.Errorf("took %v against a %v deadline; the segment was not bounded", elapsed, c.ReceiveTimeout)
	}
}

func countEvents(events []string, want string) int {
	n := 0
	for _, e := range events {
		if e == want {
			n++
		}
	}
	return n
}

// TestHandshakeSurvivesAnUnknownFrame covers a latent failure that a live trace
// of /ws/v1/t2a_v2_bidi turned up: the server sends sentence_start, which this
// adapter was not written for, in the same millisecond as task_started.
//
// In that trace it arrived second and the connection survived. Nothing
// guarantees the ordering, and the other way round the handshake returned
// `expected "task_started" but got "sentence_start"` and failed the whole
// connection — a frame carrying no error taking down a socket that was fine.
func TestHandshakeSurvivesAnUnknownFrame(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		write := func(v any) error {
			data, _ := json.Marshal(v)
			return conn.WriteMessage(websocket.TextMessage, data)
		}
		_ = write(map[string]any{"event": "connected_success"})
		for {
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var in struct {
				Event string `json:"event"`
			}
			if json.Unmarshal(data, &in) != nil {
				return
			}
			if in.Event == "task_start" {
				// The order the trace does not guarantee.
				_ = write(map[string]any{"event": "sentence_start"})
				_ = write(map[string]any{"event": "task_started"})
			}
		}
	}))
	defer srv.Close()

	endpoint := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/v1/t2a_v2_bidi"
	c := testClient(endpoint)
	stream, err := c.Open(context.Background(), provider.TTSOptions{SampleRate: 24000})
	if err != nil {
		t.Fatalf("open failed on a frame that carried no error: %v", err)
	}
	_ = stream.Close()
}

// TestStaleFlushAckDoesNotSilenceTheNextSegment is the fault behind an answer
// that plays its opening fragment on /ws/v1/t2a_v2_bidi and nothing after it.
//
// golive cuts a short unpunctuated first segment to get a syllable out early
// and flushes it; every segment after that ends a sentence and sends no flush.
// A live trace of a flushed segment on the bidirectional endpoint ends on
// is_final, so whatever task_flushed the server sends arrives *behind* the
// terminator the reader already returned on — and sits in the socket.
//
// readAudio then treated task_flushed as a terminator unconditionally, so the
// next segment's very first read found the stale acknowledgement and returned
// with no audio at all. The caller hears the greeting stop at the first segment
// boundary, at the same word every time, and the engine records a sentence that
// produced no PCM. The plain endpoint never sends a flush, which is exactly why
// the same call is fine there.
//
// A terminator has to belong to the segment being read.
func TestStaleFlushAckDoesNotSilenceTheNextSegment(t *testing.T) {
	_, endpoint := newFakeT2AFull(t, true, false, true)
	c := testClient(endpoint)
	c.ReceiveTimeout = 2 * time.Second

	stream, err := c.Open(context.Background(), provider.TTSOptions{SampleRate: 24000})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer stream.Close()

	// The shape of a real greeting: a short fragment that gets flushed, then
	// the sentences that follow.
	segments := []string{"您好，我是众", "安保险的车险管家小翠儿。", "看到您的爱车快到报价期了。"}
	for i, text := range segments {
		chunks, err := stream.Synthesize(context.Background(), text)
		if err != nil {
			t.Fatalf("segment %d (%q): %v", i, text, err)
		}
		pcm := 0
		for chunk := range chunks {
			if chunk.Err != nil {
				t.Fatalf("segment %d (%q): %v", i, text, chunk.Err)
			}
			pcm += len(chunk.PCM)
		}
		if pcm == 0 {
			t.Fatalf("segment %d (%q) produced no audio: it ended on the previous segment's "+
				"task_flushed, and the caller hears the answer stop here", i, text)
		}
	}
}

// TestTwoFlushedSegmentsInARow covers the case the terminator gate alone does
// not: the segmenter can emit two segments in a row that end without sentence
// punctuation — a long sentence hitting stream_max_chunk_chars will do it — and
// both then ask for a flush. The second one accepts a task_flushed, so it
// accepts the *first* one's, and comes back silent.
//
// Draining the acknowledgement at the end of a flushed segment is what makes
// this independent of whatever the next segment happens to be.
func TestTwoFlushedSegmentsInARow(t *testing.T) {
	_, endpoint := newFakeT2AFull(t, true, false, true)
	c := testClient(endpoint)
	c.ReceiveTimeout = 2 * time.Second

	stream, err := c.Open(context.Background(), provider.TTSOptions{SampleRate: 24000})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer stream.Close()

	// Neither ends a sentence, so both flush.
	for i, text := range []string{"您好，我是众", "安保险的车险管家"} {
		chunks, err := stream.Synthesize(context.Background(), text)
		if err != nil {
			t.Fatalf("segment %d (%q): %v", i, text, err)
		}
		pcm := 0
		for chunk := range chunks {
			if chunk.Err != nil {
				t.Fatalf("segment %d (%q): %v", i, text, chunk.Err)
			}
			pcm += len(chunk.PCM)
		}
		if pcm == 0 {
			t.Fatalf("segment %d (%q) produced no audio: it took the previous segment's "+
				"flush acknowledgement", i, text)
		}
	}
}
