package main

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestExplicitCheckoutUsesPlaceOrder(t *testing.T) {
	f := newFixture(t, nil)
	c := f.s.catalog
	c.SkillSelection = &SkillSelectionConfig{Mode: "always"}
	c.Skills = append(c.Skills, Skill{
		Name: placeOrderSkill, Description: "Explain the product page and checkout.",
		Body: "Published instructions", Enabled: true,
		Events: []string{"order.requested"}, Match: []string{"产品页"},
	})
	if _, err := f.s.publish(c); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		text, state string
		want        []string
	}{
		{"我就按这，怎么下单？", "", []string{placeOrderSkill}},
		{"投保链接在哪？", "added", []string{placeOrderSkill}},
		{"我想在产品页改三者险档位。", "", []string{placeOrderSkill}},
		{"怎么在投保页面修改保险组合？", "", []string{placeOrderSkill}},
		{"我想在产品页把三者改到三百万。", "added", []string{"motor-quotation", placeOrderSkill}},
		{"请重新算一下保费，也告诉我怎么下单。", "", []string{"motor-quotation", placeOrderSkill}},
		{"我暂时不想下单。", "consented", nil},
		{"我先不要在产品页调整保障。", "", nil},
		{"三者险三百万多少钱？", "", []string{"motor-quotation"}},
		{"产品页是什么？", "", nil},
		{"我就按这。", "", nil},
		{"微信服务通知在哪里找？", "consented", []string{"wechat-service-notification"}},
	} {
		t.Run(tc.text, func(t *testing.T) {
			meta := preparedRequest(t, f, tc.text, map[string]string{"wechat.status": tc.state})
			b, _ := json.Marshal(meta["prepared"])
			var p preparedTurn
			if err := json.Unmarshal(b, &p); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, resource := range p.Resources {
				got = append(got, resource.Manifest.Frontmatter["name"])
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("skills = %v, want %v", got, tc.want)
			}
		})
	}
}
