package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/requestlog"
)

func TestRecordsExactRenderedWireRequestAndOutcome(t *testing.T) {
	for _, status := range []int{200, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var received []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received, _ = io.ReadAll(r.Body)
				w.Header().Set("X-Request-ID", "provider-request")
				w.WriteHeader(status)
				if status == 200 {
					io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}
			}))
			defer server.Close()
			root := t.TempDir()
			recorder, err := requestlog.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			llm := &LLM{BaseURL: server.URL, APIKey: "SECRET-HEADER", DefaultModel: "fixture", HTTP: server.Client(), Extra: map[string]any{"enable_thinking": false}, Requests: recorder}
			scope := requestlog.Scope{SessionID: "sess_recorded", TurnID: "item_7", ResponseID: "resp_7", Purpose: "turn"}
			ctx := requestlog.WithScope(context.Background(), scope)
			stream, err := llm.Stream(ctx, provider.LLMRequest{Messages: []provider.Message{{Role: "system", Content: "original prompt\nrendered memory skill"}, {Role: "user", Content: "one question please"}}, MaxTokens: 42})
			if status == 200 {
				if err != nil {
					t.Fatal(err)
				}
				for range stream {
				}
			} else if err == nil {
				t.Fatal("HTTP failure hidden")
			}
			paths, _ := filepath.Glob(filepath.Join(root, "sess_recorded", "*", "request.json"))
			if len(paths) != 1 {
				t.Fatal(paths)
			}
			body, _ := os.ReadFile(paths[0])
			if !bytes.Equal(body, received) || !bytes.Contains(body, []byte("rendered memory skill")) || !bytes.Contains(body, []byte("enable_thinking")) {
				t.Fatal("wire request differs from record")
			}
			metadata, _ := os.ReadFile(filepath.Join(filepath.Dir(paths[0]), "metadata.json"))
			var record requestlog.Metadata
			json.Unmarshal(metadata, &record)
			if record.Scope != scope || record.HTTPStatus != status || record.ProviderRequestID != "provider-request" || record.FinishedAt.IsZero() {
				t.Fatal(record)
			}
			if strings.Contains(string(metadata)+string(body), "SECRET-HEADER") {
				t.Fatal("authorization header persisted")
			}
		})
	}
}
