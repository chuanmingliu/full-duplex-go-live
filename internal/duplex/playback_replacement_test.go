package duplex

import (
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/live"
)

// A final caller transcript can advance the generation while speaker-focus
// confirmation still holds the explicit interruption. The next valid segment
// must retire the old playback owner even when Interrupt was never called.
func TestReplacementRetainsPreviousPlaybackReceipt(t *testing.T) {
	for _, dispatch := range []string{"begin", "audio"} {
		for _, revise := range []bool{false, true} {
			name := dispatch + "/turn"
			if revise {
				name = dispatch + "/revision"
			}
			t.Run(name, func(t *testing.T) {
				e, c, _, pending := newAckHandoffTest(t)
				old := pending.turn
				e.player.cfg.Paced = false
				pcm := make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(40))
				seg := Segment{owner: old, TurnID: old.ID, Gen: old.Generation,
					Revision: old.Revision, Text: "Heard.", content: true, PCM: pcm}
				e.player.beginSegment(&e.gen, seg)
				e.player.playAudio(&e.gen, seg)
				var report PlaybackReport
				reports := 0
				e.player.OnTruncate(func(r TruncationReport) {
					// Callbacks must run outside the player's mutex.
					e.player.Heard()
					report, reports = r, reports+1
					e.onTruncated(r)
				})
				var next *Turn
				if revise {
					next = e.tracker.Revise(old.ID, "Corrected request.", true)
				} else {
					next = e.tracker.Begin("Next request.", false)
				}
				e.commitTurn(next)
				replacement := Segment{owner: next, TurnID: next.ID, Gen: next.Generation,
					Revision: next.Revision, content: true, PCM: pcm}
				if dispatch == "begin" {
					e.player.beginSegment(&e.gen, replacement)
				} else {
					e.player.playAudio(&e.gen, replacement)
				}
				e.runPosted()
				if reports != 1 || report.validTurn() != old || report.TotalMS != 40 || report.PlayedMS != 40 || report.SpokenText != "Heard." {
					t.Fatalf("lost previous playback receipt: reports=%d report=%+v", reports, report)
				}
				if old.Timings().CompletedAt.IsZero() || !next.Timings().CompletedAt.IsZero() {
					t.Fatal("replacement completed instead of previous owner")
				}
				// Stale end markers and repeated dispatch cannot report it twice.
				e.player.finishTurn(&e.gen, seg)
				e.player.beginSegment(&e.gen, replacement)
				e.runPosted()
				if reports != 1 || c.count(live.ExtTurnMetrics) != 1 {
					t.Fatal("lost or duplicated previous owner's metrics")
				}
				event := c.find(func(v any) bool { _, ok := v.(live.TurnMetricsEvent); return ok }).(live.TurnMetricsEvent)
				if event.TurnID != old.ID || event.Revision != old.Revision || !event.Truncated || event.OutputAudioMS != 40 {
					t.Fatalf("wrong completion metric: %+v", event)
				}
			})
		}
	}
}

func TestUnplayedReplacementDoesNotInventReceipt(t *testing.T) {
	e, c, _, pending := newAckHandoffTest(t)
	old := pending.turn
	e.player.beginSegment(&e.gen, Segment{owner: old, TurnID: old.ID, Gen: old.Generation, Text: "Never heard."})
	next := e.tracker.Begin("Next request.", false)
	e.player.beginSegment(&e.gen, Segment{owner: next, TurnID: next.ID, Gen: next.Generation})
	e.runPosted()
	if c.count(live.ExtTurnMetrics) != 0 || !old.Timings().CompletedAt.IsZero() {
		t.Fatal("unplayed turn received fabricated playback proof")
	}
}

func TestReplacementSnapshotsHeardPrefix(t *testing.T) {
	var gen Generation
	p := NewPlayer(PlayerConfig{Rate: 16000}, func(string, []byte, time.Time) {})
	var report PlaybackReport
	p.OnTruncate(func(r TruncationReport) { report = r })
	old := Segment{TurnID: "old", Gen: gen.Bump(), Text: "0123456789", PCM: make([]byte, 32000)}
	p.beginSegment(&gen, old)
	p.playAudio(&gen, old)
	p.mu.Lock()
	p.cfg.Paced = true
	p.turnStarted = time.Now().Add(-100 * time.Millisecond)
	p.mu.Unlock()
	p.beginSegment(&gen, Segment{TurnID: "new", Gen: gen.Bump()})
	if report.TurnID != "old" || report.TotalMS != 1000 || report.PlayedMS < 100 || report.PlayedMS >= 500 || report.SpokenText == old.Text {
		t.Fatalf("replacement claimed unplayed tail: %+v", report)
	}
}
