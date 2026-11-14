package skillsmcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEventResponseClient(t *testing.T) {
	for _, mode := range []string{"valid", "disabled", "wrong-event", "empty", "wrong-uri", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer private-test-token" {
					t.Error("missing auth")
				}
				var msg struct {
					Method string
					Params map[string]any
				}
				json.NewDecoder(r.Body).Decode(&msg)
				if msg.Method != "resources/read" || msg.Params["uri"] != "golive://event-responses/addV" {
					t.Error("incorrect resource")
				}
				if mode == "timeout" {
					<-r.Context().Done()
					return
				}
				response := EventResponse{Revision: 3, Event: "addV", Enabled: mode != "disabled", Templates: []string{"微信已经添加成功了。"}}
				if mode == "wrong-event" {
					response.Event = "another"
				}
				if mode == "empty" {
					response.Templates = nil
				}
				raw, _ := json.Marshal(response)
				uri := "golive://event-responses/addV"
				if mode == "wrong-uri" {
					uri = "golive://wrong"
				}
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"contents": []any{map[string]any{"uri": uri, "mimeType": "application/json", "text": string(raw)}}}})
			}))
			defer svr.Close()
			token := filepath.Join(t.TempDir(), "token")
			os.WriteFile(token, []byte("private-test-token"), 0600)
			client, err := New(Config{URL: svr.URL, TokenFile: token, TimeoutMS: 40})
			if err != nil {
				t.Fatal(err)
			}
			began := time.Now()
			got, err := client.EventResponse(context.Background(), "addV")
			if mode == "valid" || mode == "disabled" {
				if err != nil || got.Revision != 3 {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("invalid response accepted")
			}
			if mode == "timeout" && time.Since(began) > time.Second {
				t.Fatal("unbounded lookup")
			}
		})
	}
}
