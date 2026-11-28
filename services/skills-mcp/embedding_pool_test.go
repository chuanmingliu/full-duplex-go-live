package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type poolTestEndpoint struct {
	fail  atomic.Bool
	delay atomic.Int64
	calls atomic.Int64
}

func newPoolTestEndpoint(t *testing.T, name string, swapped bool) (*poolTestEndpoint, *embeddingClient) {
	t.Helper()
	endpoint := &poolTestEndpoint{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint.calls.Add(1)
		if delay := endpoint.delay.Load(); delay > 0 {
			select {
			case <-time.After(time.Duration(delay)):
			case <-r.Context().Done():
				return
			}
		}
		if endpoint.fail.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		var request struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != name {
			t.Error("invalid provider request", err, request.Model)
		}
		rows := []obj{}
		for i, text := range request.Input {
			if !strings.HasPrefix(text, name+":") {
				t.Error("provider-specific prefix missing", text)
			}
			vector := []float32{0, 1}
			if strings.Contains(text, "insurance") || strings.Contains(text, "car") {
				vector = []float32{1, 0}
			}
			if swapped {
				vector[0], vector[1] = vector[1], vector[0]
			}
			rows = append(rows, obj{"index": i, "embedding": vector})
		}
		json.NewEncoder(w).Encode(obj{"data": rows})
	}))
	t.Cleanup(server.Close)
	return endpoint, &embeddingClient{
		name: name, model: name, revision: "pinned", endpoint: server.URL,
		queryPrefix: name + ":query:", documentPrefix: name + ":document:", http: server.Client(),
	}
}

func poolSearch(t *testing.T, index *skillIndex, query, provider string) skillReply {
	t.Helper()
	out, err := index.search(context.Background(), skillRequest{Query: query, SearchMode: "semantic", MaxBytes: 1024})
	if err != nil || out.EmbeddingProvider != provider || out.EmbeddingRevision != "pinned" || len(out.Matches) == 0 || out.Matches[0].Name != "motor" || !replyFits(out, 1024) {
		t.Fatalf("query=%q provider=%q result=%+v error=%v", query, provider, out, err)
	}
	return out
}

func expirePoolHealth(client *embeddingClient) {
	client.health.mu.Lock()
	client.health.retryAfter = time.Now().Add(-time.Second)
	client.health.mu.Unlock()
}

func TestEmbeddingPoolFailoverAndRecovery(t *testing.T) {
	primary, a := newPoolTestEndpoint(t, "primary", false)
	backup, b := newPoolTestEndpoint(t, "backup", true)
	index := newSkillIndex(embeddingCatalog())
	if err := index.prepareEmbeddings(context.Background(), &embeddingClient{members: []*embeddingClient{a, b}}, nil); err != nil {
		t.Fatal(err)
	}
	if index.selectionCapabilities()["semantic_available"] != true {
		t.Fatal("prepared embedding pool was not advertised as available")
	}
	poolSearch(t, index, "insurance initial", "primary")
	primary.fail.Store(true)
	poolSearch(t, index, "insurance failover", "backup")
	count := primary.calls.Load()
	poolSearch(t, index, "insurance circuit open", "backup")
	if primary.calls.Load() != count {
		t.Fatal("open circuit called primary")
	}
	primary.fail.Store(false)
	expirePoolHealth(a)
	poolSearch(t, index, "insurance recovered", "primary")
	backup.fail.Store(true)
	poolSearch(t, index, "insurance backup down", "primary")
	primary.fail.Store(true)
	out, err := index.search(context.Background(), skillRequest{Query: "insurance both down"})
	if err != nil || out.Retrieval != "lexical" || out.FallbackReason != "embedding_unavailable" {
		t.Fatal(out, err)
	}
	if _, err := index.search(context.Background(), skillRequest{Query: "insurance", SearchMode: "semantic"}); err == nil {
		t.Fatal("explicit semantic request hid full pool outage")
	}
}

func TestEmbeddingPoolSlowPrimaryPreservesBackupBudget(t *testing.T) {
	primary, a := newPoolTestEndpoint(t, "primary", false)
	_, b := newPoolTestEndpoint(t, "backup", true)
	index := newSkillIndex(embeddingCatalog())
	if err := index.prepareEmbeddings(context.Background(), &embeddingClient{members: []*embeddingClient{a, b}}, nil); err != nil {
		t.Fatal(err)
	}
	primary.delay.Store(int64(time.Second))
	start := time.Now()
	poolSearch(t, index, "insurance slow", "backup")
	if elapsed := time.Since(start); elapsed > 650*time.Millisecond {
		t.Fatal("failover exceeded interactive budget", elapsed)
	}
}

func TestEmbeddingPoolDegradedPublicationAndRepair(t *testing.T) {
	primary, a := newPoolTestEndpoint(t, "primary", false)
	_, b := newPoolTestEndpoint(t, "backup", true)
	client := &embeddingClient{members: []*embeddingClient{a, b}}
	old := newSkillIndex(embeddingCatalog())
	if err := old.prepareEmbeddings(context.Background(), client, nil); err != nil {
		t.Fatal(err)
	}
	primary.fail.Store(true)
	catalog := embeddingCatalog()
	catalog.Revision++
	catalog.Skills[0].Description += " revised"
	current := newSkillIndex(catalog)
	if err := current.prepareEmbeddings(context.Background(), client, old); err != nil {
		t.Fatal(err)
	}
	out := poolSearch(t, current, "insurance updated", "backup")
	if out.Revision != catalog.Revision || current.pool.members[0].view != nil {
		t.Fatal("failed member reused an outdated revision", out)
	}
	primary.fail.Store(false)
	member := current.pool.members[0]
	member.mu.Lock()
	member.retryAfter = time.Time{}
	member.mu.Unlock()
	member.ready(current)
	deadline := time.Now().Add(2 * time.Second)
	for member.ready(current) == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	poolSearch(t, current, "insurance repaired", "primary")
	// Requests admitted against the old snapshot still use its own revision.
	if result := poolSearch(t, old, "insurance old snapshot", "primary"); result.Revision != old.revision {
		t.Fatal("old revision changed")
	}
}

func TestEmbeddingPoolConcurrentSearch(t *testing.T) {
	primary, a := newPoolTestEndpoint(t, "primary", false)
	_, b := newPoolTestEndpoint(t, "backup", true)
	index := newSkillIndex(embeddingCatalog())
	if err := index.prepareEmbeddings(context.Background(), &embeddingClient{members: []*embeddingClient{a, b}}, nil); err != nil {
		t.Fatal(err)
	}
	primary.fail.Store(true)
	var workers sync.WaitGroup
	for i := range 20 {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			poolSearch(t, index, fmt.Sprintf("insurance concurrent %d", i), "backup")
		}(i)
	}
	workers.Wait()
}

func TestEmbeddingPoolHealthTransitions(t *testing.T) {
	var health embeddingHealth
	generation, ok := health.acquire()
	if !ok {
		t.Fatal("healthy provider rejected")
	}
	health.finish(generation, errors.New("failed"), false)
	health.finish(generation, nil, false)
	if _, ok := health.acquire(); ok {
		t.Fatal("stale success closed the circuit")
	}
	health.retryAfter = time.Now().Add(-time.Second)
	probe, ok := health.acquire()
	if !ok {
		t.Fatal("recovery probe rejected")
	}
	if _, ok := health.acquire(); ok {
		t.Fatal("parallel half-open probe admitted")
	}
	health.finish(probe, context.Canceled, true)
	if health.retryAfter.IsZero() {
		t.Fatal("canceled probe reported recovery")
	}
	probe, _ = health.acquire()
	health.finish(probe, nil, false)
	if _, ok := health.acquire(); !ok {
		t.Fatal("successful recovery failed")
	}
}

func TestEmbeddingPoolConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.json")
	t.Setenv("SKILLS_EMBEDDING_POOL_FILE", path)
	valid := `{"members":[{"name":"a","url":"http://localhost:1/v1/embeddings","model":"a","revision":"r1"},{"name":"b","url":"http://localhost:2/v1/embeddings","model":"b","revision":"r2","query_prefix":"query: "}]}`
	for _, input := range []string{`{}`, valid + `{}`, strings.Replace(valid, `"name":"b"`, `"name":"a"`, 1), strings.Replace(valid, `"revision":"r1"`, `"unknown":"r1"`, 1),
		strings.Replace(valid, `"r1"`, `"`+strings.Repeat("r", 129)+`"`, 1),
		strings.Replace(valid, `"name":"a"`, `"name":"bad\nname"`, 1),
	} {
		os.WriteFile(path, []byte(input), 0600)
		if _, err := embeddingClientFromEnv(); err == nil {
			t.Fatal("invalid pool accepted", input)
		}
	}
	os.WriteFile(path, []byte(valid), 0600)
	client, err := embeddingClientFromEnv()
	if err != nil || len(client.members) != 2 || client.members[1].queryPrefix != "query: " {
		t.Fatal(client, err)
	}
}

func TestEmbeddingPoolDegradedStartup(t *testing.T) {
	primary, a := newPoolTestEndpoint(t, "primary", false)
	backup, b := newPoolTestEndpoint(t, "backup", true)
	primary.fail.Store(true)
	client := &embeddingClient{members: []*embeddingClient{a, b}}
	index := newSkillIndex(embeddingCatalog())
	if err := index.prepareEmbeddings(context.Background(), client, nil); err != nil {
		t.Fatal(err)
	}
	poolSearch(t, index, "insurance cold startup", "backup")
	backup.fail.Store(true)
	empty := newSkillIndex(embeddingCatalog())
	if err := empty.prepareEmbeddings(context.Background(), client, nil); err == nil {
		t.Fatal("published without any complete model index")
	}
}

func TestEmbeddingPoolMetadataBudget(t *testing.T) {
	_, client := newPoolTestEndpoint(t, "a", false)
	client.name = strings.Repeat("a", 64)
	client.revision = strings.Repeat("r", 128)
	index := newSkillIndex(embeddingCatalog())
	if err := index.prepareEmbeddings(context.Background(), &embeddingClient{members: []*embeddingClient{client}}, nil); err != nil {
		t.Fatal(err)
	}
	out, err := index.search(context.Background(), skillRequest{Query: "insurance", SearchMode: "semantic", MaxBytes: 1024})
	if err != nil || !replyFits(out, 1024) || out.EmbeddingRevision != client.revision {
		t.Fatal(out, err)
	}
}

func TestEmbeddingPoolInputErrorKeepsProvidersHealthy(t *testing.T) {
	_, a := newPoolTestEndpoint(t, "primary", false)
	backup, b := newPoolTestEndpoint(t, "backup", true)
	index := newSkillIndex(embeddingCatalog())
	if err := index.prepareEmbeddings(context.Background(), &embeddingClient{members: []*embeddingClient{a, b}}, nil); err != nil {
		t.Fatal(err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "input too long", 400) }))
	defer bad.Close()
	a.endpoint = bad.URL
	calls := backup.calls.Load()
	out, err := index.search(context.Background(), skillRequest{Query: "insurance rejected input"})
	if err != nil || out.Retrieval != "lexical" || !a.health.retryAfter.IsZero() || backup.calls.Load() != calls {
		t.Fatal(out, err)
	}
}
