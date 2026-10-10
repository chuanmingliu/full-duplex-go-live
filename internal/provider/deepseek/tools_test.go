package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
)

func testLLM(t *testing.T, handler http.HandlerFunc) *LLM {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	t.Setenv("TEST_LLM_KEY", "k")
	t.Setenv("GOLIVE_BACKEND_BASE_URL", ts.URL)
	l, err := New("TEST_LLM_KEY", ts.URL, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func sse(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// TestToolRoundTripIsWellFormed pins the request shape for the second half of a
// function call: the assistant message that made the calls, then one tool
// message per call.
//
// It is the shape every chat-completions backend validates and the one the
// engine used to get wrong — it sent the tool result with no assistant message
// in front of it, which a real server rejects with a 400 on that request and on
// every request after it, since the orphan is in history for the rest of the
// call.
func TestToolRoundTripIsWellFormed(t *testing.T) {
	var got struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			Name       string `json:"name"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
		Tools             []json.RawMessage `json:"tools"`
		ToolChoice        any               `json:"tool_choice"`
		ParallelToolCalls *bool             `json:"parallel_tool_calls"`
	}
	l := testLLM(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		sse(w, `{"choices":[{"delta":{"content":"明天晴。"}}]}`)
	})

	no := false
	deltas, err := l.Stream(context.Background(), provider.LLMRequest{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "明天天气"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
				{ID: "call_1", Name: "weather", Arguments: `{"city":"上海"}`},
				{ID: "call_2", Name: "calendar"},
			}},
			{Role: provider.RoleTool, ToolCallID: "call_1", Name: "weather", Content: `{"sky":"晴"}`},
			{Role: provider.RoleTool, ToolCallID: "call_2", Name: "calendar", Content: `[]`},
		},
		Tools:             []json.RawMessage{json.RawMessage(`{"type":"function","function":{"name":"weather"}}`)},
		ToolChoice:        "auto",
		ParallelToolCalls: &no,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range deltas {
	}

	if len(got.Messages) != 4 {
		t.Fatalf("sent %d messages, want 4", len(got.Messages))
	}
	asst := got.Messages[1]
	if len(asst.ToolCalls) != 2 {
		t.Fatalf("the assistant message carries %d tool calls, want 2", len(asst.ToolCalls))
	}
	if c := asst.ToolCalls[0]; c.ID != "call_1" || c.Type != "function" ||
		c.Function.Name != "weather" || c.Function.Arguments != `{"city":"上海"}` {
		t.Errorf("first tool call is malformed: %+v", c)
	}
	if c := asst.ToolCalls[1]; c.Function.Arguments != "{}" {
		t.Errorf("a call with no arguments must still send a JSON object, got %q", c.Function.Arguments)
	}
	for i, id := range []string{"call_1", "call_2"} {
		m := got.Messages[2+i]
		if m.Role != "tool" || m.ToolCallID != id {
			t.Errorf("message %d should answer %s; got role=%q tool_call_id=%q", 2+i, id, m.Role, m.ToolCallID)
		}
		if m.Name != "" {
			t.Errorf("tool message %d carries name=%q; strict servers reject it", 2+i, m.Name)
		}
	}
	if got.ToolChoice != "auto" {
		t.Errorf("tool_choice = %v, want auto", got.ToolChoice)
	}
	if got.ParallelToolCalls == nil || *got.ParallelToolCalls {
		t.Errorf("parallel_tool_calls=false was not sent: %v", got.ParallelToolCalls)
	}
}

// A request with no tools must not mention tool settings at all.
func TestToolSettingsAreOmittedWithoutTools(t *testing.T) {
	var raw map[string]json.RawMessage
	l := testLLM(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &raw)
		sse(w)
	})
	yes := true
	deltas, err := l.Stream(context.Background(), provider.LLMRequest{
		Messages:          []provider.Message{{Role: provider.RoleUser, Content: "你好"}},
		ToolChoice:        "auto",
		ParallelToolCalls: &yes,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range deltas {
	}
	for _, key := range []string{"tools", "tool_choice", "parallel_tool_calls"} {
		if _, ok := raw[key]; ok {
			t.Errorf("%s was sent on a request with no tools", key)
		}
	}
}

// Calls arrive as fragments keyed by index and must come out whole, in the
// order the model made them, with the provider's cache report alongside.
func TestStreamedToolCallsKeepTheirOrder(t *testing.T) {
	l := testLLM(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"second","arguments":"{\"n\""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"first","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":":2}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":900,"completion_tokens":12,"prompt_cache_hit_tokens":832}}`,
		)
	})
	deltas, err := l.Stream(context.Background(), provider.LLMRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "查一下"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var calls []provider.ToolCall
	var usage *provider.TokenUsage
	for d := range deltas {
		if d.ToolCall != nil {
			calls = append(calls, *d.ToolCall)
		}
		if d.Usage != nil {
			usage = d.Usage
		}
	}
	if len(calls) != 2 || calls[0].Name != "first" || calls[1].Name != "second" {
		t.Fatalf("calls arrived as %+v; want first then second", calls)
	}
	if calls[1].Arguments != `{"n":2}` {
		t.Errorf("fragmented arguments were not reassembled: %q", calls[1].Arguments)
	}
	if usage == nil || usage.CachedInputTokens != 832 || usage.InputTokens != 900 {
		t.Errorf("usage = %+v; want 900 prompt tokens with 832 cached", usage)
	}
}
