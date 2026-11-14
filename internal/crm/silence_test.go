package crm

import (
	"encoding/json"
	"testing"

	"github.com/chuanmingliu/golive/internal/tools"
)

func TestSilenceReactionConfigIsSeededValidatedAndVersioned(t *testing.T) {
	fx := newFixture(t)
	var got struct {
		Config struct {
			Version int                   `json:"version"`
			Body    tools.SilenceReaction `json:"body"`
		} `json:"config"`
	}
	url := "/api/configs/" + tools.SilenceReactionName
	if code, body := fx.do("GET", url, nil, &got); code != 200 || !got.Config.Body.Enabled || got.Config.Version != 1 {
		t.Fatalf("seed: %d %s", code, body)
	}
	if code, body := fx.do("PUT", url, json.RawMessage(`{"enabled":true,"min_wait_ms":0}`), nil); code != 400 {
		t.Fatalf("invalid config accepted: %d %s", code, body)
	}
	c := got.Config.Body
	c.Rounds[0].Templates = []string{"您还在吗？"}
	c.Rounds[0].MinWaitMS, c.Rounds[0].MaxWaitMS = 4000, 6000
	if code, body := fx.do("PUT", url, c, &got); code != 200 || got.Config.Version != 2 {
		t.Fatalf("save: %d %s", code, body)
	}
	if code, body := fx.do("GET", url, nil, &got); code != 200 || got.Config.Body.Rounds[0].Templates[0] != c.Rounds[0].Templates[0] {
		t.Fatalf("read saved template: %d %s", code, body)
	}
	c.Rounds[1].Templates = c.Rounds[0].Templates
	if code, _ := fx.do("PUT", url, c, nil); code != 400 {
		t.Fatal("duplicate rounds saved")
	}
	if code, _ := fx.do("GET", url, nil, &got); code != 200 || got.Config.Version != 2 {
		t.Fatal("rejected config modified saved version")
	}
	if code, body := fx.do("PUT", url, map[string]bool{"enabled": false}, &got); code != 200 || got.Config.Version != 3 {
		t.Fatalf("disable: %d %s", code, body)
	}
	if code, body := fx.do("GET", url, nil, &got); code != 200 || got.Config.Body.Enabled {
		t.Fatalf("read disabled config: %d %s", code, body)
	}
}
