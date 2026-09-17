// Command ttsprobe measures time-to-first-audio from a synthesis provider
// directly, with the rest of the cascade taken out of the picture.
//
// It exists because the question "would the bidirectional endpoint make this
// faster" cannot be answered by reading either vendor's documentation. Neither
// publishes first-packet latency, and the part that matters here is not
// documented at all: how long the server sits on a short, unpunctuated segment
// before deciding it has enough text to synthesize. golive deliberately cuts a
// six-character first segment to get a syllable out early, so if that segment
// is the one being held, the optimisation is not merely wasted — it is worse
// than sending a whole sentence.
//
// Only your own account, region and network can answer that. This measures it:
//
//	go run ./cmd/ttsprobe -compare-endpoints -runs 12
//
// Every figure is a median of paired runs over the same texts, alternating
// between configurations, so provider warm-up and network drift land on both.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/minimax"
)

// defaultTexts are the shapes that actually occur in a turn, and they behave
// differently on purpose.
var defaultTexts = []string{
	"好的",   // the tiny first chunk: no punctuation, most latency-sensitive
	"我看一下", // a holding filler: same shape, spoken while the backend works
	"明天下午两点到四点是空的。",       // a complete sentence: the server synthesizes this immediately
	"两点到四点，",              // secondary punctuation only: accumulates rather than flushing
	"Sure, let me check.", // English, complete
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ttsprobe:", err)
		os.Exit(1)
	}
}

type variant struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Flush    bool   `json:"flush"`
}

type sample struct {
	Variant string  `json:"variant"`
	Text    string  `json:"text"`
	Run     int     `json:"run"`
	FirstMS float64 `json:"first_audio_ms"`
	TotalMS float64 `json:"total_ms"`
	AudioMS float64 `json:"audio_ms"`
	Err     string  `json:"err,omitempty"`
}

func run() error {
	var (
		envPath   = flag.String("env", ".env.local", "dotenv file with credentials")
		runs      = flag.Int("runs", 8, "measured passes over the text set, per variant")
		warmup    = flag.Int("warmup", 1, "unmeasured passes first; the socket and the vendor cache are cold otherwise")
		textsFlag = flag.String("texts", "", "comma-separated texts to synthesize (default: a representative set)")
		compare   = flag.Bool("compare-endpoints", false, "measure the standard and bidirectional endpoints side by side")
		flushMode = flag.String("flush", "auto", "task_flush: on, off, both, or auto (both when the endpoint is bidirectional)")
		rate      = flag.Int("rate", provider.PipelineRate, "PCM sample rate")
		voice     = flag.String("voice", "", "voice id (default: MINIMAX_TTS_VOICE_ID)")
		jsonOut   = flag.String("json", "", "write raw samples here")
		adapter   = flag.Bool("adapter", false, "run golive's own MiniMax adapter against both endpoints and report the bytes each segment produced")
		trace     = flag.Bool("trace", false, "print the frames each endpoint sends for one segment, with and without task_flush, and say what terminated it")
		traceWait = flag.Duration("trace-wait", 20*time.Second, "how long -trace waits for a terminator before calling it a deadline")
	)
	flag.Parse()

	if err := config.LoadDotEnv(*envPath); err != nil {
		fmt.Fprintf(os.Stderr, "ttsprobe: %v (continuing with the process environment)\n", err)
	}

	base, err := minimax.New()
	if err != nil {
		return err
	}

	if *adapter {
		return runAdapter(base, *voice, *rate)
	}

	if *trace {
		v := *voice
		if v == "" {
			v = base.VoiceID
		}
		return runTrace(base.Endpoint, base.APIKey, base.Model, v, *rate, *traceWait)
	}

	texts := defaultTexts
	if *textsFlag != "" {
		texts = splitTrim(*textsFlag)
	}

	variants, err := plan(base, *compare, *flushMode)
	if err != nil {
		return err
	}

	fmt.Printf("ttsprobe: %d variant(s), %d text(s), %d run(s) + %d warmup\n\n",
		len(variants), len(texts), *runs, *warmup)

	var samples []sample
	streams := map[string]provider.TTSStream{}
	defer func() {
		for _, s := range streams {
			_ = s.Close()
		}
	}()

	ctx := context.Background()
	// Alternate variants within each run rather than finishing one before
	// starting the next: a provider that warms up, or a network that drifts,
	// then affects both equally instead of flattering whichever went second.
	for r := -*warmup; r < *runs; r++ {
		for _, v := range variants {
			st, ok := streams[v.Name]
			if !ok {
				tts := clientFor(base, v, *voice)
				st, err = tts.Open(ctx, provider.TTSOptions{
					SampleRate: *rate,
					Voice:      orEnv(*voice, base.VoiceID),
					Speed:      base.Speed,
				})
				if err != nil {
					return fmt.Errorf("opening %s: %w", v.Name, err)
				}
				streams[v.Name] = st
			}
			for _, text := range texts {
				s := measure(ctx, st, v.Name, text, r, *rate)
				if r >= 0 {
					samples = append(samples, s)
				}
				if s.Err != "" {
					fmt.Fprintf(os.Stderr, "  %s %q: %s\n", v.Name, text, s.Err)
				}
			}
		}
	}

	report(variants, texts, samples)

	if *jsonOut != "" {
		blob, _ := json.MarshalIndent(samples, "", "  ")
		if err := os.WriteFile(*jsonOut, blob, 0o644); err != nil {
			return err
		}
		fmt.Printf("\nraw samples: %s\n", *jsonOut)
	}
	return nil
}

// plan builds the variants to measure.
func plan(base *minimax.TTS, compare bool, flushMode string) ([]variant, error) {
	endpoints := []string{base.Endpoint}
	if compare {
		std, bidi := endpointPair(base.Endpoint)
		endpoints = []string{std, bidi}
	}

	var out []variant
	for _, ep := range endpoints {
		isBidi := strings.HasSuffix(ep, "_bidi")
		var flushes []bool
		switch flushMode {
		case "on":
			flushes = []bool{true}
		case "off":
			flushes = []bool{false}
		case "both":
			flushes = []bool{false, true}
		case "auto":
			// Measuring flush on an endpoint that rejects it produces a column
			// of errors, not a comparison.
			if isBidi {
				flushes = []bool{false, true}
			} else {
				flushes = []bool{false}
			}
		default:
			return nil, fmt.Errorf("-flush must be on, off, both or auto")
		}
		for _, f := range flushes {
			name := "standard"
			if isBidi {
				name = "bidi"
			}
			if f {
				name += "+flush"
			}
			out = append(out, variant{Name: name, Endpoint: ep, Flush: f})
		}
	}
	return out, nil
}

// endpointPair derives both endpoint spellings from whichever one is
// configured, so the comparison keeps the host the account belongs to.
func endpointPair(ep string) (standard, bidi string) {
	standard = strings.TrimSuffix(ep, "_bidi")
	return standard, standard + "_bidi"
}

func clientFor(base *minimax.TTS, v variant, voice string) *minimax.TTS {
	c := *base
	c.Endpoint = v.Endpoint
	c.FlushPartialSegments = v.Flush
	// Irrelevant to this measurement and a source of noise if it fires
	// mid-run.
	c.KeepAliveEvery = 0
	if voice != "" {
		c.VoiceID = voice
	}
	return &c
}

// measure times one synthesis: the call, the first PCM byte, and the end.
func measure(ctx context.Context, st provider.TTSStream, name, text string, run, rate int) sample {
	s := sample{Variant: name, Text: text, Run: run}
	started := time.Now()
	chunks, err := st.Synthesize(ctx, text)
	if err != nil {
		s.Err = err.Error()
		return s
	}
	bytes := 0
	first := time.Time{}
	for c := range chunks {
		if c.Err != nil {
			s.Err = c.Err.Error()
			continue
		}
		if len(c.PCM) == 0 {
			continue
		}
		if first.IsZero() {
			first = time.Now()
		}
		bytes += len(c.PCM)
	}
	if first.IsZero() {
		if s.Err == "" {
			s.Err = "no audio"
		}
		return s
	}
	s.FirstMS = float64(first.Sub(started).Microseconds()) / 1000
	s.TotalMS = float64(time.Since(started).Microseconds()) / 1000
	s.AudioMS = float64(bytes) / 2 / float64(rate) * 1000
	return s
}

func report(variants []variant, texts []string, samples []sample) {
	fmt.Println("## Time to first audio, by variant")
	fmt.Println()
	fmt.Println("| variant | n | failed | p50 | p95 | min | max |")
	fmt.Println("| --- | ---: | ---: | ---: | ---: | ---: | ---: |")
	for _, v := range variants {
		vals, failed := valuesFor(samples, v.Name, "")
		if len(vals) == 0 {
			fmt.Printf("| %s | 0 | %d | — | — | — | — |\n", v.Name, failed)
			continue
		}
		fmt.Printf("| %s | %d | %d | %.0f | %.0f | %.0f | %.0f |\n",
			v.Name, len(vals), failed, pct(vals, .5), pct(vals, .95), vals[0], vals[len(vals)-1])
	}

	fmt.Println()
	fmt.Println("## By text — the shape of the segment is the whole question")
	fmt.Println()
	fmt.Printf("| text | ends a sentence |")
	for _, v := range variants {
		fmt.Printf(" %s p50 |", v.Name)
	}
	fmt.Println()
	fmt.Printf("| --- | --- |")
	for range variants {
		fmt.Printf(" ---: |")
	}
	fmt.Println()
	for _, text := range texts {
		fmt.Printf("| %s | %v |", short(text), endsSentence(text))
		for _, v := range variants {
			vals, _ := valuesFor(samples, v.Name, text)
			if len(vals) == 0 {
				fmt.Printf(" — |")
				continue
			}
			fmt.Printf(" %.0f |", pct(vals, .5))
		}
		fmt.Println()
	}

	if len(variants) < 2 {
		return
	}
	fmt.Println()
	fmt.Println("## Paired differences against the first variant")
	fmt.Println()
	base := variants[0].Name
	for _, v := range variants[1:] {
		diffs := paired(samples, base, v.Name)
		if len(diffs) == 0 {
			fmt.Printf("- **%s**: no paired samples.\n", v.Name)
			continue
		}
		med := pct(diffs, .5)
		spread := pct(diffs, .75) - pct(diffs, .25)
		switch {
		case abs(med) < spread:
			fmt.Printf("- **%s**: no measurable difference (median %+.0f ms, IQR %.0f ms over %d pairs). "+
				"A gap smaller than the run-to-run spread is not a result.\n", v.Name, med, spread, len(diffs))
		case med < 0:
			fmt.Printf("- **%s**: **%.0f ms faster** (median, IQR %.0f ms, %d pairs).\n",
				v.Name, -med, spread, len(diffs))
		default:
			fmt.Printf("- **%s**: %.0f ms slower (median, IQR %.0f ms, %d pairs).\n",
				v.Name, med, spread, len(diffs))
		}
	}
}

// paired subtracts like from like: same text, same run index, so provider
// warm-up and network drift cancel instead of adding noise.
func paired(samples []sample, a, b string) []float64 {
	index := map[string]float64{}
	for _, s := range samples {
		if s.Err != "" || s.Variant != a {
			continue
		}
		index[key(s)] = s.FirstMS
	}
	var out []float64
	for _, s := range samples {
		if s.Err != "" || s.Variant != b {
			continue
		}
		if base, ok := index[key(s)]; ok {
			out = append(out, s.FirstMS-base)
		}
	}
	sort.Float64s(out)
	return out
}

func key(s sample) string { return fmt.Sprintf("%d\x00%s", s.Run, s.Text) }

func valuesFor(samples []sample, name, text string) (vals []float64, failed int) {
	for _, s := range samples {
		if s.Variant != name || (text != "" && s.Text != text) {
			continue
		}
		if s.Err != "" {
			failed++
			continue
		}
		vals = append(vals, s.FirstMS)
	}
	sort.Float64s(vals)
	return vals, failed
}

// pct takes a percentile of an already sorted slice. Medians, not means: one
// cold turn moves a mean enough to hide what every other turn did.
func pct(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q * float64(len(sorted)-1))
	return sorted[i]
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// endsSentence mirrors the provider's own predicate, so the table says which
// texts the server would have synthesized immediately anyway.
func endsSentence(text string) bool {
	text = strings.TrimRight(text, " \t\"'”’）)】」』")
	if text == "" {
		return false
	}
	r := []rune(text)
	return strings.ContainsRune("。！？!?.…\n", r[len(r)-1])
}

func short(s string) string {
	r := []rune(s)
	if len(r) <= 24 {
		return s
	}
	return string(r[:23]) + "…"
}

func splitTrim(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func orEnv(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
