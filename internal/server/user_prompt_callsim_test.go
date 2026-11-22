package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/duplex"
	"github.com/chuanmingliu/golive/internal/provider"
)

// Keep the assistant answer deterministic so the two calls have identical history.
type promptSimLLM struct{ requests chan []provider.Message }

func (p promptSimLLM) Name() string { return "prompt-sim" }
func (p promptSimLLM) Stream(_ context.Context, req provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	p.requests <- append([]provider.Message(nil), req.Messages...)
	out := make(chan provider.LLMDelta, 1)
	out <- provider.LLMDelta{Text: "好的。"}
	close(out)
	return out, nil
}

func TestCallSimUserPromptTrackingEvent(t *testing.T) {
	const guidance = "引导用户在产品页调整保障。"
	const credential = "sim-tracking"
	crm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/configs/user-prompt" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"version": 1, "body": map[string]any{"enabled": true, "template": "{% if events.product_page_opened %}" + guidance + "{% endif %}"}}})
	}))
	defer crm.Close()
	run := func(id string, event bool) [][]provider.Message {
		ts, srv, _ := newServerWith(t, func(c *config.Config) {
			c.Realtime.SampleRate = 16000
			c.ToolsBaseURL = crm.URL
			c.BusinessEventToken = credential
			c.Duplex.Speculative = false
			c.Duplex.Opener = false
		})
		model := promptSimLLM{make(chan []provider.Message, 8)}
		srv.Factory = func(o duplex.Options, d duplex.Deps) *duplex.Engine { d.LLM = model; return duplex.New(o, d) }
		c := dialRealtime(t, ts, "/v1/realtime?call="+id)
		sendRaw(t, c.conn, activeCallSessionUpdate)
		c.first("session.updated", 2*time.Second)
		var turns [][]provider.Message
		for i := 0; i < 3; i++ {
			if event && i == 1 {
				req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/calls/"+id+"/events", strings.NewReader(`{"name":"product_page.opened","event_id":"sim-open","source":"simulation"}`))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+credential)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusAccepted {
					t.Fatalf("callback status %d", resp.StatusCode)
				}
			}
			c.turn(16000, 1000)
			select {
			case msgs := <-model.requests:
				turns = append(turns, msgs)
			case <-time.After(4 * time.Second):
				t.Fatal("missing LLM request")
			}
			await(t, "completed spoken turn", 5*time.Second, func() bool { return c.count("response.done") == i+1 })
		}
		return turns
	}
	control := run("prompt-control", false)
	event := run("prompt-event", true)
	for i, base := range control {
		got := event[i]
		if len(base) != len(got) || len(base) < 2 || base[0].Role != provider.RoleSystem || base[len(base)-1].Role != provider.RoleUser {
			t.Fatalf("unexpected roles or length on turn %d", i+1)
		}
		if i > 0 && len(base) < 4 {
			t.Fatal("no conversation history in comparison")
		}
		for _, m := range base {
			if strings.Contains(m.Content, guidance) {
				t.Fatal("guidance present in control")
			}
		}
		want := append([]provider.Message(nil), base...)
		if i > 0 {
			want[len(want)-1].Content += "\n\n[本轮配置的业务指引]\n" + guidance
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("unexpected request difference on turn %d: want %+v, got %+v", i+1, want, got)
		}
	}
}
