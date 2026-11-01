package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/duplex"
)

// POST /v1/phrases caches phrases on request — the lead service's token is
// enough, since it is the lead service that asks when the owner saves the
// openers (11:27 on 10-27).
func TestPhrasesAreCachedOnRequestWithTheLeadServicesToken(t *testing.T) {
	duplex.Phrases().Reset()
	t.Cleanup(duplex.Phrases().Reset)
	tok := filepath.Join(t.TempDir(), "api.token")
	os.WriteFile(tok, []byte("lead-secret\n"), 0o600)
	cfg := config.Default()
	cfg.WebRoot = ""
	cfg.Greeting = ""
	cfg.Realtime.Enabled = true
	cfg.Duplex.PhraseCache = true
	cfg.AuthToken = "golive-secret"
	cfg.ToolsTokenFile = tok
	srv := NewServer(cfg, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	post := func(bearer string, body any) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/phrases", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, _ := post("", map[string]any{"phrases": []string{"价格的话，"}}); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := post("wrong", map[string]any{"phrases": []string{"价格的话，"}}); code != http.StatusUnauthorized {
		t.Fatalf("a wrong token: %d", code)
	}
	code, out := post("lead-secret", map[string]any{"phrases": []string{"价格的话，", "这个保额，", ""}})
	if code != 200 || out["ok"] != true || out["queued"] != float64(2) {
		t.Fatalf("the lead service's token: %d %v", code, out)
	}
	// The synthesis runs on after the answer, paced.
	deadline := time.Now().Add(10 * time.Second)
	for duplex.Phrases().Len() < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := duplex.Phrases().Len(); n != 2 {
		t.Fatalf("%d phrases in the cache", n)
	}
	// Again: nothing new to queue.
	if _, out := post("golive-secret", map[string]any{"phrases": []string{"价格的话，"}}); out["queued"] != float64(0) || out["already"] != float64(1) || out["ok"] != true {
		t.Fatalf("second time: %v", out)
	}
}
