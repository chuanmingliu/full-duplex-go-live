package duplex

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/mock"
)

func TestTalkerOpenerRejectsClaimsMalformedAndUnrelatedTopics(t *testing.T) {
	for _, tc := range []struct {
		phrase, caller string
		valid          bool
	}{
		{"车损险呢，", "车损险包含什么？", true},
		{"理赔嘛，", "理赔需要什么手续？", true},
		{"保费呢，", "车损险包含什么？", false},
		{"一定赔嘛，", "一定赔吗？", false},
		{"可以呢，", "可以给我优惠吗？", false},
		{"三百万呢，", "三百万多少钱？", false},
		{"免费的，", "有免费服务吗？", false},
		{"车损险呢，当然能赔。", "车损险能赔吗？", false},
		{"<think>车损险呢，", "车损险包含什么？", false},
		{"车损险呢", "车损险包含什么？", false},
	} {
		if got := validTalkerOpener(tc.phrase, tc.caller); got != tc.valid {
			t.Errorf("%q got %v", tc.phrase, got)
		}
	}
}

func TestTalkerOpenerHTTPBounds(t *testing.T) {
	for _, mode := range []string{"ok", "redirect", "large", "bad_json", "bad_status", "deadline", "unsafe"} {
		t.Run(mode, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/completion" || r.Method != "POST" {
					t.Error("wrong endpoint")
				}
				var req map[string]any
				if json.NewDecoder(r.Body).Decode(&req) != nil || req["grammar"] != talkerOpenerGrammar || req["n_predict"] != float64(20) {
					t.Error("missing constraints")
				}
				switch mode {
				case "redirect":
					http.Redirect(w, r, "/other", 302)
				case "large":
					io.WriteString(w, strings.Repeat("x", 16385))
				case "bad_json":
					io.WriteString(w, "{")
				case "bad_status":
					w.WriteHeader(503)
				case "deadline":
					<-r.Context().Done()
				case "unsafe":
					json.NewEncoder(w).Encode(map[string]string{"content": "保证赔嘛，"})
				default:
					json.NewEncoder(w).Encode(map[string]string{"content": "车损险呢，"})
				}
			}))
			defer s.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			phrase, err := requestTalkerOpener(ctx, s.URL, "车损险包含什么？")
			if mode == "ok" {
				if err != nil || phrase != "车损险呢，" {
					t.Fatal(phrase, err)
				}
			} else if err == nil {
				t.Fatal("unsafe response accepted")
			}
		})
	}
}

func talkerTestEngine(t *testing.T, url string) *Engine {
	t.Helper()
	cfg := testConfig()
	cfg.Duplex.Opener = true
	cfg.Duplex.TalkerOpenerURL = url
	cfg.Duplex.OpenerPhrases = []string{"好的，"}
	cfg.Duplex.OpenerQuestionPhrases = []string{"好的，"}
	cfg.Duplex.PhraseCache = true
	e := New(Options{Cfg: cfg, Delegation: "responses", ClientRate: cfg.ClientRate, Voice: "test", Language: "zh"}, Deps{TTS: mock.NewTTS(), Emit: func(any) {}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.ctx, e.cancel = context.WithCancel(context.Background())
	t.Cleanup(e.Close)
	return e
}

func seedTalkerAudio(e *Engine, phrase string, ms int) {
	pcm := make([]byte, audio.PCM16(e.opts.ClientRate).BytesForMS(ms))
	for i := 1; i < len(pcm); i += 2 {
		pcm[i] = 16
	}
	Phrases().Put(e.phraseKey(phrase), PhraseAudio{PCM: pcm, Rate: e.opts.ClientRate})
}

func TestTalkerOpenerReadyUsesGeneratedAudioAndLateFallsBack(t *testing.T) {
	for _, slow := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "late"}[slow], func(t *testing.T) {
			Phrases().Reset()
			t.Cleanup(Phrases().Reset)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if slow {
					io.Copy(io.Discard, r.Body)
					<-r.Context().Done()
					return
				}
				json.NewEncoder(w).Encode(map[string]string{"content": "车损险呢，"})
			}))
			defer s.Close()
			e := talkerTestEngine(t, s.URL)
			defer e.Close()
			seedTalkerAudio(e, "车损险呢，", 300)
			seedTalkerAudio(e, "好的，", 300)
			e.prepareTalkerOpener("车损险包含什么？")
			turn := e.tracker.Begin("车损险包含什么？", false)
			// A final-only turn has not called beginGeneration yet.
			if !slow {
				waitFor(t, "prepared opener", time.Second, func() bool { return len(e.talkerPending.ready) > 0 })
			}
			started := time.Now()
			e.sayOpener(turn)
			if time.Since(started) > 50*time.Millisecond {
				t.Fatal("opener blocked on inference")
			}
			want := "车损险呢，"
			if slow {
				want = "好的，"
			}
			if turn.Opened != want {
				t.Fatalf("opened %q want %q", turn.Opened, want)
			}
		})
	}
}

func TestTalkerOpenerCannotLeakAcrossRevisionUtteranceOrFragment(t *testing.T) {
	for _, mode := range []string{"revision", "utterance", "generation", "fragment", "repeat"} {
		t.Run(mode, func(t *testing.T) {
			e := talkerTestEngine(t, "http://unused.invalid")
			p := &talkerOpener{text: "车损险包含什么？", cancel: func() {}, ready: make(chan talkerOpenerResult, 1)}
			p.ready <- talkerOpenerResult{phrase: "车损险呢，", audio: PhraseAudio{PCM: []byte{0, 16}, Rate: 16000}, status: "ready"}
			turn := e.tracker.Begin(p.text, false)
			turn.talkerOpener = p
			switch mode {
			case "revision":
				turn.Transcript = "理赔需要什么？"
			case "utterance":
				e.utterStartMS++
			case "generation":
				e.gen.Bump()
			case "repeat":
				e.lastSaidOpener = "车损险呢，"
			}
			if _, _, ok := e.takeTalkerOpener(turn, mode == "fragment"); ok {
				t.Fatal("stale or invalid audio used")
			}
		})
	}
}

func TestTalkerOpenerBackgroundSynthesisIsPrivateAndBounded(t *testing.T) {
	Phrases().Reset()
	t.Cleanup(Phrases().Reset)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"content": "车损险呢，"})
	}))
	defer s.Close()
	e := talkerTestEngine(t, s.URL)
	raw := &quoteWarmTTS{key: t.TempDir()}
	tts := provider.HedgeTTSPool(raw, 2, 100000, 0, provider.TTSPool{}, nil)
	factory, err := provider.BackgroundTTSFor(tts)
	if err != nil {
		t.Fatal(err)
	}
	e.deps.BackgroundTTS = factory
	e.prepareTalkerOpener("车损险包含什么？")
	waitFor(t, "private synthesis", time.Second, func() bool { return len(e.talkerPending.ready) > 0 })
	r := <-e.talkerPending.ready
	if r.status != "ready" || len(r.audio.PCM) == 0 || raw.calls.Load() != 1 {
		t.Fatalf("status %s, calls %d", r.status, raw.calls.Load())
	}
	if _, ok := Phrases().Get(e.phraseKey(r.phrase)); ok {
		t.Fatal("generated audio entered shared cache")
	}
}

func TestTalkerOpenerPreservesHoldAndCancelsChangedHypothesis(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		calls.Add(1)
		<-r.Context().Done()
	}))
	defer s.Close()
	e := talkerTestEngine(t, s.URL)
	defer e.Close()
	e.muted = true
	e.prepareTalkerOpener("车损险包含什么？")
	if e.talkerPending != nil {
		t.Fatal("prepared while muted")
	}
	e.muted = false
	e.hold = &floorHold{}
	e.prepareTalkerOpener("车损险包含什么？")
	if e.talkerPending != nil {
		t.Fatal("prepared while caller/echo gate is holding")
	}
	e.hold = nil
	e.prepareTalkerOpener("车损险包含什么？")
	old := e.talkerPending
	waitFor(t, "request started", time.Second, func() bool { return calls.Load() == 1 })
	e.prepareTalkerOpener("改问理赔需要什么手续？")
	if e.talkerPending != nil {
		t.Fatal("revision retry bypassed cooldown")
	}
	waitFor(t, "request cancellation", time.Second, func() bool { return len(old.ready) > 0 })
	e.talkerAttempts = 64
	e.talkerStarted = time.Time{}
	e.prepareTalkerOpener("理赔需要什么手续？")
	if e.talkerPending != nil {
		t.Fatal("exceeded call budget")
	}
}

func TestTalkerOpenerSlowServiceDoesNotDelayInterruption(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
	defer s.Close()
	testInterruptionEarlyFinal(t, s.URL)
}

func TestTalkerOpenerKeepsBackgroundVoiceRejection(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"content": "天气呢，"})
	}))
	defer s.Close()
	testBackgroundVoiceRejected(t, s.URL)
}

func TestTalkerOpenerLongAudioFallsBackWithoutTruncatingWord(t *testing.T) {
	Phrases().Reset()
	t.Cleanup(Phrases().Reset)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"content": "车损险呢，"})
	}))
	defer s.Close()
	e := talkerTestEngine(t, s.URL)
	seedTalkerAudio(e, "车损险呢，", 800)
	seedTalkerAudio(e, "好的，", 300)
	e.prepareTalkerOpener("车损险包含什么？")
	waitFor(t, "audio length validation", time.Second, func() bool { return len(e.talkerPending.ready) > 0 })
	turn := e.tracker.Begin("车损险包含什么？", false)
	turn.talkerOpener = e.talkerPending
	e.sayOpener(turn)
	if turn.Opened != "好的，" {
		t.Fatal("overlong generated word was played")
	}
}
