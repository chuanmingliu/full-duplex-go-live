package tools

import (
	"strings"
	"testing"
)

func TestTheDefaultQuoteSpeechReadsAQuoteOut(t *testing.T) {
	qs, err := ParseQuoteSpeech([]byte(DefaultQuoteSpeech))
	if err != nil {
		t.Fatal(err)
	}
	out := qs.Readout([]byte(`{"status":"quote","total":3135,"currency":"CNY","lines":[{"coverage":"交强险","premium":950},{"coverage":"三者险","choice":"200万","premium":1185},{"coverage":"车损险","premium":1000}],"skipped":["医保外用药责任险"]}`))
	want := "交强险950元，三者险200万1185元，车损险1000元，合计3135元。医保外用药责任险暂时算不了。"
	if out != want {
		t.Fatalf("readout %q\nwant    %q", out, want)
	}
	// The lead service's quote carries no status field: total and lines say
	// what it is (the 17:30 call of 10-24 said nothing for want of this).
	if out := qs.Readout([]byte(`{"total":11404.75,"currency":"CNY","lines":[{"coverage":"交强险","premium":950}],"skipped":["玻璃险"]}`)); !strings.Contains(out, "合计11405元") || !strings.Contains(out, "玻璃险暂时算不了") {
		t.Fatalf("a status-less quote was not read out: %q", out)
	}
	if qs.Readout([]byte(`{"status":"need","missing":[]}`)) != "" {
		t.Fatal("a need was read out as a quote")
	}
	if _, err := ParseQuoteSpeech([]byte(`{"prehook":"x","template":"{{ total | nosuchfilter }}"}`)); err == nil || !strings.Contains(err.Error(), "template") {
		t.Fatalf("a bad template was accepted: %v", err)
	}
}

// A result that compares options is read from the compare template:
// organized around what changes and by how much, not the plan's lines
// read three times.
func TestAComparisonIsReadAsDifferences(t *testing.T) {
	qs, err := ParseQuoteSpeech([]byte(DefaultQuoteSpeech))
	if err != nil {
		t.Fatal(err)
	}
	out := []byte(`{"status":"quote","compare":true,"total":10299.8,"lines":[{"coverage":"交强险","premium":950}],"variants":[
	  {"label":"三者险200万","total":10299.8,"diff":0,"diff_abs":0,"same":true},
	  {"label":"三者险300万","total":10524.8,"diff":225,"diff_abs":225,"more":true},
	  {"label":"不要座位险","total":10235,"diff":-64.8,"diff_abs":64.8,"more":false},
	  {"label":"划痕险9999","error":"x"}]}`)
	got := qs.Readout(out)
	want := "现在这个方案一共10300元。三者险200万就是现在的方案；三者险300万一共10525元，多225元；不要座位险一共10235元，少65元；划痕险9999暂时算不了；"
	if got != want {
		t.Fatalf("readout:\n%s\nwant:\n%s", got, want)
	}
	// The owner's own compare template, and a broken one refused.
	own, err := ParseQuoteSpeech([]byte(`{"posthook":"算好了，","posthook_status":"quote","compare_template":"{% for v in variants %}{{ v.label }}{% if not v.error %}{{ v.total | round | int }}{% endif %}{% endfor %}"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := own.Readout(out); got != "三者险200万10300三者险300万10525不要座位险10235划痕险9999" {
		t.Fatalf("own template: %q", got)
	}
	if _, err := ParseQuoteSpeech([]byte(`{"posthook":"算好了，","compare_template":"{% for v in variants %}"}`)); err == nil {
		t.Fatal("a broken compare template was accepted")
	}
}
