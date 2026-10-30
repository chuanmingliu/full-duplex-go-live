package crm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Quotations the caller has not asked for yet.
//
// The owner's idea (11:45 on 10-26): the default plan is priced while the
// phone rings (prefetch.go); the combinations a caller is likely to ask for
// next — 三者险 at another tier, 驾乘险 added, a coverage dropped — can be
// priced in the same way, before anyone asks, and kept. A second quotation
// whose selection matches one of them is answered from the cache at once
// instead of through the business service's 3.5–4 s (the 10:46 call of
// 10-26: both changes waited that long, under an ack). A quotation that is
// computed on demand is kept too, so the same combination asked again is
// instant.
//
// These are real quotations, computed early — not estimates: each went
// through the same path as one asked for, and is recorded (quote.computed,
// prequote: true) like one. What makes a cache hit is the selection and the
// variables it ran on, normalized the way the intake normalizes them; a
// change to a variable (the caller corrects the age) misses and is priced.
//
// How many to price, and which, is the plan's `prequote` (plans.go): the
// single changes to the default selection — every other tier of a coverage
// that has tiers, each coverage of the plan added, each one dropped — tiers
// first, then additions, then removals, capped by max (8 unless the plan
// says otherwise; 0 turns it off). One at a time through the business
// service, and a caller's own ask goes first (takeAPITurn in service.go).

// PrequotePolicy is the plan's say on quotations priced ahead.
type PrequotePolicy struct {
	// Max is how many single changes to price per call; 0 is none. A plan
	// that says nothing gets DefaultPrequoteMax.
	Max *int `json:"max,omitempty"`
	// FollowSelectionMax bounds additional new background prices per call
	// after a caller selects a nondefault plan. Zero keeps this disabled.
	FollowSelectionMax int `json:"follow_selection_max,omitempty"`
	// Keep names coverages never dropped in a variant (交强险 is compulsory).
	Keep []string `json:"keep,omitempty"`
	// Concurrency is how many are started at once; 1 unless said. Through
	// the business service they run one at a time whatever this says
	// (service.go, takeAPITurn: its answer is "the latest quote for this
	// phone"); the rules can take more.
	Concurrency int `json:"concurrency,omitempty"`
}

// DefaultPrequoteMax is how many variants a plan without a policy prices.
const DefaultPrequoteMax = 8

func (p PrequotePolicy) max() int {
	if p.Max == nil {
		return DefaultPrequoteMax
	}
	if *p.Max < 0 {
		return 0
	}
	return *p.Max
}

func (p PrequotePolicy) concurrency() int {
	if p.Concurrency <= 0 {
		return 1
	}
	return p.Concurrency
}

// prequoted is one quotation priced ahead (or on demand and kept).
type prequoted struct {
	key     string
	started time.Time
	done    chan struct{}
	quote   any
	err     error
	took    time.Duration
	ahead   bool // priced before anyone asked
}

// selectionKey names a normalized request: the call, the pricing (which
// rules or service version), the selected coverages with their choices, and
// the variables the plan reads. Call it after the intake has normalized the
// request.
func selectionKey(call, pricing string, plan QuotePlan, config, vars map[string]any) string {
	var b strings.Builder
	b.WriteString(call)
	b.WriteString("|")
	b.WriteString(pricing)
	b.WriteString("|")
	covs := make([]string, 0, len(config))
	for c, v := range config {
		if selected(v) {
			covs = append(covs, c)
		}
	}
	sort.Strings(covs)
	for _, c := range covs {
		v := config[c]
		if on, ok := v.(bool); ok && on {
			fmt.Fprintf(&b, "%s;", c)
			continue
		}
		fmt.Fprintf(&b, "%s=%v;", c, v)
	}
	b.WriteString("|")
	names := make([]string, 0, len(plan.Fields))
	for _, f := range plan.Fields {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	for _, n := range names {
		if v, ok := vars[n]; ok && v != nil {
			fmt.Fprintf(&b, "%s=%v;", n, v)
		}
	}
	return b.String()
}

// pricingVersion names what a request would be priced by: the service's
// config version, or the rules version asked for. A new version of either
// is a different price, so it is part of the key.
func (s *Service) pricingVersion(req QuoteRequest) string {
	if _, v := s.quoteAPI(); v != nil {
		return fmt.Sprintf("api:%d", v.Version)
	}
	if v, err := s.configs.Get(QuoteRulesName, req.RulesVersion); err == nil {
		return fmt.Sprintf("rules:%d", v.Version)
	}
	return fmt.Sprintf("rules:%d", req.RulesVersion)
}

// prequoteValidity is how long a kept quotation answers for; the service's
// own valid_for_s, when it gives one, is honoured instead.
const prequoteValidity = 30 * time.Minute

// fresh says whether a kept quotation may still be answered.
func (p *prequoted) fresh() bool {
	valid := prequoteValidity
	if raw, err := json.Marshal(p.quote); err == nil {
		var v struct {
			ValidForS int64 `json:"valid_for_s"`
		}
		if json.Unmarshal(raw, &v) == nil && v.ValidForS > 0 {
			valid = time.Duration(v.ValidForS) * time.Second
		}
	}
	return time.Since(p.started) < valid
}

// prequoteFor finds a kept quotation, or claims the key for one about to be
// priced (claimed=true: the caller must fill it in and close done).
func (s *Service) prequoteFor(key string, claim bool) (p *prequoted, claimed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prequotes == nil {
		s.prequotes = map[string]*prequoted{}
	}
	if p, ok := s.prequotes[key]; ok {
		return p, false
	}
	if !claim {
		return nil, false
	}
	p = &prequoted{key: key, started: time.Now(), done: make(chan struct{})}
	s.prequotes[key] = p
	// A call is one conversation long; older entries go.
	if len(s.prequotes) > 512 {
		for k, old := range s.prequotes {
			if time.Since(old.started) > 2*time.Hour {
				delete(s.prequotes, k)
			}
		}
	}
	return p, true
}

// forget drops a claimed key whose pricing did not produce a quote worth
// keeping (a need, an error): the next ask prices it again.
func (s *Service) forgetPrequote(key string, expected *prequoted) {
	s.mu.Lock()
	// A completed, expired entry may already have been replaced by a new
	// background claim. Only its owner may remove the current entry.
	if s.prequotes[key] == expected {
		delete(s.prequotes, key)
	}
	s.mu.Unlock()
}

// prequoteNeighbours prices the single changes to a default request, in
// the background. Called once the default quotation is in.
func (s *Service) prequoteNeighbours(call string, def QuoteRequest) {
	v, err := s.configs.Get(QuotePlansName, 0)
	if err != nil {
		return
	}
	plans, err := ParseQuotePlans(v.Body)
	if err != nil {
		return
	}
	_, plan := plans.plan(def.Vars)
	variants := plan.prequoteVariants(def.Config)
	if len(variants) == 0 {
		return
	}
	s.log.Info("crm: pricing the neighbours of the default quotation ahead", "call", call, "variants", len(variants))
	sem := make(chan struct{}, plan.Prequote.concurrency())
	for _, cfg := range variants {
		sem <- struct{}{}
		go func(cfg map[string]any) {
			defer func() { <-sem }()
			req := QuoteRequest{Config: cfg, Vars: copyVars(def.Vars), Call: call, prequote: true}
			if _, err := s.Quote(req); err != nil {
				s.log.Warn("crm: a quotation priced ahead failed", "call", call, "err", err)
			}
		}(cfg)
	}
}

// prequoteVariants lists the single changes to a selection the plan would
// price ahead: tiers first, then additions, then removals; capped.
func (p QuotePlan) prequoteVariants(def map[string]any) []map[string]any {
	max := p.Prequote.max()
	if max == 0 || len(def) == 0 {
		return nil
	}
	keep := map[string]bool{}
	for _, c := range p.Prequote.Keep {
		keep[c] = true
	}
	var out []map[string]any
	with := func(c string, v any) {
		m := copyVars(def)
		if v == nil {
			delete(m, c)
		} else {
			m[c] = v
		}
		out = append(out, m)
	}
	// Other tiers of the coverages that have tiers, in the plan's order.
	for _, c := range p.Coverages {
		opts, has := p.Options[c]
		if !has {
			continue
		}
		cur, chosen := def[c]
		for _, o := range opts {
			if !selected(o) || (chosen && fmt.Sprint(cur) == o) {
				continue
			}
			with(c, o)
		}
	}
	// Each coverage of the plan not in the selection, added (at its first
	// real tier when it has tiers).
	for _, c := range p.Coverages {
		if _, chosen := def[c]; chosen && selected(def[c]) {
			continue
		}
		if _, has := p.Options[c]; has {
			continue // already added above at every tier
		}
		with(c, true)
	}
	// Each coverage of the selection dropped, except those kept.
	for _, c := range p.Coverages {
		if v, chosen := def[c]; !chosen || !selected(v) || keep[c] {
			continue
		}
		with(c, nil)
	}
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// markPrequoted says in the answer that it was priced ahead, and stamps the
// asker's memory version on it.
func markPrequoted(q any, p *prequoted, memoryVersion int) any {
	raw, err := json.Marshal(q)
	if err != nil {
		return q
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return q
	}
	if p.ahead {
		out["prequoted"] = true
	} else {
		out["cached"] = true
	}
	out["prequoted_ms"] = p.took.Milliseconds()
	if memoryVersion > 0 {
		out["memory_version"] = memoryVersion
	}
	return out
}
