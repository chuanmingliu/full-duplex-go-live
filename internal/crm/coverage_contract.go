package crm

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
)

// Dynamic fields allow a configured upstream dialect without guessing that a
// percentage is an amount. Reserved identity fields cannot be overwritten.
func (c apiCoverage) MarshalJSON() ([]byte, error) {
	type plain apiCoverage
	raw, err := json.Marshal(plain(c))
	if err != nil {
		return nil, err
	}
	if c.valueField == "" || c.valueField == "sumInsured" {
		return raw, nil
	}
	if !safeField(c.valueField) || c.valueField == "coverageCode" || c.valueField == "coverageName" || c.valueField == "parentCoverageCode" || c.valueField == "baseRiderType" || c.valueField == "coverageType" || c.valueField == "isNonDeductible" {
		return nil, fmt.Errorf("invalid coverage value_field")
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	delete(m, "sumInsured")
	m[c.valueField] = c.value
	return json.Marshal(m)
}
func (in apiInquiry) MarshalJSON() ([]byte, error) {
	type plain apiInquiry
	raw, err := json.Marshal(plain(in))
	if err != nil || in.taxCollection == nil {
		return raw, err
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if !safeField(in.taxField) {
		return nil, fmt.Errorf("invalid tax_field")
	}
	if _, exists := m[in.taxField]; exists {
		return nil, fmt.Errorf("tax_field conflicts with inquiry field")
	}
	m[in.taxField] = *in.taxCollection
	return json.Marshal(m)
}

var fieldName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

func safeField(s string) bool { return fieldName.MatchString(s) }

func (a *QuoteAPI) validateComplete(in apiInquiry, info apiQuoteInfo) error {
	total, err := strconv.ParseFloat(info.SumPremium, 64)
	if err != nil || math.IsNaN(total) || math.IsInf(total, 0) || total < 0 {
		return fmt.Errorf("quotation returned an invalid total")
	}
	expected := map[string]bool{}
	for _, c := range in.CoverageList {
		expected[c.CoverageCode+"/"+c.ParentCoverageCode] = true
	}
	seen := map[string]bool{}
	for _, c := range info.CoverageList {
		key := c.CoverageCode + "/" + c.ParentCoverageCode
		if !expected[key] || seen[key] {
			return fmt.Errorf("quotation returned unexpected or duplicate coverage %s", c.CoverageCode)
		}
		seen[key] = true
		if c.StandardPremium != "" {
			if err := validQuoteMoney(c.StandardPremium, true); err != nil {
				return err
			}
		}
	}
	if in.IsInsureCompelInsurance {
		if err := validQuoteMoney(info.CompelSumPremium, false); err != nil {
			return fmt.Errorf("invalid compulsory premium: %w", err)
		}
	} else if info.CompelSumPremium != "" {
		if err := validQuoteZero(info.CompelSumPremium); err != nil {
			return fmt.Errorf("quotation included declined compulsory insurance")
		}
	}
	if in.taxCollection != nil && *in.taxCollection {
		if err := validQuoteMoney(info.TaxPremium, false); err != nil {
			return fmt.Errorf("invalid tax premium: %w", err)
		}
	}
	for _, value := range []string{info.BusinessSumPremium, info.BizStandardTotal, info.BizDiscountPremium, info.TaxLateCharge} {
		if value != "" {
			if err := validQuoteMoney(value, false); err != nil {
				return err
			}
		}
	}
	for _, want := range in.CoverageList {
		found := false
		for _, got := range info.CoverageList {
			if got.CoverageCode == want.CoverageCode && got.ParentCoverageCode == want.ParentCoverageCode && got.CoveragePremium != "" {
				premium, err := strconv.ParseFloat(got.CoveragePremium, 64)
				if err != nil || math.IsNaN(premium) || math.IsInf(premium, 0) {
					return fmt.Errorf("quotation returned an invalid coverage premium")
				}
				if want.valueField == "deductibleRate" && got.Tag != want.value+"%" {
					return fmt.Errorf("quotation changed requested deductible for %s", want.CoverageName)
				}
				if want.valueField == "" || want.valueField == "sumInsured" {
					if want.SumInsured == "shared" {
						shared := false
						for _, parent := range info.CoverageList {
							if parent.CoverageCode == want.ParentCoverageCode && got.Amount == parent.Amount {
								shared = true
								break
							}
						}
						if !shared {
							return fmt.Errorf("quotation changed shared limit for %s", want.CoverageName)
						}
					} else if want.SumInsured != "1" && want.SumInsured != "" {
						expected, err1 := strconv.ParseFloat(want.SumInsured, 64)
						actual, err2 := strconv.ParseFloat(got.Amount, 64)
						if err1 != nil || err2 != nil || expected != actual {
							return fmt.Errorf("quotation changed insured amount for %s", want.CoverageName)
						}
					}
				}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("quotation omitted requested coverage %s", want.CoverageName)
		}
	}
	if in.IsInsureCompelInsurance && info.CompelSumPremium == "" {
		return fmt.Errorf("quotation omitted compulsory insurance")
	}
	if in.taxCollection != nil {
		if *in.taxCollection && info.TaxPremium == "" {
			return fmt.Errorf("quotation omitted tax collection")
		}
		if !*in.taxCollection {
			n, err := strconv.ParseFloat(info.TaxPremium, 64)
			if info.TaxPremium != "" && (err != nil || n != 0) {
				return fmt.Errorf("quotation collected declined vehicle tax")
			}
		}
	}
	return nil
}

// Preserve requested canonical tiers (including percentage/shared labels) in
// speech; monetary premiums still come only from the pricing backend.
func (a *QuoteAPI) labelStrictLines(q *APIQuote, req QuoteRequest) {
	for i := range q.Lines {
		line := &q.Lines[i]
		for name, c := range a.Coverages {
			if line.Coverage == c.Name {
				line.Coverage = name
				if v, ok := req.Config[name].(string); ok {
					line.Choice = v
				}
				break
			}
		}
	}
}

// Normalize the base and each patch independently, before overlays, so a new
// alias such as 第三者责任险 does not lose to the old canonical 三者险 entry.
func (s *Service) normalizeQuoteEnvelope(raw []byte) []byte {
	var pin struct {
		Version int `json:"quote_plan_version"`
	}
	if json.Unmarshal(raw, &pin) != nil {
		return raw
	}
	v, err := s.configs.Get(QuotePlansName, pin.Version)
	if err != nil {
		return raw
	}
	plans, err := ParseQuotePlans(v.Body)
	if err != nil {
		return raw
	}
	var env map[string]any
	if json.Unmarshal(raw, &env) != nil {
		return raw
	}
	vars, _ := env["vars"].(map[string]any)
	if memory, ok := env["memory"].(map[string]any); ok {
		vars = memory
	}
	_, plan := plans.plan(vars)
	normalize := func(v any) {
		if m, ok := v.(map[string]any); ok {
			plan.NormalizeCoverages(m)
		}
	}
	if memory, ok := env["memory"].(map[string]any); ok {
		normalize(memory["quote"])
	}
	normalize(env["config"])
	if args, ok := env["arguments"].(map[string]any); ok {
		normalize(args["config"])
		if list, ok := args["configs"].([]any); ok {
			for _, v := range list {
				normalize(v)
			}
		}
	}
	out, err := json.Marshal(env)
	if err != nil {
		return raw
	}
	return out
}

func validQuoteMoney(value string, signed bool) error {
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || (!signed && n < 0) {
		return fmt.Errorf("quotation returned invalid money")
	}
	return nil
}
func validQuoteZero(value string) error {
	if err := validQuoteMoney(value, false); err != nil {
		return err
	}
	n, _ := strconv.ParseFloat(value, 64)
	if n != 0 {
		return fmt.Errorf("quotation returned a nonzero declined premium")
	}
	return nil
}
