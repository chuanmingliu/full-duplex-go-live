package tools

import (
	"encoding/json"
	"testing"
)

func TestUserPromptValidatesJinjaAndEventBranches(t *testing.T) {
	parse := func(template string) (UserPrompt, error) {
		data, _ := json.Marshal(map[string]any{"enabled": true, "template": template})
		return ParseUserPrompt(data)
	}
	c, err := parse("{% if events.product_page_opened %}页面已打开{% elif events.wechat_added %}已加微信{% endif %}")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		input UserPromptInput
		want  string
	}{
		{UserPromptInput{CallerText: "我打开了页面"}, ""},
		{UserPromptInput{WechatAdded: true}, "已加微信"},
		{UserPromptInput{WechatAdded: true, ProductPageOpened: true}, "页面已打开"},
	} {
		if got, err := c.Render(tc.input); err != nil || got != tc.want {
			t.Fatalf("render = %q, %v, want %q", got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "{{ missing }}", "{% if events.wechat_added %}", "{{ 'x' * 999999999 }}", "{% for x in caller_text %}{{ x }}{% endfor %}"} {
		if _, err := parse(bad); err == nil {
			t.Errorf("accepted invalid template %q", bad)
		}
	}
}
