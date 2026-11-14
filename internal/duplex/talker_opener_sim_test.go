package duplex

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/provider/mock"
)

// Opt-in controlled audio simulation. Real VAD/ASR orchestration, engine,
// playback, transcript and latency accounting; deterministic recognizer/model/
// speech fixtures. TALKER_SIM_URL replaces only the opener HTTP fixture with
// the live Mac model. This is NOT a SIP or acoustic handset measurement.
func TestTalkerOpenerSimulation(t *testing.T) {
	if os.Getenv("GOLIVE_TALKER_SIM") != "1" {
		t.Skip("opt-in latency simulation")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(60 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]string{"content": "车损险呢，"})
	}))
	defer s.Close()
	endpoint := s.URL
	if live := os.Getenv("TALKER_SIM_URL"); live != "" {
		endpoint = live
	}
	type row struct {
		Mode                     string `json:"mode"`
		Index                    int    `json:"index"`
		FirstAudio, FirstContent int64
		Talker                   bool `json:"talker"`
	}
	var rows []row
	samples := 10
	if n, _ := strconv.Atoi(os.Getenv("TALKER_SIM_SAMPLES")); n > 0 {
		samples = n
	}
	for i := 0; i < samples; i++ {
		for _, enabled := range []bool{false, true} {
			mode := "preset"
			if enabled {
				mode = "talker"
			}
			t.Run(mode+strconv.Itoa(i), func(t *testing.T) {
				Phrases().Reset()
				t.Cleanup(Phrases().Reset)
				cfg := earlyFinalConfig(120).opts.Cfg
				cfg.Duplex.Opener = true
				cfg.Duplex.OpenerPhrases = []string{"好的，"}
				cfg.Duplex.OpenerQuestionPhrases = []string{"好的，"}
				cfg.Duplex.PhraseCache = true
				if enabled {
					cfg.Duplex.TalkerOpenerURL = endpoint
				}
				rate := cfg.ClientRate
				asr := newTrailingASR("车损险包含什么")
				asr.msPerRune = 60
				llm := mock.NewLLM()
				llm.FirstTokenDelay = 500 * time.Millisecond
				llm.DelayPerRune = 2 * time.Millisecond
				tts := mock.NewTTS()
				c := newCollector(rate)
				var logs openerLogBuffer
				logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
				e := startEngine(t, Options{Cfg: cfg, Delegation: "responses", Speculative: true, ClientRate: rate, Language: "zh", Voice: "test"}, Deps{ASR: asr, LLM: llm, TTS: tts, Emit: c.emit, Log: logger})
				// Equal-length audio isolates selection/preparation overhead from wording.
				seedTalkerAudio(e, "好的，", 300)
				seedTalkerAudio(e, "车损险呢，", 300)
				speak(e, rate, 1000)
				pause(e, rate, 1700)
				waitFor(t, "answer content", 3*time.Second, func() bool { return strings.Contains(c.allOutput(), "让我确认一下") })
				e.Close()
				line := ""
				for _, v := range strings.Split(logs.String(), "\n") {
					if strings.Contains(v, "turn: response latency") && strings.Contains(v, "response_to_speech=true") {
						line = v
						break
					}
				}
				if line == "" || !strings.Contains(line, "first_audio_present=true") || !strings.Contains(line, "first_content_present=true") {
					t.Fatal("missing caller latency record")
				}
				field := func(name string) int64 {
					m := regexp.MustCompile(`\b` + name + `=(-?\d+)`).FindStringSubmatch(line)
					if m == nil {
						t.Fatalf("missing %s", name)
					}
					n, _ := strconv.ParseInt(m[1], 10, 64)
					return n
				}
				r := row{Mode: mode, Index: i, FirstAudio: field("first_audio_out_ms") + field("end_silence_ms"), FirstContent: field("first_content_ms") + field("end_silence_ms"), Talker: strings.Contains(logs.String(), "source=talker")}
				if r.Talker && !strings.Contains(c.allOutput(), "车损险呢，") {
					t.Fatal("selected generated opener did not reach output transcript")
				}
				rows = append(rows, r)
				if !enabled && r.Talker {
					t.Fatal("disabled mode used Talker")
				}
			})
		}
	}
	if out := os.Getenv("TALKER_SIM_OUT"); out != "" {
		b, _ := json.MarshalIndent(rows, "", "  ")
		if err := os.WriteFile(out, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := json.Marshal(rows)
	t.Log(string(b))
}

// Same original phone profile and provider-delivery fixture in both modes.
// Only Talker is enabled in the second arm; it must not change floor decisions.
func TestTalkerOpenerInterruptionSimulation(t *testing.T) {
	if os.Getenv("GOLIVE_TALKER_SIM") != "1" {
		t.Skip("opt-in paced comparison")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"content": "停一下呢，"})
	}))
	defer s.Close()
	t.Run("preset", func(t *testing.T) { runInterruptionDeliverySimulation(t, "", "responses") })
	t.Run("talker", func(t *testing.T) { runInterruptionDeliverySimulation(t, s.URL, "responses") })
}
