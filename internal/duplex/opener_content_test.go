package duplex

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
)

// Existing opener-only scenarios must actually keep body output pending.
// A fixed first-token delay let those tests silently exercise the data-loss bug.
type blockedFirstOpenerLLM struct {
	provider.LLM
	started atomic.Bool
}

func (l *blockedFirstOpenerLLM) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	if l.started.Swap(true) {
		return l.LLM.Stream(ctx, req)
	}
	out := make(chan provider.LLMDelta)
	go func() {
		<-ctx.Done()
		close(out)
	}()
	return out, nil
}

// A continuation can be recognized after body audio has already started,
// even when its input timestamp falls inside the opener's grace window.
func TestStartedAnswerCannotBecomeAnOpenerBackchannel(t *testing.T) {
	for _, abandon := range []bool{false, true} {
		t.Run(map[bool]string{false: "barge_in", true: "abandoned_speculation"}[abandon], func(t *testing.T) {
			e, c, _, response := newAckHandoffTest(t)
			var logs bytes.Buffer
			e.log = slog.New(slog.NewTextHandler(&logs, nil))
			e.opts.Cfg.Duplex.AllowBargeIn = true
			e.opts.Cfg.Duplex.OnNewQuery = "cut"
			turn := response.turn
			turn.Opened, turn.OpenedMS, turn.OpenedAt = "Yes,", 500, time.Now()
			turn.MarkSpeechEnd(time.Now().Add(-time.Second), 0, 380)
			e.player.cfg.Paced = false
			seg := Segment{TurnID: turn.ID, owner: turn, Gen: turn.Generation, Revision: turn.Revision,
				Text: "The actual answer.", PCM: make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(80)), content: true}
			e.player.beginSegment(&e.gen, seg)
			e.player.playAudio(&e.gen, seg)
			before := turn.Timings().FirstContentOut
			if before.IsZero() {
				t.Fatal("fixture did not emit body content")
			}
			if abandon {
				e.specTurn = turn
				e.abandonSpeculation("caller continued")
			} else {
				e.yieldFloor(true)
			}
			e.runPosted()
			if strings.Contains(logs.String(), "opener became a backchannel") {
				t.Fatal("already-started answer was reclassified as a backchannel")
			}
			if c.count(live.ExtTurnMetrics) != 1 || !turn.Timings().FirstContentOut.Equal(before) {
				t.Fatal("interrupted answer lost or changed its output evidence")
			}
			metric := c.find(func(v any) bool { _, ok := v.(live.TurnMetricsEvent); return ok }).(live.TurnMetricsEvent)
			if !metric.Truncated || metric.TurnID != turn.ID || !e.skipOpenerUntil.IsZero() {
				t.Fatal("normal interruption accounting or next-opener eligibility changed")
			}
		})
	}
}

func TestRetiredOpenerRejectsLateSegmentsWithoutBlockingNextGeneration(t *testing.T) {
	e, _, _, response := newAckHandoffTest(t)
	p, turn := e.player, response.turn
	p.cfg.Paced = false
	seg := Segment{TurnID: turn.ID, owner: turn, Gen: turn.Generation, Revision: turn.Revision,
		Text: "Yes,", PCM: make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(40))}
	p.beginSegment(&e.gen, seg)
	p.playAudio(&e.gen, seg)
	body := seg
	body.content, body.Text = true, "Unplayed answer."
	p.Enqueue(body)
	if p.FinishOpenerAsBackchannel(turn.ID, turn.Generation+1) || len(p.queue) != 1 {
		t.Fatal("a different generation changed the active owner")
	}
	if !p.FinishOpenerAsBackchannel(turn.ID, turn.Generation) {
		t.Fatal("unanswered opener was not retired")
	}
	for _, late := range []Segment{seg, body} {
		if p.Enqueue(late) || p.allowed(late, &e.gen) {
			t.Fatal("late producer or dequeued segment survived opener retirement")
		}
	}
	next := e.tracker.Revise(turn.ID, "The full request.", true)
	e.commitTurn(next)
	body.owner, body.Gen, body.Revision = next, next.Generation, next.Revision
	if !p.Enqueue(body) || !p.allowed(body, &e.gen) {
		t.Fatal("retirement incorrectly blocked a later revision")
	}
}
