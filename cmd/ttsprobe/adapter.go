package main

// adapter.go drives golive's own MiniMax adapter against a live account, which
// is the one thing -trace cannot do.
//
// -trace speaks the protocol directly. That makes it the right instrument for
// asking what the *server* does, and the wrong one for asking whether golive
// handles it: its output is identical before and after any change to the
// adapter, because the adapter is not in the path. Re-running it after a fix
// and seeing the same trace proves nothing either way.
//
// This runs the shape that actually broke — a short opening fragment that gets
// flushed, then the sentences after it — through minimax.TTS.Synthesize, on
// both endpoints, and reports the bytes each segment produced. Every segment
// non-zero is the fix working. A zero after the flushed fragment is the stale
// acknowledgement still landing.
//
//	ttsprobe -adapter

import (
	"context"
	"fmt"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/minimax"
)

// greetingShape is how golive segments a real greeting: stream_first_chunk_chars
// cuts a short fragment to get a syllable out early — no sentence punctuation,
// so it is flushed — and the rest arrive as whole sentences, which are not.
//
// The last two do not end a sentence either, so they flush as well: two flushed
// segments in a row is what a long sentence hitting stream_max_chunk_chars
// produces, and it is the case a per-segment flag alone does not cover.
var greetingShape = []string{
	"您好，我是众",
	"安保险的车险管家小翠儿。",
	"看到您的爱车快到报价期了。",
	"给您来电做个",
	"最低的报价",
}

func runAdapter(base *minimax.TTS, voice string, rate int) error {
	plain, bidi := endpointPair(base.Endpoint)

	fmt.Println("ttsprobe -adapter: golive's own MiniMax adapter, live, on both endpoints")
	fmt.Println("segments are the shape of a real greeting; a flushed fragment first, then sentences")
	fmt.Println()

	type row struct {
		endpoint string
		bytes    []int
		failed   string
	}
	var rows []row
	bad := false

	for _, endpoint := range []string{plain, bidi} {
		fmt.Printf("── %s\n", endpoint)
		c := *base
		c.Endpoint = endpoint
		if voice != "" {
			c.VoiceID = voice
		}

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		stream, err := c.Open(ctx, provider.TTSOptions{
			SampleRate: rate,
			Voice:      c.VoiceID,
			Speed:      c.Speed,
		})
		if err != nil {
			cancel()
			fmt.Printf("   open failed: %v\n\n", err)
			rows = append(rows, row{endpoint, nil, err.Error()})
			bad = true
			continue
		}

		var bytes []int
		var failed string
		for i, text := range greetingShape {
			started := time.Now()
			chunks, err := stream.Synthesize(ctx, text)
			if err != nil {
				failed = fmt.Sprintf("segment %d: %v", i, err)
				break
			}
			n := 0
			for chunk := range chunks {
				if chunk.Err != nil {
					failed = fmt.Sprintf("segment %d: %v", i, chunk.Err)
					break
				}
				n += len(chunk.PCM)
			}
			bytes = append(bytes, n)
			mark := " "
			if n == 0 {
				mark = "!"
				bad = true
			}
			fmt.Printf("   %s segment %d  %-28q %7d bytes  %5d ms\n",
				mark, i, text, n, time.Since(started).Milliseconds())
			if failed != "" {
				break
			}
		}
		_ = stream.Close()
		cancel()
		if failed != "" {
			fmt.Printf("   %s\n", failed)
			bad = true
		}
		fmt.Println()
		rows = append(rows, row{endpoint, bytes, failed})
	}

	fmt.Println("summary")
	for _, r := range rows {
		status := "ok"
		if r.failed != "" {
			status = r.failed
		} else {
			for _, n := range r.bytes {
				if n == 0 {
					status = "SILENT SEGMENT"
					break
				}
			}
		}
		fmt.Printf("  %-46s %-16s %v\n", r.endpoint, status, r.bytes)
	}
	fmt.Println()
	if bad {
		fmt.Println("A zero-byte segment is the fault: the caller hears the answer stop there,")
		fmt.Println("and the engine records a sentence that produced no audio. On the")
		fmt.Println("bidirectional endpoint the usual cause is a flush acknowledgement arriving")
		fmt.Println("behind the terminator the previous segment already returned on.")
		return fmt.Errorf("at least one segment produced no audio")
	}
	fmt.Println("Every segment produced audio on both endpoints.")
	return nil
}
