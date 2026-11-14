package crm

import (
	"context"
	"encoding/json"
	"math"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/motorcatalog"
	"github.com/chuanmingliu/golive/internal/quotesvc"
	"github.com/chuanmingliu/golive/internal/tools"
)

func motorSelection() map[string]any {
	out := map[string]any{}
	for _, e := range motorcatalog.Entries() {
		if e.Kind == "boolean" {
			out[e.Name] = true
		} else {
			out[e.Name] = e.Options[0]
		}
	}
	return out
}
func motorVars() map[string]any {
	return map[string]any{"quote_plan": "motor-full", "phone": "synthetic", "carNum": "测试车", "vehicleAge": 3, "vehiclePrice": 150000, "seats": 5, "ncd": 0}
}

func TestFullMotorCatalogEveryTierAndParent(t *testing.T) {
	p := FullMotorPlan()
	if len(p.Coverages) != 18 {
		t.Fatalf("coverages=%d", len(p.Coverages))
	}
	cases := 0
	for _, e := range motorcatalog.Entries() {
		options := e.Options
		if e.Kind == "boolean" {
			options = []string{"投保"}
		}
		for _, o := range options {
			t.Run(e.Name+"/"+o, func(t *testing.T) {
				selection := motorSelection()
				selection[e.Name] = o
				if need, _ := p.Check("motor-full", "api", selection, motorVars()); need != nil {
					t.Fatalf("%+v", need)
				}
				api := FullMotorAPI()
				in, skipped, err := api.inquiry(QuoteRequest{Config: selection, Vars: motorVars(), Strict: true})
				if err != nil || len(skipped) > 0 {
					t.Fatalf("inquiry err=%v skipped=%v", err, skipped)
				}
				raw, err := json.Marshal(in)
				if err != nil {
					t.Fatal(err)
				}
				var wire map[string]any
				_ = json.Unmarshal(raw, &wire)
				if wire["collectVehicleTax"] != true {
					t.Fatal("tax flag missing")
				}
				cases++
			})
		}
		if e.Parent != "" {
			t.Run(e.Name+"/orphan", func(t *testing.T) {
				selection := motorSelection()
				selection[e.Parent] = false
				need, _ := p.Check("motor-full", "api", selection, motorVars())
				if need == nil {
					t.Fatal("orphan was accepted")
				}
				found := false
				for _, m := range need.Missing {
					if m.Kind == "dependency" && m.Field == e.Parent {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing dependency: %+v", need)
				}
			})
		}
	}
	t.Logf("validated %d allowed coverage choices", cases)
	selection := motorSelection()
	selection["三者险"] = "500万"
	if n, _ := p.Check("motor-full", "api", selection, motorVars()); n == nil {
		t.Fatal("unsupported liability tier accepted")
	}
	selection = motorSelection()
	selection["未知险种"] = true
	if n, _ := p.Check("motor-full", "api", selection, motorVars()); n == nil {
		t.Fatal("unknown selection silently omitted")
	}
}

func TestFullMotorRoundTripComparisonAndTax(t *testing.T) {
	backend, err := quotesvc.Open(quotesvc.Options{Dir: t.TempDir(), Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.AddVehicle(quotesvc.Vehicle{Plate: "测试车", Price: 150000, Age: 3, Seats: 5}); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(backend.Handler())
	defer upstream.Close()
	svc, err := Open(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	p := FullMotorPlan()
	plans := QuotePlans{Default: "motor-full", Plans: map[string]QuotePlan{"motor-full": p}}
	save := func(name string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		if _, err := svc.configs.Save(name, b, "fixture", "test"); err != nil {
			t.Fatal(err)
		}
	}
	save(QuotePlansName, plans)
	api := FullMotorAPI()
	api.BaseURL = upstream.URL + "/intranet"
	api.PollEveryMS = 1
	save(QuoteAPIName, api)
	sel := motorSelection()
	req := QuoteRequest{Config: copyVars(sel), Vars: motorVars(), Call: "full-roundtrip", PlanInMemory: true}
	q, err := svc.Quote(req)
	if err != nil {
		t.Fatal(err)
	}
	priced := q.(*APIQuote)
	if len(priced.Lines) != 18 || len(priced.Skipped) > 0 || len(priced.Selection) != 18 {
		t.Fatalf("incomplete: %+v", priced)
	}
	if priced.TaxPremium != 420 {
		t.Fatalf("tax=%v", priced.TaxPremium)
	}
	foundPercent, foundShared := false, false
	for _, line := range priced.Lines {
		if line.Coverage == "绝对免赔率-三者" && line.Choice == "5%" {
			foundPercent = true
		}
		if line.Coverage == "医保外用药责任险-司机" && line.Choice == "共享保额" {
			foundShared = true
		}
	}
	if !foundPercent || !foundShared {
		t.Fatal("lost semantic readout labels")
	}
	req.Variants = []map[string]any{{"三者险": "200万"}, {"三者险": "300万"}, {"三者险": false}, {"车船税": false}}
	comparison, err := svc.Compare(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	variants := comparison["variants"].([]Variant)
	if variants[0].Error != "" || variants[1].Error != "" || variants[0].Total == variants[1].Total {
		t.Fatalf("bad compared prices: %+v", variants)
	}
	if variants[2].Error == "" {
		t.Fatal("comparison accepted orphan riders")
	}
	if variants[3].Error != "" || math.Abs(variants[3].Total-(priced.Total-420)) > 0.001 {
		t.Fatalf("declined tax: %+v", variants[3])
	}
	if sel["三者险"] != "50万" {
		t.Fatal("comparison mutated the base")
	}
	// Alias changes must override the old canonical value before merging.
	raw, _ := json.Marshal(map[string]any{"tool": "quote_total", "call": "alias", "memory": map[string]any{"quote_plan": "motor-full", "quote": sel}, "arguments": map[string]any{"config": map[string]any{"第三者责任险": "300w"}}})
	decoded, err := quoteRequestFrom(svc.normalizeQuoteEnvelope(raw))
	if err != nil || decoded.Config["三者险"] != "300万" {
		t.Fatalf("alias overlay: %+v %v", decoded, err)
	}
	// Model tool description includes separate riders and constraints.
	raw, _ = json.Marshal(plans)
	brief, err := tools.ParseQuoteBrief(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"共享保额", "绝对免赔率-乘客", "司机责任险", "先确认", "400万"} {
		if !strings.Contains(brief.Text("api"), word) {
			t.Errorf("brief missing %s", word)
		}
	}
}

func TestFullMotorMissingMappingFailsBeforePricing(t *testing.T) {
	api := FullMotorAPI()
	delete(api.Coverages, "精神损害险-司机")
	if _, _, err := api.inquiry(QuoteRequest{Config: motorSelection(), Vars: motorVars(), Strict: true}); err == nil {
		t.Fatal("partial mapping accepted")
	}
	api = FullMotorAPI()
	delete(api.Coverages["医保外用药责任险-司机"].ChoiceValues, "共享保额")
	if _, _, err := api.inquiry(QuoteRequest{Config: motorSelection(), Vars: motorVars(), Strict: true}); err == nil {
		t.Fatal("shared limit lost")
	}
	api = FullMotorAPI()
	api.TaxField = ""
	if _, _, err := api.inquiry(QuoteRequest{Config: motorSelection(), Vars: motorVars(), Strict: true}); err == nil {
		t.Fatal("tax mapping missing")
	}
	api = FullMotorAPI()
	in, _, _ := api.inquiry(QuoteRequest{Config: motorSelection(), Vars: motorVars(), Strict: true})
	if err := api.validateComplete(in, apiQuoteInfo{}); err == nil {
		t.Fatal("empty backend result accepted")
	}
	// Percentages are separate fields, never interpreted as yuan.
	raw, _ := json.Marshal(in)
	if !strings.Contains(string(raw), `"deductibleRate":"5"`) || strings.Contains(string(raw), `"sumInsured":"5"`) {
		t.Fatal(string(raw))
	}
}

func TestFullMotorTaxOnlyAndCacheIdentity(t *testing.T) {
	api := FullMotorAPI()
	in, _, err := api.inquiry(QuoteRequest{Config: map[string]any{"车船税": "代缴"}, Vars: motorVars(), Strict: true})
	if err != nil || in.taxCollection == nil || !*in.taxCollection {
		t.Fatalf("tax only: %+v %v", in, err)
	}
	p := FullMotorPlan()
	on := map[string]any{"交强险": true, "车船税": "代缴"}
	off := map[string]any{"交强险": true, "车船税": false}
	implicit := map[string]any{"交强险": true}
	keys := map[string]bool{}
	for _, cfg := range []map[string]any{on, off, implicit} {
		key := selectionKey("same", "same", p, cfg, nil)
		if keys[key] {
			t.Fatal("tax selections share a cache identity")
		}
		keys[key] = true
	}
	if selectedConfig(off)["车船税"] != false {
		t.Fatal("explicit tax decline lost")
	}
}

func TestNamedMotorPlanNeverUsesLegacyDefault(t *testing.T) {
	svc, err := Open(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	var plans QuotePlans
	_ = json.Unmarshal([]byte(DefaultQuotePlans), &plans)
	plans.Plans["motor-full"] = FullMotorPlan()
	raw, _ := json.Marshal(plans)
	if _, err = svc.configs.Save(QuotePlansName, raw, "fixture", "test"); err != nil {
		t.Fatal(err)
	}
	req := QuoteRequest{Vars: map[string]any{"quote_plan": "motor-full"}}
	base, err := svc.defaultForRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if base["司机责任险"] != "2万" || base["座位险"] != nil {
		t.Fatal(base)
	}
	req.Vars["quote_plan"] = "missing"
	if _, err = svc.Quote(req); err == nil {
		t.Fatal("unknown plan fell back silently")
	}
}

func TestPinnedCoveragePlanAndSavedSelector(t *testing.T) {
	svc, err := Open(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	plan := FullMotorPlan()
	plans := QuotePlans{Default: "motor-full", Plans: map[string]QuotePlan{"motor-full": plan}}
	raw, _ := json.Marshal(plans)
	v, err := svc.configs.Save(QuotePlansName, raw, "fixture", "test")
	if err != nil {
		t.Fatal(err)
	}
	plan.Options["三者险"] = []string{"300万"}
	plan.DefaultSelection["三者险"] = "300万"
	plans.Plans["motor-full"] = plan
	raw, _ = json.Marshal(plans)
	_, err = svc.configs.Save(QuotePlansName, raw, "changed", "test")
	if err != nil {
		t.Fatal(err)
	}
	req := QuoteRequest{PlanVersion: v.Version, Vars: motorVars(), Config: map[string]any{"三者险": "200万"}}
	if need, _, _, _ := svc.intake(req); need != nil {
		t.Fatalf("pinned valid tier rejected: %+v", need)
	}
	req.PlanVersion = 0
	if need, _, _, _ := svc.intake(req); need == nil {
		t.Fatal("new catalog still accepts removed tier")
	}
	req.PlanVersion = v.Version
	base, err := svc.defaultForRequest(req)
	if err != nil || base["三者险"] != "200万" {
		t.Fatalf("pinned default %v %v", base, err)
	}
	request := httptest.NewRequest("GET", "/api/quote/selection-catalog?plan=motor-full", nil)
	w := httptest.NewRecorder()
	if err := svc.handleSelectedCatalog(w, request); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Version int
		Entries []motorcatalog.Entry
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, e := range got.Entries {
		if e.Name == "三者险" && (len(e.Options) != 1 || e.Options[0] != "300万") {
			t.Fatal("selector ignored saved edits")
		}
	}
	if got.Version <= v.Version {
		t.Fatal("selector did not report latest version")
	}
	envelope := []byte(`{"tool":"quote_total","quote_plan_version":123,"memory":{},"arguments":{}}`)
	parsed, err := quoteRequestFrom(envelope)
	if err != nil || parsed.PlanVersion != 123 {
		t.Fatalf("version lost %v %v", parsed, err)
	}
	if _, err := svc.Quote(parsed); err == nil {
		t.Fatal("unknown version accepted")
	}
}
