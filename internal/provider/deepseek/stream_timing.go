package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

var streamRequestSeq atomic.Uint64

// streamTiming is owned by one request: Stream until headers, then consume.
// It keeps fixed-size metadata only, never a copy/hash of text or arguments.
// These are adapter observation times: local scheduling, parsing and blocked
// downstream sends can delay later phases. They are not pure provider timings.
type streamTiming struct {
	log                                                         *slog.Logger
	id                                                          string
	started                                                     time.Time
	lane, key                                                   int
	headers, firstData, firstContent, firstTool, firstValidArgs int64
	finish, done, eof                                           int64
	frames, malformed, toolFragments, argumentBytes             int
	argumentProbes, argumentProbeBytes                          int
	argsProbeLimited                                            bool
	finishReason                                                string
	httpStatus                                                  int
}

func newStreamTiming(ctx context.Context, started time.Time, ln *lane) *streamTiming {
	log := slog.Default()
	if !log.Enabled(ctx, slog.LevelDebug) {
		return nil
	}
	return &streamTiming{log: log, id: fmt.Sprintf("llmreq_%d", streamRequestSeq.Add(1)), started: started, lane: ln.n, key: ln.keyN + 1,
		headers: -1, firstData: -1, firstContent: -1, firstTool: -1, firstValidArgs: -1, finish: -1, done: -1, eof: -1}
}
func (t *streamTiming) requestID() string {
	if t == nil {
		return ""
	}
	return t.id
}
func (t *streamTiming) stamp(dst *int64) {
	if *dst < 0 {
		*dst = time.Since(t.started).Milliseconds()
	}
}
func (t *streamTiming) response(status int) {
	if t != nil {
		t.stamp(&t.headers)
		t.httpStatus = status
	}
}
func (t *streamTiming) data() {
	if t != nil {
		t.stamp(&t.firstData)
		t.frames++
	}
}
func (t *streamTiming) malformedFrame() {
	if t != nil {
		t.malformed++
	}
}
func (t *streamTiming) content() {
	if t != nil {
		t.stamp(&t.firstContent)
	}
}
func (t *streamTiming) toolFragment(argumentBytes int) {
	if t != nil {
		t.stamp(&t.firstTool)
		t.toolFragments++
		t.argumentBytes += argumentBytes
	}
}

func (t *streamTiming) arguments(name, args string) {
	if t == nil || t.firstValidArgs >= 0 || name == "" || len(args) == 0 {
		return
	}
	// Avoid repeatedly parsing unbounded model output solely for telemetry.
	// Only object-shaped, potentially complete arguments up to 64 KiB are
	// probed. Total extra parsing is capped at 256 probes / 256 KiB per request.
	// Exceeding a cap does not change what consume passes downstream.
	if len(args) > 64<<10 {
		t.argsProbeLimited = true
		return
	}
	if t.argumentProbes >= 256 || t.argumentProbeBytes+len(args) > 256<<10 {
		t.argsProbeLimited = true
		return
	}
	t.argumentProbes++
	t.argumentProbeBytes += len(args)
	args = strings.TrimSpace(args)
	if len(args) < 2 || args[0] != '{' || args[len(args)-1] != '}' {
		return
	}
	if json.Valid([]byte(args)) {
		t.stamp(&t.firstValidArgs)
	}
}
func (t *streamTiming) finished(reason string) {
	if t == nil || t.finish >= 0 {
		return
	}
	t.stamp(&t.finish)
	switch reason {
	case "stop", "length", "tool_calls", "function_call", "content_filter":
		t.finishReason = reason
	default:
		t.finishReason = "other"
	}
}
func (t *streamTiming) doneFrame() {
	if t != nil {
		t.stamp(&t.done)
	}
}
func (t *streamTiming) endOfFile() {
	if t != nil {
		t.stamp(&t.eof)
	}
}
func (t *streamTiming) report(ctx context.Context, outcome string) {
	if t == nil {
		return
	}
	if ctx.Err() != nil {
		outcome = "canceled"
	}
	// One bounded summary per physical request, including losing/cancelled
	// hedge copies. Negative timing values mean that phase was not observed.
	t.log.Debug("llm: stream phases", "version", 1, "request_id", t.id, "lane", t.lane, "key", t.key,
		"headers_ms", t.headers, "first_data_ms", t.firstData, "first_content_ms", t.firstContent,
		"first_tool_fragment_ms", t.firstTool, "first_valid_args_ms", t.firstValidArgs,
		"finish_ms", t.finish, "done_ms", t.done, "eof_ms", t.eof, "end_ms", time.Since(t.started).Milliseconds(),
		"finish_reason", t.finishReason, "outcome", outcome, "http_status", t.httpStatus,
		"data_frames", t.frames, "malformed_frames", t.malformed, "tool_fragments", t.toolFragments,
		"argument_bytes", t.argumentBytes, "argument_probes", t.argumentProbes, "args_probe_limited", t.argsProbeLimited)
}
