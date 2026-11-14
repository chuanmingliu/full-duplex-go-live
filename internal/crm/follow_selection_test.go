package crm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func followPlan(t *testing.T, s *Service, limit int) QuotePlan {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(DefaultQuotePlans), &raw); err != nil {
		t.Fatal(err)
	}
	raw["plans"].(map[string]any)["standard"].(map[string]any)["prequote"].(map[string]any)["follow_selection_max"] = limit
	data, _ := json.Marshal(raw)
	if _, err := s.configs.Save(QuotePlansName, data, "test follow selection", "test"); err != nil {
		t.Fatal(err)
	}
	plans, err := ParseQuotePlans(data)
	if err != nil {
		t.Fatal(err)
	}
	return plans.Plans["standard"]
}

func followRequest(call string, plan QuotePlan) QuoteRequest {
	cfg := copyVars(plan.DefaultSelection)
	cfg["三者险"] = "500万"
	vars := copyVars(plan.DefaultVars)
	vars["carNum"] = "TEST"
	return QuoteRequest{Call: call, Config: cfg, Vars: vars, MemoryVersion: 1}
}

func awaitPrequote(t *testing.T, s *Service, plan QuotePlan, req QuoteRequest) *prequoted {
	t.Helper()
	key := selectionKey(req.Call, s.pricingVersion(req), plan, req.Config, req.Vars)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if p, _ := s.prequoteFor(key, false); p != nil {
			select {
			case <-p.done:
				return p
			default:
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("selected quote's next change was not priced ahead")
	return nil
}

func TestFollowSelectionWarmsChangesFromACachedForegroundQuote(t *testing.T) {
	fx := newFixture(t)
	plan := followPlan(t, fx.svc, 2)
	req := followRequest("follow-cached", plan)
	// Existing tier alternatives cost no new background inquiries.
	for _, tier := range []string{"100万", "200万", "300万", "500万"} {
		seed := req
		seed.Config = copyVars(req.Config)
		seed.Config["三者险"] = tier
		if _, err := fx.svc.Quote(seed); err != nil {
			t.Fatal(err)
		}
	}
	var got map[string]any
	if code, body := fx.do("POST", "/api/tools/quote", req, &got); code != 200 || got["cached"] != true {
		t.Fatalf("foreground cache hit: %d %s", code, body)
	}
	for _, tier := range []string{"30万", "50万"} {
		next := req
		next.Config = copyVars(req.Config)
		next.Config["驾乘险"] = tier
		if p := awaitPrequote(t, fx.svc, plan, next); !p.ahead {
			t.Fatal("next change was not a background quote")
		}
	}
	for _, tier := range []string{"30万", "50万"} {
		next := req
		next.Config = copyVars(req.Config)
		next.Config["驾乘险"] = tier
		if code, body := fx.do("POST", "/api/tools/quote", next, &got); code != 200 || got["prequoted"] != true {
			t.Fatalf("next change: %d %s", code, body)
		}
	}
}

func followSeed(t *testing.T, s *Service, plan QuotePlan, req QuoteRequest) *selectionWarmSeed {
	t.Helper()
	generation := s.beginSelectionQuote(req)
	v, err := s.configs.Get(QuotePlansName, 0)
	if err != nil {
		t.Fatal(err)
	}
	pricing := s.pricingVersion(req)
	if api, _ := s.quoteAPI(); api == nil {
		rules, _ := s.configs.Get(QuoteRulesName, 0)
		req.RulesVersion = rules.Version
	}
	req.prequote = true
	return &selectionWarmSeed{req: req, plan: plan, planVersion: v.Version, pricing: pricing, generation: generation, limit: plan.Prequote.FollowSelectionMax}
}

func followSpent(s *Service, call string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.selectionWarms[call]; st != nil {
		return st.spent
	}
	return 0
}

func awaitCondition(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not complete")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFollowSelectionPolicyAndBoundedLifetime(t *testing.T) {
	fx := newFixture(t)
	plan := followPlan(t, fx.svc, 0)
	req := followRequest("disabled", plan)
	if fx.svc.beginSelectionQuote(req) != 0 || len(fx.svc.selectionWarms) != 0 {
		t.Fatal("disabled policy allocated a call")
	}
	for _, limit := range []int{-1, 33} {
		raw := strings.Replace(DefaultQuotePlans, `"max": 8`, fmt.Sprintf(`"max": 8, "follow_selection_max": %d`, limit), 1)
		if _, err := ParseQuotePlans([]byte(raw)); err == nil {
			t.Fatalf("accepted limit %d", limit)
		}
	}
	plan = followPlan(t, fx.svc, 2)
	req = followRequest("retained", plan)
	seed := followSeed(t, fx.svc, plan, req)
	if _, ok := fx.svc.claimSelectionPrequote("budget-test", seed); !ok {
		t.Fatal("claim failed")
	}
	// Fill without aging out the first call: a long conversation retains its
	// budget even after many other calls arrive.
	for n := 1; n < maxSelectionWarmCalls; n++ {
		r := req
		r.Call = fmt.Sprint(n)
		fx.svc.beginSelectionQuote(r)
	}
	req.Call = "over-cap"
	if fx.svc.beginSelectionQuote(req) != 0 || len(fx.svc.selectionWarms) != maxSelectionWarmCalls {
		t.Fatal("call cap not enforced")
	}
	if !strings.Contains(fx.logs.String(), "capacity reached") {
		t.Fatal("capacity skip was not logged")
	}
	if _, err := fx.svc.Quote(req); err != nil {
		t.Fatalf("cap blocked foreground: %v", err)
	}
	req.Call = "retained"
	fx.svc.beginSelectionQuote(req)
	if followSpent(fx.svc, req.Call) != 1 {
		t.Fatal("active call budget reset")
	}
}

func TestFollowSelectionBudgetCountsOnlyNewClaimsAndIsolatesCalls(t *testing.T) {
	fx := newFixture(t)
	plan := followPlan(t, fx.svc, 2)
	req := followRequest("one", plan)
	seed := followSeed(t, fx.svc, plan, req)
	key := selectionKey(req.Call, seed.pricing, plan, req.Config, req.Vars)
	p, ok := fx.svc.claimSelectionPrequote(key, seed)
	if !ok {
		t.Fatal("first claim")
	}
	if _, ok := fx.svc.claimSelectionPrequote(key, seed); ok {
		t.Fatal("inflight counted twice")
	}
	p.quote = map[string]any{"status": "quote"}
	close(p.done)
	if _, ok := fx.svc.claimSelectionPrequote(key, seed); ok {
		t.Fatal("cache hit counted")
	}
	// An expired completed quotation is one new price, never a budget reset.
	p.started = time.Now().Add(-time.Hour)
	if _, ok := fx.svc.claimSelectionPrequote(key, seed); !ok {
		t.Fatal("expired quote not reclaimed")
	}
	newer := followSeed(t, fx.svc, plan, req)
	if _, ok := fx.svc.claimSelectionPrequote("third", newer); ok {
		t.Fatal("new selection reset cumulative budget")
	}
	req.Call = "two"
	other := followSeed(t, fx.svc, plan, req)
	if _, ok := fx.svc.claimSelectionPrequote("other", other); !ok || followSpent(fx.svc, "two") != 1 {
		t.Fatal("call budgets shared")
	}
}

func TestFollowSelectionRejectsStaleGenerationAndVersions(t *testing.T) {
	for _, change := range []string{"generation", "plan", "rules", "api"} {
		t.Run(change, func(t *testing.T) {
			fx := newFixture(t)
			plan := followPlan(t, fx.svc, 2)
			req := followRequest("stale", plan)
			if change == "api" {
				setFollowAPI(t, fx, "http://127.0.0.1:1")
			}
			seed := followSeed(t, fx.svc, plan, req)
			switch change {
			case "generation":
				fx.svc.beginSelectionQuote(req)
			case "plan":
				followPlan(t, fx.svc, 3)
			case "rules":
				v, _ := fx.svc.configs.Get(QuoteRulesName, 0)
				fx.svc.configs.Save(QuoteRulesName, append(v.Body, ' '), "changed", "test")
			case "api":
				v, _ := fx.svc.configs.Get(QuoteAPIName, 0)
				fx.svc.configs.Save(QuoteAPIName, append(v.Body, ' '), "changed", "test")
			}
			if fx.svc.selectionWarmCurrent(seed) {
				t.Fatal("stale seed is current")
			}
			if _, ok := fx.svc.claimSelectionPrequote("stale", seed); ok {
				t.Fatal("stale work spent budget")
			}
		})
	}
}

func TestFollowSelectionUsesCorrectedVariables(t *testing.T) {
	fx := newFixture(t)
	plan := followPlan(t, fx.svc, 2)
	req := followRequest("variables", plan)
	old := followSeed(t, fx.svc, plan, req)
	if _, err := fx.svc.quote(old.req, old); err != nil {
		t.Fatal(err)
	}
	next := req
	next.Config = copyVars(req.Config)
	next.Vars = copyVars(req.Vars)
	next.Vars["vehicleAge"] = 7
	seed := followSeed(t, fx.svc, plan, next)
	if _, err := fx.svc.quote(seed.req, seed); err != nil {
		t.Fatal(err)
	}
	oldKey := selectionKey(req.Call, old.pricing, plan, req.Config, req.Vars)
	newKey := selectionKey(next.Call, seed.pricing, plan, next.Config, next.Vars)
	if oldKey == newKey || followSpent(fx.svc, req.Call) != 2 {
		t.Fatal("corrected variables shared cached pricing")
	}
	if fx.svc.selectionWarmCurrent(old) {
		t.Fatal("prior variables still eligible for new work")
	}
}

func setFollowAPI(t *testing.T, fx *fixture, base string) {
	t.Helper()
	api := strings.Replace(DefaultQuoteAPI, `"base_url": ""`, `"base_url": "`+base+`/intranet"`, 1)
	api = strings.Replace(api, `"poll_every_ms": 1500`, `"poll_every_ms": 1`, 1)
	api = strings.Replace(api, `"timeout_ms": 90000`, `"timeout_ms": 1000, "default_phone": "13800000000"`, 1)
	if _, err := fx.svc.configs.Save(QuoteAPIName, []byte(api), "test", "test"); err != nil {
		t.Fatal(err)
	}
}

func TestFollowSelectionGatePriority(t *testing.T) {
	fx := newFixture(t)
	s := fx.svc
	plan := followPlan(t, s, 2)
	req := followRequest("priority", plan)
	seed := followSeed(t, s, plan, req)
	s.takeAPITurn(false)
	order := make(chan string, 3)
	go func() { s.takeAPITurn(true); order <- "initial" }()
	s.followDemand.Add(1)
	go func() {
		if s.takeSelectionAPITurn(seed) {
			order <- "follow"
		} else {
			order <- "canceled"
		}
	}()
	go func() { s.takeAPITurn(false); order <- "foreground" }()
	awaitCondition(t, func() bool { return s.apiDemand.Load() == 1 })
	s.releaseAPITurn()
	for _, want := range []string{"foreground", "follow", "initial"} {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("got %s want %s", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("stranded %s", want)
		}
		if want == "follow" {
			s.followDemand.Add(-1)
		}
		s.releaseAPITurn()
	}
}

func TestFollowSelectionCloseCancelsQueuedWork(t *testing.T) {
	fx := newFixture(t)
	s := fx.svc
	plan := followPlan(t, s, 2)
	req := followRequest("close", plan)
	setFollowAPI(t, fx, "http://127.0.0.1:1")
	ticket := s.beginSelectionQuote(req)
	s.takeAPITurn(false)
	s.scheduleSelectionWarming(req, map[string]any{"status": "quote"}, ticket)
	awaitCondition(t, func() bool { return s.followDemand.Load() == 1 })
	closed := make(chan struct{})
	go func() { s.stopSelectionWarming(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close stranded queued worker")
	}
	s.releaseAPITurn()
	if followSpent(s, req.Call) != 0 || s.followDemand.Load() != 0 {
		t.Fatal("canceled work claimed a price or retained priority")
	}
	// Concurrent foreground completions cannot Add a worker while Close waits.
	var wg sync.WaitGroup
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.scheduleSelectionWarming(req, map[string]any{"status": "quote"}, ticket) }()
	}
	wg.Wait()
	if s.beginSelectionQuote(req) != 0 {
		t.Fatal("closed service accepted a worker")
	}
}

func TestFollowSelectionFailureReleasesClaimAndGate(t *testing.T) {
	fx := newFixture(t)
	plan := followPlan(t, fx.svc, 1)
	req := followRequest("failure", plan)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	setFollowAPI(t, fx, srv.URL)
	seed := followSeed(t, fx.svc, plan, req)
	if _, err := fx.svc.quote(seed.req, seed); err == nil {
		t.Fatal("expected backend failure")
	}
	key := selectionKey(req.Call, seed.pricing, plan, req.Config, req.Vars)
	if p, _ := fx.svc.prequoteFor(key, false); p != nil {
		t.Fatal("failed claim retained")
	}
	if followSpent(fx.svc, req.Call) != 1 || fx.svc.followDemand.Load() != 0 {
		t.Fatal("failed computation refunded or stranded priority")
	}
	fx.svc.takeAPITurn(false)
	fx.svc.releaseAPITurn()
}

func TestFollowSelectionForegroundJoinsInflightAuthoritativeQuote(t *testing.T) {
	fx := newFixture(t)
	s := fx.svc
	plan := followPlan(t, s, 1)
	req := followRequest("join", plan)
	fake := &fakeIntranet{}
	base := fake.handler()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "vehicleApplyInquiry") {
			started <- struct{}{}
			<-release
		}
		base.ServeHTTP(w, r)
	}))
	defer srv.Close()
	setFollowAPI(t, fx, srv.URL)
	ticket := s.beginSelectionQuote(req)
	s.scheduleSelectionWarming(req, map[string]any{"status": "quote"}, ticket)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background inquiry did not start")
	}
	next := req
	next.Config = plan.prequoteVariants(req.Config)[0]
	next.Vars = copyVars(req.Vars)
	type answer struct {
		quote any
		err   error
	}
	result := make(chan answer, 1)
	go func() { q, err := s.Quote(next); result <- answer{q, err} }()
	select {
	case <-result:
		t.Fatal("foreground answered before authoritative result")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		raw, _ := json.Marshal(got.quote)
		var parsed map[string]any
		json.Unmarshal(raw, &parsed)
		if parsed["prequoted"] != true {
			t.Fatal("foreground failed to join background result")
		}
	case <-time.After(time.Second):
		t.Fatal("foreground/background cache and gate deadlocked")
	}
	s.stopSelectionWarming()
	fake.mu.Lock()
	n := len(fake.inquiries)
	fake.mu.Unlock()
	if n != 1 || followSpent(s, req.Call) != 1 {
		t.Fatalf("wanted one authoritative inquiry, got %d", n)
	}
}

func TestFollowSelectionNewerSelectionReplacesQueuedSeed(t *testing.T) {
	fx := newFixture(t)
	s := fx.svc
	plan := followPlan(t, s, 1)
	req := followRequest("replace", plan)
	fake := &fakeIntranet{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	setFollowAPI(t, fx, srv.URL)
	s.takeAPITurn(false)
	ticket := s.beginSelectionQuote(req)
	s.scheduleSelectionWarming(req, map[string]any{"status": "quote"}, ticket)
	awaitCondition(t, func() bool { return s.followDemand.Load() == 1 })
	next := req
	next.Config = copyVars(req.Config)
	next.Config["驾乘险"] = "50万"
	next.Vars = copyVars(req.Vars)
	ticket = s.beginSelectionQuote(next)
	s.scheduleSelectionWarming(next, map[string]any{"status": "quote"}, ticket)
	s.releaseAPITurn()
	candidate := next
	candidate.Config = plan.prequoteVariants(next.Config)[0]
	awaitPrequote(t, s, plan, candidate)
	s.stopSelectionWarming()
	oldCandidate := req
	oldCandidate.Config = plan.prequoteVariants(req.Config)[0]
	oldKey := selectionKey(req.Call, s.pricingVersion(req), plan, oldCandidate.Config, req.Vars)
	if p, _ := s.prequoteFor(oldKey, false); p != nil {
		t.Fatal("unstarted stale selection was priced")
	}
	if followSpent(s, req.Call) != 1 {
		t.Fatal("stale seed spent budget")
	}
}

func TestFollowSelectionDirectAndComparisonQuotesDoNotStartWorkers(t *testing.T) {
	fx := newFixture(t)
	s := fx.svc
	plan := followPlan(t, s, 2)
	req := followRequest("nonrecursive", plan)
	req.prequote = true
	if _, err := s.Quote(req); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	states := len(s.selectionWarms)
	s.mu.Unlock()
	if states != 0 {
		t.Fatal("background quote recursively started warming")
	}
	req.prequote = false
	ticket := s.beginSelectionQuote(req)
	// The comparison handler invalidates the generation but never schedules a
	// successful individual member as the next foreground selection.
	compare := req
	compare.Variants = []map[string]any{{"三者险": "100万"}, {"三者险": "200万"}}
	memory := copyVars(compare.Vars)
	memory["quote"] = compare.Config
	body := map[string]any{"tool": "quote_total", "call": compare.Call, "memory": memory, "arguments": map[string]any{"configs": compare.Variants}}
	var got map[string]any
	if code, body := fx.do("POST", "/api/tools/quote", body, &got); code != 200 {
		t.Fatalf("comparison: %d %s", code, body)
	}
	s.mu.Lock()
	state := s.selectionWarms[req.Call]
	running, spent, generation := state.running, state.spent, state.generation
	s.mu.Unlock()
	if running || spent != 0 || generation == ticket {
		t.Fatal("comparison scheduled background pricing or failed to supersede old work")
	}
}

func TestFollowSelectionAPIChangeWhileQueuedDoesNotMislabelPrice(t *testing.T) {
	fx := newFixture(t)
	s := fx.svc
	plan := followPlan(t, s, 1)
	req := followRequest("queued-version", plan)
	fake := &fakeIntranet{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	setFollowAPI(t, fx, srv.URL)
	s.takeAPITurn(false)
	ticket := s.beginSelectionQuote(req)
	s.scheduleSelectionWarming(req, map[string]any{"status": "quote"}, ticket)
	awaitCondition(t, func() bool { return s.followDemand.Load() == 1 })
	v, _ := s.configs.Get(QuoteAPIName, 0)
	if _, err := s.configs.Save(QuoteAPIName, append(v.Body, ' '), "new version", "test"); err != nil {
		t.Fatal(err)
	}
	s.releaseAPITurn()
	awaitCondition(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.selectionWarms[req.Call].running })
	fake.mu.Lock()
	n := len(fake.inquiries)
	fake.mu.Unlock()
	if n != 0 || followSpent(s, req.Call) != 0 {
		t.Fatal("queued old API version submitted or spent budget")
	}
}

func TestFollowSelectionExpiredReaderCannotDeleteReplacement(t *testing.T) {
	fx := newFixture(t)
	s := fx.svc
	plan := followPlan(t, s, 2)
	req := followRequest("expiry-owner", plan)
	seed := followSeed(t, s, plan, req)
	key := selectionKey(req.Call, seed.pricing, plan, req.Config, req.Vars)
	old, _ := s.prequoteFor(key, true)
	old.quote = map[string]any{"status": "quote"}
	old.started = time.Now().Add(-time.Hour)
	close(old.done)
	fresh, ok := s.claimSelectionPrequote(key, seed)
	if !ok {
		t.Fatal("expired entry not refreshed")
	}
	s.forgetPrequote(key, old)
	if got, _ := s.prequoteFor(key, false); got != fresh {
		t.Fatal("expired reader removed a new worker's claim")
	}
	if followSpent(s, req.Call) != 1 {
		t.Fatal("refresh budget incorrect")
	}
}

func TestFollowSelectionClaimedRulesStayPinnedWhenAPIEnabled(t *testing.T) {
	fx := newFixture(t)
	s := fx.svc
	plan := followPlan(t, s, 1)
	req := followRequest("claimed-rules-source", plan)
	seed := followSeed(t, s, plan, req)
	key := selectionKey(req.Call, seed.pricing, plan, req.Config, req.Vars)
	claim, ok := s.claimSelectionPrequote(key, seed)
	if !ok {
		t.Fatal("rules claim was not acquired")
	}
	defer close(claim.done)

	// Configuration can change between the claim and its computation. This
	// is exactly that boundary, without relying on a goroutine winning a race.
	fake := &fakeIntranet{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	setFollowAPI(t, fx, srv.URL)
	q, err := s.priceClaimedQuote(seed.req, seed, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	priced, ok := q.(*Quote)
	if !ok {
		t.Fatalf("rules cache claim received %T from newly enabled API", q)
	}
	if priced.RulesVersion != seed.req.RulesVersion {
		t.Fatalf("rules version changed: got %d want %d", priced.RulesVersion, seed.req.RulesVersion)
	}
	fake.mu.Lock()
	n := len(fake.inquiries)
	fake.mu.Unlock()
	if n != 0 {
		t.Fatal("rules claim submitted an API inquiry")
	}
}

func TestFollowSelectionClaimedPriceRejectsChangedSource(t *testing.T) {
	fx := newFixture(t)
	s := fx.svc
	plan := followPlan(t, s, 1)
	req := followRequest("claimed-source-changed", plan)
	seed := followSeed(t, s, plan, req)
	for _, pricing := range []string{"api:1", "rules:999"} {
		t.Run(pricing, func(t *testing.T) {
			changed := *seed
			changed.pricing = pricing
			q, err := s.priceClaimedQuote(seed.req, &changed, nil, nil)
			if q != nil || err != nil {
				t.Fatalf("source mismatch was priced: quote=%T error=%v", q, err)
			}
		})
	}
}
