package duplex

import "testing"

func TestTextForSynthesisThirdPartyCoverageSeparator(t *testing.T) {
	const text = "医保外用药责任险-三者400万，绝对免赔率-三者5%，保费调整-90元。"
	const want = "医保外用药责任险三者400万，绝对免赔率三者5%，保费调整-90元。"
	if got := textForSynthesis(text); got != want {
		t.Fatalf("synthesis text = %q, want %q", got, want)
	}
}
