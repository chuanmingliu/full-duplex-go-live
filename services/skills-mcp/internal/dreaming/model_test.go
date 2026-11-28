package dreaming

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestModelBoundary(t *testing.T) {
	for _, test := range []struct {
		name, content, finish string
		status                int
		wantErr               bool
	}{
		{"valid", `{"lessons":[]}`, "stop", 200, false},
		{"unknown", `{"lessons":[],"secret":"x"}`, "stop", 200, true},
		{"missing", `{}`, "stop", 200, true},
		{"trailing", `{"lessons":[]} {}`, "stop", 200, true},
		{"truncated", `{"lessons":[]}`, "length", 200, true},
		{"unavailable", "private provider error", "", 503, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-secret" {
					t.Error("request contract")
				}
				b, _ := io.ReadAll(r.Body)
				if strings.Contains(string(b), "private-audio.wav") || !strings.Contains(string(b), "turn1") {
					t.Error("evidence projection")
				}
				w.WriteHeader(test.status)
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": test.content}, "finish_reason": test.finish}}})
			}))
			defer srv.Close()
			m, err := NewModel(srv.URL, "test-model", "test-secret")
			requireOK(t, err)
			e := episode()
			e.AudioRefs = []string{"/private/private-audio.wav"}
			_, err = m.Review(context.Background(), Snapshot{Episodes: []Episode{e}})
			if (err != nil) != test.wantErr {
				t.Fatal(err)
			}
		})
	}
	for _, endpoint := range []string{"http://example.com/chat", "https://user:secret@example.com/chat", "https://example.com/chat?token=x", "ftp://127.0.0.1/chat"} {
		if _, err := NewModel(endpoint, "model", "secret"); err == nil {
			t.Fatal("accepted unsafe endpoint", endpoint)
		}
	}
}

func TestModelDoesNotFollowRedirects(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer srv.Close()
	m, err := NewModel(srv.URL, "model", "secret")
	requireOK(t, err)
	if _, err = m.Review(context.Background(), Snapshot{}); err == nil || called {
		t.Fatal("followed redirect", err)
	}
}

func TestModelRepairsRoutingOnceBeforeWorkerValidation(t *testing.T) {
	for _, fixed := range []bool{true, false} {
		t.Run(map[bool]string{true: "repaired", false: "still-invalid"}[fixed], func(t *testing.T) {
			attempts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				var request struct {
					Messages []map[string]string `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if attempts == 2 && (len(request.Messages) != 4 || !strings.Contains(request.Messages[3]["content"], "routing case 0 failed")) {
					t.Error("repair request lost validation feedback")
				}
				p := proposal()
				if attempts == 1 || !fixed {
					p.Match = []string{"unrelated"}
				}
				content, _ := json.Marshal(map[string]any{"lessons": []Proposal{p}})
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}, "finish_reason": "stop"}}})
			}))
			defer srv.Close()
			model, err := NewModel(srv.URL, "model", "secret")
			requireOK(t, err)
			store := openTest(t)
			ctx := context.Background()
			e := episode()
			e.Mode = "simulation"
			requireOK(t, store.Import(ctx, e))
			_, err = store.Queue(ctx, DreamRequest{ID: "repair", EpisodeIDs: []string{e.ID}})
			requireOK(t, err)
			err = store.Step(ctx, model, time.Now().Add(time.Second))
			if (err == nil) != fixed || attempts != 2 {
				t.Fatalf("attempts=%d error=%v", attempts, err)
			}
			lessons, err := store.Lessons(ctx)
			requireOK(t, err)
			if (len(lessons) == 1) != fixed {
				t.Fatal("invalid repaired lesson persisted", lessons)
			}
		})
	}
}
