package duplex

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestAcceptedEndCallLogsOnlyTurnID(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			opts := &slog.HandlerOptions{Level: slog.LevelDebug}
			var handler slog.Handler = slog.NewTextHandler(&output, opts)
			if format == "json" {
				handler = slog.NewJSONHandler(&output, opts)
			}
			turn := &Turn{
				ID:         "item_log_test",
				Transcript: "PRIVATE_CALLER_SENTINEL",
				Response:   "PRIVATE_RESPONSE_SENTINEL",
				Spoken:     "PRIVATE_AUDIO_SENTINEL",
			}
			// Hangup has already been accepted. Exercise the real branch that
			// logs why the backend will not resume, without a network or player.
			e := &Engine{
				log:          slog.New(handler),
				recordedUser: map[string]bool{},
				tools:        &toolState{endCall: &endCallRequest{byModel: true}},
			}
			e.onServerCalls(backendJob{turn: turn}, nil)
			logged := output.String()
			if !strings.Contains(logged, "tools: end_call accepted; not resuming the backend") {
				t.Fatal("accepted hangup log was not exercised")
			}
			if strings.Contains(logged, "PRIVATE_") {
				t.Error("accepted hangup log exposed private turn contents")
			}
			if format == "json" {
				var row map[string]any
				if err := json.Unmarshal(output.Bytes(), &row); err != nil {
					t.Fatal(err)
				}
				if id, ok := row["turn"].(string); !ok || id != turn.ID {
					t.Error("turn field is not the scalar turn ID")
				}
			} else if !strings.Contains(logged, "turn="+turn.ID+"\n") {
				t.Error("text log does not contain only the turn ID")
			}
		})
	}
}
