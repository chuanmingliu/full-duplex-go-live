package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCoverageReviewOptionQueries(t *testing.T) {
	f := newFixture(t, nil)
	c := defaults()
	s := vehicleConcepts(t)
	c.Skills = append(c.Skills, s)
	if code, _ := f.request("PUT", "/admin/catalog", "admin-private", c, nil); code != 200 {
		t.Fatal(code)
	}
	for _, text := range []string{"划痕险有哪些档位？", "三者险有哪几档？", "司机责任险可选档位？", "乘客责任险有几档？", "车船税能代缴吗？", "假日三者翻倍险周末也算吗？", "医保外用药怎么选？", "司机医保有哪些档位？", "列出乘客医保全部选项", "麻烦回答一下假日三者翻倍险的问题。"} {
		t.Run(text, func(t *testing.T) {
			out := f.result("prompts/get", obj{"name": "outbound-turn", "arguments": obj{"event": "auto", "utterance": text}})
			found := false
			names := []string{}
			for _, raw := range out["messages"].([]any) {
				link := raw.(obj)["content"].(obj)
				names = append(names, link["name"].(string))
				found = found || link["name"] == s.Name
			}
			if !found {
				t.Fatalf("concepts skill missing; selected=%v", names)
			}
		})
	}
}

func TestAllCatalogNamesRouteToConcepts(t *testing.T) {
	raw, err := os.ReadFile("../../internal/motorcatalog/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Name    string
		Aliases []string
	}
	if err = json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	c := defaults()
	c.Skills = append(c.Skills, vehicleConcepts(t))
	for _, e := range entries {
		for _, name := range append(e.Aliases, e.Name) {
			for _, query := range []string{name + "有哪些选项？", "列出" + name + "全部档位"} {
				skills, tools, err := c.selectTurn(turnInput{Event: "auto", Utterance: query})
				if err != nil || len(skills) != 1 || skills[0].Name != "vehicle_insurance_concepts" || len(tools) != 0 {
					t.Errorf("%s: skills=%d tools=%v err=%v", query, len(skills), tools, err)
				}
			}
		}
	}
}

func TestEveryCoverageChoiceViaMCP(t *testing.T) {
	raw, err := os.ReadFile("../../internal/motorcatalog/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Name    string
		Options []string
	}
	if err = json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, nil)
	c := defaults()
	c.Skills = append(c.Skills, vehicleConcepts(t))
	if code, _ := f.request("PUT", "/admin/catalog", "admin-private", c, nil); code != 200 {
		t.Fatal(code)
	}
	count := 0
	for _, e := range entries {
		options := e.Options
		if len(options) == 0 {
			options = []string{"投保"}
		}
		for _, option := range options {
			query := e.Name + "的" + option + "选项是什么意思？只解释，不报价。"
			out := f.result("prompts/get", obj{"name": "outbound-turn", "arguments": obj{"event": "auto", "utterance": query}})
			messages := out["messages"].([]any)
			if len(messages) != 1 || messages[0].(obj)["content"].(obj)["name"] != "vehicle_insurance_concepts" {
				t.Fatalf("wrong routing for %s", query)
			}
			resource := messages[0].(obj)["content"].(obj)
			body := f.result("resources/read", obj{"uri": resource["uri"]})["contents"].([]any)[0].(obj)["text"].(string)
			if !strings.Contains(body, "活动报价方案") {
				t.Fatal("catalog grounding missing")
			}
			count++
		}
	}
	if count != 77 {
		t.Fatalf("tested choices=%d", count)
	}
}

func TestRealCallCoverageRoutingGaps(t *testing.T) {
	c := defaults()
	c.Skills = append(c.Skills, vehicleConcepts(t))
	for _, tc := range []struct{ q, name string }{
		{"就是三者险都有几个档", "vehicle_insurance_concepts"},
		{"医保外用药大家一般选哪", "vehicle_insurance_concepts"},
		{"驾乘险和司机责任险有什么区别", "vehicle_insurance_concepts"},
		{"驾乘险现在多少钱", "vehicle_insurance_concepts"},
		{"三者五百万比三百万多多少", "motor-quotation"},
		{"医保费给我加个五十万", "motor-quotation"},
		{"三者险每个档位各多少钱", "motor-quotation"},
	} {
		t.Run(tc.q, func(t *testing.T) {
			skills, _, err := c.selectTurn(turnInput{Event: "auto", Utterance: tc.q})
			found := false
			for _, s := range skills {
				found = found || s.Name == tc.name
			}
			if err != nil || !found {
				t.Fatalf("missing %s: %v %v", tc.name, skills, err)
			}
		})
	}
	for _, q := range []string{"今天地铁票比昨天多多少", "不要报价，三者险有几个档", "医保外用药大家一般选哪"} {
		_, tools, err := c.selectTurn(turnInput{Event: "auto", Utterance: q})
		if err != nil || len(tools) > 0 {
			t.Fatalf("unrequested quotation %s: %v", q, tools)
		}
	}
}
