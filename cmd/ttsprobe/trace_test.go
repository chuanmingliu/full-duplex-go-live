package main

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// stubT2A is a T2A server whose only interesting property is what it does after
// task_continue. finalPerContinue models /ws/v1/t2a_v2, which finalises every
// one; without it, only an explicit task_flush ends anything — the behaviour
// -trace exists to detect.
func stubT2A(t *testing.T, finalPerContinue, answerFlush bool) string {
	return stubT2AFull(t, finalPerContinue, answerFlush, false)
}

// stubT2AFull adds lateFlushAck: the acknowledgement for a flush is withheld
// until the *next* task_continue has already been finalised, which is the live
// /ws/v1/t2a_v2_bidi ordering and the thing that used to silence a segment.
func stubT2AFull(t *testing.T, finalPerContinue, answerFlush, lateFlushAck bool) string {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	audio := hex.EncodeToString(make([]byte, 960))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		pendingFlush := false
		write := func(v any) error {
			data, _ := json.Marshal(v)
			return conn.WriteMessage(websocket.TextMessage, data)
		}
		_ = write(map[string]any{"event": "connected_success"})
		for {
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var in struct {
				Event string `json:"event"`
			}
			if json.Unmarshal(data, &in) != nil {
				return
			}
			switch in.Event {
			case "task_start":
				_ = write(map[string]any{"event": "task_started"})
			case "task_continue":
				_ = write(map[string]any{
					"event": "task_continued",
					"data":  map[string]any{"audio": audio, "status": 1},
				})
				if finalPerContinue {
					_ = write(map[string]any{"event": "task_continued", "is_final": true})
				}
				if pendingFlush {
					pendingFlush = false
					_ = write(map[string]any{"event": "task_flushed"})
				}
			case "task_flush":
				if lateFlushAck {
					pendingFlush = true
					break
				}
				if answerFlush {
					_ = write(map[string]any{"event": "task_flushed"})
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/v1/t2a_v2"
}

// TestTraceNamesWhatEndedTheSegment checks the diagnostic itself, because a
// diagnostic that reports the wrong thing is worse than none: this is what is
// run against a live account to decide whether an endpoint is at fault.
func TestTraceNamesWhatEndedTheSegment(t *testing.T) {
	cases := []struct {
		name             string
		finalPerContinue bool
		answerFlush      bool
		steps            []string
		want             []string
	}{
		{
			name:             "finalises every continue",
			finalPerContinue: true,
			steps:            []string{"continue", "read"},
			want:             []string{"is_final"},
		},
		{
			name:        "streaming, no flush: nothing ends it",
			answerFlush: true,
			steps:       []string{"continue", "read"},
			want:        []string{"deadline"},
		},
		{
			name:        "streaming, flush ends it",
			answerFlush: true,
			steps:       []string{"continue", "flush", "read"},
			want:        []string{"task_flushed"},
		},
		{
			name:             "two segments on one task",
			finalPerContinue: true,
			steps:            []string{"continue", "read", "continue", "read"},
			want:             []string{"is_final", "is_final"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := stubT2A(t, tc.finalPerContinue, tc.answerFlush)
			got, audio, err := traceOne(
				traceCase{endpoint: endpoint, name: tc.name, steps: tc.steps},
				"test-key", "speech-2.8-turbo", "v1", 24000, 900*time.Millisecond)
			if err != nil {
				t.Fatalf("traceOne: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("segment %d ended on %q, want %q (audio %v)", i, got[i], tc.want[i], audio)
				}
			}
			for i, a := range audio {
				if a == 0 && tc.want[i] != "deadline" {
					t.Errorf("segment %d counted no audio; the trace would understate the server", i)
				}
			}
		})
	}
}
