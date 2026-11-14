package tools

import (
	"strings"
	"testing"
)

func TestCoverageReviewDisabledDefault(t *testing.T) {
	b := QuoteBrief{Default: map[string]any{"交强险": true, "车损险": false, "车船税": false}, Coverages: []string{"交强险", "车损险", "车船税"}}
	text := b.Text("")
	first := strings.Split(strings.Split(text, "默认方案（")[1], "）")[0]
	if strings.Contains(first, "车损险") || strings.Contains(first, "车船税") {
		t.Fatalf("declined coverages listed in default: %s", first)
	}
}

func TestCoverageDeclinedDefaultsDoNotClaimPreparedQuote(t *testing.T) {
	for _, off := range []any{false, "false", "0", "不投保", "不代缴", nil} {
		text := (QuoteBrief{Default: map[string]any{"车损险": off}}).Text("")
		if strings.Contains(text, "在通话前已为这位客户算好") {
			t.Fatalf("decline %v claimed a prepared quote", off)
		}
	}
}
