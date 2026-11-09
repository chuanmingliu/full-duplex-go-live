package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/live"
)

func TestSessionBackendAddV(t *testing.T) {
	ts, _, _ := newServerWith(t, func(c *config.Config) { c.ToolsMemory = true })
	primary := dialPath(t, ts, "/v1/live")
	pr := startReader(primary)
	sendRaw(t, primary, startResponses)
	started := pr.first1(t, live.ServerSessionStarted, 2*time.Second)
	id := str(t, object(t, started["session"]), "id")
	side := dialPath(t, ts, "/v1/live/sessions/"+id+"/attach")
	sr := startReader(side)
	sendRaw(t, side, `{"type":"golive.business.event","name":"unknown","event_id":"bad"}`)
	await(t, "invalid event rejection", time.Second, func() bool { return sr.sawError("invalid_business_event") })
	sendRaw(t, side, `{"type":"golive.business.event","name":"addV","event_id":"trace-1"}`)
	ev := pr.first1(t, live.ExtMemory, 2*time.Second)
	var patch struct {
		Wechat struct {
			Status string `json:"status"`
		} `json:"wechat"`
	}
	if json.Unmarshal(ev["patch"], &patch) != nil || patch.Wechat.Status != "added" {
		t.Fatal("backend completion not applied", ev)
	}
	sendRaw(t, side, `{"type":"golive.business.event","name":"addV","event_id":"trace-1"}`)
	time.Sleep(50 * time.Millisecond)
	if pr.count(live.ExtMemory) != 1 {
		t.Fatal("duplicate event modified state")
	}
}

func TestRealtimeBackendAddV(t *testing.T) {
	ts, _, _ := gatewayServer(t, func(c *config.Config) { c.ToolsMemory = true })
	primary := dialRealtime(t, ts, "/v1/realtime")
	created := primary.first("session.created", 2*time.Second)
	sendRaw(t, primary.conn, activeCallSessionUpdate)
	primary.first("session.updated", 2*time.Second)
	id := str(t, object(t, created["session"]), "id")
	side := dialPath(t, ts, "/v1/live/sessions/"+id+"/attach")
	events := startReader(side)
	primary.send(map[string]string{"type": "golive.business.event", "name": "addV", "event_id": "realtime-trace"})
	event := events.first1(t, live.ExtMemory, 2*time.Second)
	var patch struct {
		Wechat struct {
			Status string `json:"status"`
		} `json:"wechat"`
	}
	if json.Unmarshal(event["patch"], &patch) != nil || patch.Wechat.Status != "added" {
		t.Fatal("realtime event did not update session memory")
	}
	if primary.count("error") != 0 {
		t.Fatal("realtime event was rejected")
	}
}
