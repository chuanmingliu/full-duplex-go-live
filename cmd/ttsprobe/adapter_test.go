package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/provider/minimax"
)

// TestAdapterModeReportsEverySegment checks the mode that verifies the fix.
//
// -trace cannot do this job: it speaks the protocol directly, so its output is
// identical before and after any change to the adapter. -adapter puts
// minimax.TTS.Synthesize in the path, against a server that withholds the flush
// acknowledgement until the next segment has been finalised — the live
// bidirectional ordering. Every segment must come back with audio.
func TestAdapterModeReportsEverySegment(t *testing.T) {
	endpoint := stubT2AFull(t, true, true, true)
	base := &minimax.TTS{
		Endpoint:             strings.TrimSuffix(endpoint, "_bidi"),
		APIKey:               "test",
		Model:                "speech-2.8-turbo",
		VoiceID:              "v1",
		Speed:                1,
		FlushPartialSegments: true,
		CancelOnAbandon:      true,
		OpenTimeout:          3 * time.Second,
		ReceiveTimeout:       2 * time.Second,
	}
	// warnOnce is unexported; New() sets it, and a zero TTS lazily creates one.
	_ = sync.Once{}

	if err := runAdapter(base, "v1", 24000); err != nil {
		t.Fatalf("adapter mode reported a silent segment against a server that sends audio "+
			"for every task_continue: %v", err)
	}
}
