package duplex

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
	"github.com/chuanmingliu/golive/internal/tools"
)

type recoveryDialogue struct {
	mu       sync.Mutex
	quoted   bool
	requests []provider.LLMRequest
}

func (*recoveryDialogue) Name() string { return "quotation-recovery-fixture" }
func (l *recoveryDialogue) Stream(ctx context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, req)
	out := make(chan provider.LLMDelta, 1)
	if !l.quoted {
		l.quoted = true
		out <- provider.LLMDelta{ToolCall: &provider.ToolCall{ID: "initial-quote", Name: "quote_total", Arguments: `{}`}}
	} else {
		text := "您可以考虑一下。"
		for _, m := range req.Messages {
			if strings.HasPrefix(m.Content, "本轮是报价中的概念澄清") {
				text = "这是合同约定的医保外医疗费用保障。"
				break
			}
			if m.Role == provider.RoleSystem && strings.HasPrefix(m.Content, "【报价讲解进度】") {
				raw, _, _ := strings.Cut(strings.TrimPrefix(m.Content, "【报价讲解进度】"), "\n")
				var progress struct {
					Status   string         `json:"status"`
					Sections []quoteSection `json:"sections"`
				}
				_ = json.Unmarshal([]byte(raw), &progress)
				if progress.Status == "interrupted" || progress.Status == "pending" {
					text = "先解释您的问题。接着刚才的报价，"
					for _, s := range progress.Sections {
						if s.Selected && !s.Complete {
							text += s.Text
						}
					}
				}
			}
		}
		out <- provider.LLMDelta{Text: text}
	}
	close(out)
	return out, nil
}

type recoveryQuoteExecutor struct{ calls atomic.Int32 }

func (q *recoveryQuoteExecutor) Execute(_ context.Context, _ tools.Tool, _ tools.Request) tools.Result {
	q.calls.Add(1)
	return tools.Result{Output: json.RawMessage(`{"status":"quote","total":4543,"selection":{"三者险":"200万"},"lines":[{"coverage":"三者险","choice":"200万","premium":1378}]}`)}
}

// Runs the actual engine/tool/speech/player path with paced synthetic audio.
// Repeated interruptions must advance only fully played sections.
func TestQuoteRecoveryPacedCallSimulation(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "baseline"
		if enabled {
			name = "recovery"
		}
		t.Run(name, func(t *testing.T) {
			opts := quoteConfig()
			opts.Cfg.Duplex.QuoteInterruptionRecovery = enabled
			opts.Cfg.Duplex.Opener = false
			opts.Cfg.Duplex.PhraseCache = false
			opts.Cfg.Duplex.PlaybackPaced = true
			opts.Cfg.Duplex.PlaybackLeadMS = 40
			if enabled {
				opts.Cfg.SkillsMCP = skillFixture(t, func(_ string, result any) any {
					raw, _ := json.Marshal(result)
					raw = []byte(strings.ReplaceAll(string(raw), "skill://test-skill/", "skill://vehicle_insurance_concepts/"))
					var rewritten any
					_ = json.Unmarshal(raw, &rewritten)
					return rewritten
				})
			}
			llm := &recoveryDialogue{}
			exec := &recoveryQuoteExecutor{}
			c := newCollector(opts.Cfg.ClientRate)
			tts := mock.NewTTS()
			tts.MSPerRune = 35
			e := startQuoteEngine(t, opts, Deps{ASR: mock.NewASR("合成输入"), LLM: llm, TTS: tts, Emit: c.emit}, exec)
			speech, err := tools.ParseQuoteSpeech([]byte(`{"posthook":"报价好了。","posthook_status":"quote","template":"` + recoveryReadout + `"}`))
			if err != nil {
				t.Fatal(err)
			}
			e.post(func() { d := e.tools.byName["quote_total"].WithSpeech(speech); e.tools.byName[d.Name] = d })
			probeBarrier(e)
			submit := func(text string) {
				e.SubmitItem(live.ResponseItem{Type: "message", Role: "user", Content: live.TextContent("input_text", text)}, "")
				e.ContinueResponse("")
			}
			submit("请比较三者险报价。")
			waitFor(t, "first option and partial second option", 8*time.Second, func() bool {
				_, heard := e.player.Heard()
				return strings.Contains(heard, "3973元") && strings.Contains(heard, "三者险100万") && !strings.Contains(heard, "4258元")
			})
			cut := time.Now()
			e.post(func() {
				e.interrupt()
				if enabled {
					prompt := e.quoteRecoveryPrompt(nil)
					if !strings.Contains(prompt, `"status":"interrupted"`) || !e.tools.quoteDelivery.sections[0].Complete {
						t.Error("immediate follow-up observed stale playback progress")
					}
				}
			})
			probeBarrier(e)
			time.Sleep(120 * time.Millisecond)
			if late := audioAfter(c, cut.Add(100*time.Millisecond)); late != 0 {
				t.Fatalf("cancelled output leaked: %d", late)
			}
			cutoffMS := lastAudioDelay(c, cut)
			if !enabled {
				t.Logf("baseline cutoff_ms=%.2f", lastAudioDelay(c, cut))
				return
			}
			e.post(func() {
				d := e.tools.quoteDelivery
				if d == nil || d.status != "interrupted" || !d.sections[0].Complete || d.sections[1].Complete {
					t.Errorf("first playback progress incorrect: %+v", d)
				}
			})
			probeBarrier(e)
			began := time.Now()
			submit("医保外用药是什么意思？解释后继续报价。")
			waitFor(t, "second option and partial third option", 8*time.Second, func() bool {
				_, heard := e.player.Heard()
				return strings.Contains(heard, "4258元") && strings.Contains(heard, "三者险300万") && !strings.Contains(heard, "4780元")
			})
			e.Interrupt()
			probeBarrier(e)
			time.Sleep(120 * time.Millisecond)
			e.post(func() {
				d := e.tools.quoteDelivery
				if !d.sections[1].Complete || d.sections[2].Complete {
					t.Error("second interruption skipped or replayed completed section")
				}
			})
			probeBarrier(e)
			submit("请接着说完剩下的。")
			waitFor(t, "quotation completion", 8*time.Second, func() bool {
				done := make(chan bool, 1)
				e.post(func() { done <- e.tools.quoteDelivery.status == "complete" })
				return <-done
			})
			if exec.calls.Load() != 1 {
				t.Fatal("recovery recalculated an unchanged quotation")
			}
			llm.mu.Lock()
			defer llm.mu.Unlock()
			for _, req := range llm.requests {
				for _, m := range req.Messages {
					if strings.Contains(m.Content, "系统已替你完整念出") {
						t.Error("premature completion claim")
					}
				}
			}
			t.Logf("recovery cutoff_ms=%.2f recovery_sequence_ms=%d backend_calculations=%d", cutoffMS, time.Since(began).Milliseconds(), exec.calls.Load())
		})
	}
}
