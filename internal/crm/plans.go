package crm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// A quote plan is the owner's answer to "what does a quotation need?":
// which coverages are in play and, for each field the quotation reads,
// which coverages need it and what to ask the customer when it is
// unknown. Plans are a versioned config on the panel (`quote-plans`), so a
// coverage or a field is added there and not in a build. A call runs on
// the default plan unless its working memory names another (`quote_plan`),
// which is how a lead's own plan reaches the call once leads are built.
//
// The quotation does not run on a plan with a must field missing. It
// answers `need` with the missing fields, in order, each with its
// question; the assistant asks them one at a time, records the answers in
// the working memory, and asks for the quote again. Only when every must
// field is known is a normal quotation made (10-20, the owner's rule).

// QuotePlansName is the config's name.
const QuotePlansName = "quote-plans"

// QuotePlans is the config: named plans and which is the default.
type QuotePlans struct {
	Default string               `json:"default"`
	Plans   map[string]QuotePlan `json:"plans"`
}

// QuotePlan is one combination.
type QuotePlan struct {
	// Coverages the plan offers; a selection naming one outside the plan is
	// skipped by the quotation and said so.
	Coverages []string `json:"coverages"`
	// Fields the quotation may need, in the order to ask for them.
	Fields []PlanField `json:"fields"`
	// Options are the tiers a coverage comes in ("三者险": ["100万", …]); a
	// coverage chosen without one of them is asked for before the
	// quotation. "0" or "不要" among the options means none, and the
	// coverage is left out.
	Options map[string][]string `json:"options,omitempty"`
	// CoverageAliases map other names for a coverage to the plan's
	// ("车上人员责任险" → "座位险"); the model used the formal name on 10-21 and
	// the coverage was skipped as unknown.
	CoverageAliases map[string]string `json:"coverage_aliases,omitempty"`
	// DefaultSelection is the fixed default quotation — what a lead is
	// quoted without being asked (10-24): coverage → true or a tier.
	// DefaultVars stand in for a lead's variables on a call that has no
	// lead (the box's redial of a kept prompt).
	DefaultSelection map[string]any `json:"default_selection,omitempty"`
	DefaultVars      map[string]any `json:"default_vars,omitempty"`
	// Prequote says which single changes to the default selection are priced
	// ahead of being asked for, and how many (prequote.go).
	Prequote PrequotePolicy `json:"prequote,omitempty"`
}

// PlanField is one variable the quotation reads.
type PlanField struct {
	Name string `json:"name"`
	// For names the coverages that need it; "*" means every quotation does.
	For []string `json:"for"`
	// Question is what to ask the customer, in their language; the
	// assistant may rephrase it.
	Question string `json:"question"`
	// IfUnknown says what to do when the customer cannot answer: "skip"
	// drops the coverages that need it and quotes the rest; "stop" (the
	// default) does not quote.
	IfUnknown string `json:"if_unknown,omitempty"`
	// Aliases are other names the assistant may record the field under
	// (car_price, 车价); the intake reads them as the field. The model named
	// its own keys on the first run (10-20 23:36: car_price, vehicle_age,
	// claim_count) and was asked for what it had just recorded.
	Aliases []string `json:"aliases,omitempty"`
	// Source limits the field to one way of quoting: "rules" (the
	// placeholder rules read it), "api" (the business service needs it),
	// "" or "any" for both. The business service looks a vehicle up by its
	// plate and does not need the price.
	Source string `json:"source,omitempty"`
}

// DefaultQuotePlans is seeded on a service that has none: the standard
// motor plan with the fields the placeholder rules read.
const DefaultQuotePlans = `{
  "default": "standard",
  "plans": {
    "standard": {
      "coverages": ["交强险", "车损险", "三者险", "座位险", "划痕险", "玻璃险", "驾乘险"],
      "options": {
        "三者险": ["100万", "200万", "300万", "500万"],
        "驾乘险": ["0", "30万", "50万", "80万"]
      },
      "default_selection": {"交强险": true, "车损险": true, "三者险": "200万", "座位险": true},
      "default_vars": {"vehiclePrice": 150000, "vehicleAge": 3, "ncd": 0, "seats": 5},
      "prequote": {"max": 8, "keep": ["交强险"]},
      "coverage_aliases": {
        "交强": "交强险", "机动车交通事故责任强制保险": "交强险",
        "车损": "车损险", "机动车损失险": "车损险", "机动车损失保险": "车损险",
        "三者": "三者险", "第三者责任险": "三者险", "第三者责任保险": "三者险", "商业三者险": "三者险",
        "座位": "座位险", "车上人员责任险": "座位险", "车上人员": "座位险",
        "划痕": "划痕险", "车身划痕险": "划痕险",
        "玻璃": "玻璃险", "玻璃单独破碎险": "玻璃险",
        "驾乘": "驾乘险", "驾乘意外险": "驾乘险", "驾乘人员意外险": "驾乘险"
      },
      "fields": [
        {"name": "carNum", "for": ["*"], "question": "您的车牌号是多少？", "source": "api", "aliases": ["car_num", "plate", "车牌", "车牌号"]},
        {"name": "vehiclePrice", "for": ["车损险"], "question": "您这辆车当时的购置价大概是多少？", "if_unknown": "skip", "source": "rules", "aliases": ["car_price", "vehicle_price", "price", "车价", "购置价"]},
        {"name": "seats", "for": ["座位险"], "question": "您的车是几座的？", "if_unknown": "skip", "source": "rules", "aliases": ["seat_count", "座位", "座位数"]},
        {"name": "vehicleAge", "for": ["*"], "question": "车是哪一年买的，用了几年了？", "aliases": ["vehicle_age", "car_age", "age", "车龄"]},
        {"name": "ncd", "for": ["*"], "question": "去年有没有出过险？出了几次？", "source": "rules", "aliases": ["claim_count", "claims", "出险次数", "出险"]}
      ]
    },
    "basic": {
      "coverages": ["交强险", "三者险"],
      "fields": [
        {"name": "vehicleAge", "for": ["*"], "question": "车用了几年了？", "aliases": ["vehicle_age", "car_age", "车龄"]},
        {"name": "ncd", "for": ["*"], "question": "去年有没有出过险？", "source": "rules", "aliases": ["claim_count", "出险次数"]}
      ]
    }
  }
}`

// ParseQuotePlans reads the config and checks it.
func ParseQuotePlans(raw []byte) (QuotePlans, error) {
	var p QuotePlans
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	if len(p.Plans) == 0 {
		return p, errors.New("no plans")
	}
	if p.Default == "" {
		return p, errors.New("no default plan")
	}
	if _, ok := p.Plans[p.Default]; !ok {
		return p, fmt.Errorf("the default plan %q does not exist", p.Default)
	}
	for name, plan := range p.Plans {
		if len(plan.Coverages) == 0 {
			return p, fmt.Errorf("plan %q has no coverages", name)
		}
		for _, f := range plan.Fields {
			if strings.TrimSpace(f.Name) == "" {
				return p, fmt.Errorf("plan %q: a field has no name", name)
			}
			if len(f.For) == 0 {
				return p, fmt.Errorf("plan %q: field %s says nothing about which coverages need it (use [\"*\"] for all)", name, f.Name)
			}
			switch f.IfUnknown {
			case "", "skip", "stop":
			default:
				return p, fmt.Errorf("plan %q: field %s: if_unknown must be skip or stop", name, f.Name)
			}
			switch f.Source {
			case "", "any", "rules", "api":
			default:
				return p, fmt.Errorf("plan %q: field %s: source must be rules, api or any", name, f.Name)
			}
		}
	}
	return p, nil
}

// Missing is one must field the customer has not given, or a coverage
// chosen without its tier (Kind "choice": record the answer under
// quote.<Field>).
type Missing struct {
	Field     string   `json:"field"`
	Kind      string   `json:"kind,omitempty"` // "", or "choice"
	For       []string `json:"for"`
	Options   []string `json:"options,omitempty"`
	Question  string   `json:"question"`
	IfUnknown string   `json:"if_unknown,omitempty"`
}

// Need is the quotation's answer when it cannot run yet.
type Need struct {
	Status  string    `json:"status"` // "need"
	Plan    string    `json:"plan"`
	Missing []Missing `json:"missing"`
	Known   []string  `json:"known"`
	// Instruction tells the assistant what to do, in its language.
	Instruction string `json:"instruction"`
}

// chosen says whether a coverage is in the selection (see selected).
func chosen(config map[string]any, coverage string) bool {
	v, ok := config[coverage]
	return ok && selected(v)
}

// known says whether a variable has a usable value.
func known(vars map[string]any, name string) bool {
	v, ok := vars[name]
	if !ok || v == nil {
		return false
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) != ""
	}
	return true
}

// Check returns what the plan still needs for this selection, or nil when
// the quotation may run. A field marked skip whose coverages the caller
// has said they cannot answer (vars[name] == "unknown") is not asked again:
// its coverages are left out, and Skipped names them.
// Normalize copies a field's value from any of its aliases, so the
// quotation reads it under the plan's name.
func (p QuotePlan) Normalize(vars map[string]any) {
	for _, f := range p.Fields {
		if known(vars, f.Name) {
			continue
		}
		for _, a := range f.Aliases {
			if known(vars, a) {
				vars[f.Name] = vars[a]
				break
			}
		}
	}
}

// NormalizeCoverages renames coverages the model named otherwise.
func (p QuotePlan) NormalizeCoverages(config map[string]any) {
	for k, v := range config {
		if canon, ok := p.CoverageAliases[k]; ok && canon != k {
			if _, exists := config[canon]; !exists {
				config[canon] = v
			}
			delete(config, k)
		}
	}
}

func (p QuotePlan) Check(name, source string, config, vars map[string]any) (need *Need, skipped []string) {
	p.Normalize(vars)
	var missing []Missing
	var have []string
	if len(config) == 0 {
		missing = append(missing, Missing{Field: "quote", Kind: "plan", For: p.Coverages, Options: p.Coverages,
			Question: fmt.Sprintf("您想投哪些险种？可选：%s。", strings.Join(p.Coverages, "、"))})
	}
	// A coverage with tiers, chosen without one: ask which.
	for _, c := range p.Coverages {
		opts := p.Options[c]
		if len(opts) == 0 || !chosen(config, c) {
			continue
		}
		choice := strings.TrimSpace(fmt.Sprint(config[c]))
		ok := false
		for _, o := range opts {
			if o == choice {
				ok = true
				break
			}
		}
		if ok {
			continue
		}
		missing = append(missing, Missing{Field: c, Kind: "choice", For: []string{c}, Options: opts,
			Question: fmt.Sprintf("%s有这几个档位：%s，您想选哪一个？", c, strings.Join(opts, "、"))})
	}
	for _, f := range p.Fields {
		if f.Source != "" && f.Source != "any" && f.Source != source {
			continue
		}
		needed := false
		var for_ []string
		for _, c := range f.For {
			if c == "*" {
				needed = true
				for_ = append(for_, "*")
				continue
			}
			if chosen(config, c) {
				needed = true
				for_ = append(for_, c)
			}
		}
		if !needed {
			continue
		}
		if v, ok := vars[f.Name]; ok {
			if s, ok := v.(string); ok && strings.EqualFold(strings.TrimSpace(s), "unknown") {
				if f.IfUnknown == "skip" {
					for _, c := range for_ {
						if c != "*" {
							skipped = append(skipped, c)
						}
					}
					continue
				}
			}
		}
		if known(vars, f.Name) && !strings.EqualFold(fmt.Sprint(vars[f.Name]), "unknown") {
			have = append(have, f.Name)
			continue
		}
		missing = append(missing, Missing{Field: f.Name, For: for_, Question: f.Question, IfUnknown: f.IfUnknown})
	}
	if len(missing) == 0 {
		return nil, skipped
	}
	return &Need{Status: "need", Plan: name, Missing: missing, Known: have,
		Instruction: "报价前还缺少必要信息。请按顺序一次只问一个问题，用户回答后用 update_memory 记录该字段，再重新调用 quote_total；若用户不知道且该字段允许 skip，记录为 \"unknown\" 后重新调用。kind 为 choice 的是险种档位，答案记录在 quote 下该险种名下（如 quote.驾乘险 = \"50万\"，选 0 表示不要）；kind 为 plan 表示还没有险种方案，请问清客户要投的险种并记录在 quote 下。"}, skipped
}

// plan picks the plan a request runs on: vars.quote_plan, else the default.
func (p QuotePlans) plan(vars map[string]any) (string, QuotePlan) {
	if v, ok := vars["quote_plan"]; ok {
		if s, ok := v.(string); ok {
			if plan, ok := p.Plans[s]; ok {
				return s, plan
			}
		}
	}
	return p.Default, p.Plans[p.Default]
}
