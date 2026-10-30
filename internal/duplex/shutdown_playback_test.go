package duplex

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
)

// Hold the control loop while the real player emits. This reproduces a socket
// close racing the posted completion without a provider or timing-dependent LLM.
func shutdownPlaybackFixture(t *testing.T, speculative, paced bool) (*Engine, *collector, *Turn, <-chan struct{}, func()) {
	t.Helper()
	cfg := testConfig()
	cfg.Duplex.PlaybackPaced = paced
	cfg.Duplex.PlaybackLeadMS = 40
	c := newCollector(cfg.ClientRate)
	output := make(chan struct{}, 1)
	e := New(Options{Cfg: cfg, ClientRate: cfg.ClientRate}, Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Emit: func(ev any) {
		c.emit(ev)
		if _, ok := ev.(live.OutputAudioDelta); ok {
			select {
			case output <- struct{}{}:
			default:
			}
		}
	}})
	turn := e.tracker.Begin("fixture question", speculative)
	if !speculative {
		e.commitTurn(turn)
	}
	turn.MarkSpeechEnd(time.Now().Add(-200*time.Millisecond), 400, 380)
	e.ctx, e.cancel = context.WithCancel(context.Background())
	held, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	e.post(func() { close(held); <-release })
	e.wg.Add(2)
	go func() { defer e.wg.Done(); e.loop() }()
	go func() { defer e.wg.Done(); e.player.Run(&e.gen) }()
	select {
	case <-held:
	case <-time.After(time.Second):
		t.Fatal("loop did not start")
	}
	closeEngine := func() {
		done := make(chan struct{})
		go func() { e.Close(); close(done) }()
		select {
		case <-e.ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("close did not cancel")
		}
		releaseOnce.Do(func() { close(release) })
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("close did not finish promptly")
		}
	}
	t.Cleanup(closeEngine)
	return e, c, turn, output, closeEngine
}

func enqueueShutdownReply(e *Engine, turn *Turn, duration int, end bool) {
	seg := Segment{owner: turn, TurnID: turn.ID, Gen: turn.Generation, Revision: turn.Revision, Text: "This is a sufficiently long fixture reply.", content: true, readout: true}
	seg.Kind = SegBegin
	e.player.Enqueue(seg)
	seg.Kind = SegAudio
	seg.PCM = make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(duration))
	e.player.Enqueue(seg)
	if end {
		seg.Kind = SegMark
		seg.PCM = nil
		e.player.Enqueue(seg)
		seg.Kind = SegEnd
		e.player.Enqueue(seg)
	}
}

func waitShutdownOutput(t *testing.T, output <-chan struct{}) {
	t.Helper()
	select {
	case <-output:
	case <-time.After(time.Second):
		t.Fatal("player did not emit")
	}
}

func TestEngineCloseAccountsPlayingReply(t *testing.T) {
	for _, speculative := range []bool{false, true} {
		t.Run(map[bool]string{false: "committed", true: "current speculative pause"}[speculative], func(t *testing.T) {
			e, c, turn, output, closeEngine := shutdownPlaybackFixture(t, speculative, true)
			turn.Readout = "fixture result"
			enqueueShutdownReply(e, turn, 400, true)
			waitShutdownOutput(t, output)
			before := turn.Timings()
			closeEngine()
			if c.count(live.ExtTurnMetrics) != 1 {
				t.Fatalf("playing reply disappeared at close: metrics=%d", c.count(live.ExtTurnMetrics))
			}
			ev := c.find(func(v any) bool { _, ok := v.(live.TurnMetricsEvent); return ok }).(live.TurnMetricsEvent)
			if !ev.Truncated || ev.TurnID != turn.ID || ev.FirstAudioOutMS != before.FirstAudioOut.Sub(before.SpeechEndAt).Milliseconds() {
				t.Fatalf("wrong shutdown metric: %+v", ev)
			}
			after := turn.Timings()
			if after.CompletedAt.IsZero() || after.CompletedNaturally || !after.FirstReadoutOut.Equal(before.FirstReadoutOut) {
				t.Fatal("shutdown changed output timing or claimed natural completion")
			}
			if e.player.Active() {
				t.Fatal("closed player retained playback")
			}
		})
	}
}

func TestEngineCloseRetainsPendingNaturalCompletion(t *testing.T) {
	e, c, turn, _, closeEngine := shutdownPlaybackFixture(t, false, false)
	reported := make(chan struct{})
	e.player.OnTurnDone(func(r PlaybackReport) {
		e.onTurnDone(r)
		e.onTurnDone(r) // a repeated receipt must not duplicate metrics or history
		close(reported)
	})
	enqueueShutdownReply(e, turn, 40, true)
	select {
	case <-reported:
	case <-time.After(time.Second):
		t.Fatal("natural completion missing")
	}
	// A general control callback must not run as a side effect of draining the
	// completion receipt after cancellation.
	ran := false
	e.post(func() { ran = true })
	closeEngine()
	if ran {
		t.Fatal("shutdown ran arbitrary control work")
	}
	if c.count(live.ExtTurnMetrics) != 1 || !turn.Timings().CompletedNaturally {
		t.Fatal("pending natural completion was lost")
	}
	ev := c.find(func(v any) bool { _, ok := v.(live.TurnMetricsEvent); return ok }).(live.TurnMetricsEvent)
	if ev.Truncated || ev.OutputAudioMS != 40 {
		t.Fatalf("natural playback became truncated: %+v", ev)
	}
}

func TestEngineCloseDoesNotInventUnheardOrBackchannelMetrics(t *testing.T) {
	for _, kind := range []string{"no audio", "filler", "opener became backchannel", "abandoned guess"} {
		t.Run(kind, func(t *testing.T) {
			e, c, turn, output, closeEngine := shutdownPlaybackFixture(t, true, true)
			switch kind {
			case "no audio":
				e.player.beginSegment(&e.gen, Segment{owner: turn, TurnID: turn.ID, Gen: turn.Generation, Text: "unheard"})
			case "filler":
				turn.ID = "bc_fixture"
				enqueueShutdownReply(e, turn, 40, false)
				waitShutdownOutput(t, output)
			case "opener became backchannel":
				enqueueShutdownReply(e, turn, 40, false)
				waitShutdownOutput(t, output)
				e.player.FinishOpenerAsBackchannel(turn.ID)
			case "abandoned guess":
				enqueueShutdownReply(e, turn, 40, false)
				waitShutdownOutput(t, output)
				turn.kill()
			}
			closeEngine()
			if c.count(live.ExtTurnMetrics) != 0 {
				t.Fatalf("%s introduced a response sample", kind)
			}
		})
	}
}

func TestEngineCloseRetainsPendingTruncation(t *testing.T) {
	e, c, turn, output, closeEngine := shutdownPlaybackFixture(t, false, true)
	enqueueShutdownReply(e, turn, 400, true)
	waitShutdownOutput(t, output)
	e.player.Interrupt()
	closeEngine()
	if c.count(live.ExtTurnMetrics) != 1 || turn.Timings().CompletedNaturally {
		t.Fatal("pending truncation was lost or completed naturally")
	}
	ev := c.find(func(v any) bool { _, ok := v.(live.TurnMetricsEvent); return ok }).(live.TurnMetricsEvent)
	if !ev.Truncated {
		t.Fatal("interrupted playback was reported as complete")
	}
}

func TestEngineCloseStoresPartialHistoryAndConcurrentSnapshots(t *testing.T) {
	e, c, turn, output, closeEngine := shutdownPlaybackFixture(t, false, true)
	enqueueShutdownReply(e, turn, 400, true)
	waitShutdownOutput(t, output)
	waitShutdownOutput(t, output) // the initial lead permits a second immediate chunk
	waitShutdownOutput(t, output) // the third chunk waits for the playhead
	var snapshots sync.WaitGroup
	for i := 0; i < 3; i++ {
		snapshots.Add(1)
		go func() { defer snapshots.Done(); e.History() }()
	}
	closeEngine()
	snapshots.Wait()
	history := e.History()
	var replies, questions int
	for _, message := range history {
		if message.Role == provider.RoleUser && message.Content == turn.Transcript {
			questions++
		}
		if message.Role == provider.RoleAssistant {
			replies++
			if message.Content == "" || message.Content == "This is a sufficiently long fixture reply." || !strings.HasPrefix("This is a sufficiently long fixture reply.", message.Content) {
				t.Fatalf("stored unplayed full response or lost its prefix: %q", message.Content)
			}
		}
	}
	if replies != 1 || questions != 1 || c.count(live.ExtTurnMetrics) != 1 {
		t.Fatalf("incomplete/duplicate shutdown history: replies=%d questions=%d metrics=%d", replies, questions, c.count(live.ExtTurnMetrics))
	}
}

func TestEngineCloseSkipsRestOfCancelledControlBatch(t *testing.T) {
	cfg := testConfig()
	c := newCollector(cfg.ClientRate)
	e := New(Options{Cfg: cfg, ClientRate: cfg.ClientRate}, Deps{Emit: c.emit, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.ctx, e.cancel = context.WithCancel(context.Background())
	t.Cleanup(e.Close)
	turn := e.tracker.Begin("fixture question", false)
	e.commitTurn(turn)
	turn.MarkSpeechEnd(time.Now(), 40, 380)
	turn.MarkFirstAudioOut()
	e.post(e.cancel)
	e.onTurnDone(PlaybackReport{owner: turn, TurnID: turn.ID, Generation: turn.Generation, Revision: turn.Revision, PlayedMS: 40, TotalMS: 40, SpokenText: "fixture answer"})
	ran := false
	e.post(func() { ran = true })
	e.runPosted()
	if ran {
		t.Fatal("cancellation ran the rest of a detached control batch")
	}
	if c.count(live.ExtTurnMetrics) != 0 {
		t.Fatal("cancelled loop processed the completion instead of retaining it")
	}
	e.Close()
	if c.count(live.ExtTurnMetrics) != 1 || !turn.Timings().CompletedNaturally {
		t.Fatal("skipped batch lost its natural completion receipt")
	}
}

func TestEngineCloseKeepsSupersededPlaybackOwner(t *testing.T) {
	e, c, old, output, closeEngine := shutdownPlaybackFixture(t, false, true)
	enqueueShutdownReply(e, old, 400, true)
	waitShutdownOutput(t, output)
	next := e.tracker.Revise(old.ID, "revised question", true)
	e.commitTurn(next)
	next.MarkSpeechEnd(time.Now(), 900, 380)
	closeEngine()
	if c.count(live.ExtTurnMetrics) != 1 {
		t.Fatal("superseded playback was lost or double counted")
	}
	ev := c.find(func(v any) bool { _, ok := v.(live.TurnMetricsEvent); return ok }).(live.TurnMetricsEvent)
	if ev.TurnID != old.ID || ev.Revision != old.Revision || !ev.Truncated || !next.Timings().CompletedAt.IsZero() {
		t.Fatalf("shutdown attributed old output to new revision: %+v", ev)
	}
}
