package speko

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestClientDelegationThroughBridge(t *testing.T) {
	completed := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/llm/responses", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer speko-secret" || r.Header.Get("Idempotency-Key") == "" {
			t.Error("missing backend credentials")
		}
		var request struct {
			Input   []backendMessage
			Routing struct{ Model string }
			Max     int `json:"max_output_tokens"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Routing.Model != "gpt-6-luna" || request.Max != 256 {
			t.Error("invalid backend request")
		}
		raw, _ := json.Marshal(request.Input)
		if !strings.Contains(string(raw), "Thursday") || !strings.Contains(string(raw), "Be brief") {
			t.Error("lost transcript or instructions")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"stop_reason": "stop", "output": []backendMessage{{"message", "assistant", []backendText{{"text", "Thursday is available."}}}}})
	})
	mux.HandleFunc("/v1/live", func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		var first struct {
			Session struct {
				Delegation struct{ Type string }
				Audio      struct {
					Format struct {
						Type string
						Rate int
					}
				}
			}
		}
		if c.ReadJSON(&first) != nil {
			return
		}
		if first.Session.Delegation.Type != "client" || first.Session.Audio.Format.Type != "audio/pcm" || first.Session.Audio.Format.Rate != 24000 {
			t.Error("wrong upstream start")
		}
		_ = c.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"audio": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}}}})
		_ = c.WriteJSON(map[string]string{"type": "session.input_transcript.delta", "delta": "Can we meet Thursday?"})
		for i := 0; i < 2; i++ {
			_ = c.WriteJSON(map[string]any{"type": "session.delegation.created", "delegation": map[string]string{"id": "task_1", "target": "client"}})
		}
		for {
			var e struct {
				Type, Content string
				DelegationID  string `json:"delegation_id"`
			}
			if c.ReadJSON(&e) != nil {
				return
			}
			if e.Type == "session.commentary.append" && e.DelegationID == "task_1" {
				completed <- e.Content
				return
			}
		}
	})
	upstream := httptest.NewServer(mux)
	defer upstream.Close()
	s, server := relayFor(t, upstream.URL, func(c *Config) { c.DelegationMode = "client" })
	c := bridgeClient(t, server, "client-delegation", "g711_alaw")
	defer c.Close()
	if opening(t, server, "client-delegation") != 200 {
		t.Fatal("opening failed")
	}
	select {
	case text := <-completed:
		if text != "Thursday is available." {
			t.Fatal(text)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("backend result was not returned")
	}
	c.Close()
	waitEmpty(t, s)
}

func TestClientBackendRejectsIncompleteAndRedactsErrors(t *testing.T) {
	for _, body := range []string{`{"stop_reason":"max_output_tokens","output":[]}`, `{"error":{"message":"speko-secret"}}`} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		s, err := New(Config{URL: "ws" + strings.TrimPrefix(upstream.URL, "http") + "/v1/live", APIKey: "speko-secret", AuthToken: "local-secret"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.clientResponse(context.Background(), "test", nil)
		if err == nil || strings.Contains(err.Error(), "speko-secret") {
			t.Fatal("incomplete output accepted or credential leaked")
		}
		upstream.Close()
	}
}
