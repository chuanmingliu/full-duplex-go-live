package journal

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDreamingSourceWaitsForCompleteArchive(t *testing.T) {
	directory := t.TempDir()
	srv := NewServer(Config{ArchiveDir: directory, StoreDir: directory})
	call := &Call{ID: "new-call", EndedAt: time.Now().UTC()}
	srv.calls = []*Call{call, {ID: "open-call"}, {ID: "old-call", EndedAt: time.Now().Add(-time.Hour)}}
	srv.checked = time.Now()
	srv.simRuns = map[string]string{}
	handler := srv.Handler()
	read := func() map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/dreaming/source?since="+url.QueryEscape(call.EndedAt.Add(-time.Second).Format(time.RFC3339Nano)), nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != 200 {
			t.Fatal(res.Code, res.Body.String())
		}
		var body map[string]any
		json.Unmarshal(res.Body.Bytes(), &body)
		return body
	}
	rows := read()["calls"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["ready"] != false {
		t.Fatal(rows)
	}
	raw, _ := json.Marshal(call)
	os.WriteFile(filepath.Join(directory, "call.json"), raw, 0600)
	srv.archive.entries[call.ID] = &ArchiveEntry{ID: call.ID, Audio: true, Dir: ".", Files: map[string]ArchiveFile{"call.json": {SHA256: fmt.Sprintf("%x", sha256.Sum256(raw))}}}
	rows = read()["calls"].([]any)
	if rows[0].(map[string]any)["ready"] != true {
		t.Fatal(rows)
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest("GET", "/api/dreaming/source/new-call", nil))
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	var evidence struct {
		ArchiveJSON string `json:"archive_json"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &evidence); err != nil || evidence.ArchiveJSON != string(raw) {
		t.Fatal("original archive bytes were changed")
	}
	os.WriteFile(filepath.Join(directory, "call.json"), []byte(`{}`), 0600)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest("GET", "/api/dreaming/source/new-call", nil))
	if res.Code != 503 {
		t.Fatal("changed archive accepted")
	}
}

func TestDreamingProxyFixedRoutesAndOrigin(t *testing.T) {
	token := "test-admin-token-long-enough"
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing authentication")
		}
		if r.URL.Path != "/admin/memory/live" && r.URL.Path != "/admin/memory/settings" && r.URL.Path != "/admin/memory/dreams/job1/retry" {
			t.Error("unexpected route", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"connected":true}`)
	}))
	defer upstream.Close()
	file := filepath.Join(t.TempDir(), "token")
	os.WriteFile(file, []byte(token), 0600)
	srv := NewServer(Config{DreamMemoryURL: upstream.URL, DreamAdminTokenFile: file}).Handler()
	for _, test := range []struct {
		method, path, origin string
		code                 int
	}{
		{"GET", "/api/dreaming/live", "", 200},
		{"PUT", "/api/dreaming/settings", "https://panel.test", 200},
		{"POST", "/api/dreaming/jobs/job1/retry", "https://panel.test", 200},
		{"PUT", "/api/dreaming/settings", "https://evil.test", 403},
	} {
		req := httptest.NewRequest(test.method, "https://panel.test"+test.path, strings.NewReader(`{}`))
		req.Header.Set("Origin", test.origin)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		srv.ServeHTTP(res, req)
		if res.Code != test.code || strings.Contains(res.Body.String(), token) {
			t.Fatal(test, res.Code, res.Body.String())
		}
	}
	if calls != 3 {
		t.Fatal("rejected request reached backend")
	}
}
