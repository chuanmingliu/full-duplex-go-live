package crm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The business's quotation service, as the interface document of 2026-07-21
// describes it: an asynchronous inquiry that answers "ok" at once and prices
// in the background, and a query that returns the latest quote for a phone
// and a plate. The adapter submits the inquiry, polls the query until a quote
// newer than the one that was there before appears (or the one there is
// marked done), and turns it into the Quote the assistant reads out.
//
// It is configured by the versioned config "quote-api"; when that config has
// a base_url, quotes go to the service, otherwise to the placeholder rules.
// The config is the business's (a base URL on the intranet, channel ids, a
// test phone) and is not in the repository.

// QuoteAPIName is the config that points at the quotation service.
const QuoteAPIName = "quote-api"

// QuoteAPI is that config.
type QuoteAPI struct {
	// BaseURL is the service, up to and including /intranet.
	BaseURL string `json:"base_url"`
	// BizSource (or BizOrigin) is the marketing channel the inquiry runs as.
	BizSource      string `json:"biz_source,omitempty"`
	BizOrigin      string `json:"biz_origin,omitempty"`
	RealChannelID  string `json:"real_channel_id,omitempty"`
	NotSilentLogin bool   `json:"not_silent_login,omitempty"`
	ProvinceCode   string `json:"insure_place_province_code,omitempty"`
	CityCode       string `json:"insure_place_city_code,omitempty"`
	// PhoneVar and VehicleVar name the memory variables that hold the phone
	// and the plate (phone, carNum by default); DefaultPhone and
	// DefaultVehicleNo stand in when the memory has none (the test line).
	PhoneVar         string `json:"phone_var,omitempty"`
	VehicleVar       string `json:"vehicle_var,omitempty"`
	DefaultPhone     string `json:"default_phone,omitempty"`
	DefaultVehicleNo string `json:"default_vehicle_no,omitempty"`
	// Coverages maps the assistant's coverage names to the service's codes.
	// A coverage the map does not have is not sent (and is reported as
	// skipped); 交强险 is sent as isInsureCompelInsurance.
	Coverages map[string]CoverageCode `json:"coverages"`
	// PollEveryMS and TimeoutMS bound the wait for the quote.
	PollEveryMS int `json:"poll_every_ms,omitempty"`
	TimeoutMS   int `json:"timeout_ms,omitempty"`
}

// CoverageCode is how one coverage is asked for.
type CoverageCode struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	BaseRiderType string `json:"base_rider_type"` // 0 main, 1 rider
	CoverageType  string `json:"coverage_type"`   // 0 single amount, 1 percentage, 2 rider
	ParentCode    string `json:"parent_code,omitempty"`
	// SumInsured is sent when the selection is just "true"; a choice like
	// "300万" is converted to yuan and sent instead. "1" means the parent's.
	SumInsured string `json:"sum_insured,omitempty"`
	// NonDeductible asks for 不计免赔.
	NonDeductible bool `json:"non_deductible,omitempty"`
}

// DefaultQuoteAPI is the config's starting point: no base URL (quotes go to
// the rules) and the standard coverage codes from the document.
const DefaultQuoteAPI = `{
  "base_url": "",
  "biz_source": "ai_test",
  "not_silent_login": false,
  "phone_var": "phone",
  "vehicle_var": "carNum",
  "coverages": {
    "车损险": {"code": "951", "name": "机动车损失保险", "base_rider_type": "0", "coverage_type": "0", "sum_insured": "1", "non_deductible": true},
    "三者险": {"code": "952", "name": "第三者责任险", "base_rider_type": "0", "coverage_type": "2", "sum_insured": "2000000", "non_deductible": true},
    "司机险": {"code": "953", "name": "司机责任险", "base_rider_type": "0", "coverage_type": "2", "sum_insured": "20000"},
    "乘客险": {"code": "954", "name": "乘客责任险", "base_rider_type": "0", "coverage_type": "2", "sum_insured": "20000"},
    "座位险": {"code": "954", "name": "乘客责任险", "base_rider_type": "0", "coverage_type": "2", "sum_insured": "20000"}
  },
  "poll_every_ms": 1500,
  "timeout_ms": 90000
}
`

func (a *QuoteAPI) pollEvery() time.Duration {
	if a.PollEveryMS > 0 {
		return time.Duration(a.PollEveryMS) * time.Millisecond
	}
	return 1500 * time.Millisecond
}

func (a *QuoteAPI) timeout() time.Duration {
	if a.TimeoutMS > 0 {
		return time.Duration(a.TimeoutMS) * time.Millisecond
	}
	return 90 * time.Second
}

// apiCoverage is coverageList[] on the inquiry.
type apiCoverage struct {
	CoverageCode       string `json:"coverageCode"`
	CoverageName       string `json:"coverageName"`
	BaseRiderType      string `json:"baseRiderType"`
	CoverageType       string `json:"coverageType"`
	IsNonDeductible    string `json:"isNonDeductible"`
	ParentCoverageCode string `json:"parentCoverageCode,omitempty"`
	SumInsured         string `json:"sumInsured"`
}

// apiInquiry is the inquiry's body.
type apiInquiry struct {
	Phone                     string        `json:"phone"`
	VehicleNo                 string        `json:"vehicleNo"`
	NotSilentLogin            bool          `json:"notSilentLogin,omitempty"`
	BizOrigin                 string        `json:"bizOrigin,omitempty"`
	BizSource                 string        `json:"bizSource,omitempty"`
	InsurePlaceProvinceCode   string        `json:"insurePlaceProvinceCode,omitempty"`
	InsurePlaceCityCode       string        `json:"insurePlaceCityCode,omitempty"`
	NeedSelectInsure          bool          `json:"needSelectInsure"`
	IsInsureBusinessInsurance bool          `json:"isInsureBusinessInsurance"`
	IsInsureCompelInsurance   bool          `json:"isInsureCompelInsurance"`
	CoverageList              []apiCoverage `json:"coverageList,omitempty"`
}

// apiEnvelope is data.Result: code, msg, data.
type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// apiQuoteDetail is the query's data.
type apiQuoteDetail struct {
	Result          string         `json:"Result"`
	ResultMessage   string         `json:"ResultMessage"`
	QuotePriceInfos []apiQuoteInfo `json:"quotePriceInfos"`
	PremiumDiff     string         `json:"preimumDiff,omitempty"`
	raw             json.RawMessage
}

type apiQuoteInfo struct {
	QuotePriceID        int64  `json:"quotePriceId"`
	Result              string `json:"result"`
	ResultMessage       string `json:"resultMessage"`
	PlanType            string `json:"planType"`
	InsureFlowCode      string `json:"insureFlowCode"`
	SumPremium          string `json:"sumPreimum"`
	SelfPricingRatio    string `json:"selfPricingRatio"`
	BusinessQuoteResult string `json:"businessQuoteResult"`
	BusinessSumPremium  string `json:"businessSumPreimum"`
	BizStandardTotal    string `json:"bizStandardTotalPremium"`
	BizDiscountPremium  string `json:"bizDiscountPremium"`
	BusinessDiscount    string `json:"businessDiscount"`
	BusinessEffective   string `json:"businessEffectiveDate"`
	BusinessExpire      string `json:"businessExpireDate"`
	CompelQuoteResult   string `json:"compelQuoteResult"`
	CompelSumPremium    string `json:"compelSumPreimum"`
	CompelEffective     string `json:"compelEffectiveDate"`
	CompelExpire        string `json:"compelExpireDate"`
	TaxPremium          string `json:"taxPreimum"`
	TaxLateCharge       string `json:"taxLateCharge"`
	IsEffective         bool   `json:"isEffective"`
	OrderTime           string `json:"orderTime"`
	OrderRemainTime     int64  `json:"orderRemainTime"`
	CanQuoteDay         string `json:"canQuoteDay"`
	CoverageList        []struct {
		CoverageCode       string `json:"coverageCode"`
		CoverageType       string `json:"coverageType"`
		CoverageSimpleName string `json:"coverageSimpleName"`
		ParentCoverageCode string `json:"parentCoverageCode"`
		Amount             string `json:"amount"`
		Tag                string `json:"tag"`
		CoveragePremium    string `json:"coveragePreimum"`
		StandardPremium    string `json:"standardPremium"`
	} `json:"coverageList"`
	AddServiceList []struct {
		CoverageCode string `json:"coverageCode"`
		Title        string `json:"tittle"`
		Time         string `json:"time"`
		Amount       string `json:"amount"`
	} `json:"addServiceList"`
}

// APIQuote is the service's answer, normalized for the assistant: the
// totals as numbers, the coverages as lines, the dates, and a few words the
// model can say. The service's own record is kept with the quote event.
type APIQuote struct {
	Total            float64     `json:"total"`
	Currency         string      `json:"currency"`
	BusinessPremium  float64     `json:"business_premium"`
	BusinessStandard float64     `json:"business_standard_premium,omitempty"`
	BusinessDiscount float64     `json:"business_discount_amount,omitempty"`
	DiscountRatio    string      `json:"discount_ratio,omitempty"`
	CompelPremium    float64     `json:"compel_premium"`
	TaxPremium       float64     `json:"tax_premium"`
	TaxLateCharge    float64     `json:"tax_late_charge,omitempty"`
	PremiumDiff      string      `json:"premium_diff_vs_last_year,omitempty"`
	Lines            []QuoteLine `json:"lines"`
	Services         []string    `json:"services,omitempty"`
	BusinessPeriod   string      `json:"business_period,omitempty"`
	CompelPeriod     string      `json:"compel_period,omitempty"`
	PlanType         string      `json:"plan_type,omitempty"`
	InsureFlowCode   string      `json:"insure_flow_code,omitempty"`
	QuotePriceID     int64       `json:"quote_price_id"`
	ValidForS        int64       `json:"valid_for_s,omitempty"`
	Skipped          []string    `json:"skipped,omitempty"`
	MemoryVersion    int         `json:"memory_version,omitempty"`
	ComputedAt       time.Time   `json:"computed_at"`
	ComputeMS        int64       `json:"compute_ms"`
	Source           string      `json:"source"`
}

// yuan turns "300万", "3000000", "200w", 2000000 into a yuan string.
func yuan(v any) (string, bool) {
	f, ok := toFloat(v)
	if !ok {
		return "", false
	}
	if s, isStr := v.(string); isStr && (strings.Contains(strings.ToLower(s), "w")) && !strings.Contains(s, "万") {
		f *= 10000
	}
	return strconv.FormatInt(int64(f), 10), true
}

// inquiry builds the inquiry from the assistant's request.
func (a *QuoteAPI) inquiry(req QuoteRequest) (apiInquiry, []string, error) {
	phoneVar, vehicleVar := a.PhoneVar, a.VehicleVar
	if phoneVar == "" {
		phoneVar = "phone"
	}
	if vehicleVar == "" {
		vehicleVar = "carNum"
	}
	phone := strings.TrimSpace(fmt.Sprint(valueOr(req.Vars, phoneVar, a.DefaultPhone)))
	vehicle := strings.TrimSpace(fmt.Sprint(valueOr(req.Vars, vehicleVar, a.DefaultVehicleNo)))
	if phone == "" || phone == "<nil>" {
		return apiInquiry{}, nil, fmt.Errorf("no phone to quote for: the working memory has no %q", phoneVar)
	}
	if vehicle == "" || vehicle == "<nil>" {
		return apiInquiry{}, nil, fmt.Errorf("no plate to quote for: the working memory has no %q", vehicleVar)
	}
	vehicle = strings.ReplaceAll(strings.ReplaceAll(vehicle, "·", ""), " ", "")
	in := apiInquiry{Phone: phone, VehicleNo: vehicle, NotSilentLogin: a.NotSilentLogin, BizOrigin: a.BizOrigin, BizSource: a.BizSource,
		InsurePlaceProvinceCode: a.ProvinceCode, InsurePlaceCityCode: a.CityCode, NeedSelectInsure: true,
		IsInsureBusinessInsurance: false, IsInsureCompelInsurance: false}
	var skipped []string
	names := make([]string, 0, len(req.Config))
	for n := range req.Config {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		choice := req.Config[name]
		if !selected(choice) {
			continue
		}
		if name == "交强险" {
			in.IsInsureCompelInsurance = true
			continue
		}
		cc, ok := a.Coverages[name]
		if !ok {
			skipped = append(skipped, name)
			continue
		}
		sum := cc.SumInsured
		if s, isStr := choice.(string); isStr && s != "true" && s != "是" && s != "要" {
			if y, ok := yuan(s); ok {
				sum = y
			}
		} else if _, isNum := choice.(float64); isNum {
			if y, ok := yuan(choice); ok {
				sum = y
			}
		}
		nd := "0"
		if cc.NonDeductible {
			nd = "1"
		}
		in.CoverageList = append(in.CoverageList, apiCoverage{CoverageCode: cc.Code, CoverageName: cc.Name, BaseRiderType: cc.BaseRiderType,
			CoverageType: cc.CoverageType, IsNonDeductible: nd, ParentCoverageCode: cc.ParentCode, SumInsured: sum})
		in.IsInsureBusinessInsurance = true
	}
	if !in.IsInsureBusinessInsurance && !in.IsInsureCompelInsurance {
		return in, skipped, errors.New("nothing selected that the service can price")
	}
	return in, skipped, nil
}

func selected(choice any) bool {
	switch x := choice.(type) {
	case bool:
		return x
	case string:
		s := strings.TrimSpace(x)
		return s != "" && s != "false" && s != "否" && s != "不要" && s != "无" && s != "0"
	case nil:
		return false
	}
	return true
}

func valueOr(vars map[string]any, key, fallback string) any {
	if v, ok := vars[key]; ok && v != nil && fmt.Sprint(v) != "" {
		return v
	}
	return fallback
}

// Quote runs the whole thing: what was there before, the inquiry, the poll.
func (a *QuoteAPI) Quote(ctx context.Context, client *http.Client, req QuoteRequest) (*APIQuote, json.RawMessage, error) {
	began := time.Now()
	in, skipped, err := a.inquiry(req)
	if err != nil {
		return nil, nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	ctx, cancel := context.WithTimeout(ctx, a.timeout())
	defer cancel()
	// The quote that is there now, so a stale one is not mistaken for ours.
	before, _ := a.query(ctx, client, in)
	var beforeID int64 = -1
	beforeFlow := ""
	if before != nil && len(before.QuotePriceInfos) > 0 {
		beforeID, beforeFlow = before.QuotePriceInfos[0].QuotePriceID, before.QuotePriceInfos[0].InsureFlowCode
	}
	if err := a.submit(ctx, client, in); err != nil {
		return nil, nil, err
	}
	for {
		select {
		case <-time.After(a.pollEvery()):
		case <-ctx.Done():
			return nil, nil, fmt.Errorf("the quotation service did not answer within %s", a.timeout())
		}
		d, err := a.query(ctx, client, in)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, fmt.Errorf("the quotation service did not answer within %s", a.timeout())
			}
			continue
		}
		if len(d.QuotePriceInfos) == 0 {
			continue
		}
		info := d.QuotePriceInfos[0]
		if info.QuotePriceID == beforeID && info.InsureFlowCode == beforeFlow && beforeID != -1 {
			continue // still the old one
		}
		if info.Result == "" && info.SumPremium == "" {
			continue // a record, nothing priced yet
		}
		q := a.normalize(d, info, skipped)
		q.MemoryVersion, q.ComputedAt, q.ComputeMS = req.MemoryVersion, began, time.Since(began).Milliseconds()
		if info.Result != "0" {
			msg := info.ResultMessage
			if msg == "" {
				msg = d.ResultMessage
			}
			return q, d.raw, fmt.Errorf("the quotation failed: %s", msg)
		}
		return q, d.raw, nil
	}
}

func (a *QuoteAPI) submit(ctx context.Context, client *http.Client, in apiInquiry) error {
	body, _ := json.Marshal(in)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.BaseURL, "/")+"/vehicle/innerProcess/vehicleApplyInquiryPriceAsync", bytes.NewReader(body))
	if err != nil {
		return err
	}
	hr.Header.Set("Content-Type", "application/json")
	if a.RealChannelID != "" {
		hr.Header.Set("realChannelId", a.RealChannelID)
	}
	resp, err := client.Do(hr)
	if err != nil {
		return fmt.Errorf("the quotation service could not be reached: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || resp.StatusCode/100 != 2 {
		return fmt.Errorf("the quotation service refused the inquiry: HTTP %d %s", resp.StatusCode, snippetOf(raw))
	}
	if env.Code != 0 {
		return fmt.Errorf("the quotation service refused the inquiry: %s", env.Msg)
	}
	return nil
}

func (a *QuoteAPI) query(ctx context.Context, client *http.Client, in apiInquiry) (*apiQuoteDetail, error) {
	qs := url.Values{"phone": {in.Phone}, "vehicleNo": {in.VehicleNo}}
	if in.NotSilentLogin {
		qs.Set("notSilentLogin", "true")
	}
	if in.BizOrigin != "" {
		qs.Set("bizOrigin", in.BizOrigin)
	} else if in.BizSource != "" {
		qs.Set("bizOrigin", in.BizSource)
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.BaseURL, "/")+"/vehicle/simpleProcess/queryLastQuoteDetailByPhone?"+qs.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if a.RealChannelID != "" {
		hr.Header.Set("realChannelId", a.RealChannelID)
	}
	resp, err := client.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("the query answered something else: %s", snippetOf(raw))
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("the query failed: %s", env.Msg)
	}
	d := &apiQuoteDetail{raw: env.Data}
	if len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, d); err != nil {
			return nil, fmt.Errorf("the quote detail is not readable: %w", err)
		}
	}
	return d, nil
}

func (a *QuoteAPI) normalize(d *apiQuoteDetail, info apiQuoteInfo, skipped []string) *APIQuote {
	f := func(s string) float64 { v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return v }
	q := &APIQuote{Currency: "CNY", Total: f(info.SumPremium), BusinessPremium: f(info.BusinessSumPremium), BusinessStandard: f(info.BizStandardTotal),
		BusinessDiscount: f(info.BizDiscountPremium), DiscountRatio: info.SelfPricingRatio, CompelPremium: f(info.CompelSumPremium),
		TaxPremium: f(info.TaxPremium), TaxLateCharge: f(info.TaxLateCharge), PremiumDiff: d.PremiumDiff, PlanType: info.PlanType,
		InsureFlowCode: info.InsureFlowCode, QuotePriceID: info.QuotePriceID, ValidForS: info.OrderRemainTime, Skipped: skipped, Source: "quote-api", Lines: []QuoteLine{}}
	if info.BusinessEffective != "" {
		q.BusinessPeriod = info.BusinessEffective + " 至 " + info.BusinessExpire
	}
	if info.CompelEffective != "" {
		q.CompelPeriod = info.CompelEffective + " 至 " + info.CompelExpire
	}
	for _, c := range info.CoverageList {
		choice := c.Tag
		if choice == "" {
			choice = c.Amount
		}
		q.Lines = append(q.Lines, QuoteLine{Coverage: c.CoverageSimpleName, Choice: choice, Base: f(c.StandardPremium), Premium: f(c.CoveragePremium)})
	}
	if info.CompelSumPremium != "" {
		q.Lines = append(q.Lines, QuoteLine{Coverage: "交强险", Premium: q.CompelPremium})
	}
	if info.TaxPremium != "" {
		q.Lines = append(q.Lines, QuoteLine{Coverage: "车船税", Premium: q.TaxPremium})
	}
	for _, s := range info.AddServiceList {
		q.Services = append(q.Services, fmt.Sprintf("%s %s%s", s.Title, s.Amount, s.Time))
	}
	return q
}

func snippetOf(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
