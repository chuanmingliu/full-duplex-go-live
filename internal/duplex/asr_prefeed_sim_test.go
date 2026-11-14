package duplex

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/focus"
	"github.com/chuanmingliu/golive/internal/live"
	"github.com/chuanmingliu/golive/internal/provider"
)

// A controlled receiver: a decisive partial follows 80 ms of voiced input
// by 180 ms. This fixture models ASR delivery, not a live provider or handset.
type prefeedSimASR struct {
	text    string
	streams chan *prefeedSimStream
}

func (a *prefeedSimASR) Name() string { return "prefeed-simulation" }
func (a *prefeedSimASR) Open(context.Context, provider.ASROptions) (provider.ASRStream, error) {
	s := &prefeedSimStream{text: a.text, results: make(chan provider.ASRResult, 8)}
	a.streams <- s
	return s, nil
}

type prefeedSimStream struct {
	mu             sync.Mutex
	text           string
	pcm            []byte
	voiced         int
	queued, closed bool
	results        chan provider.ASRResult
}

func (s *prefeedSimStream) Write(pcm []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.pcm = append(s.pcm, pcm...)
	x, _ := audio.DecodePCM16(pcm)
	for off := 0; off+320 <= len(x); off += 320 {
		if audio.DBFS(audio.RMS(x[off:off+320])) > -40 {
			s.voiced += 20
		}
	}
	if s.voiced >= 80 && !s.queued {
		s.queued = true
		time.AfterFunc(180*time.Millisecond, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.closed {
				s.results <- provider.ASRResult{Text: s.text}
			}
		})
	}
	return nil
}
func (s *prefeedSimStream) Results() <-chan provider.ASRResult { return s.results }
func (s *prefeedSimStream) CloseSend() error                   { return nil }
func (s *prefeedSimStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.results)
	}
	return nil
}

// The same fixture runs against baseline and candidate sources with the same
// phone profile. Speech gates and speaker evidence are unchanged between runs.
func TestInterruptionASRDeliverySimulation(t *testing.T) {
	if os.Getenv("GOLIVE_INTERRUPT_SIM") != "1" {
		t.Skip("opt-in paced comparison")
	}
	runInterruptionDeliverySimulation(t, "", live.DelegationClient)
}

func runInterruptionDeliverySimulation(t *testing.T, talkerURL, delegation string) {
	for _, tc := range []struct {
		name, text string
		speaker    int
		echo, cut  bool
	}{
		{"caller", "请停一下", 0, false, true},
		{"background", "请停一下", 1, false, false},
		{"echo", "请停一下", 1, true, false},
		{"backchannel", "嗯", 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load("../../configs/telephony.json")
			if err != nil {
				t.Fatal(err)
			}
			cfg.Duplex.TalkerOpenerURL = talkerURL
			a := &prefeedSimASR{text: tc.text, streams: make(chan *prefeedSimStream, 10)}
			var logs openerLogBuffer
			c := newCollector(provider.PipelineRate)
			e := New(Options{Cfg: cfg, ClientRate: provider.PipelineRate, Delegation: delegation}, Deps{ASR: a, Emit: c.emit, Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
			ctx, cancel := context.WithCancel(context.Background())
			e.ctx = ctx
			defer func() { cancel(); e.closeASR(true); e.closeStandby(); e.Close() }()
			e.backendPending.Store(1) // Keep an answer in flight without a real provider.
			e.speaking.Store(true)
			e.vad.SetAssistantSpeaking(true)
			tracker := focus.NewTracker(focus.Config{})
			tracker.Pin(0)
			if tc.echo {
				tracker.Mask(-2000, 0)
				tracker.Observe(focus.Frame{StartMS: -2000, EndMS: 0, P: []float32{0, 1}})
				if tracker.Echo() != 1 {
					t.Fatal("echo fixture did not establish echo identity")
				}
			}
			e.focus = &focusState{tracker: tracker, scorer: newFakeScorer(func(int64) int { return tc.speaker }, 0)}
			defer e.focus.scorer.Close()
			pcm := voiced(provider.PipelineRate, 1000, .45)
			started := time.Now()
			cutMS := int64(-1)
			openMS := int64(-1)
			for off := 0; off < len(pcm); off += 640 {
				at := int64(off / 32)
				p := []float32{0, 0}
				p[tc.speaker] = 1
				tracker.Observe(focus.Frame{StartMS: at, EndMS: at + 20, P: p})
				e.onAudio(pcm[off : off+640])
				if openMS < 0 && e.userOpen {
					openMS = time.Since(started).Milliseconds()
				}
				until := time.Now().Add(20 * time.Millisecond)
				for time.Now().Before(until) {
					select {
					case ev := <-e.asrCh:
						e.onASREvent(ev)
					default:
						time.Sleep(time.Millisecond)
					}
					e.expireHold()
					if cutMS < 0 && strings.Contains(logs.String(), "cutting the answer") {
						cutMS = time.Since(started).Milliseconds()
					}
				}
			}
			if (cutMS >= 0) != tc.cut {
				t.Fatalf("cut=%d want cut=%v; %s", cutMS, tc.cut, logs.String())
			}
			delay := int64(-1)
			if cutMS >= 0 {
				delay = cutMS - openMS
			}
			t.Logf("SIM kind=%s vad_open_ms=%d onset_to_cut_ms=%d detection_to_cut_ms=%d", tc.name, openMS, cutMS, delay)
		})
	}
}
