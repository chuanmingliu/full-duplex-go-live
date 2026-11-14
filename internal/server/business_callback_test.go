package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/live"
)

func TestTrackingCallbackTargetsOnlyItsCall(t *testing.T) {
	const token = "synthetic-tracking-secret"
	ts, _, _ := gatewayServer(t, func(c *config.Config) { c.ToolsMemory = true; c.BusinessEventToken = token })
	post := func(call, body, credential string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/calls/"+call+"/events", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+credential)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	body := `{"name":"addV","event_id":"synthetic-1","source":"simulation"}`
	if got := post("first", body, "wrong"); got != 401 {
		t.Fatalf("unauthorized=%d", got)
	}
	if got := post("missing", body, token); got != 404 {
		t.Fatalf("missing=%d", got)
	}
	first := dialRealtime(t, ts, "/v1/realtime?call=first")
	created := first.first("session.created", time.Second)
	if got := post("first", body, token); got != 409 {
		t.Fatalf("not ready=%d", got)
	}
	sendRaw(t, first.conn, activeCallSessionUpdate)
	first.first("session.updated", time.Second)
	side := dialPath(t, ts, "/v1/live/sessions/"+str(t, object(t, created["session"]), "id")+"/attach")
	observed := startReader(side)
	second := dialRealtime(t, ts, "/v1/realtime?call=second")
	createdSecond := second.first("session.created", time.Second)
	sendRaw(t, second.conn, activeCallSessionUpdate)
	second.first("session.updated", time.Second)
	otherSide := dialPath(t, ts, "/v1/live/sessions/"+str(t, object(t, createdSecond["session"]), "id")+"/attach")
	other := startReader(otherSide)
	if got := post("first", body, token); got != 202 {
		t.Fatalf("accepted=%d", got)
	}
	observed.first1(t, live.ExtMemory, time.Second)
	if got := post("first", body, token); got != 202 {
		t.Fatalf("duplicate=%d", got)
	}
	time.Sleep(50 * time.Millisecond)
	if observed.count(live.ExtMemory) != 1 || other.count(live.ExtMemory) != 0 {
		t.Fatal("duplicate or cross-call state update")
	}
	duplicate := dialRealtime(t, ts, "/v1/realtime?call=first")
	duplicate.first("session.created", time.Second)
	if got := post("first", body, token); got != 409 {
		t.Fatalf("ambiguous=%d", got)
	}
	for _, bad := range []string{`{}`, `{"name":"other","event_id":"1"}`, `{"name":"addV","event_id":""}`, `{"name":"addV","event_id":"1","source":"model"}`, body + `{}`, `{"name":"addV","event_id":"1","customer":"private"}`} {
		if got := post("first", bad, token); got != 400 {
			t.Fatalf("invalid body status=%d", got)
		}
	}
}

func TestTrackingCallbackDisabledByDefault(t *testing.T) {
	ts, _, _ := gatewayServer(t, nil)
	resp, err := http.Post(ts.URL+"/v1/calls/missing/events", "application/json", strings.NewReader(`{"name":"addV","event_id":"1"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("disabled=%d", resp.StatusCode)
	}
}
