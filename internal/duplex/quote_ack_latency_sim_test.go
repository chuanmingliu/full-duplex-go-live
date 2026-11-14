package duplex

import (
	"github.com/chuanmingliu/golive/internal/audio"
	"os"
	"testing"
)

// Compare actual player output duration under the same cached PCM fixtures.
// This is a controlled playback simulation, not acoustic handset evidence.
func TestQuoteAckLatencySimulation(t *testing.T) {
	if os.Getenv("GOLIVE_TOOL_LATENCY_SIM") != "1" {
		t.Skip("opt-in latency comparison")
	}
	e, _, task, p := readyQuoteHandoff(t)
	p.turn.MarkFirstAudioOut()
	p.turn.markCompletedNaturally()
	p.scripted = "这会儿正在给您计算保费，请您稍等。"
	for text, ms := range map[string]int{p.scripted: 2380, "这会儿正在给您计算保费，": 1540, "请您稍等。": 840} {
		pcm := make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(ms))
		pcm[0], pcm[1] = 0xff, 0x7f
		Phrases().Put(e.phraseKey(text), PhraseAudio{PCM: pcm, Rate: e.opts.ClientRate})
	}
	e.sayAck(p)
	ack := e.tracker.Current()
	awaitHandoffSignal(t, e.speech.Load().done)
	e.runPosted()
	var duration int64
	e.player.OnTurnDone(func(r PlaybackReport) { duration = r.TotalMS; e.onTurnDone(r); e.player.Close() })
	e.player.Run(&e.gen)
	e.runPosted()
	if !ack.Timings().CompletedNaturally {
		t.Fatal("ack did not finish naturally")
	}
	e.onTaskDone(task)
	if e.tools.delivered != 1 || e.tracker.Current() == ack {
		t.Fatal("result handoff failed")
	}
	t.Logf("LATENCY_SIM ack_audio_ms=%d result_ready_after_ack_start_ms=610 modeled_remaining_wait_ms=%d", duration, max(duration-610, 0))
}
