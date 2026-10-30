package duplex

import (
	"context"
	"testing"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/tools"
)

func TestOriginCompletionProofRequiresANaturalEnd(t *testing.T) {
	for _, ending := range []string{"natural", "truncated", "truncated_then_natural", "natural_then_truncated", "zero_audio"} {
		t.Run(ending, func(t *testing.T) {
			e, _, task, p := readyQuoteHandoff(t)
			p.turn.MarkFirstAudioOut()
			done := func(ms int64) {
				e.onTurnDone(p.turn.ID, ms, p.turn.Opened)
				e.runPosted()
			}
			truncate := func() {
				e.onTruncated(TruncationReport{TurnID: p.turn.ID, PlayedMS: 40, TotalMS: 40, SpokenText: p.turn.Opened})
				e.runPosted()
			}
			switch ending {
			case "natural":
				done(40)
			case "truncated":
				truncate()
			case "truncated_then_natural":
				truncate()
				done(40)
			case "natural_then_truncated":
				done(40)
				truncate()
			case "zero_audio":
				done(0)
			}
			want := ending == "natural"
			if p.turn.Timings().CompletedNaturally != want || e.completedOriginOpener(e.tools.origins[task.ID]) != want {
				t.Fatal("origin completion proof accepted a truncated or empty turn")
			}
		})
	}
}

func TestSoftlyTruncatedOriginCannotAuthorizePosthookOmission(t *testing.T) {
	for _, path := range []string{"direct", "ack_link", "late_origin_direct", "late_origin_ack_link"} {
		t.Run(path, func(t *testing.T) {
			e, _, task, p := readyQuoteHandoff(t)
			qs, err := tools.ParseQuoteSpeech([]byte(tools.DefaultQuoteSpeech))
			if err != nil {
				t.Fatal(err)
			}
			task.Tool = task.Tool.WithSpeech(qs)
			task.Result.Output = []byte(`{"status":"quote","total":100,"lines":[]}`)
			origin := e.tools.origins[task.ID]
			lateOrigin := path == "late_origin_direct" || path == "late_origin_ack_link"
			if lateOrigin {
				delete(e.tools.origins, task.ID)
			}
			pcm := make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(40))
			pcm[0], pcm[1] = 0xff, 0x7f
			opener := Segment{TurnID: p.turn.ID, Gen: p.turn.Generation, Text: p.turn.Opened, PCM: pcm}
			e.player.beginSegment(opener)
			e.player.playAudio(&e.gen, opener)
			e.player.markSegment(opener)
			pipe := &speechPipe{e: e, turn: p.turn, ctx: context.Background()}
			for _, text := range []string{"Calling.", "More words."} {
				if !pipe.speakCached(text, PhraseAudio{PCM: pcm, Rate: e.opts.ClientRate}) {
					t.Fatal("could not queue caller-turn content")
				}
			}
			e.player.Enqueue(Segment{Kind: SegEnd, TurnID: p.turn.ID, Gen: p.turn.Generation})
			e.player.OnTruncate(func(r TruncationReport) {
				e.onTruncated(r)
				e.player.Close()
			})
			e.player.OnTurnDone(func(string, int64, string) {
				e.player.Close()
				t.Error("softly interrupted caller turn completed naturally")
			})
			e.softInterrupt()
			e.player.Run(&e.gen)
			e.runPosted()
			if !e.tracker.IsCurrent(p.turn) || p.turn.Timings().CompletedAt.IsZero() || p.turn.Timings().FirstContentOut.IsZero() {
				t.Fatal("test did not reproduce a completed soft-truncated caller turn with the same generation")
			}
			if lateOrigin {
				// Submission may capture the origin after playback completion.
				// A proof stored only in the task map would miss that truncation.
				e.tools.origins[task.ID] = origin
			}
			if path == "direct" || path == "late_origin_direct" {
				delete(e.tools.ackPending, task.ID)
				e.onTaskDone(task)
				if e.tracker.Current().Opening != tools.DefaultQuotePosthook {
					t.Fatal("soft-truncated origin authorized direct posthook omission")
				}
			} else {
				Phrases().Put(e.phraseKey(p.scripted), PhraseAudio{PCM: pcm, Rate: e.opts.ClientRate})
				e.sayAck(p)
				awaitHandoffSignal(t, e.speech.Load().done)
				if e.tools.origins[task.ID].ackTurnID != "" {
					t.Fatal("soft-truncated origin authorized an acknowledgement link")
				}
			}
		})
	}
}
