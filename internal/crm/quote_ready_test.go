package crm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type readyResponse struct {
	Call   string `json:"call"`
	Quotes []struct {
		ID        string          `json:"id"`
		ExpiresAt time.Time       `json:"expires_at"`
		Result    json.RawMessage `json:"result"`
	} `json:"quotes"`
}

type readyTestFixture struct {
	svc  *Service
	logs *safeBuffer
	t    *testing.T
}

func (f *readyTestFixture) token() string { return f.svc.token }
func (f *readyTestFixture) do(method, path string, body any, out any) (int, string) {
	f.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+f.token())
	recorder := httptest.NewRecorder()
	f.svc.Handler().ServeHTTP(recorder, request)
	if out != nil {
		if err := json.Unmarshal(recorder.Body.Bytes(), out); err != nil {
			f.t.Fatal(err)
		}
	}
	return recorder.Code, recorder.Body.String()
}
func readyFixture(t *testing.T) (*readyTestFixture, QuotePlan) {
	t.Helper()
	logs := &safeBuffer{}
	service, err := Open(Config{Dir: t.TempDir(), Log: slog.New(slog.NewTextHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	fx := &readyTestFixture{svc: service, logs: logs, t: t}
	var document map[string]any
	if err := json.Unmarshal([]byte(DefaultQuotePlans), &document); err != nil {
		t.Fatal(err)
	}
	document["plans"].(map[string]any)["standard"].(map[string]any)["prequote"] = map[string]any{"max": 0}
	raw, _ := json.Marshal(document)
	if _, err := fx.svc.configs.Save(QuotePlansName, raw, "disable extra test work", "test"); err != nil {
		t.Fatal(err)
	}
	plans, err := ParseQuotePlans(raw)
	if err != nil {
		t.Fatal(err)
	}
	return fx, plans.Plans["standard"]
}
func fetchReady(t *testing.T, fx *readyTestFixture, call string) readyResponse {
	t.Helper()
	var got readyResponse
	if code, body := fx.do("GET", "/api/quote/ready?call="+url.QueryEscape(call), nil, &got); code != 200 {
		t.Fatalf("ready response %d: %s", code, body)
	}
	if got.Call != call || got.Quotes == nil {
		t.Fatalf("wrong response shape: %+v", got)
	}
	return got
}
func readyResult(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestQuoteReadyAuthenticatesAndValidatesExactCall(t *testing.T) {
	fx, _ := readyFixture(t)
	for _, token := range []string{"", "wrong"} {

		req := httptest.NewRequest("GET", "/api/quote/ready?call=out-ready-one", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		recorder := httptest.NewRecorder()
		fx.svc.Handler().ServeHTTP(recorder, req)
		if recorder.Code != 401 {
			t.Fatalf("unauthorized response %d", recorder.Code)
		}
	}
	for _, query := range []string{"", "call=", "call=out-ready-one&call=out-ready-two", "call=%20out-ready-one", "call=out-ready-one%7Capi", "call=" + strings.Repeat("a", 129)} {
		if code, _ := fx.do("GET", "/api/quote/ready?"+query, nil, nil); code != 400 {
			t.Fatalf("invalid query %q returned %d", query, code)
		}
	}
	got := fetchReady(t, fx, "out-ready-empty")
	if len(got.Quotes) != 0 {
		t.Fatal("empty call has quotes")
	}
}
func TestQuoteReadyCompletedExactOutputAndCallIsolation(t *testing.T) {
	fx, plan := readyFixture(t)
	req := followRequest("out-ready-one", plan)
	req.Config["outside-plan"] = true
	answer, err := fx.svc.Quote(req)
	if err != nil {
		t.Fatal(err)
	}
	got := fetchReady(t, fx, req.Call)
	if len(got.Quotes) != 1 {
		t.Fatalf("want one completed quote, got %d", len(got.Quotes))
	}
	raw, _ := json.Marshal(answer)
	if !reflect.DeepEqual(readyResult(t, got.Quotes[0].Result), readyResult(t, raw)) {
		t.Fatal("ready result lost authoritative fields or request skipped coverage")
	}
	if got.Quotes[0].ID == "" || !got.Quotes[0].ExpiresAt.After(time.Now()) {
		t.Fatal("missing identity or expiry")
	}
	if again := fetchReady(t, fx, req.Call); !reflect.DeepEqual(got, again) {
		t.Fatal("unchanged quote snapshot is unstable")
	}
	if len(fetchReady(t, fx, "out-ready-two").Quotes) != 0 || len(fetchReady(t, fx, "out-ready-on").Quotes) != 0 {
		t.Fatal("other call or prefix leaked quotes")
	}
}
func TestQuoteReadyDefaultFirstWithoutDuplicateCacheResult(t *testing.T) {
	fx, plan := readyFixture(t)
	call := "out-ready-default"
	if _, err := fx.svc.Quote(followRequest(call, plan)); err != nil {
		t.Fatal(err)
	}
	answer, err := fx.svc.DefaultQuote(context.Background(), call, copyVars(plan.DefaultVars), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got := fetchReady(t, fx, call)
	if len(got.Quotes) != 2 {
		t.Fatalf("expected default and one variant, got %d", len(got.Quotes))
	}
	raw, _ := json.Marshal(answer)
	if !reflect.DeepEqual(readyResult(t, got.Quotes[0].Result), readyResult(t, raw)) {
		t.Fatal("first result is not exact default answer")
	}
	for _, entry := range fx.svc.prequotes {
		if entry.ready != nil && entry.ready.value.ID == got.Quotes[0].ID {
			t.Fatal("default presentation reused the ordinary cached result ID")
		}
	}
}
func TestQuoteReadyPendingIsNonblockingAndConcurrentCompletionAppears(t *testing.T) {
	fx, plan := readyFixture(t)
	rules := strings.Replace(DefaultQuoteRules, `"simulate_delay_ms": 0`, `"simulate_delay_ms": 150`, 1)
	if _, err := fx.svc.configs.Save(QuoteRulesName, []byte(rules), "test delay", "test"); err != nil {
		t.Fatal(err)
	}
	req := followRequest("out-ready-concurrent", plan)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := fx.svc.Quote(req); err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		fx.svc.mu.Lock()
		n := len(fx.svc.prequotes)
		fx.svc.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("quote never claimed")
		}
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	first := fetchReady(t, fx, req.Call)
	if len(first.Quotes) != 0 || time.Since(start) > 100*time.Millisecond {
		t.Fatal("pending quote was returned or awaited")
	}
	for i := 0; i < 10; i++ {
		fetchReady(t, fx, req.Call)
	}
	wg.Wait()
	if len(fetchReady(t, fx, req.Call).Quotes) != 1 {
		t.Fatal("completion did not become visible")
	}
}
func TestQuoteReadyInvalidatesAfterPricingChangeAndOldRulesPin(t *testing.T) {
	fx, plan := readyFixture(t)
	req := followRequest("out-ready-source", plan)
	if _, err := fx.svc.Quote(req); err != nil {
		t.Fatal(err)
	}
	if len(fetchReady(t, fx, req.Call).Quotes) != 1 {
		t.Fatal("initial quote absent")
	}
	updated := strings.Replace(DefaultQuoteRules, `"flat": 950`, `"flat": 951`, 1)
	if _, err := fx.svc.configs.Save(QuoteRulesName, []byte(updated), "new pricing", "test"); err != nil {
		t.Fatal(err)
	}
	if len(fetchReady(t, fx, req.Call).Quotes) != 0 {
		t.Fatal("stale pricing leaked")
	}
	req.Call = "out-ready-oldpin"
	req.RulesVersion = 1
	if _, err := fx.svc.Quote(req); err != nil {
		t.Fatal(err)
	}
	if len(fetchReady(t, fx, req.Call).Quotes) != 0 {
		t.Fatal("explicit old rules leaked")
	}
}
func TestQuoteReadyDoesNotTriggerPricingOrLogResults(t *testing.T) {
	fx, _ := readyFixture(t)
	before := fx.svc.events.Seq()
	for i := 0; i < 3; i++ {
		fetchReady(t, fx, fmt.Sprintf("out-ready-%d", i))
	}
	if fx.svc.events.Seq() != before || len(fx.svc.prefetches) != 0 || len(fx.svc.prequotes) != 0 {
		t.Fatal("read-only request caused pricing")
	}

	logs := fx.logs.String()
	if strings.Contains(logs, "out-ready-") || strings.Contains(logs, fx.token()) || strings.Contains(logs, "\"result\"") {
		t.Fatal("ready request logged call, credential, or result")
	}
}

func TestQuoteReadyCountAndByteBounds(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		fx, plan := readyFixture(t)
		call := "out-ready-many"
		for i := 0; i < 40; i++ {
			req := followRequest(call, plan)
			req.Vars["vehiclePrice"] = 100000 + i
			if _, err := fx.svc.Quote(req); err != nil {
				t.Fatal(err)
			}
		}
		got := fetchReady(t, fx, call)
		if len(got.Quotes) != 32 {
			t.Fatalf("expected bounded 32, got %d", len(got.Quotes))
		}
		previous := time.Time{}
		for _, entry := range got.Quotes {
			var q Quote
			if err := json.Unmarshal(entry.Result, &q); err != nil {
				t.Fatal(err)
			}
			if q.ComputedAt.Before(previous) {
				t.Fatal("normal quotes are not chronological")
			}
			previous = q.ComputedAt
		}
	})
	t.Run("bytes", func(t *testing.T) {
		fx, plan := readyFixture(t)
		call := "out-ready-large"
		for i := 0; i < 3; i++ {
			req := followRequest(call, plan)
			req.Vars["vehiclePrice"] = 100000 + i
			size := 140000
			if i == 2 {
				size = 270000
			}
			req.Config[strings.Repeat("x", size)] = true
			if _, err := fx.svc.Quote(req); err != nil {
				t.Fatal(err)
			}
		}
		var got readyResponse
		code, body := fx.do("GET", "/api/quote/ready?call="+call, nil, &got)
		if code != 200 || len(body) > 256<<10 || len(got.Quotes) != 1 {
			t.Fatalf("bounded response: status=%d bytes=%d quotes=%d", code, len(body), len(got.Quotes))
		}
	})
}
func TestQuoteReadyExpiryAndValidityMetadata(t *testing.T) {
	fx, plan := readyFixture(t)
	req := followRequest("out-ready-expiry", plan)
	answer, err := fx.svc.Quote(req)
	if err != nil {
		t.Fatal(err)
	}
	pricing := fx.svc.currentReadyPricing()
	if fx.svc.completedReadyQuote(req.Call, time.Now().Add(-time.Hour), pricing, answer) != nil {
		t.Fatal("accepted already-expired completed quote")
	}
	key := selectionKey(req.Call, fx.svc.pricingVersion(req), plan, req.Config, req.Vars)
	entry, _ := fx.svc.prequoteFor(key, false)
	if entry == nil || entry.ready == nil {
		t.Fatal("missing test cache result")
	}
	entry.ready.value.ExpiresAt = time.Now().Add(-time.Second)
	if len(fetchReady(t, fx, req.Call).Quotes) != 0 {
		t.Fatal("expired cached quote leaked")
	}
	// An explicit service validity is measured from the same cache claim.
	raw, _ := json.Marshal(answer)
	result := readyResult(t, raw)
	result["valid_for_s"] = 1
	started := time.Now().Add(-2 * time.Second)
	if fx.svc.completedReadyQuote(req.Call, started, pricing, result) != nil {
		t.Fatal("ignored service validity")
	}
	result["valid_for_s"] = int64(1<<63 - 1)
	if fx.svc.completedReadyQuote(req.Call, time.Now(), pricing, result) != nil {
		t.Fatal("overflowing validity accepted")
	}
}
func TestQuoteReadyCompletionDuringConfigChangeStaysUnavailable(t *testing.T) {
	fx, plan := readyFixture(t)
	rules := strings.Replace(DefaultQuoteRules, `"simulate_delay_ms": 0`, `"simulate_delay_ms": 150`, 1)
	if _, err := fx.svc.configs.Save(QuoteRulesName, []byte(rules), "slow", "test"); err != nil {
		t.Fatal(err)
	}
	req := followRequest("out-ready-changing", plan)
	finished := make(chan error, 1)
	go func() { _, err := fx.svc.Quote(req); finished <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		fx.svc.mu.Lock()
		claimed := len(fx.svc.prequotes) > 0
		fx.svc.mu.Unlock()
		if claimed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("quote not claimed")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := fx.svc.configs.Save(QuoteRulesName, []byte(DefaultQuoteRules), "changed while computing", "test"); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if len(fetchReady(t, fx, req.Call).Quotes) != 0 {
		t.Fatal("completion under changed source was published")
	}
}
func TestQuoteReadySourceSwitchInvalidates(t *testing.T) {
	fx, plan := readyFixture(t)
	req := followRequest("out-ready-switch", plan)
	if _, err := fx.svc.Quote(req); err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(DefaultQuoteAPI), &config); err != nil {
		t.Fatal(err)
	}
	config["base_url"] = "http://must-not-be-called.invalid"
	body, _ := json.Marshal(config)
	if _, err := fx.svc.configs.Save(QuoteAPIName, body, "switch source", "test"); err != nil {
		t.Fatal(err)
	}
	if len(fetchReady(t, fx, req.Call).Quotes) != 0 {
		t.Fatal("rules result survived API source change")
	}
}
func TestQuoteReadyDoesNotReconstructAuditOrPublishFailedNeeds(t *testing.T) {
	fx, plan := readyFixture(t)
	call := "out-ready-failed"
	if _, err := fx.svc.events.Append(Event{Type: "quote.computed", Quote: &QuoteRecord{Call: call, Total: 999}}); err != nil {
		t.Fatal(err)
	}
	if len(fetchReady(t, fx, call).Quotes) != 0 {
		t.Fatal("reconstructed audit record")
	}
	req := followRequest(call, plan)
	req.Vars = nil
	if _, err := fx.svc.Quote(req); err != nil {
		t.Fatal(err)
	}
	if len(fetchReady(t, fx, call).Quotes) != 0 {
		t.Fatal("need published as quote")
	}
	req = followRequest(call, plan)
	req.Config["三者险"] = "missing-tier"
	fx.svc.Quote(req)
	if len(fetchReady(t, fx, call).Quotes) != 0 {
		t.Fatal("failed result published")
	}
}

type readyRoundTripper func(*http.Request) (*http.Response, error)

func (f readyRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestQuoteReadyPreservesCompletedAPIResultWithoutAnotherInquiry(t *testing.T) {
	fx, plan := readyFixture(t)
	fake := &fakeIntranet{readyAt: 2}
	handler := fake.handler()
	fx.svc.quoteClient = &http.Client{Transport: readyRoundTripper(func(r *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		return recorder.Result(), nil
	})}
	api := strings.Replace(DefaultQuoteAPI, `"base_url": ""`, `"base_url": "http://fixture.invalid/intranet"`, 1)
	api = strings.Replace(api, `"poll_every_ms": 1500`, `"poll_every_ms": 1`, 1)
	api = strings.Replace(api, `"timeout_ms": 90000`, `"timeout_ms": 2000, "default_phone": "13800000000"`, 1)
	if _, err := fx.svc.configs.Save(QuoteAPIName, []byte(api), "API fixture", "test"); err != nil {
		t.Fatal(err)
	}
	req := followRequest("out-ready-api", plan)
	req.Config["outside-api-plan"] = true
	answer, err := fx.svc.Quote(req)
	if err != nil {
		t.Fatal(err)
	}
	got := fetchReady(t, fx, req.Call)
	if len(got.Quotes) != 1 {
		t.Fatal("completed API result absent")
	}
	raw, _ := json.Marshal(answer)
	if !reflect.DeepEqual(readyResult(t, raw), readyResult(t, got.Quotes[0].Result)) {
		t.Fatal("API response fields changed")
	}
	if len(fake.inquiries) != 1 {
		t.Fatal("snapshot triggered another API inquiry")
	}
	if got.Quotes[0].ExpiresAt.Before(time.Now().Add(59 * time.Minute)) {
		t.Fatal("API validity not honored")
	}
}
func TestQuoteReadyConfigFileTamperingInvalidates(t *testing.T) {
	fx, plan := readyFixture(t)
	req := followRequest("out-ready-tamper", plan)
	if _, err := fx.svc.Quote(req); err != nil {
		t.Fatal(err)
	}
	version, err := fx.svc.configs.Get(QuoteRulesName, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fx.svc.configs.path(QuoteRulesName, version.Version), []byte(`{"tampered":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if len(fetchReady(t, fx, req.Call).Quotes) != 0 {
		t.Fatal("tampered pricing file did not invalidate snapshot")
	}
}
