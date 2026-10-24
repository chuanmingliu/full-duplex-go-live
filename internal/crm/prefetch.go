package crm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// The default quotation is worked out while the phone is still ringing.
//
// The owner's business logic (10-24): a lead is known before the call is
// placed — plate, price, age, claims, seats — and there is a fixed default
// quotation plan, so the customer is never asked for any of it. The inquiry
// for the default plan is submitted as the dial goes out and the result is
// waiting by the time the conversation reaches the price: the assistant
// asks the tool with no selection and reads it out at once. Only a second
// quotation — the customer wants a different combination — goes through
// the questions (the quote-plans intake), the async inquiry, the prehook
// while it runs, and the posthook and template when it is back.
//
// Prefetches are kept by the originator's call id, which golive puts on
// every tool request (tools.Request.Call); a golive session that was not
// placed from a lead (the box's redial of a kept prompt) asks for one at its
// start, and the plan's default_vars stand in for the lead.

// prefetched is one default quotation in flight or done.
type prefetched struct {
	call    string
	started time.Time
	vars    map[string]any
	done    chan struct{}
	quote   any
	err     error
	took    time.Duration
}

// Prefetch starts the default quotation for a call with the lead's variables
// (nil for a call without a lead), once per call. It returns at once.
func (s *Service) Prefetch(call string, vars map[string]any) {
	if call == "" {
		return
	}
	s.mu.Lock()
	if s.prefetches == nil {
		s.prefetches = map[string]*prefetched{}
	}
	if _, dup := s.prefetches[call]; dup {
		s.mu.Unlock()
		return
	}
	p := &prefetched{call: call, started: time.Now(), vars: copyVars(vars), done: make(chan struct{})}
	s.prefetches[call] = p
	// Older prefetches go: a call is one quotation long.
	for id, old := range s.prefetches {
		if time.Since(old.started) > 2*time.Hour {
			delete(s.prefetches, id)
		}
	}
	s.mu.Unlock()
	go func() {
		defer close(p.done)
		req, err := s.defaultRequest(call, p.vars)
		if err != nil {
			p.err = err
		} else {
			p.quote, p.err = s.Quote(req)
			if n, isNeed := p.quote.(*Need); isNeed && n != nil {
				p.err = fmt.Errorf("the default plan is missing %d field(s) for this lead: %s", len(n.Missing), missingNames(n))
				p.quote = nil
			}
		}
		p.took = time.Since(p.started)
		s.log.Info("crm: default quotation prefetched", "call", call, "ms", p.took.Milliseconds(), "err", errString(p.err), "vars", len(p.vars))
	}()
}

func missingNames(n *Need) string {
	s := ""
	for i, m := range n.Missing {
		if i > 0 {
			s += ","
		}
		s += m.Field
	}
	return s
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func copyVars(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

// defaultRequest is the default plan's selection over the lead's variables,
// with the plan's default_vars under them.
func (s *Service) defaultRequest(call string, vars map[string]any) (QuoteRequest, error) {
	v, err := s.configs.Get(QuotePlansName, 0)
	if err != nil {
		return QuoteRequest{}, err
	}
	plans, err := ParseQuotePlans(v.Body)
	if err != nil {
		return QuoteRequest{}, err
	}
	name, plan := plans.plan(vars)
	if len(plan.DefaultSelection) == 0 {
		return QuoteRequest{}, fmt.Errorf("plan %q has no default_selection", name)
	}
	req := QuoteRequest{Config: copyVars(plan.DefaultSelection), Vars: map[string]any{}, Call: call}
	for k, val := range plan.DefaultVars {
		req.Vars[k] = val
	}
	for k, val := range vars {
		req.Vars[k] = val
	}
	return req, nil
}

// prefetchFor finds a call's prefetch.
func (s *Service) prefetchFor(call string) *prefetched {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prefetches[call]
}

// DefaultQuote answers a tool request that chose nothing: the prefetched
// default quotation when there is one (waiting for it if it is still in
// flight, within wait), else the default plan priced now.
func (s *Service) DefaultQuote(ctx context.Context, call string, vars map[string]any, wait time.Duration) (any, error) {
	p := s.prefetchFor(call)
	if p == nil {
		s.Prefetch(call, vars)
		p = s.prefetchFor(call)
		if p == nil {
			return nil, errors.New("no call to quote for")
		}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		return nil, fmt.Errorf("the default quotation is still being worked out (%s so far)", time.Since(p.started).Round(time.Second))
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if p.err != nil {
		return nil, p.err
	}
	return p.quote, nil
}

// LeadVars are the variables the call's lead supplied, for a second
// quotation: the customer is not asked again for what the lead knows.
func (s *Service) LeadVars(call string) map[string]any {
	p := s.prefetchFor(call)
	if p == nil {
		return nil
	}
	return copyVars(p.vars)
}

// handlePrefetch is POST /api/quote/prefetch {call, vars}: golive asks for
// the default quotation at a session's start when the call was not placed
// from a lead; a lead-placed call has it from Call().
func (s *Service) handlePrefetch(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Call string         `json:"call"`
		Vars map[string]any `json:"vars"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		return fail(http.StatusBadRequest, "the request is not readable")
	}
	if in.Call == "" {
		return fail(http.StatusBadRequest, "call is required")
	}
	s.Prefetch(in.Call, in.Vars)
	writeJSON(w, http.StatusAccepted, map[string]any{"call": in.Call, "started": true})
	return nil
}
