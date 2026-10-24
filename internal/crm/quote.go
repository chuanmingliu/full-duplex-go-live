package crm

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Configs are versioned JSON documents kept the way templates are: every
// save is a new version in a file of its own, configs/<name>/v0003.json,
// recorded in the log as config.put with its checksum, so that any quote
// can say exactly which rules it was computed under.
type Configs struct {
	dir string
	log *Log

	mu       sync.Mutex
	versions map[string][]*ConfigVersion
}

// ConfigVersion is one saved version of a config, without its body.
type ConfigVersion struct {
	Name      string          `json:"name"`
	Version   int             `json:"version"`
	SHA       string          `json:"sha"`
	Chars     int             `json:"chars"`
	Note      string          `json:"note,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	By        string          `json:"by"`
	Body      json.RawMessage `json:"body,omitempty"`
}

func (c *Configs) path(name string, version int) string {
	return filepath.Join(c.dir, "configs", name, fmt.Sprintf("v%04d.json", version))
}

func (c *Configs) apply(ev Event) {
	if ev.Type != "config.put" || ev.Config == nil {
		return
	}
	v := *ev.Config
	v.Body = nil
	c.versions[v.Name] = append(c.versions[v.Name], &v)
}

// Save stores body as the next version of the config called name. The body
// must be a JSON object; it is kept exactly as given.
func (c *Configs) Save(name string, body []byte, note, by string) (*ConfigVersion, error) {
	if !templateName.MatchString(name) {
		return nil, fmt.Errorf("crm: a config's name is lower-case letters, digits, - and _, up to 64 of them; %q is not", name)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return nil, errors.New("crm: the config is not a JSON object")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	versions := c.versions[name]
	sha := shortSHA(string(body))
	if n := len(versions); n > 0 && versions[n-1].SHA == sha {
		v := *versions[n-1]
		return &v, nil
	}
	v := &ConfigVersion{Name: name, Version: len(versions) + 1, SHA: sha, Chars: len([]rune(string(body))), Note: note, By: by}
	path := c.path(name, v.Version)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("crm: %w", err)
	}
	if err := writeFileAtomic(path, body); err != nil {
		return nil, err
	}
	v.CreatedAt = time.Now().Round(time.Millisecond)
	if _, err := c.log.Append(Event{At: v.CreatedAt, By: by, Type: "config.put", Ref: fmt.Sprintf("%s v%d", name, v.Version), Config: v, Note: note}); err != nil {
		os.Remove(path)
		return nil, err
	}
	c.versions[name] = append(versions, v)
	out := *v
	return &out, nil
}

// Get returns one version with its body; version 0 is the latest.
func (c *Configs) Get(name string, version int) (*ConfigVersion, error) {
	c.mu.Lock()
	versions := c.versions[name]
	if version == 0 {
		version = len(versions)
	}
	if version < 1 || version > len(versions) {
		c.mu.Unlock()
		return nil, ErrNotFound
	}
	v := *versions[version-1]
	c.mu.Unlock()
	body, err := os.ReadFile(c.path(name, version))
	if err != nil {
		return nil, fmt.Errorf("crm: config %s v%d is in the log and its file is gone: %w", name, version, err)
	}
	if sha := shortSHA(string(body)); sha != v.SHA {
		return nil, fmt.Errorf("crm: config %s v%d has been changed on disk since it was saved", name, version)
	}
	v.Body = body
	return &v, nil
}

// List is every config's latest version.
func (c *Configs) List() []*ConfigVersion {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*ConfigVersion, 0, len(c.versions))
	for _, vs := range c.versions {
		v := *vs[len(vs)-1]
		out = append(out, &v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// --- the quotation rules ---

// QuoteRulesName is the config the quotation reads.
const QuoteRulesName = "quote-rules"

// QuoteRules are how a premium is put together. They are a placeholder
// shape, to be replaced by the business's own tables: every number in the
// default set is made up.
//
//	{
//	  "currency": "CNY",
//	  "coverages": {
//	    "交强险":   {"flat": 950},
//	    "车损险":   {"rate": 0.012, "of": "vehiclePrice", "min": 600},
//	    "三者险":   {"table": {"100万": 1150, "200万": 1450, "300万": 1700, "500万": 2100}},
//	    "座位险":   {"per_seat": 45, "seats_of": "seats"},
//	    "划痕险":   {"table": {"2000": 320, "5000": 560}},
//	    "医保外用药": {"flat": 80}
//	  },
//	  "factors": {
//	    "vehicleAge": [{"max": 1, "f": 1.0}, {"max": 3, "f": 0.95}, {"max": 6, "f": 0.9}, {"max": 99, "f": 0.85}],
//	    "ncd": {"0": 1.0, "1": 0.85, "2": 0.7, "3": 0.6}
//	  },
//	  "factor_applies_to": ["车损险", "三者险", "座位险", "划痕险", "医保外用药", "驾乘险"],
//	  "simulate_delay_ms": 0
//	}
//
// A coverage is selected by a value in the request's config: true, or the
// table key ("200万"), or for per-seat covers the amount per seat as text
// ("1万" is ignored: the rate is per seat). Anything false or absent is not
// bought.
type QuoteRules struct {
	Currency        string                     `json:"currency"`
	Coverages       map[string]json.RawMessage `json:"coverages"`
	Factors         map[string]json.RawMessage `json:"factors"`
	FactorAppliesTo []string                   `json:"factor_applies_to"`
	SimulateDelayMS int                        `json:"simulate_delay_ms"`
}

type coverageRule struct {
	Flat    float64            `json:"flat"`
	Rate    float64            `json:"rate"`
	Of      string             `json:"of"`
	Min     float64            `json:"min"`
	Table   map[string]float64 `json:"table"`
	PerSeat float64            `json:"per_seat"`
	SeatsOf string             `json:"seats_of"`
}

// DefaultQuoteRules is what a new store starts with: placeholders.
const DefaultQuoteRules = `{
  "currency": "CNY",
  "coverages": {
    "交强险": {"flat": 950},
    "车损险": {"rate": 0.012, "of": "vehiclePrice", "min": 600},
    "三者险": {"table": {"100万": 1150, "200万": 1450, "300万": 1700, "500万": 2100}},
    "座位险": {"per_seat": 45, "seats_of": "seats"},
    "划痕险": {"table": {"2000": 320, "5000": 560}},
    "驾乘险": {"table": {"30万": 180, "50万": 260, "80万": 360}},
    "医保外用药": {"flat": 80}
  },
  "factors": {
    "vehicleAge": [{"max": 1, "f": 1.0}, {"max": 3, "f": 0.95}, {"max": 6, "f": 0.9}, {"max": 99, "f": 0.85}],
    "ncd": {"0": 1.0, "1": 0.85, "2": 0.7, "3": 0.6}
  },
  "factor_applies_to": ["车损险", "三者险", "座位险", "划痕险", "医保外用药", "驾乘险"],
  "simulate_delay_ms": 0
}
`

// QuoteRequest is what the assistant asks for: the coverages chosen and
// what is known about the vehicle and the policyholder.
type QuoteRequest struct {
	// Config is the coverage selection: coverage → true, a table key, or false.
	Config map[string]any `json:"config"`
	// Vars are the lead's variables the rules may read: vehiclePrice, seats,
	// vehicleAge, ncd.
	Vars map[string]any `json:"vars"`
	// MemoryVersion is the caller's version of the configuration, echoed back
	// so the answer can be matched to what it was computed on.
	MemoryVersion int `json:"memory_version,omitempty"`
	// RulesVersion pins the rules; 0 is the latest.
	RulesVersion int `json:"rules_version,omitempty"`
	// Call names the call this is for, when there is one.
	Call string `json:"call,omitempty"`
	// Skipped are coverages the intake left out (the customer could not
	// answer for them, or they are outside the plan); echoed in the answer.
	Skipped []string `json:"-"`
}

// toolCall is the shape golive's tool executor posts (internal/tools
// Request): the call's working memory and the model's arguments.
type toolCall struct {
	Tool          string          `json:"tool"`
	CallID        string          `json:"call_id"`
	Session       string          `json:"session"`
	Call          string          `json:"call"` // the phone call, when golive knows it
	Arguments     json.RawMessage `json:"arguments"`
	Memory        map[string]any  `json:"memory"`
	MemoryVersion int             `json:"memory_version"`
}

// quoteRequestFrom reads either a QuoteRequest or golive's tool call. From
// a tool call the coverage selection is memory.quote (the plan the
// conversation has settled on) with anything in arguments.config laid over
// it, and the variables are the rest of the memory.
func quoteRequestFrom(raw []byte) (QuoteRequest, error) {
	var probe struct {
		Tool string `json:"tool"`
	}
	if json.Unmarshal(raw, &probe) == nil && probe.Tool != "" {
		var tc toolCall
		if err := json.Unmarshal(raw, &tc); err != nil {
			return QuoteRequest{}, errors.New("the tool call is not readable")
		}
		req := QuoteRequest{Config: map[string]any{}, Vars: map[string]any{}, MemoryVersion: tc.MemoryVersion, Call: tc.Session}
		if tc.Call != "" {
			req.Call = tc.Call
		}
		for k, v := range tc.Memory {
			if k == "quote" {
				if m, ok := v.(map[string]any); ok {
					for ck, cv := range m {
						req.Config[ck] = cv
					}
				}
				continue
			}
			req.Vars[k] = v
		}
		var args struct {
			Config map[string]any `json:"config"`
			Vars   map[string]any `json:"vars"`
		}
		if len(tc.Arguments) > 0 && json.Unmarshal(tc.Arguments, &args) == nil {
			for k, v := range args.Config {
				req.Config[k] = v
			}
			for k, v := range args.Vars {
				req.Vars[k] = v
			}
		}
		// No selection yet is the first thing to ask for, not an error: the
		// intake answers need with the plan's coverages (10-21: the model
		// asked for a total before recording the plan and got a 400).
		return req, nil
	}
	var req QuoteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return req, errors.New("the request is not the JSON this expects")
	}
	return req, nil
}

// QuoteLine is one coverage priced.
type QuoteLine struct {
	Coverage string  `json:"coverage"`
	Choice   string  `json:"choice,omitempty"`
	Base     float64 `json:"base"`
	Factor   float64 `json:"factor"`
	Premium  float64 `json:"premium"`
}

// Quote is the answer.
type Quote struct {
	// Status is "quote": the answer to a request that could run (a request
	// that could not answers a Need with status "need").
	Status        string      `json:"status,omitempty"`
	Total         float64     `json:"total"`
	Currency      string      `json:"currency"`
	Lines         []QuoteLine `json:"lines"`
	Skipped       []string    `json:"skipped,omitempty"` // selected but unknown to the rules
	RulesVersion  int         `json:"rules_version"`
	RulesSHA      string      `json:"rules_sha"`
	MemoryVersion int         `json:"memory_version,omitempty"`
	ComputedAt    time.Time   `json:"computed_at"`
	ComputeMS     int64       `json:"compute_ms"`
}

var numberIn = regexp.MustCompile(`[0-9]+(\.[0-9]+)?`)

// toFloat reads a number from what a variable holds: 128000, "128000",
// "12.8万", "5座".
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		m := numberIn.FindString(x)
		if m == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(m, 64)
		if err != nil {
			return 0, false
		}
		if strings.Contains(x, "万") {
			f *= 10000
		}
		return f, true
	}
	return 0, false
}

// Compute prices a request under the rules.
func (r *QuoteRules) Compute(req QuoteRequest) (*Quote, error) {
	q := &Quote{Status: "quote", Currency: r.Currency, Lines: []QuoteLine{}}
	if q.Currency == "" {
		q.Currency = "CNY"
	}
	factor := r.factor(req.Vars)
	applies := map[string]bool{}
	for _, n := range r.FactorAppliesTo {
		applies[n] = true
	}
	names := make([]string, 0, len(req.Config))
	for n := range req.Config {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		choice := req.Config[name]
		if b, ok := choice.(bool); ok && !b {
			continue
		}
		if s, ok := choice.(string); ok && (s == "" || s == "false" || s == "否" || s == "不要") {
			continue
		}
		raw, ok := r.Coverages[name]
		if !ok {
			q.Skipped = append(q.Skipped, name)
			continue
		}
		var rule coverageRule
		if err := json.Unmarshal(raw, &rule); err != nil {
			return nil, fmt.Errorf("crm: the rule for %s is not readable: %w", name, err)
		}
		line := QuoteLine{Coverage: name, Factor: 1}
		switch {
		case rule.Flat > 0:
			line.Base = rule.Flat
		case rule.Rate > 0:
			of, ok := toFloat(req.Vars[rule.Of])
			if !ok {
				return nil, fmt.Errorf("crm: %s needs the variable %s", name, rule.Of)
			}
			line.Base = math.Max(of*rule.Rate, rule.Min)
		case len(rule.Table) > 0:
			key, _ := choice.(string)
			if b, isBool := choice.(bool); isBool && b {
				// true with a table: the first (cheapest) option.
				keys := make([]string, 0, len(rule.Table))
				for k := range rule.Table {
					keys = append(keys, k)
				}
				sort.Slice(keys, func(i, j int) bool { return rule.Table[keys[i]] < rule.Table[keys[j]] })
				key = keys[0]
			}
			v, ok := rule.Table[key]
			if !ok {
				return nil, fmt.Errorf("crm: %s has no option %q; the options are %s", name, key, strings.Join(sortedKeys(rule.Table), ", "))
			}
			line.Choice, line.Base = key, v
		case rule.PerSeat > 0:
			seats, ok := toFloat(req.Vars[rule.SeatsOf])
			if !ok {
				seats = 5
			}
			line.Choice = fmt.Sprintf("%d座", int(seats))
			line.Base = rule.PerSeat * seats
		default:
			return nil, fmt.Errorf("crm: the rule for %s prices nothing", name)
		}
		if applies[name] {
			line.Factor = factor
		}
		line.Premium = math.Round(line.Base*line.Factor*100) / 100
		q.Total += line.Premium
		q.Lines = append(q.Lines, line)
	}
	q.Total = math.Round(q.Total*100) / 100
	return q, nil
}

// factor is the product of every factor the variables select.
func (r *QuoteRules) factor(vars map[string]any) float64 {
	f := 1.0
	for name, raw := range r.Factors {
		v, ok := vars[name]
		if !ok {
			continue
		}
		var bands []struct {
			Max float64 `json:"max"`
			F   float64 `json:"f"`
		}
		if json.Unmarshal(raw, &bands) == nil && len(bands) > 0 {
			x, ok := toFloat(v)
			if !ok {
				continue
			}
			for _, b := range bands {
				if x <= b.Max {
					f *= b.F
					break
				}
			}
			continue
		}
		var table map[string]float64
		if json.Unmarshal(raw, &table) == nil {
			key := fmt.Sprint(v)
			if x, ok := toFloat(v); ok {
				key = strconv.Itoa(int(x))
			}
			if m, ok := table[key]; ok {
				f *= m
			}
		}
	}
	return f
}

func sortedKeys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// QuoteRecord is one quote computed, for the log: the choices, never the
// person.
type QuoteRecord struct {
	At            time.Time `json:"at"`
	Call          string    `json:"call,omitempty"`
	Coverages     []string  `json:"coverages"`
	Total         float64   `json:"total"`
	RulesVersion  int       `json:"rules_version,omitempty"`
	MemoryVersion int       `json:"memory_version,omitempty"`
	ComputeMS     int64     `json:"compute_ms"`
	Error         string    `json:"error,omitempty"`
	// Source is "rules" or "quote-api"; for the service, its config version
	// and the flow code and quote id it answered with.
	Source         string `json:"source,omitempty"`
	APIVersion     int    `json:"api_version,omitempty"`
	InsureFlowCode string `json:"insure_flow_code,omitempty"`
	QuotePriceID   int64  `json:"quote_price_id,omitempty"`
}
