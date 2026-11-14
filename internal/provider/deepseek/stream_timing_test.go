package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
)

type phaseBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *phaseBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}
func (b *phaseBuffer) snapshot() string { b.Lock(); defer b.Unlock(); return b.Buffer.String() }

type phaseTransport func(*http.Request) (*http.Response, error)

func (f phaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func phaseLogs(t *testing.T) *phaseBuffer {
	t.Helper()
	b := &phaseBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return b
}
func phaseClient(rt phaseTransport) *LLM {
	return &LLM{BaseURL: "http://fixture.invalid", APIKey: "secret-credential", DefaultModel: "fixture", HTTP: &http.Client{Transport: rt}}
}
func phaseSummary(t *testing.T, b *phaseBuffer) map[string]any {
	t.Helper()
	var summaries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.snapshot()), "\n") {
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) == nil && row["msg"] == "llm: stream phases" {
			summaries = append(summaries, row)
		}
	}
	if len(summaries) != 1 {
		t.Fatalf("phase summaries=%d, want exactly one", len(summaries))
	}
	return summaries[0]
}
func phaseDrain(t *testing.T, ch <-chan provider.LLMDelta) []provider.LLMDelta {
	t.Helper()
	var result []provider.LLMDelta
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case d, ok := <-ch:
			if !ok {
				return result
			}
			result = append(result, d)
		case <-timer.C:
			t.Fatal("stream did not close")
			return nil
		}
	}
}

type phaseSecondReadBody struct {
	io.ReadCloser
	reads int
	next  chan struct{}
}

func (b *phaseSecondReadBody) Read(p []byte) (int, error) {
	b.reads++
	if b.reads == 2 {
		close(b.next)
	}
	return b.ReadCloser.Read(p)
}

func TestStreamPhasesToolCallPreservesFinishBoundary(t *testing.T) {
	logs := phaseLogs(t)
	rd, wr := io.Pipe()
	ready := make(chan struct{})
	finish := make(chan struct{})
	var finishOnce sync.Once
	unblock := func() { finishOnce.Do(func() { close(finish) }) }
	t.Cleanup(unblock)
	t.Cleanup(func() { rd.Close(); wr.Close() })
	l := phaseClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: &phaseSecondReadBody{ReadCloser: rd, next: ready}, Header: http.Header{}}, nil
	})
	go func() {
		io.WriteString(wr, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"private-id\",\"function\":{\"name\":\"private-tool\",\"arguments\":\"{\\\"private-field\\\":\\\"private-value\\\"}\"}}]}}]}\n\n")
		<-finish
		io.WriteString(wr, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":7}}\n\ndata: [DONE]\n\n")
		wr.Close()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := l.Stream(ctx, provider.LLMRequest{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("fixture not consumed")
	}
	select {
	case <-ch:
		t.Fatal("tool call emitted before provider finish")
	default:
	}
	unblock()
	got := phaseDrain(t, ch)
	if len(got) != 2 || got[0].ToolCall == nil || got[0].ToolCall.Arguments != `{"private-field":"private-value"}` || got[1].Usage == nil {
		t.Fatal("tool/usage behavior changed")
	}
	s := phaseSummary(t, logs)
	for _, key := range []string{"headers_ms", "first_data_ms", "first_tool_fragment_ms", "first_valid_args_ms", "finish_ms", "done_ms", "end_ms"} {
		if n, ok := s[key].(float64); !ok || n < 0 {
			t.Errorf("missing %s", key)
		}
	}
	previous := float64(-1)
	for _, key := range []string{"headers_ms", "first_data_ms", "first_tool_fragment_ms", "first_valid_args_ms", "finish_ms", "done_ms", "end_ms"} {
		current := s[key].(float64)
		if current < previous {
			t.Fatalf("phase %s moved backward", key)
		}
		previous = current
	}
	if s["first_content_ms"] != float64(-1) || s["eof_ms"] != float64(-1) || s["outcome"] != "done" || s["finish_reason"] != "tool_calls" || s["tool_fragments"] != float64(1) {
		t.Fatalf("incorrect phase summary: %v", s)
	}
	for _, private := range []string{"secret-credential", "private-field", "private-value", "private-id", "private-tool"} {
		if strings.Contains(logs.snapshot(), private) {
			t.Fatalf("phase log leaked fixture %q", private)
		}
	}
	var open map[string]any
	for _, line := range strings.Split(logs.snapshot(), "\n") {
		var r map[string]any
		if json.Unmarshal([]byte(line), &r) == nil && r["msg"] == "llm: stream open" {
			open = r
		}
	}
	if s["request_id"] == nil || open["request_id"] != s["request_id"] {
		t.Fatal("stream-open correlation missing")
	}
}

func TestStreamPhasesNormalMalformedAndPartial(t *testing.T) {
	for _, tc := range []struct {
		name, body, outcome string
		text                string
		calls, malformed    int
		valid               bool
	}{
		{"normal", "data: {\"choices\":[{\"delta\":{\"content\":\"fixture answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", "done", "fixture answer", 0, 0, false},
		{"malformed", "data: broken-json\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"kept\"},\"finish_reason\":\"untrusted-finish-value\"}]}\n\n", "eof", "kept", 0, 1, false},
		{"partial", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"fixture\",\"arguments\":\"{\\\"field\\\":\"}}]}}]}\n\n", "eof", "", 0, 0, false},
		{"valid-eof", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"fixture\",\"arguments\":\"{}\"}}]}}]}\n\n", "eof", "", 1, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := phaseLogs(t)
			l := phaseClient(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{}}, nil
			})
			ch, err := l.Stream(context.Background(), provider.LLMRequest{})
			if err != nil {
				t.Fatal(err)
			}
			var text string
			calls := 0
			for _, d := range phaseDrain(t, ch) {
				text += d.Text
				if d.ToolCall != nil {
					calls++
				}
			}
			if text != tc.text || calls != tc.calls {
				t.Fatal("stream behavior changed")
			}
			s := phaseSummary(t, logs)
			if s["outcome"] != tc.outcome || s["malformed_frames"] != float64(tc.malformed) {
				t.Fatalf("summary %v", s)
			}
			if (s["first_valid_args_ms"].(float64) >= 0) != tc.valid {
				t.Fatal("argument completeness misreported")
			}
			if tc.name == "malformed" && (s["finish_reason"] != "other" || strings.Contains(logs.snapshot(), "untrusted-finish-value")) {
				t.Fatal("untrusted finish reason escaped allowlist")
			}
		})
	}
}

type phaseCancelBody struct {
	ctx    context.Context
	closed bool
}

func (b *phaseCancelBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (b *phaseCancelBody) Close() error             { b.closed = true; return nil }
func TestStreamPhasesCancellationAndRequestFailures(t *testing.T) {
	for _, mode := range []string{"cancel", "request_error", "http_error"} {
		t.Run(mode, func(t *testing.T) {
			logs := phaseLogs(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := &phaseCancelBody{ctx: ctx}
			l := phaseClient(func(*http.Request) (*http.Response, error) {
				if mode == "request_error" {
					return nil, errors.New("private-error-detail")
				}
				if mode == "http_error" {
					return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"fixture"}}`)), Header: http.Header{}}, nil
				}
				return &http.Response{StatusCode: 200, Body: body, Header: http.Header{}}, nil
			})
			ch, err := l.Stream(ctx, provider.LLMRequest{})
			if mode == "cancel" {
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				phaseDrain(t, ch)
				if !body.closed {
					t.Fatal("body not closed")
				}
			} else if err == nil {
				t.Fatal("error lost")
			}
			s := phaseSummary(t, logs)
			want := mode
			if mode == "cancel" {
				want = "canceled"
			}
			if s["outcome"] != want {
				t.Fatalf("outcome=%v", s["outcome"])
			}
			if strings.Contains(logs.snapshot(), "private-error-detail") {
				t.Fatal("phase summary leaked error detail")
			}
		})
	}
}

type phaseErrorBody struct{}

func (phaseErrorBody) Read([]byte) (int, error) { return 0, errors.New("private-read-error") }
func (phaseErrorBody) Close() error             { return nil }

func TestStreamPhasesReadErrorPreserved(t *testing.T) {
	logs := phaseLogs(t)
	l := phaseClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: phaseErrorBody{}, Header: http.Header{}}, nil
	})
	ch, err := l.Stream(context.Background(), provider.LLMRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := phaseDrain(t, ch)
	if len(got) != 1 || got[0].Err == nil || !strings.Contains(got[0].Err.Error(), "private-read-error") {
		t.Fatal("read error changed")
	}
	s := phaseSummary(t, logs)
	if s["outcome"] != "read_error" || s["eof_ms"] != float64(-1) || strings.Contains(logs.snapshot(), "private-read-error") {
		t.Fatal("read-error telemetry incorrect")
	}
}

func TestStreamPhasesBoundedArgumentProbeDoesNotChangeOutput(t *testing.T) {
	logs := phaseLogs(t)
	args := `{"field":"` + strings.Repeat("x", 65<<10) + `"}`
	wire, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"name": "fixture", "arguments": args}}}}, "finish_reason": "tool_calls"}}})
	l := phaseClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: " + string(wire) + "\n\ndata: [DONE]\n\n")), Header: http.Header{}}, nil
	})
	ch, err := l.Stream(context.Background(), provider.LLMRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := phaseDrain(t, ch)
	if len(got) != 1 || got[0].ToolCall == nil || got[0].ToolCall.Arguments != args {
		t.Fatal("large arguments changed")
	}
	s := phaseSummary(t, logs)
	if s["args_probe_limited"] != true || s["first_valid_args_ms"] != float64(-1) {
		t.Fatal("probe cap not visible")
	}
	if len(logs.snapshot()) > 4096 {
		t.Fatal("telemetry grew with private argument size")
	}
}

func TestStreamPhasesManyShortArgumentFragments(t *testing.T) {
	logs := phaseLogs(t)
	args := `{"config":{"fixture":"` + strings.Repeat("x", 80) + `"}}`
	var body strings.Builder
	for _, character := range args {
		frame, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"name": "fixture", "arguments": string(character)}}}}}}})
		if err != nil {
			t.Fatal(err)
		}
		body.WriteString("data: " + string(frame) + "\n\n")
	}
	body.WriteString("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	l := phaseClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body.String())), Header: http.Header{}}, nil
	})
	ch, err := l.Stream(context.Background(), provider.LLMRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := phaseDrain(t, ch)
	if len(got) != 1 || got[0].ToolCall == nil || got[0].ToolCall.Arguments != args {
		t.Fatal("fragmented tool arguments changed")
	}
	s := phaseSummary(t, logs)
	if s["tool_fragments"] != float64(len(args)) || s["first_valid_args_ms"].(float64) < 0 || s["args_probe_limited"] != false {
		t.Fatalf("ordinary fragmented arguments exhausted telemetry: %v", s)
	}
}

func TestStreamPhasesProbeBudgetAndUniqueIDs(t *testing.T) {
	phaseLogs(t)
	a := newStreamTiming(context.Background(), time.Now(), &lane{})
	b := newStreamTiming(context.Background(), time.Now(), &lane{})
	if a.id == b.id {
		t.Fatal("request IDs collide")
	}
	for i := 0; i < 300; i++ {
		a.arguments("fixture", `{invalid}`)
	}
	a.arguments("fixture", `{}`)
	if a.argumentProbes != 256 || a.firstValidArgs >= 0 || !a.argsProbeLimited {
		t.Fatal("probe-count budget bypassed")
	}
	for i := 0; i < 10; i++ {
		b.arguments("fixture", "{"+strings.Repeat("x", 60000)+"}")
	}
	if b.argumentProbes != 4 || b.argumentProbeBytes > 256<<10 || !b.argsProbeLimited {
		t.Fatal("probe-byte budget bypassed")
	}
}
