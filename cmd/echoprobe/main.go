// Command echoprobe times the phone leg of a stored call by its echo.
//
// golive can keep a call as two tracks on one timeline: input.pcm, the caller
// as received, and output.pcm, the assistant as sent (internal/store). A phone
// line gives some of the assistant's voice back. The delay between a sound in
// the one track and its echo in the other is the round trip of whatever lies
// between golive and the thing that echoed — the gateway and the PBX if the
// echo is theirs, the whole leg out to the handset and back if it is the
// handset's — and nothing else measures that leg.
//
// Timing the first sound to come back after the assistant starts was tried
// (`listen: echo return time`) and does not work on a line that is never
// quiet: on one call it took 4 measurements and declined 49.
// This does not look for a first sound. It compares where the assistant's
// level rises with where the input's does, over every second the assistant
// speaks, at each delay from nothing to -max-lag. Noise and the caller's own
// voice rise at times that have nothing to do with the assistant's syllables
// and average away; an echo rises with every one of them, at one delay.
//
//	echoprobe -dir <session directory>      # holds input.pcm, output.pcm, session.json
//	echoprobe -store <store directory>      # the most recently modified session in it
//	          [-max-lag 1500ms] [-hop 5ms] [-json]
//
// It prints numbers and nothing else: no audio, no levels over time, and of
// session.json only the sample rate — the conversation in that file is never
// read into anything that is printed.
//
// What the figure is worth, plainly:
//
//   - It is the offset between two files. The store places the assistant's
//     audio where golive's clock says it was sent and appends the caller's as
//     it arrives, so buffering on the way in is part of the figure, and an
//     input that has lost frames reads early by what it lost (the store
//     closes such a gap only once it is 200 ms wide). The spread of the
//     per-stretch lags is where that would show.
//   - It is as fine as -hop: 5 ms.
//   - It has been tried on synthetic calls only (probe_test.go): tones
//     switched at the rate of syllables, an echo that is a plain delayed copy
//     25 dB down, noise that is a hiss or a hiss whose level wanders. There
//     an echo 5 dB under wandering noise is found in every call of 90 s and
//     in about half of those of 45 s; 10 dB under, it takes five minutes;
//     15 dB under it is not found at all. When it fails it says there is no
//     echo; it gave no wrong lag, and in 2,700 calls with no echo it reported
//     none. A real line's echo has been through a codec twice and perhaps an
//     echo canceller, and no recording of one has been put through this yet.
//
// Exit status: 0 when the call was read and measured, echo or no echo; 1 when
// it could not be read; 2 for a command line that makes no sense.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The names the store gives a session's files.
const (
	metaFile   = "session.json"
	inputFile  = "input.pcm"
	outputFile = "output.pcm"

	// defaultRate is assumed for a session with no record: a call cut off
	// before the record was written still has its audio, and the phone path
	// runs at 16 kHz (realtime.sample_rate in the telephony profile).
	defaultRate = 16000
)

// minHop is the finest step offered. The work grows as the cube of the
// fineness — more frames, more lags, longer runs of products — and the one
// trial at 1 ms took six seconds a minute of call and, in one call of thirty,
// reported a second echo that was not there. At 2 ms it reported none.
const minHop = 2 * time.Millisecond

// An echo this soon turned round close by; one this late went somewhere.
// TELEPHONY.md, "Measuring the phone leg", draws the same line.
const (
	localUnder = 150 * time.Millisecond
	farFrom    = 250 * time.Millisecond
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("echoprobe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		dir    = fs.String("dir", "", "a stored session's directory: input.pcm, output.pcm, session.json")
		store  = fs.String("store", "", "a store directory; the most recently modified session in it is measured")
		maxLag = fs.Duration("max-lag", 1500*time.Millisecond, "the longest delay to look for an echo at")
		hop    = fs.Duration("hop", 5*time.Millisecond, "the step between envelope values, and so between lags")
		asJSON = fs.Bool("json", false, "print one JSON object instead of text")
	)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "echoprobe times the phone leg of a stored call by the assistant's echo in the caller's track.")
		fmt.Fprint(stderr, "\n  echoprobe -dir <session directory> | -store <store directory> [-max-lag 1500ms] [-hop 5ms] [-json]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	usage := func(msg string) int {
		fmt.Fprintln(stderr, "echoprobe:", msg)
		return 2
	}
	switch {
	case fs.NArg() > 0:
		return usage("unexpected argument " + fs.Arg(0))
	case (*dir == "") == (*store == ""):
		return usage("give one of -dir <session directory> or -store <store directory>")
	case *hop < minHop || *hop > envelopeWindow:
		return usage(fmt.Sprintf("-hop must be between %v and %v", minHop, envelopeWindow))
	case *maxLag < 10**hop || *maxLag > 5*time.Second:
		return usage("-max-lag must be at least ten hops and no more than 5s")
	}

	session := *dir
	if session == "" {
		var err error
		if session, err = latestSession(*store); err != nil {
			fmt.Fprintln(stderr, "echoprobe:", err)
			return 1
		}
	}
	rep, err := measure(session, *maxLag, *hop)
	if err != nil {
		fmt.Fprintln(stderr, "echoprobe:", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintln(stderr, "echoprobe:", err)
			return 1
		}
		return 0
	}
	rep.print(stdout)
	return 0
}

// latestSession picks the session in a store that was written to last: the
// call that has just ended is the one being asked about. A session's time is
// that of the newest of its files, because a directory's own time does not
// move when a file inside it grows.
func latestSession(store string) (string, error) {
	entries, err := os.ReadDir(store)
	if err != nil {
		return "", err
	}
	var latest string
	var when time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(store, e.Name())
		var newest time.Time
		for _, name := range []string{inputFile, outputFile, metaFile} {
			if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
		// A directory with none of the three is not a session.
		if !newest.IsZero() && newest.After(when) {
			latest, when = dir, newest
		}
	}
	if latest == "" {
		return "", fmt.Errorf("%s: no stored session in it", store)
	}
	return latest, nil
}

// sampleRate reads the rate of a session's tracks from its record, and that
// alone: the record also holds the conversation, which is none of this
// program's business.
func sampleRate(dir string) (rate int, assumed bool, err error) {
	data, err := os.ReadFile(filepath.Join(dir, metaFile))
	if errors.Is(err, os.ErrNotExist) {
		return defaultRate, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	var meta struct {
		Rate int `json:"rate"` // store.Meta's field
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return 0, false, fmt.Errorf("%s: %s is not a session record: %w", dir, metaFile, err)
	}
	switch {
	case meta.Rate == 0:
		return defaultRate, true, nil
	case meta.Rate < 4000 || meta.Rate > 192000:
		return 0, false, fmt.Errorf("%s: %s gives a sample rate of %d", dir, metaFile, meta.Rate)
	}
	return meta.Rate, false, nil
}

// track reads one of a session's two tracks as hop powers.
func track(dir, name string, hop int) ([]float64, int64, error) {
	f, err := os.Open(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, fmt.Errorf("%s: no %s; the echo is looked for between %s and %s, so both are needed",
			dir, name, outputFile, inputFile)
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	power, samples, err := hopPower(f, hop)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: reading %s: %w", dir, name, err)
	}
	return power, samples, nil
}

// report is what echoprobe says about one call. Every field is a number, a
// path or a sentence about numbers.
type report struct {
	Dir         string `json:"dir"`
	RateHz      int    `json:"rate_hz"`
	RateAssumed bool   `json:"rate_assumed"`
	DurationMS  int64  `json:"duration_ms"`
	// AssistantSpeechMS is the total length of the assistant's stretches of
	// speech, the pauses for breath inside them included.
	AssistantSpeechMS  int64   `json:"assistant_speech_ms"`
	AssistantStretches int     `json:"assistant_stretches"`
	MaxLagMS           float64 `json:"max_lag_ms"`
	HopMS              float64 `json:"hop_ms"`

	EchoFound          bool    `json:"echo_found"`
	SignificanceNeeded float64 `json:"significance_needed"`
	// StrongestSignificance is the highest at any lag, found or not.
	StrongestSignificance float64      `json:"strongest_significance"`
	Echoes                []echoReport `json:"echoes"`

	PerStretch stretchReport `json:"per_stretch"`

	Interpretation string `json:"interpretation"`
}

type echoReport struct {
	LagMS        float64 `json:"lag_ms"`
	Correlation  float64 `json:"correlation"`
	Significance float64 `json:"significance"`
	// StretchesAgreeing counts the stretches whose own best lag is within
	// agreeWithin of this one, significant by themselves or not.
	StretchesAgreeing int `json:"stretches_agreeing"`
}

type stretchReport struct {
	// Eligible is how many stretches were long enough to correlate alone.
	Eligible           int     `json:"eligible"`
	SignificanceNeeded float64 `json:"significance_needed"`
	Significant        int     `json:"significant"`
	// AgreeingByChance is how many of the eligible stretches would have their
	// best lag within agreeWithin of any one lag if there were no echo.
	AgreeingByChance float64 `json:"agreeing_by_chance"`
	// BestLagMS is the spread of the significant stretches' best lags. It is
	// left out when no echo was found: a lag is not printed for a call that
	// has none.
	BestLagMS *spread `json:"best_lag_ms,omitempty"`
}

type spread struct {
	Min    float64 `json:"min"`
	Q1     float64 `json:"q1"`
	Median float64 `json:"median"`
	Q3     float64 `json:"q3"`
	Max    float64 `json:"max"`
}

func measure(dir string, maxLag, hop time.Duration) (report, error) {
	rate, assumed, err := sampleRate(dir)
	if err != nil {
		return report{}, err
	}
	samples := int(math.Round(hop.Seconds() * float64(rate)))
	// A hop is a whole number of samples, so at some rates it is not quite
	// what was asked for; everything after this is in hops as they are.
	frame := time.Duration(float64(samples) / float64(rate) * float64(time.Second))

	out, outSamples, err := track(dir, outputFile, samples)
	if err != nil {
		return report{}, err
	}
	in, inSamples, err := track(dir, inputFile, samples)
	if err != nil {
		return report{}, err
	}
	f := analyse(out, in, frame, maxLag)

	ms := func(frames int) float64 { return round(float64(frames)*frame.Seconds()*1000, 1) }
	lags := int(maxLag / frame)
	near := 2*int((agreeWithin+frame-1)/frame) + 1
	rep := report{
		Dir:                   dir,
		RateHz:                rate,
		RateAssumed:           assumed,
		DurationMS:            max(outSamples, inSamples) * 1000 / int64(rate),
		AssistantSpeechMS:     int64(ms(f.speech)),
		AssistantStretches:    f.stretches,
		MaxLagMS:              ms(lags),
		HopMS:                 round(frame.Seconds()*1000, 3),
		EchoFound:             len(f.peaks) > 0,
		SignificanceNeeded:    significant,
		StrongestSignificance: round(f.strongest, 1),
		Echoes:                []echoReport{},
		PerStretch: stretchReport{
			Eligible:           f.eligible,
			SignificanceNeeded: stretchSignificant,
			Significant:        len(f.stretchLags),
			AgreeingByChance:   round(float64(f.eligible)*float64(near)/float64(lags+1), 1),
		},
	}
	for _, p := range f.peaks {
		rep.Echoes = append(rep.Echoes, echoReport{
			LagMS:             ms(p.lag),
			Correlation:       round(p.correlation, 3),
			Significance:      round(p.significance, 1),
			StretchesAgreeing: p.agreeing,
		})
	}
	var settled int // significant stretches at one of the echoes' lags
	if rep.EchoFound && len(f.stretchLags) > 0 {
		at := make([]float64, len(f.stretchLags))
		for i, lag := range f.stretchLags {
			at[i] = ms(lag)
			for _, p := range f.peaks {
				if abs(p.lag-lag) <= int((agreeWithin+frame-1)/frame) {
					settled++
					break
				}
			}
		}
		rep.PerStretch.BestLagMS = &spread{
			Min:    at[0],
			Q1:     round(quantile(at, 0.25), 1),
			Median: round(quantile(at, 0.5), 1),
			Q3:     round(quantile(at, 0.75), 1),
			Max:    at[len(at)-1],
		}
	}
	rep.Interpretation = interpret(rep, settled)
	return rep, nil
}

func round(v float64, places int) float64 {
	scale := math.Pow(10, float64(places))
	return math.Round(v*scale) / scale
}

// interpret is the one line a reader in a hurry wants: is there an echo,
// whose is it likely to be, and does the call agree with itself about it.
func interpret(rep report, settled int) string {
	switch {
	case rep.AssistantStretches == 0:
		return "no echo found; the assistant's track is silent, so there was nothing to come back"
	case !rep.EchoFound:
		return "no echo found; this line returns too little of the assistant's voice to time"
	}
	echoes := append([]echoReport(nil), rep.Echoes...)
	sort.Slice(echoes, func(i, j int) bool { return echoes[i].LagMS < echoes[j].LagMS })
	var parts []string
	for _, e := range echoes {
		lag := time.Duration(e.LagMS * float64(time.Millisecond))
		reading := "between the two readings: long for an echo inside the gateway or PBX, short for the far handset's"
		switch {
		case lag < localUnder:
			reading = "consistent with an echo inside the gateway or PBX (the local part of the phone leg)"
		case lag >= farFrom:
			reading = "consistent with the far handset's echo: the full round trip of the phone leg"
		}
		parts = append(parts, fmt.Sprintf("echo at %.0f ms: %s", e.LagMS, reading))
	}

	// The whole call and its stretches are two looks at the same thing, and
	// the figure is as good as their agreement.
	n := rep.PerStretch.Significant
	alone := fmt.Sprintf("the %d stretches significant by themselves have a median of", n)
	if n == 1 {
		alone = "the one stretch significant by itself says"
	}
	switch {
	case n == 0:
		parts = append(parts, "no single stretch was significant by itself, so this rests on the whole call and on how many stretches agree")
	case len(echoes) == 1 && math.Abs(rep.PerStretch.BestLagMS.Median-echoes[0].LagMS) <= float64(agreeWithin/time.Millisecond):
		parts = append(parts, fmt.Sprintf("%s %.0f ms, which agrees", alone, rep.PerStretch.BestLagMS.Median))
	case len(echoes) == 1:
		parts = append(parts, fmt.Sprintf("but %s %.0f ms, which does not agree: unconfirmed", alone, rep.PerStretch.BestLagMS.Median))
	case 2*settled > n:
		parts = append(parts, fmt.Sprintf("%d of the %d stretches significant by themselves are at one of these lags", settled, n))
	default:
		parts = append(parts, fmt.Sprintf("but only %d of the %d stretches significant by themselves are at one of these lags: unconfirmed", settled, n))
	}
	return strings.Join(parts, "; ")
}

func (rep report) print(w io.Writer) {
	fmt.Fprintf(w, "echoprobe: %s\n", rep.Dir)
	assumed := ""
	if rep.RateAssumed {
		assumed = " (assumed: " + metaFile + " does not say)"
	}
	fmt.Fprintf(w, "  rate %d Hz%s, duration %.1f s\n", rep.RateHz, assumed, float64(rep.DurationMS)/1000)
	fmt.Fprintf(w, "  assistant speech %.1f s in %d stretches\n", float64(rep.AssistantSpeechMS)/1000, rep.AssistantStretches)
	fmt.Fprintf(w, "  searched delays of 0 to %.0f ms in steps of %g ms\n", rep.MaxLagMS, rep.HopMS)

	fmt.Fprintf(w, "\nwhole call (an echo counts at significance %.1f or more)\n", rep.SignificanceNeeded)
	if rep.EchoFound {
		fmt.Fprintf(w, "  %-5s %7s %12s %13s %19s\n", "echo", "lag ms", "correlation", "significance", "stretches agreeing")
		for i, e := range rep.Echoes {
			fmt.Fprintf(w, "  %-5d %7.0f %12.3f %13.1f %19s\n", i+1, e.LagMS, e.Correlation, e.Significance,
				fmt.Sprintf("%d of %d", e.StretchesAgreeing, rep.PerStretch.Eligible))
		}
		fmt.Fprintf(w, "  (agreeing: the stretch's own best lag is within %d ms; with no echo about %.1f of %d would be)\n",
			agreeWithin/time.Millisecond, rep.PerStretch.AgreeingByChance, rep.PerStretch.Eligible)
	} else {
		fmt.Fprintf(w, "  no echo found; the strongest significance at any delay was %.1f\n", rep.StrongestSignificance)
	}

	fmt.Fprintf(w, "\neach stretch by itself (%d of %d ms or longer; one counts at %.1f or more)\n",
		rep.PerStretch.Eligible, minStretch/time.Millisecond, rep.PerStretch.SignificanceNeeded)
	fmt.Fprintf(w, "  significant: %d\n", rep.PerStretch.Significant)
	if s := rep.PerStretch.BestLagMS; s != nil {
		fmt.Fprintf(w, "  their best lags, ms: min %.0f  q1 %.0f  median %.0f  q3 %.0f  max %.0f\n", s.Min, s.Q1, s.Median, s.Q3, s.Max)
	}

	fmt.Fprintf(w, "\n%s\n", rep.Interpretation)
}
