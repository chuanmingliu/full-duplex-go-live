package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Deterministic vectors exercise transport, ranking and cache behavior. Model
// relevance and inference latency need a separately configured real endpoint.
type embeddingFixture struct {
	mu      sync.Mutex
	inputs  [][]string
	fail    bool
	block   chan struct{}
	started chan struct{}
}

func newEmbeddingFixture(t *testing.T) (*embeddingFixture, *embeddingClient) {
	t.Helper()
	f := &embeddingFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer embedding-secret" {
			t.Error("missing embedding credential")
		}
		var req struct {
			Input    []string `json:"input"`
			Model    string   `json:"model"`
			Encoding string   `json:"encoding_format"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Model != "test-model" || req.Encoding != "float" {
			t.Error("bad embedding request")
		}
		f.mu.Lock()
		f.inputs = append(f.inputs, req.Input)
		fail, block, started := f.fail, f.block, f.started
		f.mu.Unlock()
		if block != nil {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-block:
			case <-r.Context().Done():
				return
			}
		}
		if fail {
			http.Error(w, "sensitive provider error", 503)
			return
		}
		rows := []obj{}
		for i := len(req.Input) - 1; i >= 0; i-- {
			text := req.Input[i]
			vector := []float32{-1, 0}
			if strings.Contains(text, "insurance") || strings.Contains(text, "cover my car") {
				vector = []float32{4, 0}
			}
			if strings.Contains(text, "messaging") {
				vector = []float32{0, 3}
			}
			rows = append(rows, obj{"index": i, "embedding": vector})
		}
		json.NewEncoder(w).Encode(obj{"data": rows})
	}))
	t.Cleanup(server.Close)
	return f, &embeddingClient{endpoint: server.URL, model: "test-model", token: "embedding-secret", queryPrefix: "query: ", documentPrefix: "passage: ", http: server.Client()}
}

func (f *embeddingFixture) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.inputs) }

func embeddingCatalog() Catalog {
	c := defaults()
	c.SkillSelection = &SkillSelectionConfig{Mode: "always"}
	c.Skills = []Skill{
		{Name: "motor", Description: "Calculate insurance premiums", Body: "private instructions", Enabled: true, Events: []string{"quote.requested"}},
		{Name: "chat", Description: "Help with messaging", Body: "chat", Enabled: true},
		{Name: "consent", Description: "insurance requiring consent", Body: "consent", Enabled: true, Priority: 100, State: map[string][]string{"consent.status": {"yes"}}},
		{Name: "disabled", Description: "insurance disabled", Body: "disabled", Enabled: false},
	}
	return c
}

func TestEmbeddingSearchThroughMCP(t *testing.T) {
	f, client := newEmbeddingFixture(t)
	path := filepath.Join(t.TempDir(), "catalog.json")
	data, _ := json.Marshal(embeddingCatalog())
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := openStoreWithEmbeddings(path, backend{}, client)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler(s, "admin-private", "runtime-private"))
	t.Cleanup(server.Close)
	rpc := fixture{t, s, server.URL}
	for _, call := range []func(string, obj) obj{rpc.rpc, func(method string, p obj) obj { return codeRPC(rpc, method, p) }} {
		out := selectionResult(t, call("skills/search", obj{"context": "How much to cover my car?", "search_mode": "semantic", "max_bytes": 1024}))
		if out.Retrieval != "semantic" || len(out.Matches) != 1 || out.Matches[0].Name != "motor" || math.Abs(*out.Matches[0].Similarity-1) > 1e-6 || !replyFits(out, 1024) {
			t.Fatal(out)
		}
	}
	if f.count() != 2 {
		t.Fatalf("wanted one document batch and one cached query, got %d", f.count())
	}
	code := codeResult(rpc, obj{"code": `const found = await tools.skills_search({context:"How much to cover my car?",search_mode:"semantic"}); return await tools.skills_load({revision:found.revision,uris:found.matches.map(x=>x.uri)});`})
	if code["isError"] == true || code["structuredContent"].(obj)["result"].(obj)["status"] != "ok" {
		t.Fatal(code)
	}
	lexical, err := s.index.search(context.Background(), skillRequest{Context: "How much to cover my car?", SearchMode: "lexical"})
	if err != nil || len(lexical.Matches) != 0 {
		t.Fatal(lexical, err)
	}
	guarded, err := s.index.search(context.Background(), skillRequest{Context: "How much to cover my car?", State: map[string]string{"consent.status": "yes"}, Limit: 1})
	if err != nil || guarded.Matches[0].Name != "consent" || !guarded.Truncated {
		t.Fatal(guarded, err)
	}
	filtered, err := s.index.search(context.Background(), skillRequest{Context: "How much to cover my car?", Event: "quote.requested", State: map[string]string{"consent.status": "yes"}})
	if err != nil || len(filtered.Matches) != 1 || filtered.Matches[0].Name != "motor" {
		t.Fatal(filtered, err)
	}
	f.mu.Lock()
	for _, batch := range f.inputs {
		for _, input := range batch {
			if strings.Contains(input, "private instructions") || strings.Contains(input, "insurance disabled") {
				t.Error("sent body or unavailable skill")
			}
		}
	}
	f.mu.Unlock()
}

func TestEmbeddingPublicationCacheAndFailure(t *testing.T) {
	f, client := newEmbeddingFixture(t)
	s, err := openEmbeddingTestStore(t, client)
	if err != nil {
		t.Fatal(err)
	}
	old := s.index
	c := s.catalog
	c.Skills = append([]Skill(nil), c.Skills...)
	c.Skills[0].Body = "updated instructions"
	c, err = s.publish(c)
	if err != nil || f.count() != 1 || s.index == old {
		t.Fatal(err, f.count())
	}
	c.Skills = append([]Skill(nil), c.Skills...)
	c.Skills[0].Description = "insurance updated description"
	c, err = s.publish(c)
	if err != nil || f.count() != 2 {
		t.Fatal(err, f.count())
	}
	f.mu.Lock()
	if !reflect.DeepEqual(f.inputs[1], []string{"passage: insurance updated description"}) {
		t.Error(f.inputs)
	}
	f.fail = true
	f.mu.Unlock()
	before, _ := os.ReadFile(s.path)
	current := s.index
	c.Skills = append([]Skill(nil), c.Skills...)
	c.Skills[0].Description = "another new description"
	if _, err = s.publish(c); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(s.path)
	if string(before) != string(after) || s.index != current {
		t.Fatal("failed publication changed active revision")
	}
	out, err := s.index.search(context.Background(), skillRequest{Query: "insurance"})
	if err != nil || out.Retrieval != "lexical" || out.FallbackReason != "embedding_unavailable" || len(out.Matches) == 0 {
		t.Fatal(out, err)
	}
	if _, err = s.index.search(context.Background(), skillRequest{Query: "insurance", SearchMode: "semantic"}); err == nil {
		t.Fatal("explicit semantic search hid failure")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.index.search(ctx, skillRequest{Query: "insurance"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestEmbeddingPublicationDoesNotBlockReaders(t *testing.T) {
	f, client := newEmbeddingFixture(t)
	s, err := openEmbeddingTestStore(t, client)
	if err != nil {
		t.Fatal(err)
	}
	c := s.catalog
	c.Skills = append([]Skill(nil), c.Skills...)
	c.Skills[0].Description = "insurance changed"
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	f.mu.Lock()
	f.block = block
	f.started = started
	f.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := s.publish(c); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("publication did not start")
	}
	read := make(chan int, 1)
	go func() { s.mu.RLock(); revision := s.catalog.Revision; s.mu.RUnlock(); read <- revision }()
	select {
	case revision := <-read:
		if revision != c.Revision {
			t.Error(revision)
		}
	case <-time.After(time.Second):
		t.Error("embedding request held store lock")
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := s.publish(c); !errors.Is(err, errConflict) {
		t.Fatal("stale publish accepted", err)
	}
}

func TestEmbeddingResponseValidation(t *testing.T) {
	for name, body := range map[string]string{
		"missing":       "{}",
		"missing-index": `{"data":[{"embedding":[1,0]}]}`,
		"wrong-index":   `{"data":[{"index":1,"embedding":[1,0]}]}`,
		"zero":          `{"data":[{"index":0,"embedding":[0,0]}]}`,
		"empty":         `{"data":[{"index":0,"embedding":[]}]}`,
		"overflow":      `{"data":[{"index":0,"embedding":[1e100]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			client := &embeddingClient{endpoint: server.URL, http: server.Client()}
			if _, err := client.embed(context.Background(), []string{"test"}); err == nil {
				t.Fatal("accepted invalid response")
			}
		})
	}
}

func TestEmbeddingQueryDeadlineAndInputs(t *testing.T) {
	f, client := newEmbeddingFixture(t)
	x := newSkillIndex(embeddingCatalog())
	if err := x.prepareEmbeddings(context.Background(), client, nil); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.block = make(chan struct{})
	f.started = make(chan struct{}, 1)
	f.mu.Unlock()
	start := time.Now()
	out, err := x.search(context.Background(), skillRequest{Query: "insurance"})
	if err != nil || out.FallbackReason != "embedding_unavailable" || time.Since(start) > 2*time.Second {
		t.Fatal(out, err, time.Since(start))
	}
	for _, args := range []skillRequest{{Context: strings.Repeat("x", 8193)}, {Query: "x", SearchMode: "unknown"}, {Query: "x", MinSimilarity: floatPointer(2)}} {
		if _, err := x.search(context.Background(), args); err == nil {
			t.Fatal("invalid input accepted", args)
		}
	}
	plain := newSkillIndex(embeddingCatalog())
	if _, err := plain.search(context.Background(), skillRequest{Query: "x", SearchMode: "semantic"}); err == nil {
		t.Fatal("semantic enabled without provider")
	}
}

func floatPointer(v float64) *float64 { return &v }

func BenchmarkSemanticSkillSearch(b *testing.B) {
	for _, count := range []int{10000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			const dimensions = 384
			c := defaults()
			c.Skills = make([]Skill, count)
			for i := range c.Skills {
				c.Skills[i] = Skill{Name: fmt.Sprintf("skill-%d", i), Description: "Insurance guidance", Body: "body", Enabled: true}
			}
			x := newSkillIndex(c)
			query := make([]float32, dimensions)
			for i := range query {
				query[i] = 1 / float32(math.Sqrt(dimensions))
			}
			x.semantic = &semanticIndex{dimension: dimensions, vectors: make([][]float32, count), queries: map[[32]byte][]float32{sha256.Sum256([]byte("insurance")): query}}
			for i := range x.semantic.vectors {
				x.semantic.vectors[i] = append([]float32(nil), query...)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err := x.search(context.Background(), skillRequest{Query: "insurance", Limit: 10})
				if err != nil || len(out.Matches) != 10 {
					b.Fatal(out, err)
				}
			}
		})
	}
}

func TestEmbeddingBatchesThresholdAndQueryCache(t *testing.T) {
	f, client := newEmbeddingFixture(t)
	c := defaults()
	c.Skills = nil
	for i := 0; i < 65; i++ {
		c.Skills = append(c.Skills, Skill{Name: fmt.Sprintf("item-%d", i), Description: fmt.Sprintf("insurance %d", i), Body: "private", Enabled: true})
	}
	x := newSkillIndex(c)
	if err := x.prepareEmbeddings(context.Background(), client, nil); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	sizes := []int{}
	for _, input := range f.inputs {
		sizes = append(sizes, len(input))
	}
	f.mu.Unlock()
	if !reflect.DeepEqual(sizes, []int{32, 32, 1}) {
		t.Fatal(sizes)
	}
	for i := 0; i < 260; i++ {
		if _, err := x.semantic.query(context.Background(), fmt.Sprintf("insurance query %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(x.semantic.queries) != 256 || len(x.semantic.queryOrder) != 256 {
		t.Fatal("unbounded query cache")
	}
	if _, exists := x.semantic.queries[sha256.Sum256([]byte("insurance query 0"))]; exists {
		t.Fatal("oldest query retained")
	}
	out, err := x.search(context.Background(), skillRequest{Query: "messaging", MinSimilarity: floatPointer(0.1)})
	if err != nil || out.Status != "no_match" {
		t.Fatal(out, err)
	}
	out, err = x.search(context.Background(), skillRequest{Query: "insurance", Limit: 2})
	if err != nil || len(out.Matches) != 2 || out.Matches[0].Name != "item-0" || out.Matches[1].Name != "item-1" || !out.Truncated {
		t.Fatal(out, err)
	}
	x.semantic.dimension = 3
	if _, err := x.semantic.query(context.Background(), "uncached dimension check"); err == nil {
		t.Fatal("dimension mismatch accepted")
	}
}

func TestEmbeddingConfiguration(t *testing.T) {
	t.Setenv("SKILLS_EMBEDDING_POOL_FILE", "")
	t.Setenv("SKILLS_EMBEDDING_URL", "")
	t.Setenv("SKILLS_EMBEDDING_TOKEN_FILE", "")
	if client, err := embeddingClientFromEnv(); client != nil || err != nil {
		t.Fatal(client, err)
	}
	t.Setenv("SKILLS_EMBEDDING_URL", "http://127.0.0.1:8080/v1/embeddings")
	t.Setenv("SKILLS_EMBEDDING_MODEL", "")
	if _, err := embeddingClientFromEnv(); err == nil {
		t.Fatal("missing model accepted")
	}
	t.Setenv("SKILLS_EMBEDDING_MODEL", "test-model")
	client, err := embeddingClientFromEnv()
	if err != nil || client.model != "test-model" {
		t.Fatal(client, err)
	}
	for _, endpoint := range []string{"file:///tmp/embeddings", "https://name:secret@example.com/v1/embeddings", "https://example.com/v1/embeddings?key=secret"} {
		t.Setenv("SKILLS_EMBEDDING_URL", endpoint)
		if _, err := embeddingClientFromEnv(); err == nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
}

func openEmbeddingTestStore(t *testing.T, client *embeddingClient) (*store, error) {
	t.Helper()
	c := defaults()
	c.SkillSelection = &SkillSelectionConfig{Mode: "always"}
	path := filepath.Join(t.TempDir(), "catalog.json")
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return nil, err
	}
	return openStoreWithEmbeddings(path, backend{}, client)
}
