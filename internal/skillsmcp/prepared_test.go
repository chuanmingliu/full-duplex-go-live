package skillsmcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPreparedLoadOneRequestAndValidation(t *testing.T) {
	for _, fault := range []string{"", "digest", "uri", "version", "count", "frontmatter"} {
		t.Run(fault, func(t *testing.T) {
			text := "---\nname: \"test\"\ndescription: \"Description\"\n---\nInstruction"
			uri := "skill://test/SKILL.md"
			file := File{URI: uri, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text))), Size: len(text)}
			entry := Entry{URI: uri, Resources: []File{file}, Frontmatter: map[string]string{"name": "test", "description": "Description"}}
			version := 1
			resourceURI := uri
			switch fault {
			case "digest":
				text += "wrong"
			case "uri":
				resourceURI = "skill://other/SKILL.md"
			case "version":
				version = 2
			case "frontmatter":
				entry.Frontmatter["name"] = "wrong"
			}
			entry.URI = resourceURI
			resources := []any{map[string]any{"manifest": entry, "mimeType": "text/markdown", "text": text}}
			if fault == "count" {
				resources = nil
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req map[string]any
				json.NewDecoder(r.Body).Decode(&req)
				if req["method"] != "prompts/get" {
					t.Error("unexpected extra RPC", req["method"])
				}
				params := req["params"].(map[string]any)
				caps := params["_meta"].(map[string]any)["io.modelcontextprotocol/clientCapabilities"].(map[string]any)
				if caps["extensions"].(map[string]any)[turnPreparationExtension] == nil {
					t.Error("missing negotiation")
				}
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"_meta": map[string]any{routeKey: map[string]any{"revision": 3, "auto_selection": true, "prepared": map[string]any{"version": version, "retrieval": "semantic", "embedding_provider": "bge", "resources": resources}}}, "messages": []any{map[string]any{"content": map[string]any{"type": "resource_link", "uri": uri, "_meta": map[string]any{routeKey: file}}}}}})
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "token")
			os.WriteFile(path, []byte("private-test-token"), 0600)
			client, err := New(Config{URL: server.URL, TokenFile: path})
			if err != nil {
				t.Fatal(err)
			}
			trace := &Trace{}
			docs, _, err := client.PrepareWithTrace(context.Background(), "test", nil, trace)
			if calls != 1 || (err != nil) != (fault != "") {
				t.Fatal(calls, err)
			}
			if fault == "" && (len(docs) != 1 || docs[0].Text != "Instruction" || trace.EmbeddingProvider != "bge") {
				t.Fatal(docs, trace)
			}
		})
	}
}

// Opt-in read-only check through the normal host entry point. The endpoint is
// the deployed MCP service or an isolated compatible fixture.
func TestPreparedLiveEndpoint(t *testing.T) {
	if os.Getenv("GOLIVE_TEST_PREPARED") != "1" {
		t.Skip("opt-in deployed preparation check")
	}
	client, err := New(Config{URL: os.Getenv("SKILLS_SIM_URL"), TokenFile: os.Getenv("SKILLS_SIM_TOKEN_FILE")})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		text, memory string
		want         bool
	}{
		{"请给我报价", `{}`, true},
		{"服务通知在哪里找", `{"wechat":{"status":"consented"}}`, true},
		{"服务通知在哪里找", `{"wechat":{"status":"added"}}`, false},
		{"你好", `{}`, false},
	} {
		trace := &Trace{}
		guides, version, err := client.PrepareWithTrace(context.Background(), tc.text, json.RawMessage(tc.memory), trace)
		if err != nil || (len(guides) > 0) != tc.want || trace.Retrieval == "" {
			t.Fatal(err, len(guides), trace)
		}
		for _, step := range trace.Steps {
			if step.Stage == "skills/get" || step.Stage == "resources/read" {
				t.Fatal("extra resource roundtrip", trace)
			}
		}
		t.Logf("prepared=true resources=%d renderer=%d retrieval=%s revision=%d", len(guides), version, trace.Retrieval, trace.Revision)
	}
}
