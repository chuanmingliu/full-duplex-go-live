package crm

import (
	"encoding/json"
	"testing"
)

func TestCoverageReviewRejectsUnrequestedAndMalformedResult(t *testing.T) {
	a := FullMotorAPI()
	tests := []struct {
		name       string
		compulsory bool
		extra      string
		cp         string
	}{
		{"unrequested_coverage", false, `,{"coverageCode":"unexpected","amount":"2000000","coveragePreimum":"100"}`, ""},
		{"duplicate_coverage", false, `,{"coverageCode":"952","amount":"2000000","coveragePreimum":"100"}`, ""},
		{"unrequested_compulsory", false, "", "950"},
		{"malformed_compulsory", true, "", "bad"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := apiInquiry{IsInsureCompelInsurance: tc.compulsory, CoverageList: []apiCoverage{{CoverageCode: "952", SumInsured: "2000000"}}}
			raw := `{"coverageList":[{"coverageCode":"952","amount":"2000000","coveragePreimum":"1000"}` + tc.extra + `]}`
			var info apiQuoteInfo
			if err := json.Unmarshal([]byte(raw), &info); err != nil {
				t.Fatal(err)
			}
			info.SumPremium = "1000"
			info.CompelSumPremium = tc.cp
			if err := a.validateComplete(in, info); err == nil {
				t.Fatal("strict response check accepted invalid response")
			}
		})
	}
}

func TestPinnedDefaultRetainsDefaultReadoutMarker(t *testing.T) {
	f := newFixture(t)
	plans := QuotePlans{Default: "simple", Plans: map[string]QuotePlan{"simple": {Coverages: []string{"交强险"}, DefaultSelection: map[string]any{"交强险": true}}}}
	raw, _ := json.Marshal(plans)
	version, err := f.svc.configs.Save(QuotePlansName, raw, "fixture", "test")
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	code, _ := f.do("POST", "/api/tools/quote", map[string]any{"tool": "quote_total", "call": "pinned-default", "quote_plan_version": version.Version, "memory": map[string]any{}, "arguments": map[string]any{}}, &out)
	if code != 200 || out["default"] != true || out["status"] != "quote" {
		t.Fatalf("default marker lost: status=%d body=%v", code, out)
	}
}
