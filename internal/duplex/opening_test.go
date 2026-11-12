package duplex

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

const protectedTestOpening = "您好，这是固定开场。我们会先完整说明这次来电的目的，再根据您的回答继续交流，请稍等这段开场说完。"

func openingEngine(t *testing.T, protected bool, tts provider.TTS) (*Engine, *collector, *recordingLLM) {
	t.Helper()
	cfg := testConfig()
	cfg.Duplex.ProtectOpening = protected
	cfg.Duplex.PhraseCache = false
	cfg.Duplex.Opener = false
	cfg.Duplex.Speculative = true
	asr := mock.NewASR("先确认续保时间")
	asr.MSPerRune = 40
	llm := &recordingLLM{inner: mock.NewLLM()}
	c := newCollector(cfg.ClientRate)
	e := New(Options{Cfg: cfg, SessionID: "opening-test", ClientRate: cfg.ClientRate,
		Delegation: live.DelegationResponses, Speculative: true, Greeting: protectedTestOpening},
		Deps{ASR: asr, LLM: llm, TTS: tts, Emit: c.emit})
	e.Start(context.Background())
	t.Cleanup(e.Close)
	return e, c, llm
}

func TestProtectedOpeningRetainsCallerUntilPlaybackCompletes(t *testing.T) {
	for _, greet := range []bool{false, true} {
		t.Run(map[bool]string{false: "answered-call-opening", true: "greeting"}[greet], func(t *testing.T) {
			e, c, llm := openingEngine(t, true, mock.NewTTS())
			if greet {
				e.Greet()
			} else {
				e.SayOpening(protectedTestOpening)
			}
			waitFor(t, "opening audio", 3*time.Second, func() bool { return c.audioMS() > 150 })
			e.Interrupt() // client-originated barge-in has the same protection
			speak(e, e.opts.ClientRate, 600)
			pause(e, e.opts.ClientRate, 650)
			if len(llm.requests()) != 0 || c.count(live.ExtAudioTruncated) != 0 {
				t.Fatal("caller replaced the unfinished opening")
			}
			waitFor(t, "deferred caller response", 10*time.Second, func() bool { return len(llm.requests()) > 0 })
			requests := llm.requests()
			timing := make(chan Timings, 1)
			e.post(func() { timing <- e.tracker.Current().Timings() })
			observed := <-timing
			if !observed.TranscriptFinal.Before(observed.StartedAt) || !observed.SpeechEndAt.Before(observed.StartedAt) {
				t.Fatal("opening wait was misattributed to ASR or caller speech")
			}
			var heardOpening, heardCaller bool
			for _, message := range requests[0].Messages {
				heardOpening = heardOpening || message.Role == provider.RoleAssistant && message.Content == protectedTestOpening
				heardCaller = heardCaller || message.Role == provider.RoleUser && strings.Contains(message.Content, "先确认续保时间")
			}
			if !heardOpening || !heardCaller {
				t.Fatalf("deferred request lost opening or caller: opening=%t caller=%t", heardOpening, heardCaller)
			}
			if c.count(live.ExtAudioTruncated) != 0 {
				t.Fatal("opening was truncated")
			}
			waitFor(t, "subsequent answer audio", 5*time.Second, func() bool { return c.outputTranscript("item_2") != "" })
			e.Interrupt()
			waitFor(t, "normal answer interruption", 3*time.Second, func() bool { return c.count(live.ExtAudioTruncated) > 0 })
		})
	}
}

func TestProtectedOpeningCombinesRetainedUtterances(t *testing.T) {
	e, _, llm := openingEngine(t, true, mock.NewTTS())
	e.SayOpening(protectedTestOpening)
	e.post(func() {
		e.onFinalTranscript(nil, "第一件事")
		e.onFinalTranscript(nil, "第二件事")
	})
	waitFor(t, "combined deferred response", 10*time.Second, func() bool { return len(llm.requests()) > 0 })
	var user string
	for _, message := range llm.requests()[0].Messages {
		if message.Role == provider.RoleUser {
			user = message.Content
		}
	}
	if user != "第一件事 第二件事" {
		t.Fatal("lost deferred caller words", user)
	}
}

func TestOpeningProtectionCanBeDisabled(t *testing.T) {
	e, c, _ := openingEngine(t, false, mock.NewTTS())
	e.SayOpening(protectedTestOpening)
	waitFor(t, "opening audio", 3*time.Second, func() bool { return c.audioMS() > 150 })
	e.Interrupt()
	waitFor(t, "original interruption behavior", 3*time.Second, func() bool { return c.count(live.ExtAudioTruncated) > 0 })
}

type failedOpeningTTS struct{ provider.TTS }
type failedOpeningStream struct{ provider.TTSStream }

func (f failedOpeningTTS) Open(ctx context.Context, options provider.TTSOptions) (provider.TTSStream, error) {
	stream, err := f.TTS.Open(ctx, options)
	return failedOpeningStream{stream}, err
}

func (f failedOpeningStream) Synthesize(ctx context.Context, text string) (<-chan provider.TTSChunk, error) {
	if strings.Contains(text, "固定开场") {
		return nil, errors.New("test opening synthesis failure")
	}
	return f.TTSStream.Synthesize(ctx, text)
}

func TestFailedOpeningReleasesDeferredInput(t *testing.T) {
	e, _, llm := openingEngine(t, true, failedOpeningTTS{mock.NewTTS()})
	e.SayOpening("这是固定开场。")
	e.post(func() {
		e.typed = append(e.typed, "开场失败后继续回答")
		e.startTypedTurn()
	})
	waitFor(t, "response after failed opening", 5*time.Second, func() bool { return len(llm.requests()) > 0 })
}
