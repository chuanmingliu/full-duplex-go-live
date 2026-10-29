package duplex

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"
)

// A zero latency can mean either an immediate cached opener or no audio at
// all. Consumers need explicit presence before using a turn in a latency gate.
func TestTurnMetricsLogPresence(t *testing.T) {
	origin := time.Unix(100, 0)
	tests := []struct {
		name             string
		turn             *Turn
		responseToSpeech bool
		audioPresent     bool
		contentPresent   bool
		opened           bool
		audioMS          float64
		contentMS        float64
	}{
		{
			name:             "no output is not a zero latency answer",
			turn:             &Turn{speechEndAt: origin, endSilenceMS: 380},
			responseToSpeech: true,
		},
		{
			name:             "answer at speech close is present",
			turn:             &Turn{speechEndAt: origin, firstAudioOut: origin, endSilenceMS: 380},
			responseToSpeech: true, audioPresent: true, contentPresent: true,
		},
		{
			name: "opener without content does not fabricate a content observation",
			turn: &Turn{
				speechEndAt: origin, firstAudioOut: origin, endSilenceMS: 380,
				Opened: "acknowledgement", OpenedMS: 420,
			},
			responseToSpeech: true, audioPresent: true, opened: true,
			contentMS: 420,
		},
		{
			name: "content ready during opener is present even at zero milliseconds",
			turn: &Turn{
				speechEndAt: origin, firstAudioOut: origin, firstContent: origin,
				endSilenceMS: 380, Opened: "acknowledgement", OpenedMS: 420,
			},
			responseToSpeech: true, audioPresent: true, contentPresent: true, opened: true,
			contentMS: 420,
		},
		{
			name:         "greeting or unsolicited result has audio but no speech origin",
			turn:         &Turn{startedAt: origin, firstAudioOut: origin.Add(100 * time.Millisecond)},
			audioPresent: true, contentPresent: true, audioMS: 100, contentMS: 100,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			e := &Engine{
				log:  slog.New(slog.NewJSONHandler(&output, nil)),
				deps: Deps{Emit: func(any) {}},
			}
			e.emitMetrics(tc.turn, 0, false)
			var fields map[string]any
			if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
				t.Fatalf("read metrics log: %v", err)
			}
			for key, want := range map[string]bool{
				"response_to_speech":    tc.responseToSpeech,
				"first_audio_present":   tc.audioPresent,
				"first_content_present": tc.contentPresent,
				"opened":                tc.opened,
			} {
				if got, ok := fields[key].(bool); !ok || got != want {
					t.Errorf("%s = %v, want explicit boolean %t", key, fields[key], want)
				}
			}
			for key, want := range map[string]float64{
				"first_audio_out_ms": tc.audioMS,
				"first_content_ms":   tc.contentMS,
			} {
				if got, ok := fields[key].(float64); !ok || got != want {
					t.Errorf("%s = %v, want unchanged timing %v", key, fields[key], want)
				}
			}
		})
	}
}
