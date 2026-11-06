package duplex

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/backchannel"
	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/focus"
	"github.com/gorilla/websocket"
)

func detectorFixture(t *testing.T) *Engine {
	t.Helper()
	up := websocket.Upgrader{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		var h any
		if c.ReadJSON(&h) != nil {
			return
		}
		c.WriteJSON(map[string]any{"type": "ready", "mode": "shadow", "fallback": "legacy"})
		for {
			var p struct{ Seq int }
			if c.ReadJSON(&p) != nil {
				return
			}
			if c.WriteJSON(map[string]any{"type": "prediction", "seq": p.Seq, "fallback": "legacy", "frames": []any{map[string]float64{"p_floor": .9}, map[string]float64{"p_floor": .9}}}) != nil {
				return
			}
		}
	}))
	t.Cleanup(s.Close)
	cfg, err := config.Load("../../configs/telephony.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Duplex.BackchannelDetectorMode = "asr_assist"
	e := New(Options{Cfg: cfg, ClientRate: 16000}, Deps{Emit: func(any) {}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.detector = backchannel.New(context.Background(), "ws"+strings.TrimPrefix(s.URL, "http"), e.log)
	t.Cleanup(e.detector.Close)
	waitFor(t, "detector ready", time.Second, e.detector.Ready)
	return e
}

func assistFixture(t *testing.T) *Engine {
	t.Helper()
	e := detectorFixture(t)
	for i := 0; i < 3; i++ {
		e.detector.Push(make([]byte, 1280), int64(280+i*40), e.gen.Current(), true, false)
		want := int64(320 + i*40)
		waitFor(t, "prediction", time.Second, func() bool { return e.detector.Snapshot() != nil && e.detector.Snapshot().EndMS == want })
	}
	e.hold = &floorHold{bargeIn: true}
	e.userOpen = true
	e.inputMS.Store(400)
	tr := focus.NewTracker(focus.Config{})
	tr.Pin(0)
	tr.Observe(focus.Frame{StartMS: 0, EndMS: 400, P: []float32{1, 0}})
	e.focus = &focusState{tracker: tr}
	return e
}
func TestDetectorAssistPreservesLegacyGuards(t *testing.T) {
	for _, kind := range []string{"caller", "ack", "ack_prefix", "hesitation", "background", "unknown", "stale", "generation", "kept", "already_assisted", "shadow", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			e := assistFixture(t)
			r := &asrRun{}
			switch kind {
			case "ack":
				r.lastText = "嗯"
			case "ack_prefix":
				r.lastText = "你接"
			case "hesitation":
				r.lastText = "呃"
			case "background":
				tr := focus.NewTracker(focus.Config{})
				tr.Pin(0)
				tr.Observe(focus.Frame{StartMS: 0, EndMS: 400, P: []float32{0, 1}})
				e.focus.tracker = tr
			case "unknown":
				e.focus = nil
			case "stale":
				e.inputMS.Store(600)
			case "generation":
				e.gen.Bump()
			case "kept":
				e.hold.kept = true
			case "already_assisted":
				e.detectorAssisted = true
				e.detectorAssistedStart = e.utterStartMS
			case "shadow":
				e.opts.Cfg.Duplex.BackchannelDetectorMode = "shadow"
			case "unavailable":
				e.detector.Disable("test")
			}
			if got := e.detectorMayAssist(r); got != (kind == "caller") {
				t.Fatalf("%s assist=%v", kind, got)
			}
			if e.hold == nil {
				t.Fatal("model changed floor ownership")
			}
		})
	}
}

// Same engine/ASR code and positive fake model in both arms: compare only the
// opt-in scheduling policy. No generated answer or handset latency is implied.
func TestBackchannelASRFinalSchedulingSimulation(t *testing.T) {
	for _, tc := range []struct {
		mode, text string
		want       int
	}{
		{"shadow", "", 120}, {"asr_assist", "", 40}, {"asr_assist", "嗯", 120},
	} {
		t.Run(tc.mode+tc.text, func(t *testing.T) {
			e := assistFixture(t)
			e.opts.Cfg.Duplex.BackchannelDetectorMode = tc.mode
			e.ctx, e.cancel = context.WithCancel(context.Background())
			defer e.cancel()
			e.deps.ASR = newTrailingASR("等一下")
			e.opts.Speculative = true
			r := e.startRun(0, "")
			r.lastText = tc.text
			t.Cleanup(func() {
				e.closeASR(true)
				if e.earlyRun != nil {
					e.earlyRun.stream.Close()
				}
			})
			samples, _ := audio.DecodePCM16(voiced(16000, 400, .45))
			for i := 0; i < len(samples); i += 320 {
				e.vad.Push(samples[i : i+320])
			}
			requested := 0
			for pause := 20; pause <= 160; pause += 20 {
				e.vad.Push(make([]float32, 320))
				e.inputMS.Store(int64(400 + pause))
				e.maybeEarlyFinal()
				if r.sendDone {
					requested = pause
					break
				}
			}
			if requested != tc.want {
				t.Fatalf("requested final at %d ms, want %d", requested, tc.want)
			}
			if e.hold == nil || e.earlyRun != r || e.asrRun == r || !e.asrRun.continuation {
				t.Fatal("floor or continuation semantics changed")
			}
			t.Logf("pause_to_final_request_ms=%d mode=%s", requested, tc.mode)
		})
	}
}

// Active Call's pre-answer mute must not disable a fresh model stream. Once
// audio has been consumed, a mute still invalidates history for this session.
func TestDetectorPreAnswerMuteAndLaterGap(t *testing.T) {
	e := detectorFixture(t)
	e.ctx = context.Background()
	for i := 0; i < 2; i++ {
		e.SetMuted(true, "pre-answer")
		e.runPosted()
		e.onAudio(make([]byte, 1280))
		if e.inputMS.Load() != 0 || !e.detector.Ready() || e.detector.Snapshot() != nil {
			t.Fatal("pre-answer mute consumed audio or disabled detector")
		}
	}
	e.SetMuted(false, "answered")
	e.runPosted()
	e.onAudio(make([]byte, 1280))
	waitFor(t, "first post-answer prediction", time.Second, func() bool {
		p := e.detector.Snapshot()
		return p != nil && p.EndMS == 40
	})
	e.SetMuted(true, "mid-call")
	e.runPosted()
	if e.detector.Ready() || e.detector.Snapshot() != nil {
		t.Fatal("mid-call mute retained potentially stale evidence")
	}
	e.SetMuted(false, "resumed")
	e.runPosted()
	e.onAudio(make([]byte, 1280))
	if e.detector.Ready() {
		t.Fatal("mid-call audio gap silently re-enabled detector")
	}
}
