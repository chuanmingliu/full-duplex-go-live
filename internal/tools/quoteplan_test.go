package tools

import (
	"strings"
	"testing"
)

func TestTheQuoteBriefListsThePlansFieldsForTheModel(t *testing.T) {
	raw := `{"default":"standard","plans":{"standard":{"coverages":["交强险","三者险","座位险"],"options":{"三者险":["100万","200万"]},
	 "fields":[{"name":"vehicleAge","for":["*"],"question":"车用了几年了？"},{"name":"seats","for":["座位险"],"question":"几座的？","source":"rules"},{"name":"carNum","for":["*"],"question":"车牌？","source":"api"}]}}}`
	b, err := ParseQuoteBrief([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	text := b.Text("rules")
	for _, want := range []string{"交强险、三者险、座位险", "三者险档位 100万/200万", "vehicleAge：车用了几年了？（所有方案需要）", "seats：几座的？（选了座位险时需要）", "不要在对话一开始"} {
		if !strings.Contains(text, want) {
			t.Fatalf("brief lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "carNum") {
		t.Fatalf("an api-only field is in a rules brief:\n%s", text)
	}
	if !strings.Contains(b.Text(""), "carNum") {
		t.Fatal("the unfiltered brief lacks carNum")
	}
}
