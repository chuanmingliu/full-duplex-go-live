package duplex

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/focus"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

// These fixtures exercise scheduling with the original timing/gating profile.
// They do not include a real recognizer, SIP buffering or handset playback.
func TestPresetSchedulingSimulation(t *testing.T) {
	if os.Getenv("GOLIVE_PRESET_SCHED_SIM") != "1" {
		t.Skip("opt-in paired scheduling simulation")
	}
	type row struct {
		Kind           string `json:"kind"`
		OffsetMS       int    `json:"offset_ms,omitempty"`
		DelayMS        int64  `json:"delay_ms"`
		FirstAudioMS   int64  `json:"first_audio_ms,omitempty"`
		FirstContentMS int64  `json:"first_content_ms,omitempty"`
	}
	var rows []row
	count := 3
	if n, _ := strconv.Atoi(os.Getenv("SCHED_SIM_SAMPLES")); n > 0 {
		count = n
	}
	for i := 0; i < count; i++ {
		t.Run("short_reply_"+strconv.Itoa(i), func(t *testing.T) {
			Phrases().Reset()
			t.Cleanup(Phrases().Reset)
			cfg, err := config.Load("../../configs/telephony.json")
			if err != nil {
				t.Fatal(err)
			}
			cfg.Duplex.Focus = "" // No speaker-label delay in this ASR scheduling fixture.
			cfg.Duplex.TalkerOpenerURL = ""
			asr := newTrailingASR("贵吗")
			asr.msPerRune = 80
			asr.partialLag = 180 * time.Millisecond
			asr.finalDelay = 20 * time.Millisecond
			llm := mock.NewLLM()
			llm.FirstTokenDelay = 400 * time.Millisecond
			llm.DelayPerRune = 2 * time.Millisecond
			c := newCollector(cfg.ClientRate)
			var logs openerLogBuffer
			e := startEngine(t, Options{Cfg: cfg, Delegation: "responses", Speculative: true, ClientRate: cfg.ClientRate}, Deps{ASR: asr, LLM: llm, TTS: mock.NewTTS(), Emit: c.emit, Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
			for _, phrase := range append(append([]string{}, cfg.Duplex.OpenerPhrases...), cfg.Duplex.OpenerQuestionPhrases...) {
				seedTalkerAudio(e, phrase, 300)
			}
			speak(e, cfg.ClientRate, 220)
			pause(e, cfg.ClientRate, 1500)
			waitFor(t, "ordinary answer content", time.Second, func() bool { return strings.Contains(c.allOutput(), "让我确认一下") })
			e.Close()
			for _, line := range strings.Split(logs.String(), "\n") {
				if !strings.Contains(line, "turn: response latency") || !strings.Contains(line, "response_to_speech=true") {
					continue
				}
				field := func(k string) int64 {
					m := regexp.MustCompile(`\b` + k + `=(-?\d+)`).FindStringSubmatch(line)
					if m == nil {
						t.Fatalf("missing %s", k)
					}
					n, _ := strconv.ParseInt(m[1], 10, 64)
					return n
				}
				if !strings.Contains(line, "first_content_present=true") {
					t.Fatal("content missing")
				}
				rows = append(rows, row{Kind: "short_reply", FirstAudioMS: field("end_silence_ms") + field("first_audio_out_ms"), FirstContentMS: field("end_silence_ms") + field("first_content_ms")})
				return
			}
			t.Fatal("ordinary response timing missing")
		})
		for _, kind := range []string{"caller", "background", "echo", "below_threshold", "acknowledgment"} {
			t.Run(kind+strconv.Itoa(i), func(t *testing.T) {
				offset := 10 + 30*(i%3)
				delay := simulateFocusEvidence(t, kind, offset)
				rows = append(rows, row{Kind: kind, OffsetMS: offset, DelayMS: delay})
			})
		}
	}
	b, _ := json.MarshalIndent(rows, "", "  ")
	if out := os.Getenv("SCHED_SIM_OUT"); out != "" {
		if err := os.WriteFile(out, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(b))
}

func simulateFocusEvidence(t *testing.T, kind string, offset int) int64 {
	t.Helper()
	cfg, err := config.Load("../../configs/telephony.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Duplex.TalkerOpenerURL = ""
	scorer := newFakeScorer(func(int64) int { return 0 }, 0)
	var logs openerLogBuffer
	e := New(Options{Cfg: cfg, ClientRate: provider.PipelineRate, Delegation: "client"}, Deps{Emit: func(any) {}, Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	e.focusDial = func(context.Context) (focus.Scorer, error) { return scorer, nil }
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)
	defer func() { e.Close(); cancel() }()
	armed := make(chan struct{})
	e.post(func() {
		e.focus.tracker.Pin(0)
		if kind == "echo" {
			e.focus.tracker.Mask(-2000, 0)
			e.focus.tracker.Observe(focus.Frame{StartMS: -2000, EndMS: 0, P: []float32{0, 1}})
		}
		e.focus.tracker.Observe(focus.Frame{StartMS: 0, EndMS: 820, P: []float32{0, 1}})
		e.inputMS.Store(1220)
		e.utterStartMS = 0
		e.userOpen = true
		e.uttVerdict = uttIgnored
		e.backendPending.Store(1)
		e.speaking.Store(true)
		e.hold = &floorHold{kept: true, words: true, bargeIn: true, started: time.Now(), until: time.Now().Add(time.Second)}
		if kind == "acknowledgment" {
			e.uttVerdict = uttCaller
			e.hold = &floorHold{bargeIn: true, started: time.Now(), until: time.Now().Add(time.Second)}
		}
		close(armed)
	})
	<-armed
	time.Sleep(time.Duration(offset) * time.Millisecond)
	duration := int64(400)
	speaker := 0
	if kind == "below_threshold" {
		duration = 399
	}
	if kind == "background" || kind == "echo" {
		speaker = 1
	}
	p := []float32{0, 0}
	p[speaker] = 1
	at := time.Now()
	scorer.frames <- focus.Frame{StartMS: 820, EndMS: 820 + duration, P: p}
	delay := int64(-1)
	until := at.Add(180 * time.Millisecond)
	for time.Now().Before(until) {
		if strings.Contains(logs.String(), "cutting the answer") {
			delay = time.Since(at).Milliseconds()
			break
		}
		time.Sleep(time.Millisecond)
	}
	if (delay >= 0) != (kind == "caller") {
		t.Fatalf("%s cut=%d", kind, delay)
	}
	return delay
}

func TestFocusEvidenceWakesCallerAndPreservesRejection(t *testing.T) {
	for _, kind := range []string{"caller", "background", "echo", "below_threshold", "acknowledgment"} {
		t.Run(kind, func(t *testing.T) {
			delay := simulateFocusEvidence(t, kind, 10)
			if kind == "caller" && delay >= 80 {
				t.Fatalf("decisive labels waited %d ms without incoming audio", delay)
			}
		})
	}
}
