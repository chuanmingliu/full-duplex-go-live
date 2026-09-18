package main

// tagtiming.go measures how long each sound tag and each pause marker actually
// takes to speak, for the voice you ship.
//
// It exists because internal/prosody ships a table of placeholder durations and
// SpokenPrefix leans on it: after a barge-in, the engine decides what the caller
// heard by walking a segment's tokens and charging each one its duration. An
// over-estimate writes words into conversation history that were never spoken,
// which is the failure paced playback exists to prevent. The table is only
// honest once it has been measured, and it varies by voice.
//
// The method is a carrier sentence with and without the markup, several passes,
// median of the differences — the median because a vendor's first synthesis of
// a phrase is reliably slower than its later ones.
//
//	ttsprobe -tag-timing

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/prosody"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/minimax"
)

// carrier surrounds the markup with speech on both sides, because a tag at the
// edge of an utterance is rendered differently from one inside it, and because
// the API refuses a marker that does not sit between speakable text.
const (
	carrierHead = "One two three "
	carrierTail = " four five six."
)

// pauseProbes check whether the synthesizer honours a requested duration or
// merely approximates it. The whole correction depends on it doing the former.
var pauseProbes = []float64{0.2, 0.5, 0.8}

func runTagTiming(base *minimax.TTS, voice string, rate, runs int) error {
	if runs <= 0 {
		runs = 3
	}
	c := *base
	if voice != "" {
		c.VoiceID = voice
	}

	fmt.Printf("ttsprobe -tag-timing: %s, voice %s, %d runs\n", c.Model, c.VoiceID, runs)
	if !prosody.TagsSupported(c.Model) {
		fmt.Printf("  note: %s does not render sound tags; expect every tag to measure ~0 ms\n", c.Model)
	}
	fmt.Println("  carrier:", carrierHead+"<markup>"+carrierTail)
	fmt.Println()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	stream, err := c.Open(ctx, provider.TTSOptions{SampleRate: rate, Voice: c.VoiceID, Speed: c.Speed})
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = stream.Close() }()

	baseline, err := medianMS(ctx, stream, strings.TrimSpace(carrierHead+carrierTail), runs)
	if err != nil {
		return err
	}
	fmt.Printf("  baseline %6.0f ms  (carrier with no markup)\n\n", baseline)

	type result struct {
		markup string
		ms     float64
	}
	var tags, pauses []result

	for _, name := range prosody.Tags() {
		markup := "(" + name + ")"
		got, err := medianMS(ctx, stream, carrierHead+markup+carrierTail, runs)
		if err != nil {
			return err
		}
		d := got - baseline
		if d < 0 {
			d = 0
		}
		tags = append(tags, result{name, d})
		fmt.Printf("  %-14s %6.0f ms\n", markup, d)
	}

	fmt.Println()
	for _, secs := range pauseProbes {
		markup := fmt.Sprintf("<#%.2f#>", secs)
		got, err := medianMS(ctx, stream, carrierHead+markup+carrierTail, runs)
		if err != nil {
			return err
		}
		d := got - baseline
		pauses = append(pauses, result{markup, d})
		fmt.Printf("  %-14s %6.0f ms  (asked for %.0f ms, off by %+.0f)\n",
			markup, d, secs*1000, d-secs*1000)
	}

	fmt.Println()
	fmt.Println("paste into internal/prosody/prosody.go, tagDurationMS:")
	fmt.Println()
	sort.Slice(tags, func(i, j int) bool { return tags[i].markup < tags[j].markup })
	for _, t := range tags {
		fmt.Printf("\t%-16q %.0f,\n", t.markup+":", t.ms)
	}
	fmt.Println()

	for _, p := range pauses {
		if off := p.ms - pauseSeconds(p.markup)*1000; off > 150 || off < -150 {
			fmt.Printf("WARNING: %s was off by %+.0f ms. SpokenPrefix assumes a requested\n", p.markup, off)
			fmt.Println("pause is delivered as asked; if it is not, truncation accounting will drift")
			fmt.Println("in proportion to how much markup a reply carries.")
			break
		}
	}
	return nil
}

// medianMS synthesizes text runs+1 times and returns the median audio length of
// the measured passes. The first pass is discarded: a vendor's first synthesis
// of a phrase is reliably slower than its later ones, and a cold socket is not
// what this is measuring.
func medianMS(ctx context.Context, stream provider.TTSStream, text string, runs int) (float64, error) {
	var got []float64
	for i := 0; i <= runs; i++ {
		chunks, err := stream.Synthesize(ctx, text)
		if err != nil {
			return 0, fmt.Errorf("synthesize %q: %w", text, err)
		}
		n := 0
		for chunk := range chunks {
			if chunk.Err != nil {
				return 0, fmt.Errorf("synthesize %q: %w", text, chunk.Err)
			}
			n += len(chunk.PCM)
		}
		if i == 0 {
			continue // warmup
		}
		// Bytes to milliseconds directly: 16-bit mono, so two bytes a sample.
		got = append(got, float64(n)/2/float64(stream.SampleRate())*1000)
	}
	if len(got) == 0 {
		return 0, fmt.Errorf("synthesize %q: no measured passes", text)
	}
	sort.Float64s(got)
	return got[len(got)/2], nil
}

func pauseSeconds(markup string) float64 {
	var secs float64
	_, err := fmt.Sscanf(markup, "<#%f#>", &secs)
	if err != nil {
		return 0
	}
	return secs
}
