package tools

import (
	"strings"
	"testing"
)

func TestTheDefaultOpenersParseAndMatchByTheCallersWords(t *testing.T) {
	o, err := ParseOpeners([]byte(DefaultOpeners))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		words    string
		question bool
		want     string
	}{
		{"这个一共多少钱啊", true, "price"},
		{"三者险能不能改成三百万", true, "tier"},
		{"座位险是保什么的", true, "coverage"},
		{"去年出过一次险", false, "claim"},
		{"我再看看别家的吧", false, "doubt"},
		{"别家报的比你们便宜", false, "doubt"}, // doubt before price: 便宜 is a comparison
		{"行吧那就这样", false, "yes"},
		{"行吧那就这样？", true, ""}, // a statement-only scenario does not fit a question
		{"今天天气不错", false, ""},
		{"", true, ""},
	} {
		got := ""
		if sc := o.Match(tc.words, tc.question); sc != nil {
			got = sc.ID
		}
		if got != tc.want {
			t.Errorf("%q (question %v): scenario %q, want %q", tc.words, tc.question, got, tc.want)
		}
	}
	// The price scenario is first, so a price question about a tier opens
	// on the price.
	if sc := o.Match("三者险三百万要多少钱", true); sc == nil || sc.ID != "price" {
		t.Fatalf("order: %v", sc)
	}
	if n := len(o.Phrases()); n < 10 {
		t.Fatalf("%d phrases to cache", n)
	}
	for _, p := range o.Phrases() {
		if !strings.HasSuffix(p, "，") {
			t.Errorf("opener %q does not end in a comma", p)
		}
	}
}

func TestOpenersAreCheckedOnSave(t *testing.T) {
	for _, bad := range []string{
		`{"scenarios": [{"id": "", "keywords": ["a"], "phrases": ["好的，"]}]}`,
		`{"scenarios": [{"id": "a", "keywords": [], "phrases": ["好的，"]}]}`,
		`{"scenarios": [{"id": "a", "keywords": ["x"], "phrases": []}]}`,
		`{"scenarios": [{"id": "a", "keywords": ["x"], "phrases": ["这个保额的话，"]}]}`,
		`{"scenarios": [{"id": "a", "keywords": ["x"], "phrases": ["好，"]}, {"id": "a", "keywords": ["y"], "phrases": ["好，"]}]}`,
		`{"scenarios": [{"id": "a", "kind": "maybe", "keywords": ["x"], "phrases": ["好，"]}]}`,
		`not json`,
	} {
		if _, err := ParseOpeners([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	o, err := ParseOpeners([]byte(`{"scenarios": [{"id": "a", "keywords": [" x ", ""], "phrases": [" 好， ", ""], "enabled": false}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if o.Scenarios[0].Keywords[0] != "x" || o.Scenarios[0].Phrases[0] != "好，" {
		t.Fatalf("not trimmed: %+v", o.Scenarios[0])
	}
	if o.Match("x", false) != nil {
		t.Fatal("a disabled scenario matched")
	}
}
