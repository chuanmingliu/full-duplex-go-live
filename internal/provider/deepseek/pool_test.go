package deepseek

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
)

func TestPoolKeysComeFromThreePlacesInOrderWithoutDuplicates(t *testing.T) {
	file := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(file, []byte("# the team's spare accounts\nsk-file-1\n\nsk-b   # also in the list above\n  sk-file-2  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POOLTEST_API_KEY", "sk-a")
	t.Setenv("POOLTEST_API_KEYS", `sk-b, sk-a,"sk-c" sk-d`)
	t.Setenv("POOLTEST_API_KEYS_FILE", file)
	got, err := poolKeys("POOLTEST_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sk-a", "sk-b", "sk-c", "sk-d", "sk-file-1", "sk-file-2"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("keys %v; want %v", got, want)
	}

	t.Run("the list alone is enough", func(t *testing.T) {
		t.Setenv("POOLTEST_API_KEY", "")
		t.Setenv("POOLTEST_API_KEYS_FILE", "")
		if got, err := poolKeys("POOLTEST_API_KEY"); err != nil || len(got) != 4 {
			t.Errorf("got %v, %v", got, err)
		}
	})
	t.Run("none at all is an error that says where they go", func(t *testing.T) {
		t.Setenv("POOLTEST_API_KEY", "")
		t.Setenv("POOLTEST_API_KEYS", "")
		t.Setenv("POOLTEST_API_KEYS_FILE", "")
		_, err := poolKeys("POOLTEST_API_KEY")
		if err == nil || !strings.Contains(err.Error(), "POOLTEST_API_KEYS") {
			t.Errorf("err = %v", err)
		}
	})
}

// poolServer answers completions and records which key and which connection
// each arrived on. Keys in refuse are answered with that status.
type poolServer struct {
	*httptest.Server
	mu     sync.Mutex
	seen   []poolHit
	refuse map[string]int
}

type poolHit struct{ key, conn string }

func newPoolServer(t *testing.T) *poolServer {
	ps := &poolServer{refuse: map[string]int{}}
	ps.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		ps.mu.Lock()
		ps.seen = append(ps.seen, poolHit{key: key, conn: r.RemoteAddr})
		status := ps.refuse[key]
		ps.mu.Unlock()
		if status != 0 {
			http.Error(w, `{"error":{"message":"Authentication Fails"}}`, status)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"好的\"}}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(ps.Close)
	return ps
}

func (ps *poolServer) hits() []poolHit {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return append([]poolHit(nil), ps.seen...)
}

func complete(t *testing.T, l *LLM, ctx context.Context) (string, error) {
	t.Helper()
	ch, err := l.Stream(ctx, provider.LLMRequest{Messages: []provider.Message{{Role: "user", Content: "你好"}}})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for d := range ch {
		if d.Err != nil {
			return b.String(), d.Err
		}
		b.WriteString(d.Text)
	}
	return b.String(), nil
}

// Copies of a completion are worth sending only if they share nothing: each
// lane is a key and a connection of its own, and a lane is the same key and
// the same connection every time it is asked for.
func TestLanesShareNeitherKeyNorConnection(t *testing.T) {
	ps := newPoolServer(t)
	l := &LLM{BaseURL: ps.URL, APIKey: "sk-a", DefaultModel: "m", HTTP: newHTTPClient(), keys: []string{"sk-a", "sk-b", "sk-c", "sk-d"}}
	if l.Lanes() != 4 || l.Keys() != 4 {
		t.Fatalf("lanes %d, keys %d", l.Lanes(), l.Keys())
	}
	for round := 0; round < 2; round++ {
		for lane := 0; lane < 4; lane++ {
			if _, err := complete(t, l, provider.WithLane(context.Background(), lane)); err != nil {
				t.Fatal(err)
			}
		}
	}
	hits := ps.hits()
	keys, conns := map[string]bool{}, map[string]bool{}
	for lane := 0; lane < 4; lane++ {
		first, again := hits[lane], hits[lane+4]
		keys[first.key], conns[first.conn] = true, true
		if first.key != again.key {
			t.Errorf("lane %d used key %q and then %q; a session's cache lives on one account", lane, first.key, again.key)
		}
		if first.conn != again.conn {
			t.Errorf("lane %d did not reuse its connection", lane)
		}
	}
	if len(keys) != 4 {
		t.Errorf("four lanes used %d distinct keys", len(keys))
	}
	if len(conns) != 4 {
		t.Errorf("four lanes used %d distinct connections; copies on one connection stall together", len(conns))
	}
}

// One key is still four lanes: its copies get a connection each.
func TestOneKeyStillGetsAConnectionPerLane(t *testing.T) {
	ps := newPoolServer(t)
	l := &LLM{BaseURL: ps.URL, APIKey: "sk-only", DefaultModel: "m", HTTP: newHTTPClient()}
	for lane := 0; lane < 4; lane++ {
		if _, err := complete(t, l, provider.WithLane(context.Background(), lane)); err != nil {
			t.Fatal(err)
		}
	}
	conns := map[string]bool{}
	for _, h := range ps.hits() {
		conns[h.conn] = true
		if h.key != "sk-only" {
			t.Errorf("sent key %q", h.key)
		}
	}
	if len(conns) != 4 {
		t.Errorf("four lanes on one key used %d connection(s)", len(conns))
	}
}

// Eight copies of a completion want eight connections, shared evenly between
// the keys there are.
func TestMoreLanesCanBeAskedFor(t *testing.T) {
	t.Setenv("DEEPSEEK_LANES", "8")
	ps := newPoolServer(t)
	l := &LLM{BaseURL: ps.URL, APIKey: "sk-a", DefaultModel: "m", HTTP: newHTTPClient(), keys: []string{"sk-a", "sk-b"}}
	if l.Lanes() != 8 {
		t.Fatalf("%d lanes with DEEPSEEK_LANES=8", l.Lanes())
	}
	for lane := 0; lane < 8; lane++ {
		if _, err := complete(t, l, provider.WithLane(context.Background(), lane)); err != nil {
			t.Fatal(err)
		}
	}
	conns, perKey := map[string]bool{}, map[string]int{}
	for _, h := range ps.hits() {
		conns[h.conn] = true
		perKey[h.key]++
	}
	if len(conns) != 8 {
		t.Errorf("eight lanes used %d connection(s)", len(conns))
	}
	if perKey["sk-a"] != 4 || perKey["sk-b"] != 4 {
		t.Errorf("requests per key: %v; want four each", perKey)
	}

	t.Setenv("DEEPSEEK_LANES", "500")
	if n := (&LLM{APIKey: "sk-a", HTTP: newHTTPClient()}).Lanes(); n != maxLanes {
		t.Errorf("%d lanes when 500 were asked for; the ceiling is %d", n, maxLanes)
	}
}

// A key the vendor refuses — wrong, or out of credit — would otherwise cost one
// copy of every completion for as long as the process ran.
func TestARefusedKeyIsRestedAndNeverNamed(t *testing.T) {
	ps := newPoolServer(t)
	ps.refuse["sk-secret-bad"] = http.StatusUnauthorized
	l := &LLM{BaseURL: ps.URL, APIKey: "sk-a", DefaultModel: "m", HTTP: newHTTPClient(), keys: []string{"sk-a", "sk-secret-bad", "sk-c", "sk-d"}}

	_, err := complete(t, l, provider.WithLane(context.Background(), 1))
	if err == nil {
		t.Fatal("a refused key reported success")
	}
	if strings.Contains(err.Error(), "sk-secret-bad") {
		t.Errorf("the error names the key: %v", err)
	}
	if !strings.Contains(err.Error(), "key 2 of 4") {
		t.Errorf("the error does not say which key by position: %v", err)
	}
	before := len(ps.hits())
	for i := 0; i < 3; i++ {
		if _, err := complete(t, l, provider.WithLane(context.Background(), 1)); err != nil {
			t.Fatalf("lane 1 was not served by another key while its own rests: %v", err)
		}
	}
	for _, h := range ps.hits()[before:] {
		if h.key == "sk-secret-bad" {
			t.Fatal("the refused key was tried again at once")
		}
	}
}
