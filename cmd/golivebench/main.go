// Command golivebench measures spoken-response latency, and compares two voice
// services head to head.
//
// It drives both golive (gpt-live-1 event protocol) and an OpenAI Realtime
// service — such as the Python cascade this project was modelled on — from one
// client, with one clock, over the same recorded audio. That is the only way
// the comparison means anything: a live microphone segments differently on
// every take, and the resulting VAD variance is larger than the pipeline
// difference you are trying to measure.
//
// The headline number is measured entirely client-side:
//
//	response latency = (first output audio byte received)
//	                 - (last frame of speech sent)
//
// Neither service's own instrumentation is trusted for it, so neither can
// flatter itself by choosing a different origin. It deliberately includes each
// engine's end-of-turn silence threshold, because a caller waits through that
// too — it is a design choice, not an overhead to be excluded.
//
//	golivebench -a golive=ws://127.0.0.1:8080/v1/live \
//	            -b cascade=ws://127.0.0.1:8765/v1/realtime \
//	            -clips clips/*.wav -runs 12
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chuanmingliu/golive/internal/audio"
	"github.com/chuanmingliu/golive/internal/live"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "golivebench:", err)
		os.Exit(1)
	}
}

// stack is one service under test.
type stack struct {
	name string
	url  string
	kind string // "golive" | "realtime"
}

func parseStack(spec, kind string) (stack, error) {
	name, url, ok := strings.Cut(spec, "=")
	if !ok {
		return stack{}, fmt.Errorf("expected name=url, got %q", spec)
	}
	if !strings.HasPrefix(url, "ws://") && !strings.HasPrefix(url, "wss://") {
		return stack{}, fmt.Errorf("%s: url must be ws:// or wss://, got %q", name, url)
	}
	return stack{name: name, url: url, kind: kind}, nil
}

func run() error {
	var (
		aSpec     = flag.String("a", "golive=ws://127.0.0.1:8080/v1/live", "first stack, as name=url (gpt-live protocol)")
		bSpec     = flag.String("b", "", "second stack, as name=url (OpenAI Realtime protocol); empty benchmarks only -a")
		clipGlob  = flag.String("clips", "", "glob of 16-bit PCM WAV files to speak; empty uses synthetic audio")
		runs      = flag.Int("runs", 10, "measured runs per stack")
		warmup    = flag.Int("warmup", 1, "unmeasured runs per stack first, to pay connection setup")
		rate      = flag.Int("rate", 24000, "session PCM rate")
		silenceMS = flag.Int("silence-ms", 1200, "trailing silence after each clip, so the engine's VAD closes the turn")
		synthMS   = flag.Int("synth-ms", 1800, "length of synthetic speech when -clips is empty")
		timeout   = flag.Int("timeout-s", 30, "give up on a run after this long")
		outJSON   = flag.String("json", "", "write raw per-run results here")
		outMD     = flag.String("md", "", "write the report here as markdown")
		verbose   = flag.Bool("v", false, "print every run as it completes")
	)
	flag.Parse()

	a, err := parseStack(*aSpec, "golive")
	if err != nil {
		return err
	}
	stacks := []stack{a}
	if *bSpec != "" {
		b, err := parseStack(*bSpec, "realtime")
		if err != nil {
			return err
		}
		stacks = append(stacks, b)
	}

	clips, err := loadClips(*clipGlob, *rate, *synthMS)
	if err != nil {
		return err
	}
	fmt.Printf("· %d clip(s), %d run(s) per stack, %d warmup, %d Hz\n",
		len(clips), *runs, *warmup, *rate)
	for _, s := range stacks {
		fmt.Printf("·   %-10s %s (%s)\n", s.name, s.url, s.kind)
	}

	ctx := context.Background()
	var results []result

	// Alternate stacks within each run so a drifting network or a warming
	// provider cache hits both equally. Benchmarking one stack to completion
	// and then the other measures the passage of time as much as the software.
	total := *warmup + *runs
	for i := 0; i < total; i++ {
		clip := clips[i%len(clips)]
		measured := i >= *warmup
		for _, s := range stacks {
			r := drive(ctx, s, clip, *rate, *silenceMS, time.Duration(*timeout)*time.Second)
			r.Index = i - *warmup
			r.Measured = measured
			results = append(results, r)
			if *verbose || r.Err != "" {
				tag := "warmup"
				if measured {
					tag = fmt.Sprintf("run %2d", r.Index+1)
				}
				if r.Err != "" {
					fmt.Printf("  %-10s %s  FAILED: %s\n", s.name, tag, r.Err)
				} else {
					fmt.Printf("  %-10s %s  first audio %5d ms   transcript %5d ms   audio %.2fs   %q\n",
						s.name, tag, r.FirstAudioMS, r.TranscriptMS, r.AudioMS/1000, trim(r.Transcript, 24))
				}
			} else if measured {
				fmt.Print(".")
			}
		}
	}
	if !*verbose {
		fmt.Println()
	}

	report := summarize(stacks, results)
	fmt.Println()
	fmt.Println(report)

	if *outJSON != "" {
		data, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*outJSON, data, 0o644); err != nil {
			return err
		}
		fmt.Printf("· wrote %s\n", *outJSON)
	}
	if *outMD != "" {
		if err := os.WriteFile(*outMD, []byte(report), 0o644); err != nil {
			return err
		}
		fmt.Printf("· wrote %s\n", *outMD)
	}
	return nil
}

// incoming is one normalized event from either dialect. Both protocols are
// reduced to this vocabulary at the socket, so the measurement code below never
// branches on which stack it is talking to — which is what keeps the two
// measurements identical rather than merely similar.
type incoming struct {
	kind string // ready | audio | transcript | reply | speech_stopped | error
	text string
	n    int
	at   time.Time
}

// result is one measured turn.
type result struct {
	Stack string `json:"stack"`
	Clip  string `json:"clip"`
	Index int    `json:"index"`
	// Measured is false for warmup runs, which are excluded from every figure.
	Measured bool `json:"measured"`

	// FirstAudioMS is the headline: last speech frame sent to first audio byte
	// received, measured on the client's clock for both stacks alike.
	FirstAudioMS int64 `json:"first_audio_ms"`
	// TranscriptMS is last speech frame to the final transcript.
	TranscriptMS int64 `json:"transcript_ms"`
	// SpeechStoppedMS is last speech frame to the engine's own VAD close, where
	// the engine reports one. It separates "their VAD waits longer" from "their
	// pipeline is slower", which the headline figure alone cannot.
	SpeechStoppedMS int64 `json:"speech_stopped_ms"`
	// AudioMS is how much audio the answer contained.
	AudioMS float64 `json:"audio_ms"`
	// Transcript is what the service heard, for sanity — two stacks that heard
	// different words are not comparable on latency.
	Transcript string `json:"transcript"`
	Reply      string `json:"reply"`
	Err        string `json:"error,omitempty"`
}

type clip struct {
	name string
	pcm  []byte
}

func loadClips(glob string, rate, synthMS int) ([]clip, error) {
	if glob == "" {
		return []clip{{name: "synthetic", pcm: syntheticSpeech(rate, synthMS)}}, nil
	}
	paths, err := filepath.Glob(glob)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no files matched %q", glob)
	}
	sort.Strings(paths)
	out := make([]clip, 0, len(paths))
	for _, p := range paths {
		pcm, srcRate, err := audio.ReadWAV(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		conv, err := audio.ResamplePCM16(pcm, srcRate, rate)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, clip{name: filepath.Base(p), pcm: conv})
	}
	return out, nil
}

// drive runs one turn against one stack and returns what the client observed.
func drive(ctx context.Context, s stack, c clip, rate, silenceMS int, timeout time.Duration) result {
	r := result{Stack: s.name, Clip: c.name}

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, s.url, nil)
	if err != nil {
		r.Err = "dial: " + err.Error()
		return r
	}
	defer conn.Close()

	events := make(chan incoming, 512)

	go func() {
		defer close(events)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			at := time.Now()
			var env struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &env) != nil {
				continue
			}
			switch env.Type {
			// --- ready ---
			case "session.started", "session.updated", "session.created":
				events <- incoming{kind: "ready", at: at}

			// --- output audio, both dialects ---
			case "session.output_audio.delta", "response.output_audio.delta", "response.audio.delta":
				var ev struct {
					Audio string `json:"audio"`
					Delta string `json:"delta"`
				}
				_ = json.Unmarshal(data, &ev)
				b64 := ev.Audio
				if b64 == "" {
					b64 = ev.Delta
				}
				pcm, err := base64.StdEncoding.DecodeString(b64)
				if err != nil {
					continue
				}
				events <- incoming{kind: "audio", n: len(pcm), at: at}

			// --- the engine's own end-of-speech, where it reports one ---
			case "input_audio_buffer.speech_stopped", live.ExtSpeechStopped:
				events <- incoming{kind: "speech_stopped", at: at}

			// --- final input transcript, both dialects ---
			case "conversation.item.input_audio_transcription.completed":
				var ev struct {
					Transcript string `json:"transcript"`
				}
				_ = json.Unmarshal(data, &ev)
				events <- incoming{kind: "transcript", text: ev.Transcript, at: at}
			case live.ServerInputTranscriptDelta:
				var ev live.TranscriptDelta
				if json.Unmarshal(data, &ev) == nil && ev.Final {
					events <- incoming{kind: "transcript", text: ev.Content, at: at}
				}

			// --- assistant text, both dialects ---
			case live.ServerOutputTranscriptDelta, "response.output_audio_transcript.delta",
				"response.audio_transcript.delta":
				var ev struct {
					Content string `json:"content"`
					Delta   string `json:"delta"`
				}
				_ = json.Unmarshal(data, &ev)
				t := ev.Content
				if t == "" {
					t = ev.Delta
				}
				events <- incoming{kind: "reply", text: t, at: at}

			case "error":
				events <- incoming{kind: "error", text: string(data), at: at}
			}
		}
	}()

	send := func(v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return conn.WriteMessage(websocket.TextMessage, data)
	}

	if err := sendSessionConfig(send, s.kind, rate); err != nil {
		r.Err = "session config: " + err.Error()
		return r
	}

	// Wait for the service to acknowledge the session before any audio, rather
	// than sleeping a fixed amount. Audio sent into a session that has not
	// finished configuring is audio the engine may discard.
	if !await(events, "ready", 10*time.Second) {
		r.Err = "no session acknowledgement"
		return r
	}

	// --- stream the clip at real time ---
	frame := audio.PCM16(rate).BytesForMS(20)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	appendType := "session.input_audio.append"
	if s.kind == "realtime" {
		appendType = "input_audio_buffer.append"
	}
	push := func(pcm []byte) error {
		for off := 0; off < len(pcm); off += frame {
			end := off + frame
			if end > len(pcm) {
				end = len(pcm)
			}
			<-ticker.C
			if err := send(map[string]any{
				"type":  appendType,
				"audio": base64.StdEncoding.EncodeToString(pcm[off:end]),
			}); err != nil {
				return err
			}
		}
		return nil
	}

	if err := push(c.pcm); err != nil {
		r.Err = "stream: " + err.Error()
		return r
	}
	// Everything is measured from here: the instant the caller stopped talking.
	speechEnd := time.Now()

	silence := make([]byte, audio.PCM16(rate).BytesForMS(silenceMS))
	go func() { _ = push(silence) }()

	// --- collect ---
	deadline := time.After(timeout)
	var (
		firstAudio time.Time
		bytesOut   int
		reply      strings.Builder
	)
	quiet := time.NewTimer(timeout)
	defer quiet.Stop()

	for {
		select {
		case <-deadline:
			if firstAudio.IsZero() {
				r.Err = "timed out before any audio"
				return r
			}
			goto done
		case <-quiet.C:
			goto done
		case ev, ok := <-events:
			if !ok {
				goto done
			}
			switch ev.kind {
			case "audio":
				if firstAudio.IsZero() {
					firstAudio = ev.at
				}
				bytesOut += ev.n
				// The turn is over once audio stops arriving for a beat. A
				// fixed wait would either cut long answers off or idle after
				// short ones.
				quiet.Reset(1500 * time.Millisecond)
			case "transcript":
				if r.Transcript == "" {
					r.Transcript = ev.text
					r.TranscriptMS = ev.at.Sub(speechEnd).Milliseconds()
				}
			case "speech_stopped":
				if r.SpeechStoppedMS == 0 {
					r.SpeechStoppedMS = ev.at.Sub(speechEnd).Milliseconds()
				}
			case "reply":
				reply.WriteString(ev.text)
			case "error":
				if r.Err == "" {
					r.Err = trim(ev.text, 200)
				}
			}
		}
	}

done:
	if firstAudio.IsZero() {
		if r.Err == "" {
			r.Err = "no audio received"
		}
		return r
	}
	r.FirstAudioMS = firstAudio.Sub(speechEnd).Milliseconds()
	r.AudioMS = float64(bytesOut) / float64(audio.BytesPerSample) / float64(rate) * 1000
	r.Reply = reply.String()
	return r
}

func sendSessionConfig(send func(any) error, kind string, rate int) error {
	if kind == "golive" {
		return send(live.SessionStartEvent{
			Envelope: live.Envelope{Type: live.ClientSessionStart, EventID: "bench"},
			Session: live.SessionConfig{
				Audio:      &live.AudioConfig{Format: &live.AudioFormat{Type: "audio/pcm", Rate: rate}},
				Delegation: &live.DelegationConfig{Type: live.DelegationResponses},
			},
		})
	}
	// OpenAI Realtime (GA shape), which is what the Python cascade speaks.
	return send(map[string]any{
		"type":     "session.update",
		"event_id": "bench",
		"session": map[string]any{
			"type": "realtime",
			"audio": map[string]any{
				"input":  map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": rate}},
				"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": rate}},
			},
		},
	})
}

// await drains events until one of the wanted kind arrives.
func await(ch <-chan incoming, want string, d time.Duration) bool {
	timeout := time.After(d)
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return false
			}
			if v.kind == want {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

// --- statistics -------------------------------------------------------------

type dist struct {
	n                  int
	min, p50, p95, max int64
	iqr                int64
	failures           int
}

func describe(vals []int64, failures int) dist {
	d := dist{n: len(vals), failures: failures}
	if len(vals) == 0 {
		return d
	}
	v := append([]int64(nil), vals...)
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	q := func(p float64) int64 { return v[int(math.Round(p*float64(len(v)-1)))] }
	d.min, d.p50, d.p95, d.max = v[0], q(0.5), q(0.95), v[len(v)-1]
	d.iqr = q(0.75) - q(0.25)
	return d
}

func summarize(stacks []stack, results []result) string {
	var b strings.Builder
	b.WriteString("# Response latency\n\n")
	b.WriteString("Last frame of speech sent → first audio byte received, measured client-side.\n")
	b.WriteString("Warmup runs excluded. Medians, not means: one cold-start turn moves a mean\n")
	b.WriteString("enough to hide what every other turn did.\n\n")

	byStack := map[string][]result{}
	for _, r := range results {
		if r.Measured {
			byStack[r.Stack] = append(byStack[r.Stack], r)
		}
	}

	b.WriteString("| stack | runs | failed | min | p50 | p95 | max | IQR | transcript p50 | VAD close p50 |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")

	dists := map[string]dist{}
	for _, s := range stacks {
		rs := byStack[s.name]
		var lat, tr, vad []int64
		fails := 0
		for _, r := range rs {
			if r.Err != "" {
				fails++
				continue
			}
			lat = append(lat, r.FirstAudioMS)
			if r.TranscriptMS > 0 {
				tr = append(tr, r.TranscriptMS)
			}
			if r.SpeechStoppedMS > 0 {
				vad = append(vad, r.SpeechStoppedMS)
			}
		}
		d := describe(lat, fails)
		dists[s.name] = d
		td, vd := describe(tr, 0), describe(vad, 0)
		b.WriteString(fmt.Sprintf("| %s | %d | %d | %d | **%d** | %d | %d | %d | %s | %s |\n",
			s.name, d.n, d.failures, d.min, d.p50, d.p95, d.max, d.iqr,
			orDash(td.p50, td.n), orDash(vd.p50, vd.n)))
	}
	b.WriteString("\nAll figures in milliseconds.\n")

	if len(stacks) < 2 {
		return b.String()
	}

	// Paired comparison: the same clip at the same moment on both stacks, so
	// network drift and provider warmth cancel instead of adding noise.
	a, bb := stacks[0].name, stacks[1].name
	idxA := map[int]result{}
	for _, r := range byStack[a] {
		if r.Err == "" {
			idxA[r.Index] = r
		}
	}
	var deltas []int64
	for _, r := range byStack[bb] {
		if r.Err != "" {
			continue
		}
		if other, ok := idxA[r.Index]; ok {
			deltas = append(deltas, r.FirstAudioMS-other.FirstAudioMS)
		}
	}

	b.WriteString("\n## Paired difference\n\n")
	if len(deltas) < 3 {
		b.WriteString("Not enough paired runs to compare.\n")
		return b.String()
	}
	dd := describe(deltas, 0)
	b.WriteString(fmt.Sprintf("%d pairs, %s minus %s: median **%+d ms** (p5..p95 %+d..%+d, IQR %d).\n\n",
		dd.n, bb, a, dd.p50, dd.min, dd.max, dd.iqr))

	// Refuse to call a winner when the difference is inside the noise. A
	// median gap smaller than the spread of the differences is not a result.
	da, db := dists[a], dists[bb]
	spread := maxi(dd.iqr, maxi(da.iqr, db.iqr))
	switch {
	case abs64(dd.p50) <= spread:
		b.WriteString(fmt.Sprintf("**No measurable difference.** The median gap (%d ms) is within the "+
			"run-to-run spread (%d ms), so these runs do not separate the two stacks. "+
			"More runs, or a quieter machine, would be needed to say anything stronger.\n",
			abs64(dd.p50), spread))
	case dd.p50 < 0:
		b.WriteString(fmt.Sprintf("**%s is faster** by a median of %d ms, and the gap exceeds the "+
			"run-to-run spread (%d ms).\n", bb, -dd.p50, spread))
	default:
		b.WriteString(fmt.Sprintf("**%s is faster** by a median of %d ms, and the gap exceeds the "+
			"run-to-run spread (%d ms).\n", a, dd.p50, spread))
	}

	// Heard-the-same-thing check. Latency on different words is not comparable.
	same, total := 0, 0
	for _, r := range byStack[bb] {
		if other, ok := idxA[r.Index]; ok && r.Err == "" {
			total++
			if normalize(r.Transcript) == normalize(other.Transcript) {
				same++
			}
		}
	}
	if total > 0 && same < total {
		b.WriteString(fmt.Sprintf("\n> Caution: the two stacks transcribed the clip identically in only "+
			"%d of %d pairs. Where they heard different words they answered different questions, and "+
			"the latency comparison is weaker than it looks.\n", same, total))
	}
	return b.String()
}

func orDash(v int64, n int) string {
	if n == 0 {
		return "—"
	}
	return fmt.Sprintf("%d", v)
}

func normalize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '，', ',', '。', '.', '？', '?', '！', '!', '、':
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func maxi(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// syntheticSpeech is a fallback so the harness itself can be smoke-tested
// without recordings. It is not speech: use -clips for a real measurement,
// because a recognizer's behaviour on a tone says nothing about its behaviour
// on a sentence.
func syntheticSpeech(rate, ms int) []byte {
	n := rate * ms / 1000
	out := make([]float32, n)
	for i := range out {
		t := float64(i) / float64(rate)
		env := 0.62 + 0.38*math.Sin(2*math.Pi*4.5*t)
		v := 0.45 * env * (math.Sin(2*math.Pi*140*t) +
			0.5*math.Sin(2*math.Pi*280*t) +
			0.25*math.Sin(2*math.Pi*560*t)) / 1.75
		out[i] = float32(v)
	}
	return audio.EncodePCM16(out)
}
