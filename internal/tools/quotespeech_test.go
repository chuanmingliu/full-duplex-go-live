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
