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
	"time"
)

func TestDreamingRecordReadAndRefresh(t *testing.T) {
	directory := t.TempDir()
	server := httptest.NewServer(NewServer(Config{NotesDir: directory}).Handler())
	defer server.Close()
	var absent struct {
		Available bool          `json:"available"`
		Runs      []DreamingRun `json:"runs"`
	}
	if getJSON(t, server.URL+"/api/dreaming", &absent) != http.StatusOK || absent.Available || len(absent.Runs) != 0 {
		t.Fatal("missing record must be an empty view")
	}
	data, err := os.ReadFile("../../journal/dreaming.example.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "dreaming.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var record DreamingRecord
	if getJSON(t, server.URL+"/api/dreaming", &record) != http.StatusOK || len(record.Runs) != 1 || !record.valid() {
		t.Fatal("example lifecycle record did not load")
	}
	record.Runs[0].Title = "Updated reviewed record"
	record.RecordedAt = time.Now()
	changed, _ := json.Marshal(record)
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	var refreshed DreamingRecord
	getJSON(t, server.URL+"/api/dreaming", &refreshed)
	if refreshed.Runs[0].Title != record.Runs[0].Title {
		t.Fatal("record did not refresh")
	}
	response, err := http.Post(server.URL+"/api/dreaming", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatal("read-only endpoint accepted POST")
	}
}

func TestDreamingRejectsInvalidRecords(t *testing.T) {
	directory := t.TempDir()
	server := httptest.NewServer(NewServer(Config{NotesDir: directory}).Handler())
	defer server.Close()
	valid, err := os.ReadFile("../../journal/dreaming.example.json")
	if err != nil {
		t.Fatal(err)
	}
	invalidCall := strings.Replace(string(valid), "example-call", "../private", -1)
	for _, body := range []string{"{", `{"version":2,"recorded_at":"2026-11-12T00:00:00Z","runs":[]}`, invalidCall, strings.Repeat(" ", (2<<20)+1)} {
		if err := os.WriteFile(filepath.Join(directory, "dreaming.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if code := getJSON(t, server.URL+"/api/dreaming", nil); code != http.StatusServiceUnavailable {
			t.Fatalf("invalid record status %d", code)
		}
	}
}

func TestDreamingEmbeddedAssets(t *testing.T) {
	server := httptest.NewServer(NewServer(Config{}).Handler())
	defer server.Close()
	for path, want := range map[string]string{"/": "data-nav=\"dreaming\"", "/dreaming.js": "window.JournalDreaming", "/dreaming.css": ".dream-workspace"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), want) {
			t.Fatalf("missing asset %s", path)
		}
	}
}
