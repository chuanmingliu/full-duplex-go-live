package duplex

import (
	"testing"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/tools"
)

func TestStandardQuoteAckDropsOnlyRedundantTailAfterOpener(t *testing.T) {
	const full = "这会儿正在给您计算保费，请您稍等。"
	const first = "这会儿正在给您计算保费，"
	for _, test := range []struct {
		name, text, want string
		opener           bool
	}{
		{"completed_opener", full, first, true},
		{"no_completed_opener", full, full, false},
		{"custom", "Please wait for the final terms.", "Please wait for the final terms.", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			e, task, ack := completedQuoteAck(t, func(e *Engine, task *tools.Task, p *pendingResponse) {
				p.scripted = test.text
				if !test.opener {
					p.turn.Opened = ""
				}
				pcm := make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(40))
				pcm[0], pcm[1] = 0xff, 0x7f
				for _, text := range []string{test.want, first, "请您稍等。", "Please wait for the final terms."} {
					Phrases().Put(e.phraseKey(text), PhraseAudio{PCM: pcm, Rate: e.opts.ClientRate})
				}
			})
			if ack.Spoken != test.want {
				t.Fatalf("spoken acknowledgment %q, want %q", ack.Spoken, test.want)
			}
			e.onTaskDone(task)
			if e.tools.delivered != 1 {
				t.Fatal("result did not follow the completed acknowledgment")
			}
			if test.opener && e.tracker.Current().Opening != "" {
				t.Fatal("redundant posthook returned")
			}
		})
	}
}
