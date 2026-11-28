package crm

import (
	"encoding/json"
	"testing"

	"github.com/chuanmingliu/golive/internal/tools"
)

func TestStreamingGuardConfigIsSeededAndValidated(t *testing.T) {
	fx := newFixture(t)
	url := "/api/configs/" + tools.StreamingGuardName
	var got struct {
		Config ConfigVersion `json:"config"`
	}
	if code, body := fx.do("GET", url, nil, &got); code != 200 || got.Config.Version != 1 {
		t.Fatalf("seed: %d %s", code, body)
	}
	cfg, err := tools.ParseStreamingGuard(got.Config.Body)
	if err != nil || !cfg.Enabled || len(cfg.Rules) == 0 {
		t.Fatalf("seed config: %+v, %v", cfg, err)
	}
	for _, raw := range []string{
		`null`,
		`{"enabled":true,"rules":[{"match":""}]}`,
		`{"enabled":true,"rules":[{"match":"我不猜"},{"match":"我不猜"}]}`,
		`{"enabled":true,"rules":[{"match":"我不猜","replacement":"我不猜。"}]}`,
	} {
		if code, body := fx.do("PUT", url, json.RawMessage(raw), nil); code != 400 {
			t.Fatalf("invalid config: %d %s", code, body)
		}
	}
	if code, body := fx.do("PUT", url, json.RawMessage(`{"enabled":true,"rules":[{"match":"[内部]","replacement":""}]}`), &got); code != 200 || got.Config.Version != 2 {
		t.Fatalf("save: %d %s", code, body)
	}
}

func TestTurnTakingConfigIsSeededValidatedAndVersioned(t *testing.T) {
	fx := newFixture(t)
	url := "/api/configs/" + tools.TurnTakingName
	var got struct {
		Config ConfigVersion `json:"config"`
	}
	if code, body := fx.do("GET", url, nil, &got); code != 200 || got.Config.Version != 1 {
		t.Fatalf("seed: %d %s", code, body)
	}
	if code, body := fx.do("PUT", url, json.RawMessage(`{"enabled":true,"wait_ms":1,"phrases":["等下"]}`), nil); code != 400 {
		t.Fatalf("invalid wait accepted: %d %s", code, body)
	}
	if code, body := fx.do("PUT", url, json.RawMessage(`{"enabled":true,"wait_ms":3000,"phrases":["先听我说"],"acknowledgment":""}`), &got); code != 200 || got.Config.Version != 2 {
		t.Fatalf("save: %d %s", code, body)
	}
}
