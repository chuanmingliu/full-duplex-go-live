package crm

import (
	"encoding/json"
	"testing"

	"github.com/chuanmingliu/golive/internal/tools"
)

func TestUserPromptConfigIsSeededAndValidatesSave(t *testing.T) {
	fx := newFixture(t)
	url := "/api/configs/" + tools.UserPromptName
	var got struct {
		Config ConfigVersion `json:"config"`
	}
	if code, body := fx.do("GET", url, nil, &got); code != 200 || got.Config.Version != 1 {
		t.Fatalf("seed: %d %s", code, body)
	}
	if c, err := tools.ParseUserPrompt(got.Config.Body); err != nil || c.Enabled {
		t.Fatalf("unexpected seed: %+v, %v", c, err)
	}
	if code, body := fx.do("PUT", url, json.RawMessage(`{"enabled":true,"template":"{% if events.wechat_added %}"}`), nil); code != 400 {
		t.Fatalf("invalid template: %d %s", code, body)
	}
	if code, body := fx.do("PUT", url, json.RawMessage(`{"enabled":true,"template":"{% if events.product_page_opened %}指导页面操作{% endif %}"}`), &got); code != 200 || got.Config.Version != 2 {
		t.Fatalf("save: %d %s", code, body)
	}
}
