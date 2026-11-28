package journal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTalkerStatusChecksOnlyTheConfiguredModelEndpoint(t *testing.T) {
	requests := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/models" || r.Method != "GET" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"local-talker"}]}`))
	}))
	defer model.Close()
	s := NewServer(Config{TalkerURL: model.URL, TalkerPlayground: "http://desk.local/#/talker"})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/talker-status?url=http://elsewhere.invalid", nil))
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || got["up"] != true || got["model"] != "local-talker" || got["playground_url"] != "http://desk.local/#/talker" {
		t.Fatalf("status %v requests=%d", got, requests)
	}
	if _, ok := got["generation_ms"]; ok {
		t.Fatal("a status probe claimed generation")
	}
}

func TestTalkerStatusDoesNotFollowRedirectsOrExposeCredentials(t *testing.T) {
	hits := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer redirect.Close()
	for _, target := range []string{redirect.URL, "http://secret:token@example.invalid", ""} {
		s := NewServer(Config{TalkerURL: target, TalkerPlayground: "javascript:alert(1)"})
		w := httptest.NewRecorder()
		s.handleTalkerStatus(w, httptest.NewRequest("GET", "/api/talker-status", nil))
		if strings.Contains(w.Body.String(), `"up":true`) || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "playground_url") {
			t.Fatalf("unsafe status %s", w.Body.String())
		}
	}
	if hits != 0 {
		t.Fatalf("followed %d redirects", hits)
	}
}
