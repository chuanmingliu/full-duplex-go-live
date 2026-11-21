package journal

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/requestlog"
)

func requestRecordingFixture(t *testing.T, directory, session string) (string, string) {
	t.Helper()
	store, err := requestlog.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"model":"fixture","messages":[{"role":"system","content":"原始提示\n<script>unsafe()</script>"},{"role":"system","name":"load_business_skill","content":"{\"status\":\"loaded\",\"guidance\":[{\"uri\":\"skill://memory-example/SKILL.md\",\"text\":\"Ask one question.\"}]}"},{"role":"user","content":"Hello"}]}`
	record, err := store.Begin(requestlog.Scope{SessionID: session, TurnID: "item_1", ResponseID: "resp_1", Purpose: "turn"}, requestlog.Metadata{Provider: "fixture", Model: "fixture"}, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err = record.Finish(200, "upstream-1", "done"); err != nil {
		t.Fatal(err)
	}
	return record.ID(), raw
}

func TestRequestBrowserReadsExactBodyAndMetadata(t *testing.T) {
	directory := t.TempDir()
	id, raw := requestRecordingFixture(t, directory, "sess_one")
	requestRecordingFixture(t, directory, "sess_two")
	server := httptest.NewServer(NewServer(Config{RequestLogDir: directory}).Handler())
	defer server.Close()
	var sessions struct {
		Sessions []recordedSession `json:"sessions"`
		Total    int               `json:"total"`
	}
	if code := getJSON(t, server.URL+"/api/requests/sessions?q=one", &sessions); code != 200 || len(sessions.Sessions) != 1 || sessions.Sessions[0].ID != "sess_one" {
		t.Fatalf("sessions %d %+v", code, sessions)
	}
	var rows struct {
		Requests []recordedRequest `json:"requests"`
	}
	if code := getJSON(t, server.URL+"/api/requests/sessions/sess_one", &rows); code != 200 || len(rows.Requests) != 1 || rows.Requests[0].ID != id {
		t.Fatalf("requests %d %+v", code, rows)
	}
	var detail struct {
		Raw       string              `json:"raw_request"`
		Metadata  requestlog.Metadata `json:"metadata"`
		Integrity bool                `json:"integrity_ok"`
	}
	url := server.URL + "/api/requests/sessions/sess_one/" + id
	if code := getJSON(t, url, &detail); code != 200 || detail.Raw != raw || !detail.Integrity || detail.Metadata.ProviderRequestID != "upstream-1" || detail.Metadata.SessionID != "sess_one" {
		t.Fatalf("detail %d %+v", code, detail)
	}
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("private requests must not be cached")
	}
	if err = os.WriteFile(filepath.Join(directory, "sess_one", id, "request.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if code := getJSON(t, url, &detail); code != 200 || detail.Integrity {
		t.Fatal("tampering was not reported")
	}
}

func TestRequestBrowserHandlesUnscopedAndHashedSessions(t *testing.T) {
	directory := t.TempDir()
	requestRecordingFixture(t, directory, "")
	requestRecordingFixture(t, directory, "old/session")
	server := httptest.NewServer(NewServer(Config{RequestLogDir: directory}).Handler())
	defer server.Close()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		var rows struct {
			Requests []recordedRequest `json:"requests"`
		}
		if code := getJSON(t, server.URL+"/api/requests/sessions/"+entry.Name(), &rows); code != 200 || len(rows.Requests) != 1 || rows.Requests[0].Error != "" {
			t.Fatalf("legacy session %d %+v", code, rows)
		}
	}
}

func TestRequestBrowserConfinesFilesAndReportsInvalidRecords(t *testing.T) {
	directory := t.TempDir()
	id, _ := requestRecordingFixture(t, directory, "sess_one")
	other := t.TempDir()
	otherID, _ := requestRecordingFixture(t, other, "sess_outside")
	if err := os.Symlink(filepath.Join(other, "sess_outside"), filepath.Join(directory, "sess_link")); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(Config{RequestLogDir: directory}).Handler())
	defer server.Close()
	for _, path := range []string{"sess_link", "sess_link/" + otherID, "sess_two/" + id, "sess_one/invalid"} {
		response, err := http.Get(server.URL + "/api/requests/sessions/" + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode < 400 {
			t.Errorf("unsafe or missing path %s returned %d", path, response.StatusCode)
		}
	}
	// A body symlink must not escape the store even when its metadata is valid.
	body := filepath.Join(directory, "sess_one", id, "request.json")
	if err := os.Remove(body); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, "sess_outside", otherID, "request.json"), body); err != nil {
		t.Fatal(err)
	}
	var failure map[string]any
	if code := getJSON(t, server.URL+"/api/requests/sessions/sess_one/"+id, &failure); code != 422 {
		t.Fatalf("body escape %d", code)
	}
	if err := os.WriteFile(filepath.Join(directory, "sess_one", id, "metadata.json"), []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	var rows struct {
		Requests []recordedRequest `json:"requests"`
	}
	getJSON(t, server.URL+"/api/requests/sessions/sess_one", &rows)
	if len(rows.Requests) != 1 || rows.Requests[0].Error == "" {
		t.Fatal("invalid metadata silently omitted")
	}
}

func TestRequestBrowserPaginationAndConfiguration(t *testing.T) {
	directory := t.TempDir()
	for i := 0; i < 51; i++ {
		if err := os.Mkdir(filepath.Join(directory, fmt.Sprintf("sess_%02d", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(NewServer(Config{RequestLogDir: directory}).Handler())
	defer server.Close()
	var page struct {
		Sessions []recordedSession `json:"sessions"`
		More     bool              `json:"more"`
		Total    int               `json:"total"`
	}
	getJSON(t, server.URL+"/api/requests/sessions?offset=50", &page)
	if len(page.Sessions) != 1 || page.More || page.Total != 51 {
		t.Fatalf("bad page %+v", page)
	}
	for _, directory := range []string{"", filepath.Join(t.TempDir(), "absent")} {
		recorder := httptest.NewRecorder()
		NewServer(Config{RequestLogDir: directory}).Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/requests/sessions", nil))
		if recorder.Code != 503 || !json.Valid(recorder.Body.Bytes()) {
			t.Fatalf("missing config %d", recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	NewServer(Config{}).Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))
	for _, asset := range []string{`data-nav="requests"`, `requests.js?v=`, `requests.css?v=`} {
		if !strings.Contains(recorder.Body.String(), asset) {
			t.Errorf("missing %s", asset)
		}
	}
}
