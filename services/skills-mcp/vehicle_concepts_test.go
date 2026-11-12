package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func vehicleConcepts(t *testing.T) Skill {
	t.Helper()
	body, err := os.ReadFile("vehicle_insurance_concepts/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	skill := seedSkill(string(body))
	routing, err := os.ReadFile("vehicle_insurance_concepts/routing.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(routing, &skill); err != nil {
		t.Fatal(err)
	}
	return skill
}

func TestVehicleConceptsPublication(t *testing.T) {
	f := newFixture(t, nil)
	skill := vehicleConcepts(t)
	c := defaults()
	c.Skills = append(c.Skills, skill)
	if code, data := f.request("PUT", "/admin/catalog", "admin-private", c, nil); code != 200 {
		t.Fatalf("publish: %d %s", code, data)
	}
	cases := []struct {
		text  string
		names string
	}{
		{"交强险一般保障什么？", skill.Name},
		{"什么是交强险？", skill.Name},
		{"医保外用药责任险保障哪些费用？", skill.Name},
		{"医保外用药有哪些档位？", skill.Name},
		{"医保外用药你们都支持哪些？", skill.Name},
		{"医保外用药都有多少档位？", skill.Name},
		{"列出医保外用药的全部选项", skill.Name},
		{"司机的医保外用药有哪些保额？", skill.Name},
		{"告诉我乘客医保外用药的所有档位", skill.Name},
		{"医保外用药是什么意思？", skill.Name},
		{"司机责任险和乘客责任险有什么区别？", skill.Name},
		{"绝对免赔率是什么意思？", skill.Name},
		{"把司机责任险改成五万", c.Skills[0].Name},
		{"加上假日三者翻倍险", c.Skills[0].Name},
		{"三者险和交强险有什么区别？", skill.Name},
		{"车损险能赔自己的车吗？", skill.Name},
		{"车上人员责任险保障哪些人？", skill.Name},
		{"保额和保费有什么区别？", skill.Name},
		{"不要报价，只解释交强险保障什么。", skill.Name},
		{"交强险保障什么？另外请按现有方案给我报个价。", c.Skills[0].Name + "," + skill.Name},
		{"请按现有方案给我报个价", c.Skills[0].Name},
		{"把三者险改成三百万，重新算一下。", c.Skills[0].Name},
		{"微信服务通知在哪里？", c.Skills[1].Name},
		{"你好", ""},
		{"不要再解释交强险了", ""},
		{"今天天气怎么样？", ""},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			out := f.result("prompts/get", obj{"name": "outbound-turn", "arguments": obj{"event": "auto", "utterance": tc.text, "state": `{"wechat.status":"consented"}`}})
			var names []string
			for _, raw := range out["messages"].([]any) {
				link := raw.(obj)["content"].(obj)
				names = append(names, link["name"].(string))
				resource := f.result("resources/read", obj{"uri": link["uri"]})["contents"].([]any)[0].(obj)["text"].(string)
				if link["name"] == skill.Name && !strings.Contains(resource, skill.Body) {
					t.Fatal("selected guidance missing")
				}
			}
			if strings.Join(names, ",") != tc.names {
				t.Fatalf("got %v want %s", names, tc.names)
			}
		})
	}
	c.Revision++
	c.Skills[len(c.Skills)-1].Enabled = false
	if code, _ := f.request("PUT", "/admin/catalog", "admin-private", c, nil); code != 200 {
		t.Fatal(code)
	}
	out := f.result("prompts/get", obj{"name": "outbound-turn", "arguments": obj{"event": "auto", "utterance": "交强险一般保障什么？"}})
	if len(out["messages"].([]any)) != 0 {
		t.Fatal("disabled skill selected")
	}
}

func TestSkillNameSeparators(t *testing.T) {
	for _, name := range []string{"vehicle_insurance_concepts", "existing-hyphen", "a_b-c"} {
		c := defaults()
		c.Skills[0].Name = name
		if err := c.validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"_leading", "trailing_", "two__parts", "path/segment", "Uppercase", "a..b"} {
		c := defaults()
		c.Skills[0].Name = name
		if c.validate() == nil {
			t.Fatalf("accepted %s", name)
		}
	}
}
