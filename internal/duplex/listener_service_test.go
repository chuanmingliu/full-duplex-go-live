package duplex

import (
	"context"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
	"github.com/chuanmingliu/golive/internal/tools"
)

type listenerFixtureLLM struct {
	reply   string
	request provider.LLMRequest
}

func (l *listenerFixtureLLM) Name() string { return "listener-fixture" }
func (l *listenerFixtureLLM) Stream(_ context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	l.request = req
	out := make(chan provider.LLMDelta, 1)
	out <- provider.LLMDelta{Text: l.reply}
	close(out)
	return out, nil
}

func TestListenerServiceKeepsSynthesisAndPlaybackSeparate(t *testing.T) {
	for _, scenario := range []string{"play", "disabled", "revision", "caller finished", "main priority", "model silence"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := testConfig()
			cfg.Duplex.BackchannelAfterMS = 0
			log := newCollector(cfg.ClientRate)
			llm := &listenerFixtureLLM{reply: "嗯，我在听。"}
			if scenario == "model silence" {
				llm.reply = "SILENCE"
			}
			mainTTS, sideTTS := mock.NewTTS(), mock.NewTTS()
			e := New(Options{Cfg: cfg, ClientRate: cfg.ClientRate}, Deps{LLM: llm, TTS: mainTTS, ListenerTTS: sideTTS, Emit: log.emit})
			e.ctx, e.cancel = context.WithCancel(context.Background())
			defer e.cancel()
			e.listenerConfig = tools.BackchannelService{Enabled: scenario != "disabled", Prompt: "Only acknowledge.", Version: 3}
			e.userOpen = true
			e.userSince = time.Now().Add(-time.Second)
			e.asrRun = &asrRun{lastText: "孩子的教育费用"}
			e.maybeBackchannel()
			e.wg.Wait()
			if scenario == "disabled" {
				if llm.request.Model != "" || len(llm.request.Messages) != 0 || sideTTS.Opens() != 0 {
					t.Fatal("disabled service started")
				}
				return
			}
			e.runPosted()
			if got := llm.request.Messages; len(got) != 2 || got[0].Content != e.listenerConfig.Prompt || got[1].Content != e.asrRun.lastText {
				t.Fatalf("context: %+v", got)
			}
			if scenario == "model silence" {
				if e.listener != nil || sideTTS.Opens() != 0 {
					t.Fatal("silence synthesized")
				}
				return
			}
			defer e.stopListener("test_end")
			if mainTTS.Opens() != 0 || sideTTS.Opens() != 1 || !e.listener.ready || len(log.audio) != 0 {
				t.Fatal("candidate must be buffered on its own connection before release")
			}
			switch scenario {
			case "revision":
				e.asrRun.lastText = "不是孩子的教育费用"
			case "caller finished":
				e.userOpen = false
			case "main priority":
				e.player.Enqueue(Segment{Kind: SegBegin, TurnID: "answer", Gen: e.gen.Current()})
			case "play":
				// A speculative main request and a punctuation-only ASR edit
				// still leave the caller holding the floor.
				e.gen.Bump()
				e.backendPending.Add(1)
				e.asrRun.lastText = "孩子的，教育费用还要再考虑"
			}
			e.listener.released = true
			e.playListenerFrame()
			if scenario != "play" {
				if len(log.audio) != 0 || e.listener != nil {
					t.Fatal("obsolete or lower-priority audio leaked")
				}
				return
			}
			if len(log.audio) == 0 || e.player.PendingMS() != 0 || e.speaking.Load() {
				t.Fatal("side track failed or took main floor")
			}
			e.userOpen = false
			e.playListenerFrame()
			if log.find(func(v any) bool {
				ev, ok := v.(live.BackchannelStageEvent)
				return ok && ev.Stage == "playback_end" && ev.ConfigVersion == 3
			}) == nil {
				t.Fatal("missing end receipt")
			}
		})
	}
}

// Drive the same input queue, VAD and timer loop as a live session. A ready
// candidate must stay silent through continuous voice and release in a pause
// that is too short to close the caller's utterance.
func TestListenerServiceReleasesInAnOpenUtterance(t *testing.T) {
	cfg := testConfig()
	cfg.Duplex.BackchannelAfterMS = 400
	cfg.Duplex.Speculative = false
	cfg.Duplex.SettledCloseMS = 0
	cfg.VAD.MinSilenceMS = 650
	rec := newCollector(cfg.ClientRate)
	asr := mock.NewASR("我在说一件事情")
	asr.MSPerRune = 50
	e := New(Options{Cfg: cfg, ClientRate: cfg.ClientRate, Delegation: live.DelegationClient}, Deps{
		ASR: asr, LLM: &listenerFixtureLLM{reply: "嗯。"}, TTS: mock.NewTTS(), Emit: rec.emit,
	})
	e.listenerConfig = tools.BackchannelService{Enabled: true, Prompt: "Acknowledge briefly."}
	e.Start(context.Background())
	t.Cleanup(e.Close)
	stage := func(name string) bool {
		return rec.find(func(x any) bool { v, ok := x.(live.BackchannelStageEvent); return ok && v.Stage == name }) != nil
	}
	speak(e, cfg.ClientRate, 900)
	if !stage("audio_ready") || stage("playback_start") {
		t.Fatalf("during continuous voice: ready=%v started=%v", stage("audio_ready"), stage("playback_start"))
	}
	pause(e, cfg.ClientRate, 160)
	speak(e, cfg.ClientRate, 160)
	if !stage("playback_start") || rec.count(live.ExtSpeechStopped) != 0 || rec.audioMS() == 0 {
		t.Fatal("ready acknowledgment did not play while caller retained floor")
	}
}
