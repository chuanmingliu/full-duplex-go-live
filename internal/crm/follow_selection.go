package crm

import (
	"encoding/json"
	"fmt"
	"time"
)

// At capacity we stop accepting new speculative calls. Retaining each counter
// until Close avoids resetting a still-active call's cumulative budget.
const maxSelectionWarmCalls = 512

// These states belong to a phone call, not a model session. Only new cache
// claims spend the extra budget; a repeated or already-running price is free.
type selectionWarmState struct {
	generation uint64
	spent      int
	pending    *selectionWarmSeed
	running    bool
	finished   string
}

type selectionWarmSeed struct {
	req         QuoteRequest
	plan        QuotePlan
	planVersion int
	pricing     string
	key         string
	generation  uint64
	limit       int
}

// Even a failed quote or comparison supersedes queued work from an earlier
// selection. Already-started pricing keeps the shared API gate until it ends.
func (s *Service) beginSelectionQuote(req QuoteRequest) uint64 {
	if req.Call == "" {
		return 0
	}
	// Do not allocate per-call state for the default-disabled policy, default
	// quotes or comparisons. Existing work is still invalidated by any ask.
	enabled := false
	if !req.prequote && len(req.Config) > 0 && len(req.Variants) == 0 {
		if version, err := s.configs.Get(QuotePlansName, 0); err == nil {
			if plans, err := ParseQuotePlans(version.Body); err == nil {
				vars := copyVars(s.LeadVars(req.Call))
				for k, v := range req.Vars {
					vars[k] = v
				}
				_, plan := plans.plan(vars)
				enabled = plan.Prequote.FollowSelectionMax > 0
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selectionWarmClosed {
		return 0
	}
	state := s.selectionWarms[req.Call]
	if state == nil {
		if !enabled {
			return 0
		}
		if len(s.selectionWarms) >= maxSelectionWarmCalls {
			if !s.selectionWarmFullLogged {
				s.selectionWarmFullLogged = true
				s.log.Warn("crm: selection warming capacity reached; new calls skip speculative pricing", "max_calls", maxSelectionWarmCalls)
			}
			return 0
		}
		if s.selectionWarms == nil {
			s.selectionWarms = make(map[string]*selectionWarmState)
		}
		state = &selectionWarmState{}
		s.selectionWarms[req.Call] = state
	}
	state.generation++
	state.pending = nil
	return state.generation
}

func (s *Service) scheduleSelectionWarming(req QuoteRequest, quote any, generation uint64) {
	if generation == 0 || req.prequote || len(req.Variants) != 0 {
		return
	}
	var result struct {
		Status string `json:"status"`
	}
	data, err := json.Marshal(quote)
	if err != nil || json.Unmarshal(data, &result) != nil || result.Status != "quote" {
		return
	}
	version, err := s.configs.Get(QuotePlansName, 0)
	if err != nil {
		return
	}
	plans, err := ParseQuotePlans(version.Body)
	if err != nil {
		return
	}
	_, plan := plans.plan(req.Vars)
	if plan.Prequote.FollowSelectionMax == 0 {
		return
	}
	req.Config, req.Vars = copyVars(req.Config), copyVars(req.Vars)
	req.prequote = true
	pricing := s.pricingVersion(req)
	if selectionKey("", "", plan, req.Config, nil) == selectionKey("", "", plan, plan.DefaultSelection, nil) {
		return
	}
	// Pin rules just as an API configuration is pinned before its inquiry.
	if api, _ := s.quoteAPI(); api == nil {
		if v, e := s.configs.Get(QuoteRulesName, req.RulesVersion); e == nil {
			req.RulesVersion = v.Version
		}
	}
	seed := &selectionWarmSeed{req: req, plan: plan, planVersion: version.Version, pricing: pricing,
		generation: generation, limit: plan.Prequote.FollowSelectionMax}
	seed.key = fmt.Sprintf("%d/%s", version.Version, selectionKey(req.Call, pricing, plan, req.Config, req.Vars))
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.selectionWarms[req.Call]
	if s.selectionWarmClosed || state == nil || state.generation != generation || state.spent >= seed.limit || state.finished == seed.key {
		return
	}
	state.pending = seed
	if !state.running {
		state.running = true
		// Close sets closed under the same mutex before waiting, so no worker
		// can be added after it begins waiting.
		s.selectionWarmWG.Add(1)
		go s.runSelectionWarming(req.Call)
	}
}

func (s *Service) selectionWarmCurrent(seed *selectionWarmSeed) bool {
	version, err := s.configs.Get(QuotePlansName, 0)
	if err != nil || version.Version != seed.planVersion || s.pricingVersion(seed.req) != seed.pricing {
		return false
	}
	// An explicitly pinned old rules version must not outlive a config change.
	if api, _ := s.quoteAPI(); api == nil {
		version, err = s.configs.Get(QuoteRulesName, 0)
		if err != nil || version.Version != seed.req.RulesVersion {
			return false
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.selectionWarms[seed.req.Call]
	return !s.selectionWarmClosed && state != nil && state.generation == seed.generation && state.spent < seed.limit
}

func (s *Service) claimSelectionPrequote(key string, seed *selectionWarmSeed) (*prequoted, bool) {
	if !s.selectionWarmCurrent(seed) {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.selectionWarms[seed.req.Call]
	if s.selectionWarmClosed || state == nil || state.generation != seed.generation || state.spent >= seed.limit {
		return nil, false
	}
	if s.prequotes == nil {
		s.prequotes = make(map[string]*prequoted)
	}
	if p := s.prequotes[key]; p != nil && usablePrequote(p) {
		return nil, false
	}
	p := &prequoted{key: key, started: time.Now(), done: make(chan struct{}), ahead: true}
	s.prequotes[key] = p
	state.spent++
	return p, true
}

func (s *Service) runSelectionWarming(call string) {
	defer s.selectionWarmWG.Done()
	for {
		s.mu.Lock()
		state := s.selectionWarms[call]
		seed := state.pending
		state.pending = nil
		if s.selectionWarmClosed || seed == nil {
			state.running = false
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		// The initial budget is not a limit on free cache lookups. Inspect all
		// single changes; claimSelectionPrequote enforces the cumulative new work.
		plan := seed.plan
		unlimited := int(^uint(0) >> 1)
		plan.Prequote.Max = &unlimited
		for _, cfg := range plan.prequoteVariants(seed.req.Config) {
			if !s.selectionWarmCurrent(seed) {
				break
			}
			req := seed.req
			req.Config, req.Vars = cfg, copyVars(seed.req.Vars)
			if _, err := s.quote(req, seed); err != nil {
				s.log.Warn("crm: selected plan neighbour failed", "call", call)
			}
		}
		s.mu.Lock()
		if state.generation == seed.generation {
			state.finished = seed.key
		}
		s.mu.Unlock()
	}
}

// A follow-up waits behind foreground inquiries and goes before unstarted
// initial prequotes. A canceled generation never takes the gate to price.
func (s *Service) takeSelectionAPITurn(seed *selectionWarmSeed) bool {
	s.mu.Lock()
	if s.apiGate == nil {
		s.apiGate = make(chan struct{}, 1)
	}
	gate := s.apiGate
	s.mu.Unlock()
	for s.selectionWarmCurrent(seed) {
		if s.apiDemand.Load() > 0 {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		select {
		case gate <- struct{}{}:
			if s.apiDemand.Load() == 0 && s.selectionWarmCurrent(seed) {
				return true
			}
			<-gate
		case <-time.After(20 * time.Millisecond):
		}
	}
	return false
}

func (s *Service) stopSelectionWarming() {
	s.mu.Lock()
	s.selectionWarmClosed = true
	for _, state := range s.selectionWarms {
		state.pending = nil
		state.generation++
	}
	s.mu.Unlock()
	s.selectionWarmWG.Wait()
}

// A pending quote belongs to its existing worker. A completed, expired quote
// may be refreshed as one new claim without waiting on or replacing a worker.
func usablePrequote(p *prequoted) bool {
	select {
	case <-p.done:
		return p.err == nil && p.quote != nil && p.fresh()
	default:
		return true
	}
}
