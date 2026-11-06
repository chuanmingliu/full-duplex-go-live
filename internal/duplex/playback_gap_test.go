package duplex

import (
	"github.com/chuanmingliu/golive/internal/audio"
	"testing"
	"time"
)

func TestPlaybackGapRebasesClockWithoutCountingSilenceAsHeard(t *testing.T) {
	e, _, _, response := newAckHandoffTest(t)
	p := e.player
	p.cfg.Paced = true
	p.cfg.LeadMS = 300
	owner := response.turn
	seg := Segment{TurnID: owner.ID, owner: owner, Gen: owner.Generation, Revision: owner.Revision, Text: "New content", PCM: make([]byte, audio.PCM16(p.cfg.Rate).BytesForMS(80))}
	p.beginSegment(&e.gen, seg)
	// A previous 100 ms segment finished a second before new synthesis arrived.
	p.turnStarted = time.Now().Add(-1100 * time.Millisecond)
	p.playhead = p.turnStarted.Add(100 * time.Millisecond)
	p.emittedMS = 100
	before := time.Now()
	var starts []time.Time
	p.emit = func(_ string, _ []byte, at time.Time) { starts = append(starts, at) }
	var announced time.Time
	p.onSegment = func(_, _ string, at time.Time) { announced = at }
	p.playAudio(&e.gen, seg)
	if len(starts) == 0 || starts[0].Before(before) || announced != starts[0] {
		t.Fatalf("post-gap output/transcript retained stale time: starts=%v announcement=%v before=%v", starts, announced, before)
	}
	if want := starts[0].Add(-100 * time.Millisecond); !p.turnStarted.Equal(want) {
		t.Fatalf("gap remained in elapsed-audio clock: got %v want %v", p.turnStarted, want)
	}
	if p.emittedMS != 180 {
		t.Fatalf("gap changed emitted duration: %v", p.emittedMS)
	}
}

func TestPlaybackClockPreservesBufferedAndUnpacedOutput(t *testing.T) {
	for _, paced := range []bool{true, false} {
		t.Run(map[bool]string{true: "buffered", false: "unpaced"}[paced], func(t *testing.T) {
			e, _, _, response := newAckHandoffTest(t)
			p := e.player
			p.cfg.Paced = paced
			p.cfg.LeadMS = 300
			owner := response.turn
			seg := Segment{TurnID: owner.ID, owner: owner, Gen: owner.Generation, Revision: owner.Revision, PCM: make([]byte, audio.PCM16(p.cfg.Rate).BytesForMS(40))}
			p.beginSegment(&e.gen, seg)
			head := time.Now().Add(200 * time.Millisecond)
			if !paced {
				head = time.Now().Add(-time.Second)
			}
			p.playhead = head
			p.turnStarted = head.Add(-100 * time.Millisecond)
			start := p.turnStarted
			p.emittedMS = 100
			var got time.Time
			p.emit = func(_ string, _ []byte, at time.Time) { got = at }
			p.playAudio(&e.gen, seg)
			if got != head || p.turnStarted != start {
				t.Fatalf("existing clock changed: paced=%v got=%v want=%v", paced, got, head)
			}
		})
	}
}
