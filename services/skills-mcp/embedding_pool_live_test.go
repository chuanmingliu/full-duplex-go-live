package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Requests use the real model endpoints. Failures are injected in local proxy
// handlers so the deployment remains available to other callers throughout.
func TestEmbeddingPoolLiveEndpoints(t *testing.T) {
	if os.Getenv("GOLIVE_TEST_EMBEDDINGS") != "1" || os.Getenv("SKILLS_EMBEDDING_POOL_FILE") == "" {
		t.Skip("requires opt-in and a configured real embedding pool")
	}
	configured, err := embeddingClientFromEnv()
	if err != nil || configured == nil || len(configured.members) != 2 {
		t.Fatalf("expected a two-member live pool: %v", err)
	}
	var offline [2]atomic.Bool
	pool := &embeddingClient{}
	for i, source := range configured.members {
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if offline[i].Load() {
				http.Error(w, "injected endpoint outage", http.StatusServiceUnavailable)
				return
			}
			request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, source.endpoint, r.Body)
			if err != nil {
				http.Error(w, "proxy request failed", 502)
				return
			}
			request.Header = r.Header.Clone()
			response, err := source.http.Do(request)
			if err != nil {
				http.Error(w, "model request failed", 502)
				return
			}
			defer response.Body.Close()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(response.StatusCode)
			io.Copy(w, response.Body)
		}))
		t.Cleanup(proxy.Close)
		pool.members = append(pool.members, &embeddingClient{
			endpoint: proxy.URL, model: source.model, token: source.token,
			name: source.name, revision: source.revision,
			queryPrefix: source.queryPrefix, documentPrefix: source.documentPrefix,
			http: proxy.Client(),
		})
	}
	data, err := json.Marshal(liveEmbeddingCatalog())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := openStoreWithEmbeddings(path, backend{}, pool)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler(store, "admin-private", "runtime-private"))
	defer server.Close()
	fixture := fixture{t, store, server.URL}
	check := func(phase, text, expected, provider string) {
		t.Helper()
		start := time.Now()
		out := selectionResult(t, fixture.rpc("skills/search", obj{"context": text, "search_mode": "semantic", "limit": 1}))
		if out.EmbeddingProvider != provider || len(out.Matches) != 1 || out.Matches[0].Name != expected {
			t.Fatalf("%s: %+v", phase, out)
		}
		t.Logf("phase=%s provider=%s result=%s elapsed_ms=%.2f", phase, out.EmbeddingProvider, out.Matches[0].Name, float64(time.Since(start).Microseconds())/1000)
	}
	check("primary", "How much does it cost to insure my car?", "quote", pool.members[0].name)
	offline[0].Store(true)
	check("primary-outage", "我想给车买保险，帮我算一下保费。", "quote", pool.members[1].name)
	for _, query := range []struct{ text, expected string }{
		{"How much would it cost to insure my car?", "quote"},
		{"Where can I find the service notification in WeChat?", "wechat"},
		{"微信里那条服务通知消息在哪儿找？", "wechat"},
		{"I forgot my password and cannot log in.", "password"},
		{"我忘记密码了，账号登不进去。", "password"},
		{"Where is my package and when will it arrive?", "delivery"},
		{"我的包裹现在到哪里了，什么时候送到？", "delivery"},
	} {
		check("backup-bilingual", query.text, query.expected, pool.members[1].name)
	}
	offline[0].Store(false)
	time.Sleep(embeddingRetryDelay + 20*time.Millisecond)
	check("primary-recovered", "Please calculate the insurance premium for my vehicle.", "quote", pool.members[0].name)
	offline[1].Store(true)
	check("backup-outage", "给我的车计算一下保险价格。", "quote", pool.members[0].name)

	// Reverse preference to exercise BGE backing Qwen as well.
	offline[1].Store(false)
	reversed := newSkillIndex(liveEmbeddingCatalog())
	if err := reversed.prepareEmbeddings(context.Background(), &embeddingClient{members: []*embeddingClient{pool.members[1], pool.members[0]}}, nil); err != nil {
		t.Fatal(err)
	}
	offline[1].Store(true)
	out, err := reversed.search(context.Background(), skillRequest{Context: "I need a price for motor insurance", SearchMode: "semantic", Limit: 1})
	if err != nil || out.EmbeddingProvider != pool.members[0].name || len(out.Matches) != 1 || out.Matches[0].Name != "quote" {
		t.Fatal(out, err)
	}
	t.Logf("phase=reverse-failover provider=%s result=quote", out.EmbeddingProvider)
}
