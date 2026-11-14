package quotesvc

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/chuanmingliu/golive/internal/motorcatalog"
)

// DefaultRating extends the existing synthetic table. Existing rating.json
// files are never rewritten automatically. These figures are test data.
var DefaultRating = fullMotorRating()

func fullMotorRating() string {
	var r Rating
	if err := json.Unmarshal([]byte(legacyDefaultRating), &r); err != nil {
		panic(err)
	}
	codes := map[string]string{}
	for _, e := range motorcatalog.Entries() {
		codes[e.Name] = e.Code
	}
	for _, e := range motorcatalog.Entries() {
		if e.Code == "" {
			continue
		}
		c, exists := r.Coverages[e.Code]
		if !exists {
			c = CoverageRating{Name: e.Label, PerWan: 8}
		}
		c.Name = e.Label
		switch {
		case e.Kind == "percent":
			c = CoverageRating{Name: e.Label, Kind: "deductible", ParentCode: codes[e.Parent], ParentRate: 0.5}
		case e.Name == "假日三者翻倍险":
			c = CoverageRating{Name: e.Label, Kind: "holiday", ParentCode: codes[e.Parent], ParentRate: 0.1}
		case e.Kind == "shared_amount":
			c = CoverageRating{Name: e.Label, Kind: "shared_amount", ParentCode: codes[e.Parent], PerWan: 2}
		case strings.HasPrefix(e.Code, "XC-MENTAL-"):
			c.ParentCode = codes[e.Parent]
		}
		if e.Code == "952" {
			for k, v := range map[string]float64{"500000": 850, "1500000": 1300, "4000000": 1900} {
				c.Table[k] = v
			}
		}
		if e.Code == "955" {
			c.Table["10000"] = 850
			c.Table["20000"] = 1300
		}
		// Existing 955 requests did not carry a parent; the strict CRM plan enforces it.
		r.Coverages[e.Code] = c
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func (r Rating) validate(in Inquiry) error {
	byCode := map[string]Coverage{}
	for _, c := range in.CoverageList {
		if _, dup := byCode[c.CoverageCode]; dup {
			return fmt.Errorf("duplicate coverage code %s", c.CoverageCode)
		}
		if _, ok := r.Coverages[c.CoverageCode]; !ok {
			return fmt.Errorf("unmapped coverage code %s", c.CoverageCode)
		}
		byCode[c.CoverageCode] = c
	}
	for _, c := range in.CoverageList {
		rule := r.Coverages[c.CoverageCode]
		parent := rule.ParentCode
		if parent != "" && c.ParentCoverageCode != parent {
			return fmt.Errorf("incorrect parent for %s", c.CoverageCode)
		}
		if parent == "" {
			parent = c.ParentCoverageCode
		}
		if parent != "" {
			if _, ok := byCode[parent]; !ok {
				return fmt.Errorf("missing parent for %s", c.CoverageCode)
			}
		}
		if rule.Kind == "deductible" {
			switch c.DeductibleRate {
			case "5", "10", "15", "20":
			default:
				return fmt.Errorf("invalid deductible rate for %s", c.CoverageCode)
			}
		} else if rule.Kind != "holiday" {
			if c.SumInsured == "shared" && rule.Kind == "shared_amount" {
				continue
			}
			n, err := strconv.ParseFloat(c.SumInsured, 64)
			if err != nil || n <= 0 || math.IsInf(n, 0) || math.IsNaN(n) {
				return fmt.Errorf("invalid sum for %s", c.CoverageCode)
			}
		}
	}
	return nil
}

func (r Rating) priceLines(in Inquiry, v Vehicle, factor float64) ([]LinePriced, float64, float64, error) {
	if err := r.validate(in); err != nil {
		return nil, 0, 0, err
	}
	byCode := map[string]Coverage{}
	for _, c := range in.CoverageList {
		byCode[c.CoverageCode] = c
	}
	calculated := map[string]LinePriced{}
	visiting := map[string]bool{}
	var price func(string) (LinePriced, error)
	price = func(code string) (LinePriced, error) {
		if line, ok := calculated[code]; ok {
			return line, nil
		}
		if visiting[code] {
			return LinePriced{}, fmt.Errorf("cyclic rating dependency")
		}
		visiting[code] = true
		c := byCode[code]
		rule := r.Coverages[code]
		sum, _ := strconv.ParseFloat(c.SumInsured, 64)
		base := 0.0
		tag := ""
		var parent LinePriced
		if c.ParentCoverageCode != "" {
			var err error
			parent, err = price(c.ParentCoverageCode)
			if err != nil {
				return LinePriced{}, err
			}
		}
		if c.SumInsured == "shared" {
			sum, _ = strconv.ParseFloat(parent.Amount, 64)
			tag = "共享保额"
		} else if sum == 1 && rule.Kind != "holiday" {
			sum = v.Price
		}
		switch {
		case rule.Kind == "deductible":
			rate, _ := strconv.ParseFloat(c.DeductibleRate, 64)
			parentBase, _ := strconv.ParseFloat(parent.StandardPremium, 64)
			base = -parentBase * rate / 100 * rule.ParentRate
			sum = 0
			tag = c.DeductibleRate + "%"
		case rule.Kind == "holiday":
			parentBase, _ := strconv.ParseFloat(parent.StandardPremium, 64)
			base = parentBase * rule.ParentRate
			sum, _ = strconv.ParseFloat(parent.Amount, 64)
			sum *= 2
			tag = "投保"
		case rule.Flat > 0:
			base = rule.Flat
		case rule.Rate > 0:
			base = math.Max(sum*rule.Rate, rule.Min)
		case len(rule.Table) > 0:
			keys := make([]float64, 0, len(rule.Table))
			for k := range rule.Table {
				n, _ := strconv.ParseFloat(k, 64)
				keys = append(keys, n)
			}
			sort.Float64s(keys)
			pick := keys[len(keys)-1]
			for _, k := range keys {
				if sum <= k {
					pick = k
					break
				}
			}
			base = rule.Table[strconv.FormatFloat(pick, 'f', -1, 64)]
			sum = pick
		case rule.PerWan > 0:
			seats := 1.0
			if rule.Passengers {
				seats = math.Max(float64(v.Seats-1), 1)
			} else if rule.PerSeat {
				seats = float64(v.Seats)
			}
			base = sum / 10000 * rule.PerWan * seats
		default:
			return LinePriced{}, fmt.Errorf("no rating for %s", code)
		}
		if tag == "" && sum >= 10000 {
			tag = strconv.FormatFloat(sum/10000, 'f', -1, 64) + "万"
		}
		line := LinePriced{CoverageCode: code, ParentCoverageCode: c.ParentCoverageCode, CoverageType: c.CoverageType, CoverageSimpleName: rule.Name, Amount: strconv.FormatFloat(sum, 'f', -1, 64), Tag: tag, CoveragePremium: money(base * factor), StandardPremium: money(base)}
		calculated[code] = line
		delete(visiting, code)
		return line, nil
	}
	lines := make([]LinePriced, 0, len(in.CoverageList))
	standard, business := 0.0, 0.0
	for _, c := range in.CoverageList {
		line, err := price(c.CoverageCode)
		if err != nil {
			return nil, 0, 0, err
		}
		lines = append(lines, line)
		b, _ := strconv.ParseFloat(line.StandardPremium, 64)
		p, _ := strconv.ParseFloat(line.CoveragePremium, 64)
		standard += b
		business += p
	}
	return lines, standard, business, nil
}
