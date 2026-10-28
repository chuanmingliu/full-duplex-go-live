package deepseek

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
)

// A vendor's own field has to reach the vendor, and must not be a second way
// to change what the request already says.
func TestExtraFieldsAreSentAndReplaceNothing(t *testing.T) {
	var got map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = nil
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	t.Setenv("EXTRA_TEST_KEY", "k")
	llm, err := NewNamed("", "EXTRA_TEST_KEY", srv.URL, "the-model")
	if err != nil {
		t.Fatal(err)
	}
	llm.BaseURL, llm.DefaultModel = srv.URL, "the-model"
	llm.Extra, err = ParseExtra("enable_thinking=false, top_k=20, model=another, note=plain words")
	if err != nil {
		t.Fatal(err)
	}

	deltas, err := llm.Stream(context.Background(), provider.LLMRequest{
		Messages: []provider.Message{{Role: "user", Content: "你好"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range deltas {
	}

	want := map[string]string{
		"enable_thinking": "false",
		"top_k":           "20",
		"note":            `"plain words"`,
		"model":           `"the-model"`, // the request's own, not Extra's
		"stream":          "true",
	}
	for k, v := range want {
		if string(got[k]) != v {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}

	// And with nothing extra, nothing is added.
	llm.Extra = nil
	deltas, err = llm.Stream(context.Background(), provider.LLMRequest{
		Messages: []provider.Message{{Role: "user", Content: "你好"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range deltas {
	}
	if _, has := got["enable_thinking"]; has {
		t.Error("a request without Extra carried enable_thinking")
	}
}

func TestParseExtraRefusesAPairWithoutAName(t *testing.T) {
	for _, bad := range []string{"=false", "enable_thinking"} {
		if _, err := ParseExtra(bad); err == nil {
			t.Errorf("ParseExtra(%q) accepted it", bad)
		}
	}
	if m, err := ParseExtra(""); err != nil || len(m) != 0 {
		t.Errorf("ParseExtra(\"\") = %v, %v", m, err)
	}
}
