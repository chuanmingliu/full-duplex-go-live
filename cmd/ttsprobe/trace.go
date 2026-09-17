package main

// trace.go answers one question that no amount of reading the adapter can
// settle: on each MiniMax T2A endpoint, what actually ends a segment?
//
// The adapter stops reading a segment on is_final or on task_flushed. That is
// an assumption about the server, and the two endpoints appear not to share it:
// /ws/v1/t2a_v2 finalises every task_continue, while a bidirectional stream has
// no reason to — text keeps arriving, audio keeps coming back, and is_final
// describes the task rather than each piece of text. If that is right, a
// sentence-ending segment sent without a flush has nothing to terminate it and
// sits until the read deadline, which is what a caller hears as an answer whose
// opening fragment plays and whose rest never does.
//
// So: send one task_continue, send no flush, print every frame that comes back
// with the time it arrived, and stop at a terminator or at the deadline. Then
// do it again with a flush. The difference between those two traces is the
// whole answer.
//
//	ttsprobe -trace                    # both endpoints, with and without flush
//	ttsprobe -trace -trace-wait 40s    # if you suspect the server is merely slow
//
// It prints event names, flags, frame sizes and timings. It never prints the
// credential, and it reads it from the dotenv file itself rather than taking it
// on the command line.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// traceFrame is the subset of a T2A frame worth printing.
type traceFrame struct {
	Event    string `json:"event"`
	IsFinal  bool   `json:"is_final"`
	BaseResp *struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
	Data *struct {
		Audio  string `json:"audio"`
		Status int    `json:"status"`
	} `json:"data"`
}

type traceCase struct {
	endpoint string
	name     string
	// steps is what to send after task_start, in order. A step of "" means
	// "read until this segment ends" — everything else is an event to send.
	steps []string
}

// runTrace executes every scenario on both endpoints and reports what happened
// to each segment.
func runTrace(endpoint, apiKey, model, voice string, rate int, wait time.Duration) error {
	if apiKey == "" {
		return fmt.Errorf("no MINIMAX_TTS_API_KEY; -trace reads it from the dotenv file")
	}
	plain, bidi := endpointPair(endpoint)

	// Scenarios, in the order they narrow things down. The first two ask what
	// terminates a single segment. The rest are what a real turn does and a
	// single segment cannot show: a second sentence on the same task, and a
	// barge-in — golive sends task_cancel there on the bidirectional endpoint
	// and drains on the plain one, which is a real behavioural difference
	// between the two and a candidate for "works on one, breaks on the other".
	scenarios := []struct {
		name  string
		steps []string
	}{
		{"one segment, no flush", []string{"continue", "read"}},
		{"one segment, flush", []string{"continue", "flush", "read"}},
		{"two segments on one task", []string{"continue", "read", "continue", "read"}},
		{"three segments on one task", []string{"continue", "read", "continue", "read", "continue", "read"}},
		{"flush, then another segment", []string{"continue", "flush", "read", "continue", "read"}},
		{"cancel, then another segment", []string{"continue", "cancel", "drain", "continue", "read"}},
	}

	fmt.Printf("ttsprobe -trace: %s deadline per read\n", wait)
	fmt.Printf("text: %q (ends a sentence)\n\n", traceText)

	type result struct {
		endpoint string
		name     string
		endings  []string
		audio    []int
	}
	var results []result

	for _, ep := range []string{plain, bidi} {
		for _, sc := range scenarios {
			fmt.Printf("── %s\n   %s\n", ep, sc.name)
			endings, audio, err := traceOne(traceCase{endpoint: ep, name: sc.name, steps: sc.steps},
				apiKey, model, voice, rate, wait)
			if err != nil {
				fmt.Printf("   error: %v\n", err)
				endings = append(endings, "error: "+err.Error())
			}
			fmt.Printf("   → %v  audio=%v\n\n", endings, audio)
			results = append(results, result{ep, sc.name, endings, audio})
		}
	}

	fmt.Println("summary")
	fmt.Printf("  %-46s %-32s %s\n", "endpoint", "scenario", "each segment ended on / audio bytes")
	for _, r := range results {
		fmt.Printf("  %-46s %-32s %v %v\n", r.endpoint, r.name, r.endings, r.audio)
	}
	fmt.Println()
	fmt.Println("What to look for: a segment that ends on \"deadline\", or that returns 0 bytes,")
	fmt.Println("in a scenario where the other endpoint is fine.")
	fmt.Println()
	fmt.Println("\"flush, then another segment\" is the shape of a real greeting, and the row")
	fmt.Println("that matters most. If its second segment ends on task_flushed with 0 bytes,")
	fmt.Println("the flush acknowledgement arrived behind the terminator the first segment")
	fmt.Println("already returned on, and the next segment inherited it.")
	return nil
}

const traceText = "众安保险的车险管家小翠儿。"

// traceOne runs one scripted scenario and returns what ended each segment.
func traceOne(c traceCase, apiKey, model, voice string, rate int, wait time.Duration) ([]string, []int, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+apiKey)

	ctx, cancel := context.WithTimeout(context.Background(), wait*4+30*time.Second)
	defer cancel()

	conn, resp, err := dialer.DialContext(ctx, c.endpoint, header)
	if err != nil {
		if resp != nil {
			return nil, nil, fmt.Errorf("dial: HTTP %d: %w", resp.StatusCode, err)
		}
		return nil, nil, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	send := func(v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return conn.WriteMessage(websocket.TextMessage, data)
	}
	origin := time.Now()
	read := func(deadline time.Duration) (*traceFrame, error) {
		_ = conn.SetReadDeadline(time.Now().Add(deadline))
		_, data, err := conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		var f traceFrame
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("malformed frame: %w", err)
		}
		audio := 0
		if f.Data != nil && f.Data.Audio != "" {
			audio = hex.DecodedLen(len(f.Data.Audio))
		}
		extra := ""
		if f.BaseResp != nil && f.BaseResp.StatusCode != 0 {
			extra = fmt.Sprintf("  base_resp=%d:%s", f.BaseResp.StatusCode, f.BaseResp.StatusMsg)
		}
		// Audio frames are the bulk and say nothing individually; only the
		// first of each run is printed, with the rest summarised by the
		// per-segment totals.
		if audio == 0 || extra != "" {
			fmt.Printf("   %6dms  event=%-18s is_final=%-5v audio=%-7d%s\n",
				time.Since(origin).Milliseconds(), f.Event, f.IsFinal, audio, extra)
		}
		return &f, nil
	}

	if err := send(map[string]any{
		"event": "task_start",
		"model": model,
		"voice_setting": map[string]any{
			"voice_id": voice, "speed": 1.0, "vol": 1.0, "pitch": 0,
		},
		"audio_setting": map[string]any{
			"sample_rate": rate, "format": "pcm", "channel": 1,
		},
	}); err != nil {
		return nil, nil, err
	}
	for {
		f, err := read(15 * time.Second)
		if err != nil {
			return nil, nil, fmt.Errorf("handshake: %w", err)
		}
		if f.BaseResp != nil && f.BaseResp.StatusCode != 0 {
			return nil, nil, fmt.Errorf("task_start rejected: %d %s", f.BaseResp.StatusCode, f.BaseResp.StatusMsg)
		}
		if f.Event == "task_started" {
			break
		}
	}

	var endings []string
	var audios []int

	// readSegment consumes until something terminates the segment.
	readSegment := func(label string) {
		audio := 0
		deadline := time.Now().Add(wait)
		for {
			left := time.Until(deadline)
			if left <= 0 {
				endings, audios = append(endings, label+"deadline"), append(audios, audio)
				return
			}
			f, err := read(left)
			if err != nil {
				if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline") {
					endings, audios = append(endings, label+"deadline"), append(audios, audio)
					return
				}
				endings, audios = append(endings, label+"closed:"+err.Error()), append(audios, audio)
				return
			}
			if f.Data != nil && f.Data.Audio != "" {
				audio += hex.DecodedLen(len(f.Data.Audio))
			}
			if f.BaseResp != nil && f.BaseResp.StatusCode != 0 {
				endings, audios = append(endings, fmt.Sprintf("%sbase_resp %d", label, f.BaseResp.StatusCode)), append(audios, audio)
				return
			}
			switch {
			case f.Event == "task_failed":
				endings, audios = append(endings, label+"task_failed"), append(audios, audio)
				return
			case f.IsFinal:
				endings, audios = append(endings, label+"is_final"), append(audios, audio)
				return
			case f.Event == "task_flushed":
				endings, audios = append(endings, label+"task_flushed"), append(audios, audio)
				return
			case f.Event == "task_canceled" || f.Event == "task_cancelled":
				endings, audios = append(endings, label+"task_canceled"), append(audios, audio)
				return
			}
		}
	}

	for _, step := range c.steps {
		switch step {
		case "continue":
			if err := send(map[string]any{"event": "task_continue", "text": traceText}); err != nil {
				return endings, audios, err
			}
		case "flush":
			if err := send(map[string]any{"event": "task_flush"}); err != nil {
				return endings, audios, err
			}
		case "cancel":
			// A barge-in: stop reading partway, then cancel, which is exactly
			// what the engine does.
			if _, err := read(wait); err != nil {
				return endings, audios, err
			}
			if err := send(map[string]any{"event": "task_cancel"}); err != nil {
				return endings, audios, err
			}
		case "drain", "read":
			readSegment("")
		}
	}
	return endings, audios, nil
}
