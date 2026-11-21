package crm

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Intake exclusions belong to this request, not the cached price of the
// remaining selection. Check both the cache miss and subsequent hit orders.
func TestCachedQuotationKeepsRequestSpecificSkippedCoverages(t *testing.T) {
	for _, name := range []string{"rules/base", "rules/excluded", "api/base", "api/excluded"} {
		t.Run(name, func(t *testing.T) {
			fx := newFixture(t)
			if strings.HasPrefix(name, "api/") {
				fake := &fakeIntranet{readyAt: 2}
				srv := httptest.NewServer(fake.handler())
				t.Cleanup(srv.Close)
				var api QuoteAPI
				if err := json.Unmarshal([]byte(DefaultQuoteAPI), &api); err != nil {
					t.Fatal(err)
				}
				api.BaseURL, api.DefaultPhone = srv.URL+"/intranet", lockedNumber
				api.PollEveryMS, api.TimeoutMS = 1, 1000
				raw, err := json.Marshal(api)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := fx.svc.configs.Save(QuoteAPIName, raw, "test", "test"); err != nil {
					t.Fatal(err)
				}
			}
			quote := func(excluded bool, version int) map[string]any {
				t.Helper()
				config := map[string]any{"交强险": true, "三者险": "500万"}
				if excluded {
					config["医保外用药"] = true // Not in the configured plan.
				}
				q, err := fx.svc.Quote(QuoteRequest{Call: "out-skipped-test", Config: config,
					Vars: map[string]any{"vehicleAge": 2, "ncd": 1, "carNum": "test"}, MemoryVersion: version})
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(q)
				if err != nil {
					t.Fatal(err)
				}
				var out map[string]any
				if err := json.Unmarshal(raw, &out); err != nil {
					t.Fatal(err)
				}
				var want any
				if excluded {
					want = []any{"医保外用药"}
				}
				if !reflect.DeepEqual(out["skipped"], want) {
					t.Errorf("request excluded=%v: skipped=%v, want %v", excluded, out["skipped"], want)
				}
				if version > 1 && out["cached"] != true {
					t.Errorf("same effective selection repriced: %v", out)
				}
				if out["memory_version"] != float64(version) {
					t.Errorf("memory version=%v, want %d", out["memory_version"], version)
				}
				return out
			}
			initial := quote(strings.HasSuffix(name, "/excluded"), 1)
			for i, excluded := range []bool{true, false, true, false} {
				if got := quote(excluded, i+2); got["total"] != initial["total"] {
					t.Errorf("exclusion changed the effective selection's price: %v", got)
				}
			}
		})
	}
}

func TestRequestSkippedCoveragesDoNotShareCachedStorage(t *testing.T) {
	for _, kind := range []string{"rules", "api"} {
		t.Run(kind, func(t *testing.T) {
			skipped := make([]string, 1, 4)
			skipped[0] = "pricing exclusion"
			var cached any = &Quote{Skipped: skipped}
			if kind == "api" {
				cached = &APIQuote{Skipped: skipped}
			}
			result := quoteWithSkipped(cached, []string{"intake exclusion"})
			var got []string
			switch q := result.(type) {
			case *Quote:
				got = q.Skipped
			case *APIQuote:
				got = q.Skipped
			}
			if !reflect.DeepEqual(got, []string{"pricing exclusion", "intake exclusion"}) {
				t.Fatalf("lost pricing or request exclusion: %v", got)
			}
			got[0] = "changed answer"
			if skipped[0] != "pricing exclusion" || skipped[:cap(skipped)][1] != "" {
				t.Fatalf("answer mutated cached backing slice: %v", skipped[:cap(skipped)])
			}
		})
	}
}
