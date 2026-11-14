package crm

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFullMotorPrequotePriority(t *testing.T) {
	p := FullMotorPlan()
	max := 8
	p.Prequote.Max = &max
	got := p.prequoteVariants(p.DefaultSelection)
	want := []string{"50万", "100万", "150万", "300万", "400万"}
	for i, tier := range want {
		if got[i]["三者险"] != tier {
			t.Fatalf("slot %d = %v", i, got[i])
		}
	}
	if len(got) != 8 {
		t.Fatal(len(got))
	}
	if !reflect.DeepEqual(p.DefaultSelection, FullMotorPlan().DefaultSelection) {
		t.Fatal("default mutated")
	}
	p.Prequote.Priority = nil
	if legacy := p.prequoteVariants(p.DefaultSelection); legacy[0]["三者险"] != "200万" {
		t.Fatal("legacy order changed")
	}
	p.Prequote.Priority = []string{"missing"}
	raw, _ := json.Marshal(QuotePlans{Default: "test", Plans: map[string]QuotePlan{"test": p}})
	if _, err := ParseQuotePlans(raw); err == nil {
		t.Fatal("unknown priority accepted")
	}
	p.Prequote.Priority = []string{"三者险", "三者险"}
	raw, _ = json.Marshal(QuotePlans{Default: "test", Plans: map[string]QuotePlan{"test": p}})
	if _, err := ParseQuotePlans(raw); err == nil {
		t.Fatal("duplicate priority accepted")
	}
}
