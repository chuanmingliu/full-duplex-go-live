package journal

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExperimentJournalReadOnlyProxy(t *testing.T) {
	hits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cf-Access-Jwt-Assertion") != "" {
			t.Error("identity forwarded")
		}
		if r.URL.Path != "/api/calls/test-call/audio.wav" || r.URL.RawQuery != "raw=1" || r.Header.Get("Range") != "bytes=0-3" {
			t.Errorf("incorrect proxy request: %s %v", r.URL, r.Header)
		}
		w.Header().Set("Content-Range", "bytes 0-3/8")
		w.WriteHeader(206)
		_, _ = w.Write([]byte("RIFF"))
	}))
	defer upstream.Close()
	notes := t.TempDir()
	data, _ := json.Marshal([]ExperimentSource{{ID: "native", Kind: "real", Title: "Native SIP", URL: upstream.URL}})
	if err := os.WriteFile(filepath.Join(notes, "experiments.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(Config{NotesDir: notes, DataDir: t.TempDir()}).Handler()
	req := httptest.NewRequest("GET", "/experiments/native/api/calls/test-call/audio.wav?raw=1", nil)
	req.Header.Set("Range", "bytes=0-3")
	req.Header.Set("Cookie", "private")
	req.Header.Set("Authorization", "Bearer private")
	req.Header.Set("Cf-Access-Jwt-Assertion", "private")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != 206 || res.Body.String() != "RIFF" || res.Header().Get("Content-Range") != "bytes 0-3/8" {
		t.Fatalf("audio response: %d %s", res.Code, res.Body.String())
	}
	for _, tc := range []struct {
		method, path string
		code         int
	}{
		{"POST", "/experiments/native/api/calls/test-call/feedback", 405},
		{"GET", "/experiments/native/api/calls/test-call/feedback", 404},
		{"GET", "/experiments/native/v1/opening", 404},
		{"GET", "/experiments/native/api/talker-status", 404},
		{"GET", "/experiments/unknown/api/overview", 404},
		{"GET", "/experiments/native/", 200},
		{"GET", "/experiments/native/app.js", 200},
		{"GET", "/api/experiments", 200},
	} {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(tc.method, tc.path, nil))
		if res.Code != tc.code {
			t.Errorf("%s %s = %d want %d", tc.method, tc.path, res.Code, tc.code)
		}
		if tc.path == "/api/experiments" && strings.Contains(res.Body.String(), upstream.URL) {
			t.Error("upstream address exposed")
		}
	}
	if hits != 1 {
		t.Fatalf("blocked request reached upstream: %d hits", hits)
	}
}

func TestExperimentJournalFailureAndRedirect(t *testing.T) {
	destinationHits := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationHits++ }))
	defer destination.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) }))
	notes := t.TempDir()
	data, _ := json.Marshal([]ExperimentSource{{ID: "test", Kind: "simulated", URL: upstream.URL}})
	os.WriteFile(filepath.Join(notes, "experiments.json"), data, 0600)
	handler := NewServer(Config{NotesDir: notes, DataDir: t.TempDir()}).Handler()
	for i := 0; i < 2; i++ {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest("GET", "/experiments/test/api/overview", nil))
		if res.Code != 502 || res.Header().Get("Location") != "" {
			t.Fatalf("response %d %v", res.Code, res.Header())
		}
		upstream.Close()
	}
	if destinationHits != 0 {
		t.Fatal("followed upstream redirect")
	}
	os.WriteFile(filepath.Join(notes, "experiments.json"), []byte(`[{"id":"bad","url":"https://example.com/path"}]`), 0600)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest("GET", "/api/experiments", nil))
	if res.Code != 503 {
		t.Fatal("invalid configuration accepted")
	}
}

func TestExperimentJournalEmptyConfiguration(t *testing.T) {
	s := httptest.NewServer(NewServer(Config{DataDir: t.TempDir()}).Handler())
	defer s.Close()
	resp, err := http.Get(s.URL + "/api/experiments")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}
