package duplex

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
)

// A barge-in is decided some time after it begins, and the answer plays on
// while it is being decided. What the caller heard is what had played when
// they started talking, not when the engine made up its mind.

// The player, on its own: a cut with a lead reports the earlier position and
// the shorter text, and says beside it where the cut itself fell.
func TestInterruptBeforeReportsWhatHadPlayedAtTheOnset(t *testing.T) {
	var mu sync.Mutex
	var reports []TruncationReport
	p := NewPlayer(PlayerConfig{Rate: 24000, ChunkMS: 40, Paced: true, LeadMS: 40},
		func(string, []byte, time.Time) {})
	p.OnTruncate(func(r TruncationReport) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	})
	var gen Generation
	go p.Run(&gen)
	defer p.Close()

	twoSeconds := make([]byte, 2*2*24000)
	p.Enqueue(Segment{Kind: SegBegin, Gen: gen.Current(), TurnID: "item_1", Text: "这款车险包含交强险和商业险两部分"})
	p.Enqueue(Segment{Kind: SegAudio, Gen: gen.Current(), TurnID: "item_1", PCM: twoSeconds})
	p.Enqueue(Segment{Kind: SegMark, Gen: gen.Current(), TurnID: "item_1"})
	waitFor(t, "the answer to start", 2*time.Second, func() bool { id, _ := p.Heard(); return id == "item_1" })
	// Paced: a second from now, a second of it has been heard.
	time.Sleep(time.Second)

	p.InterruptBefore(600 * time.Millisecond)

	mu.Lock()
	if len(reports) != 1 {
		mu.Unlock()
		t.Fatalf("%d truncation reports, want 1", len(reports))
	}
	r := reports[0]
	if r.CutMS <= 0 {
		t.Fatalf("cut at %dms: nothing had played", r.CutMS)
	}
	if lead := r.CutMS - r.PlayedMS; lead < 590 || lead > 610 {
		t.Errorf("played %d of a cut at %d: the lead should be 600ms", r.PlayedMS, r.CutMS)
	}
	if r.SpokenText == "" && r.PlayedMS > 300 {
		t.Error("the caller heard some of the answer and the report says none")
	}
	// The text reported is for the earlier position, so it is a prefix of —
	// and shorter than — what had played at the cut.
	atCut := partialText("这款车险包含交强险和商业险两部分", float64(r.CutMS), float64(r.TotalMS))
	if !strings.HasPrefix(atCut, r.SpokenText) || len(r.SpokenText) >= len(atCut) {
		t.Errorf("heard %q is not a shorter prefix of what had played at the cut, %q", r.SpokenText, atCut)
	}

	// A lead longer than the turn means none of it was heard: clamped, and
	// still reported, since audio did go out.
	p.Enqueue(Segment{Kind: SegBegin, Gen: gen.Current(), TurnID: "item_2", Text: "好的"})
	p.Enqueue(Segment{Kind: SegAudio, Gen: gen.Current(), TurnID: "item_2", PCM: twoSeconds[:2*4800]})
	mu.Unlock()
	waitFor(t, "the second answer to start", 2*time.Second, func() bool { id, _ := p.Heard(); return id == "item_2" })
	p.InterruptBefore(time.Minute)
	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 2 {
		t.Fatalf("%d truncation reports, want 2", len(reports))
	}
	if r := reports[1]; r.PlayedMS != 0 || r.SpokenText != "" {
		t.Errorf("an onset before the turn began should report nothing heard, got %dms %q", r.PlayedMS, r.SpokenText)
	}
}

// The engine: a barge-in on a real answer is recorded as heard up to the
// detector's minimum speech before the cut — the time it took to call the
// sound an interruption — and not at all before that if the option is off.
func TestBargeInIsRecordedAtTheCallersOnset(t *testing.T) {
	for _, atOnset := range []bool{true, false} {
		t.Run(map[bool]string{true: "at onset", false: "at the cut"}[atOnset], func(t *testing.T) {
			cfg := testConfig()
			cfg.Duplex.TruncateAtOnset = atOnset
			cfg.Duplex.UserBackchannelHoldMS = 0
			c := newCollector(cfg.ClientRate)
			e, _ := newTestEngine(t, cfg, c)

			speak(e, cfg.ClientRate, 1200)
			pause(e, cfg.ClientRate, 700)
			waitFor(t, "assistant audio", 5*time.Second, func() bool { return c.audioMS() > 600 })

			speak(e, cfg.ClientRate, 1000)
			waitFor(t, "a truncation report", 4*time.Second, func() bool {
				return c.count(live.ExtAudioTruncated) > 0
			})
			ev := c.find(func(a any) bool {
				_, ok := a.(live.AudioTruncatedEvent)
				return ok
			}).(live.AudioTruncatedEvent)

			lead := ev.CutMS - ev.PlayedMS
			minSpeech := int64(cfg.VAD.BargeInMinSpeechMS)
			if !atOnset {
				if lead != 0 {
					t.Fatalf("truncate_at_onset off, yet played %d against a cut at %d", ev.PlayedMS, ev.CutMS)
				}
				return
			}
			// The detector holds barge_in_min_speech_ms of voice before it
			// reports the utterance; the lead is at least that, and at most
			// that plus a tick or two of the engine's loop.
			if lead < minSpeech || lead > minSpeech+400 {
				t.Errorf("played %d of a cut at %d: lead %dms, want about barge_in_min_speech_ms (%d)", ev.PlayedMS, ev.CutMS, lead, minSpeech)
			}
			if ev.PlayedMS < 0 || ev.PlayedMS > ev.TotalMS {
				t.Errorf("played %d of %d emitted", ev.PlayedMS, ev.TotalMS)
			}
		})
	}
}
