package crm

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
	"unicode/utf8"
)

func TestComparedOptionsRetainTheirAuthoritativeLinePrices(t *testing.T) {
	for _, source := range []string{"rules", "api"} {
		t.Run(source, func(t *testing.T) {
			fx := newFixture(t)
			if source == "api" {
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
			base := map[string]any{"交强险": true, "三者险": "200万", "座位险": true}
			vars := map[string]any{"vehicleAge": 2, "ncd": 1, "seats": 5, "carNum": "test"}
			changes := []map[string]any{{"三者险": "200万"}, {"三者险": "300万"}, {"三者险": "500万"}, {"医保外用药": true}, {"座位险": false}, {"三者险": "999万"}}
			result, err := fx.svc.Compare(context.Background(), QuoteRequest{Call: "out-compare-lines", Config: base, Vars: vars, PlanInMemory: true, Variants: changes})
			if err != nil {
				t.Fatal(err)
			}
			type priced struct {
				Lines   []QuoteLine `json:"lines"`
				Skipped []string    `json:"skipped"`
				Error   string      `json:"error"`
			}
			var got struct {
				priced
				Variants []priced `json:"variants"`
			}
			raw, _ := json.Marshal(result)
			var previousShape map[string]any
			if err := json.Unmarshal(raw, &previousShape); err != nil {
				t.Fatal(err)
			}
			for _, variant := range previousShape["variants"].([]any) {
				delete(variant.(map[string]any), "lines")
			}
			previousRaw, _ := json.Marshal(previousShape)
			t.Logf("six-option fixture: added serialized line data=%d characters", utf8.RuneCount(raw)-utf8.RuneCount(previousRaw))
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Lines) == 0 || len(got.Variants) != len(changes) {
				t.Fatalf("missing base or variants: %s", raw)
			}
			for i, change := range changes {
				if i == len(changes)-1 {
					if got.Variants[i].Error == "" || len(got.Variants[i].Lines) != 0 {
						t.Errorf("unpriced option has valid-looking lines: %+v", got.Variants[i])
					}
					continue
				}
				config := copyVars(base)
				for key, value := range change {
					config[key] = value
				}
				quote, err := fx.svc.Quote(QuoteRequest{Call: "out-compare-lines", Config: config, Vars: copyVars(vars)})
				if err != nil {
					t.Fatal(err)
				}
				var want priced
				raw, _ := json.Marshal(quote)
				if err := json.Unmarshal(raw, &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got.Variants[i].Lines, want.Lines) || !reflect.DeepEqual(got.Variants[i].Skipped, want.Skipped) {
					t.Errorf("variant %d discarded authoritative prices/exclusions: got %+v; want %+v", i, got.Variants[i], want)
				}
			}
			// Editing the returned same-plan option cannot alter either the
			// returned baseline or the independently cached price.
			variants := result["variants"].([]Variant)
			variants[0].Lines[0].Premium = -999
			baseRaw, _ := json.Marshal(result["lines"])
			var baseLines []QuoteLine
			if err := json.Unmarshal(baseRaw, &baseLines); err != nil {
				t.Fatal(err)
			}
			cached, err := fx.svc.Quote(QuoteRequest{Call: "out-compare-lines", Config: copyVars(base), Vars: copyVars(vars)})
			if err != nil {
				t.Fatal(err)
			}
			cachedRaw, _ := json.Marshal(cached)
			var cachedPrice priced
			if err := json.Unmarshal(cachedRaw, &cachedPrice); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(baseLines, got.Lines) || !reflect.DeepEqual(cachedPrice.Lines, got.Lines) {
				t.Fatal("a returned variant changed baseline/cached line storage")
			}
		})
	}
}
