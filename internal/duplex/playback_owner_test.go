package duplex

import (
	"fmt"
	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"testing"
	"time"
)

func TestDelayedPlaybackReportCannotCompleteReplacement(t *testing.T) {
	for _, natural := range []bool{false, true} {
		for _, revise := range []bool{false, true} {
			for _, received := range []bool{false, true} {
				name := "truncate"
				if natural {
					name = "natural"
				}
				if revise {
					name += "_revision"
				}
				if received {
					name += "_received"
				}
				t.Run(name, func(t *testing.T) {
					e, c, _, p := newAckHandoffTest(t)
					old := p.turn
					var deliver func()
					e.player.cfg.Paced = false
					e.player.OnTruncate(func(r TruncationReport) { deliver = func() { e.onTruncated(r) } })
					e.player.OnTurnDone(func(r PlaybackReport) { deliver = func() { e.onTurnDone(r) } })
					seg := Segment{TurnID: old.ID, owner: old, Gen: old.Generation, Revision: old.Revision, Text: "Heard.", PCM: make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(40))}
					e.player.beginSegment(&e.gen, seg)
					e.player.playAudio(&e.gen, seg)
					e.player.markSegment(&e.gen, seg)
					if natural {
						e.player.finishTurn(&e.gen, seg)
					} else {
						e.player.Interrupt()
					}
					if deliver == nil {
						t.Fatal("no playback report")
					}
					if received {
						deliver()
						deliver()
					}
					var next *Turn
					if revise {
						next = e.tracker.Revise(old.ID, "new revision", true)
					} else {
						next = e.tracker.Begin("new turn", false)
					}
					e.commitTurn(next)
					if !received {
						deliver()
						deliver()
					}
					e.runPosted()
					if c.count(live.ExtTurnMetrics) != 1 {
						t.Fatal("lost or duplicated completed owner's metrics")
					}
					event := c.find(func(v any) bool { _, ok := v.(live.TurnMetricsEvent); return ok }).(live.TurnMetricsEvent)
					if event.TurnID != old.ID || event.Revision != old.Revision || event.Truncated == natural {
						t.Fatalf("wrong metric owner: %+v", event)
					}
					if old.Timings().CompletedAt.IsZero() || !next.Timings().CompletedAt.IsZero() {
						t.Fatal("completion mutated replacement instead of playback owner")
					}
				})
			}
		}
	}
}

func TestPlaybackReportRequiresExactOwner(t *testing.T) {
	for _, natural := range []bool{false, true} {
		for _, invalid := range []string{"missing", "generation", "revision"} {
			e, c, _, p := newAckHandoffTest(t)
			r := PlaybackReport{owner: p.turn, TurnID: p.turn.ID, Generation: p.turn.Generation, Revision: p.turn.Revision, TotalMS: 40, PlayedMS: 40}
			switch invalid {
			case "missing":
				r.owner = nil
			case "generation":
				r.Generation++
			case "revision":
				r.Revision++
			}
			if natural {
				e.onTurnDone(r)
			} else {
				e.onTruncated(r)
			}
			e.runPosted()
			if c.count(live.ExtTurnMetrics) != 0 || !p.turn.Timings().CompletedAt.IsZero() || len(e.history) != 0 {
				t.Fatal("unknown owner borrowed the current turn")
			}
		}
	}
}

func TestInheritedOpenerExplicitlyTransfersPlaybackOwnership(t *testing.T) {
	e, c, _, pending := newAckHandoffTest(t)
	old := pending.turn
	old.Opened = "opener"
	old.OpenedMS = 80
	e.player.cfg.Paced = false
	pcm := make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(40))
	head := Segment{owner: old, TurnID: old.ID, Gen: old.Generation, Revision: old.Revision, Text: "opener", PCM: pcm}
	e.player.beginSegment(&e.gen, head)
	e.player.playAudio(&e.gen, head)
	tail := head
	mark := head
	mark.Kind = SegMark
	body := head
	body.Kind = SegAudio
	body.content = true
	end := head
	end.Kind = SegEnd
	end.content = true
	e.player.Enqueue(mark)
	e.player.Enqueue(body)
	e.player.Enqueue(end)
	e.player.mu.Lock()
	next := e.tracker.Revise(old.ID, "corrected request", true)
	next.InheritOpener(old)
	adopted := e.player.adoptOpenerLocked(old, next)
	e.player.mu.Unlock()
	if !adopted {
		t.Fatal("opener adoption rejected")
	}
	e.commitTurn(next)
	if !e.player.allowed(tail, &e.gen) {
		t.Fatal("dequeued opener tail was lost across revision")
	}
	e.player.playAudio(&e.gen, tail)
	// An old end marker cannot finish the revised owner.
	e.player.finishTurn(&e.gen, end)
	if !next.Timings().CompletedAt.IsZero() {
		t.Fatal("old end marker completed the new owner")
	}
	for _, s := range e.player.queue {
		if s.owner != next || s.content {
			t.Fatal("adoption retained stale body or owner")
		}
	}
	speech := Segment{owner: next, TurnID: next.ID, Gen: next.Generation, Revision: next.Revision, Text: "answer", PCM: pcm, content: true}
	begin, audioSeg, done := speech, speech, speech
	begin.Kind = SegBegin
	audioSeg.Kind = SegAudio
	done.Kind = SegEnd
	e.player.Enqueue(begin)
	e.player.Enqueue(audioSeg)
	e.player.Enqueue(done)
	e.player.OnTurnDone(func(r PlaybackReport) { e.onTurnDone(r); e.player.Close() })
	e.player.Run(&e.gen)
	e.runPosted()
	if c.count(live.ExtTurnMetrics) != 1 {
		t.Fatal("inherited opener did not produce one completed-owner metric")
	}
	event := c.find(func(v any) bool { _, ok := v.(live.TurnMetricsEvent); return ok }).(live.TurnMetricsEvent)
	if event.Revision != next.Revision || event.OutputAudioMS != 120 || !next.Timings().CompletedNaturally || !old.Timings().CompletedAt.IsZero() {
		t.Fatalf("incorrect inherited accounting: %+v", event)
	}
	if next.Timings().FirstAudioOut != old.Timings().FirstAudioOut {
		t.Fatal("inherited first output origin changed")
	}
	if e.player.owner != nil || e.player.adoptedFrom != nil || e.player.adoptedTo != nil {
		t.Fatal("completed player retained owners")
	}
}

func TestHeardBodyCannotBeAdoptedAsAnOpener(t *testing.T) {
	e, _, _, p := newAckHandoffTest(t)
	old := p.turn
	seg := Segment{owner: old, TurnID: old.ID, Gen: old.Generation, Revision: old.Revision, content: true, PCM: make([]byte, 80)}
	e.player.beginSegment(&e.gen, seg)
	e.player.playAudio(&e.gen, seg)
	e.player.mu.Lock()
	next := e.tracker.Revise(old.ID, "next revision", true)
	adopted := e.player.adoptOpenerLocked(old, next)
	e.player.mu.Unlock()
	if adopted || e.player.owner != old {
		t.Fatal("heard body silently changed owner")
	}
}

// Output is an irrevocable commit. A synchronous output sink may let the caller
// interrupt before returning; its old chunk must never accrue to the new owner.
func TestOutputCommitCannotAccrueToReplacement(t *testing.T) {
	e, _, _, pending := newAckHandoffTest(t)
	old := pending.turn
	e.player.cfg.Paced = false
	var report PlaybackReport
	var next *Turn
	e.player.OnTruncate(func(r TruncationReport) { report = r })
	e.player.emit = func(string, []byte, time.Time) {
		e.gen.Bump()
		e.player.Interrupt()
		next = e.tracker.Revise(old.ID, "replacement", true)
		e.player.beginSegment(&e.gen, Segment{owner: next, Gen: next.Generation, Revision: next.Revision, TurnID: next.ID})
	}
	seg := Segment{owner: old, Gen: old.Generation, Revision: old.Revision, TurnID: old.ID, content: true, readout: true, PCM: make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(40))}
	e.player.playAudio(&e.gen, seg)
	if report.validTurn() != old || report.TotalMS != 40 || old.Timings().FirstContentOut.IsZero() || old.Timings().FirstReadoutOut.IsZero() {
		t.Fatalf("old output lost its owner or accounting: %+v", report)
	}
	if e.player.owner != next || e.player.emittedMS != 0 {
		t.Fatal("old output accrued to replacement playback")
	}
	if !next.Timings().FirstAudioOut.IsZero() {
		t.Fatal("unplayed replacement received an output stamp")
	}
}

func TestStaleAudioCannotTakeReplacementOwnership(t *testing.T) {
	e, _, _, pending := newAckHandoffTest(t)
	old := pending.turn
	oldSeg := Segment{owner: old, Gen: old.Generation, Revision: old.Revision, TurnID: old.ID, PCM: make([]byte, 80)}
	if !e.player.allowed(oldSeg, &e.gen) {
		t.Fatal("old segment never valid")
	}
	next := e.tracker.Revise(old.ID, "replacement", true)
	e.player.beginSegment(&e.gen, Segment{owner: next, Gen: next.Generation, Revision: next.Revision, TurnID: next.ID})
	// Emulate dispatch after the generation changed but before audio entered
	// its first chunk; Run's earlier allowed check cannot establish ownership.
	e.player.playAudio(&e.gen, oldSeg)
	if e.player.owner != next || e.player.emittedMS != 0 || !old.Timings().FirstAudioOut.IsZero() {
		t.Fatal("stale dispatch changed playback owner or output")
	}
}

func TestDequeuedOpenerAdoptsBeforeItClaimsTheFloor(t *testing.T) {
	e, _, _, pending := newAckHandoffTest(t)
	old := pending.turn
	old.Opened, old.OpenedMS = "opener", 40
	seg := Segment{owner: old, Gen: old.Generation, Revision: old.Revision, TurnID: old.ID, Text: old.Opened}
	// Run has removed this begin marker from its queue, but not dispatched it.
	body, mark := seg, seg
	body.Kind, body.PCM = SegAudio, make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(40))
	mark.Kind = SegMark
	e.player.Enqueue(body)
	e.player.Enqueue(mark)
	e.player.mu.Lock()
	next := e.tracker.Revise(old.ID, "replacement", true)
	next.InheritOpener(old)
	ok := e.player.adoptOpenerLocked(old, next)
	e.player.mu.Unlock()
	if !ok || !e.player.allowed(seg, &e.gen) {
		t.Fatal("dequeued opener lost during adoption")
	}
	e.player.beginSegment(&e.gen, seg)
	if e.player.owner != next {
		t.Fatal("dequeued opener claimed stale ownership")
	}
	e.player.playAudio(&e.gen, e.player.queue[0])
	if next.Timings().FirstAudioOut.IsZero() || !old.Timings().FirstAudioOut.IsZero() {
		t.Fatal("first output stamped the wrong owner")
	}
}

func TestOldAndNewPlaybackCallbacksKeepIndependentCompletion(t *testing.T) {
	for _, order := range []string{"oonn", "nnoo", "onon", "nono"} {
		for _, oldNatural := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/natural=%t", order, oldNatural), func(t *testing.T) {
				e, c, _, pending := newAckHandoffTest(t)
				old := pending.turn
				next := e.tracker.Revise(old.ID, "replacement", true)
				e.commitTurn(next)
				oldReport := PlaybackReport{owner: old, TurnID: old.ID, Revision: old.Revision, Generation: old.Generation, TotalMS: 40, PlayedMS: 40, SpokenText: "old"}
				nextReport := PlaybackReport{owner: next, TurnID: next.ID, Revision: next.Revision, Generation: next.Generation, TotalMS: 80, PlayedMS: 80, SpokenText: "new"}
				for _, which := range order {
					r, natural := oldReport, oldNatural
					if which == 'n' {
						r, natural = nextReport, !oldNatural
					}
					if natural {
						e.onTurnDone(r)
					} else {
						e.onTruncated(r)
					}
					e.runPosted()
				}
				if c.count(live.ExtTurnMetrics) != 2 || old.Timings().CompletedNaturally != oldNatural || next.Timings().CompletedNaturally == oldNatural {
					t.Fatal("completion leaked across owners or was duplicated")
				}
				if old.Spoken != "old" || next.Spoken != "new" {
					t.Fatal("heard text attached to wrong owner")
				}
				count := 0
				for _, m := range e.history {
					if m.Role == provider.RoleAssistant {
						count++
					}
				}
				if count != 2 {
					t.Fatalf("duplicate callbacks entered history %d times", count)
				}
			})
		}
	}
}

func TestLateOldTruncationCannotInvalidateReplacementOrHandoff(t *testing.T) {
	e, c, _, pending := newAckHandoffTest(t)
	old := pending.turn
	r := PlaybackReport{owner: old, TurnID: old.ID, Revision: old.Revision, Generation: old.Generation, TotalMS: 40, PlayedMS: 40}
	e.onTurnDone(r)
	e.runPosted()
	next := e.tracker.Revise(old.ID, "replacement", true)
	e.tools.handoff = next
	e.onTurnDone(r)
	e.onTruncated(r)
	e.runPosted()
	if e.tools.handoff != next || !next.Timings().CompletedAt.IsZero() || old.Timings().CompletedNaturally {
		t.Fatal("old callback affected replacement or retained natural proof")
	}
	r.owner, r.Generation, r.Revision = next, next.Generation, next.Revision
	e.onTurnDone(r)
	e.runPosted()
	if !next.Timings().CompletedNaturally || c.count(live.ExtTurnMetrics) != 2 || e.tools.handoff != nil {
		t.Fatal("replacement could not complete independently")
	}
}

func TestPlayerCloseReleasesPlaybackOwners(t *testing.T) {
	e, _, _, pending := newAckHandoffTest(t)
	old := pending.turn
	old.Opened = "opener"
	s := Segment{owner: old, TurnID: old.ID, Gen: old.Generation, Revision: old.Revision}
	e.player.beginSegment(&e.gen, s)
	e.player.mu.Lock()
	next := e.tracker.Revise(old.ID, "replacement", true)
	e.player.adoptOpenerLocked(old, next)
	e.player.mu.Unlock()
	e.player.Close()
	if e.player.owner != nil || e.player.adoptedFrom != nil || e.player.adoptedTo != nil || len(e.player.queue) != 0 || e.player.allowed(s, &e.gen) {
		t.Fatal("closed player retained or accepted an owner")
	}
}

func TestTruncationQuietClockBelongsToCurrentPointer(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		e, _, _, pending := newAckHandoffTest(t)
		old := pending.turn
		before := time.Unix(100, 0)
		e.tools.lastQuietAt = before
		e.gen.Bump() // a real interrupt invalidates production before its report
		if replaced {
			e.tracker.Revise(old.ID, "replacement", true)
		}
		e.onTruncated(TruncationReport{owner: old, TurnID: old.ID, Revision: old.Revision, Generation: old.Generation, TotalMS: 40, PlayedMS: 40})
		e.runPosted()
		if e.tools.lastQuietAt.Equal(before) != replaced {
			t.Fatal("quiet clock ignored current interrupted owner or accepted stale owner")
		}
	}
}

func TestInterruptedInactiveAdoptionCannotReplayOpener(t *testing.T) {
	e, _, _, pending := newAckHandoffTest(t)
	old := pending.turn
	old.Opened = "opener"
	seg := Segment{owner: old, Gen: old.Generation, Revision: old.Revision, TurnID: old.ID, Text: old.Opened, PCM: make([]byte, 80)}
	e.player.mu.Lock()
	next := e.tracker.Revise(old.ID, "replacement", true)
	next.InheritOpener(old)
	e.player.adoptOpenerLocked(old, next)
	e.player.mu.Unlock()
	e.gen.Bump()
	if e.player.allowed(seg, &e.gen) {
		t.Fatal("stale adoption ignores its target generation")
	}
	e.player.Interrupt()
	if e.player.allowed(seg, &e.gen) {
		t.Fatal("interrupted inherited opener remains allowed")
	}
	e.player.playAudio(&e.gen, seg)
	if e.player.owner != nil || e.player.adoptedFrom != nil || e.player.adoptedTo != nil || !next.Timings().FirstAudioOut.IsZero() {
		t.Fatal("interrupted inactive adoption replayed or retained owners")
	}
}

func TestInheritedOpenerGraceRetainsExactOwner(t *testing.T) {
	e, _, _, pending := newAckHandoffTest(t)
	old := pending.turn
	old.Opened = "opener"
	e.player.cfg.Paced = false
	seg := Segment{owner: old, Gen: old.Generation, Revision: old.Revision, TurnID: old.ID, Text: old.Opened, PCM: make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(40))}
	e.player.beginSegment(&e.gen, seg)
	e.player.playAudio(&e.gen, seg)
	e.player.mu.Lock()
	next := e.tracker.Revise(old.ID, "first revision", true)
	next.InheritOpener(old)
	e.player.adoptOpenerLocked(old, next)
	last := e.tracker.Revise(next.ID, "second revision", true)
	last.InheritOpener(next)
	e.player.adoptOpenerLocked(next, last)
	e.player.mu.Unlock()
	e.player.GraceFinishSegment(last.ID)
	e.gen.Bump()
	if !e.player.allowed(seg, &e.gen) {
		t.Fatal("explicit grace lost the inherited opener tail")
	}
	e.player.playAudio(&e.gen, seg)
	var report PlaybackReport
	e.player.OnTruncate(func(r TruncationReport) { report = r })
	seg.Kind = SegMark
	e.player.markSegment(&e.gen, seg)
	if report.validTurn() != last || report.TotalMS != 80 {
		t.Fatalf("grace completed the wrong inherited owner: %+v", report)
	}
}

func TestOwnedBackchannelDoesNotBecomeResponseMetrics(t *testing.T) {
	e, c, _, pending := newAckHandoffTest(t)
	filler := &Turn{ID: "bc_1", Generation: e.gen.Current()}
	filler.markOutput(false, false)
	e.onTurnDone(PlaybackReport{owner: filler, TurnID: filler.ID, Generation: filler.Generation, TotalMS: 40, PlayedMS: 40, SpokenText: "filler"})
	e.runPosted()
	if c.count(live.ExtTurnMetrics) != 0 || len(e.history) != 0 || !pending.turn.Timings().CompletedAt.IsZero() {
		t.Fatal("filler affected caller metrics or history")
	}
}
