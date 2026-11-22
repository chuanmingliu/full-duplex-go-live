package main

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestPurchaseRoutesExistingWeChatSkill(t *testing.T) {
	f := newFixture(t, nil)
	c := f.s.catalog
	c.SkillSelection = &SkillSelectionConfig{Mode: "always"}
	if _, err := f.s.publish(c); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, text, state string
		want              bool
	}{
		{"before-consent", "我就按这，怎么下单？", "", true},
		{"confirmed", "我就按这。", "unknown", true},
		{"consented", "投保链接在哪？", "consented", true},
		{"declined", "请问怎么下单？", "declined", false},
		{"added", "请问怎么下单？", "added", false},
		{"refused-purchase", "我暂时不想下单。", "consented", false},
		{"just-quoting", "三者险三百万多少钱？", "", false},
		{"just-navigation-without-consent", "微信服务通知在哪里找？", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := preparedRequest(t, f, tc.text, map[string]string{"wechat.status": tc.state})
			b, _ := json.Marshal(meta["prepared"])
			var p preparedTurn
			if err := json.Unmarshal(b, &p); err != nil {
				t.Fatal(err)
			}
			got := slices.ContainsFunc(p.Resources, func(resource loadedSkill) bool {
				return resource.Manifest.URI == "skill://wechat-service-notification/SKILL.md"
			})
			if got != tc.want {
				t.Fatalf("WeChat routing = %t, want %t; fallback %s", got, tc.want, p.FallbackReason)
			}
		})
	}
}
