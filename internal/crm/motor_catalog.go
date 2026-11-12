package crm

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/chuanmingliu/golive/internal/motorcatalog"
)

// FullMotorPlan is an opt-in template. It never replaces a saved default plan.
func FullMotorPlan() QuotePlan {
	zero := 0
	p := QuotePlan{Strict: true, Options: map[string][]string{}, CoverageAliases: map[string]string{}, CoverageDetails: map[string]motorcatalog.Detail{}, Prequote: PrequotePolicy{Max: &zero}}
	for _, e := range motorcatalog.Entries() {
		p.Coverages = append(p.Coverages, e.Name)
		p.CoverageDetails[e.Name] = e.Detail
		if len(e.Options) > 0 {
			p.Options[e.Name] = e.Options
		}
		for _, a := range e.Aliases {
			p.CoverageAliases[a] = e.Name
		}
	}
	var old QuotePlans
	_ = json.Unmarshal([]byte(DefaultQuotePlans), &old)
	p.Fields = old.Plans[old.Default].Fields
	p.DefaultVars = old.Plans[old.Default].DefaultVars
	p.DefaultSelection = map[string]any{"交强险": true, "车船税": "代缴", "车损险": true, "三者险": "200万", "司机责任险": "2万", "乘客责任险": "2万"}
	return p
}

func validateCoverageDetails(p QuotePlan) error {
	names := map[string]bool{}
	for _, c := range p.Coverages {
		if names[c] {
			return fmt.Errorf("duplicate coverage %s", c)
		}
		names[c] = true
	}
	for c, d := range p.CoverageDetails {
		if !names[c] {
			return fmt.Errorf("details name an unknown coverage %s", c)
		}
		switch d.Kind {
		case "boolean", "amount", "shared_amount", "percent", "enum":
		default:
			return fmt.Errorf("invalid kind for %s", c)
		}
		if d.Kind != "boolean" && len(p.Options[c]) == 0 {
			return fmt.Errorf("%s requires options", c)
		}
		if d.Parent != "" && !names[d.Parent] {
			return fmt.Errorf("unknown parent for %s", c)
		}
		seen := map[string]bool{c: true}
		for parent := d.Parent; parent != ""; parent = p.CoverageDetails[parent].Parent {
			if seen[parent] {
				return fmt.Errorf("cyclic parent for %s", c)
			}
			seen[parent] = true
		}
	}
	for alias, c := range p.CoverageAliases {
		if len(p.CoverageDetails) > 0 && (!names[c] || (names[alias] && alias != c)) {
			return fmt.Errorf("invalid alias %s", alias)
		}
	}
	return nil
}

// fullMotorAPI uses our own test-service contract. The operator keeps their
// existing URL/channel/credentials and reviews these mappings before saving.
func FullMotorAPI() QuoteAPI {
	a := QuoteAPI{Coverages: map[string]CoverageCode{}, TaxField: "collectVehicleTax"}
	entries := motorcatalog.Entries()
	codes := map[string]string{}
	for _, e := range entries {
		codes[e.Name] = e.Code
	}
	for _, e := range entries {
		if e.Code == "" {
			continue
		}
		c := CoverageCode{Code: e.Code, Name: e.Label, BaseRiderType: "0", CoverageType: "0", SumInsured: "1"}
		if e.Parent != "" {
			c.BaseRiderType = "1"
			c.ParentCode = codes[e.Parent]
		}
		if e.Kind == "percent" {
			c.ValueField = "deductibleRate"
			c.ChoiceValues = map[string]string{}
			for _, o := range e.Options {
				c.ChoiceValues[o] = o[:len(o)-1]
			}
		}
		if e.Kind == "shared_amount" {
			c.ChoiceValues = map[string]string{"共享保额": "shared"}
		}
		a.Coverages[e.Name] = c
	}
	return a
}

func (s *Service) handleMotorCatalog(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, map[string]any{"name": "motor-full", "plan": FullMotorPlan(), "quote_api": FullMotorAPI(), "entries": motorcatalog.Entries(), "pricing": "synthetic Xiaocui test service; no insurer rates"})
	return nil
}

// A named per-call plan must exist; silently falling back would quote another
// product. Preserve the call's known facts when resolving its default selection.
func (s *Service) namedDefault(req QuoteRequest) (QuoteRequest, bool, error) {
	name, ok := req.Vars["quote_plan"].(string)
	if (!ok || name == "") && req.PlanVersion == 0 {
		return QuoteRequest{}, false, nil
	}
	v, err := s.configs.Get(QuotePlansName, req.PlanVersion)
	if err != nil {
		return QuoteRequest{}, true, err
	}
	plans, err := ParseQuotePlans(v.Body)
	if err != nil {
		return QuoteRequest{}, true, err
	}
	if name == "" {
		name = plans.Default
	}
	plan, exists := plans.Plans[name]
	if !exists {
		return QuoteRequest{}, true, fmt.Errorf("unknown quote_plan %q", name)
	}
	vars := copyVars(plan.DefaultVars)
	for k, v := range s.LeadVars(req.Call) {
		vars[k] = v
	}
	for k, v := range req.Vars {
		vars[k] = v
	}
	return QuoteRequest{Call: req.Call, Vars: vars, Config: copyVars(plan.DefaultSelection), MemoryVersion: req.MemoryVersion, PlanVersion: req.PlanVersion}, true, nil
}
func (s *Service) defaultForRequest(req QuoteRequest) (map[string]any, error) {
	if named, ok, err := s.namedDefault(req); ok || err != nil {
		return named.Config, err
	}
	return s.DefaultSelection(req.Call), nil
}

// handleSelectedCatalog reflects saved edits, unlike the built-in draft template.
func (s *Service) handleSelectedCatalog(w http.ResponseWriter, r *http.Request) error {
	v, err := s.configs.Get(QuotePlansName, 0)
	if err != nil {
		return err
	}
	plans, err := ParseQuotePlans(v.Body)
	if err != nil {
		return err
	}
	name := r.URL.Query().Get("plan")
	if name == "" {
		name = plans.Default
	}
	plan, ok := plans.Plans[name]
	if !ok {
		return fail(http.StatusNotFound, "unknown quote plan")
	}
	entries := []motorcatalog.Entry{}
	for _, name := range plan.Coverages {
		d := plan.CoverageDetails[name]
		if d.Label == "" {
			d.Label = name
		}
		if d.Kind == "" {
			if len(plan.Options[name]) > 0 {
				d.Kind = "amount"
			} else {
				d.Kind = "boolean"
			}
		}
		entries = append(entries, motorcatalog.Entry{Name: name, Detail: d, Options: plan.Options[name]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "version": v.Version, "entries": entries})
	return nil
}
