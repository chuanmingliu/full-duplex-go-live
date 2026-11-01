package crm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestQuotePollStartsPromptlyAndRetainsFreshness(t *testing.T) {
	var mu sync.Mutex
	var submitted bool
	var starts []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/vehicle/innerProcess/vehicleApplyInquiryPriceAsync" {
			submitted = true
			_, _ = w.Write([]byte(`{"code":0}`))
			return
		}
		id := 1
		if submitted {
			starts = append(starts, time.Now())
			// Return the old completed result twice, then a new unpriced record.
			if len(starts) >= 3 {
				id = 2
			}
		}
		info := map[string]any{"quotePriceId": id, "insureFlowCode": "flow", "result": "0", "sumPreimum": "100"}
		if len(starts) == 3 {
			info["result"], info["sumPreimum"] = "", ""
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"quotePriceInfos": []any{info}}})
	}))
	defer srv.Close()
	a := QuoteAPI{BaseURL: srv.URL, DefaultPhone: "test", DefaultVehicleNo: "test", PollEveryMS: 80, TimeoutMS: 2000}
	q, _, err := a.Quote(context.Background(), srv.Client(), QuoteRequest{Config: map[string]any{"交强险": true}})
	if err != nil || q == nil || q.QuotePriceID != 2 {
		t.Fatalf("quote=%+v err=%v", q, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 4 {
		t.Fatalf("old/unpriced result was accepted: %d queries", len(starts))
	}
	for i := 1; i < len(starts); i++ {
		if starts[i].Sub(starts[i-1]) < 70*time.Millisecond {
			t.Fatal("queries exceeded configured polling cadence")
		}
	}
}

func TestQuoteReadyAtSubmissionDoesNotWaitForPollInterval(t *testing.T) {
	var submitted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/vehicle/innerProcess/vehicleApplyInquiryPriceAsync" {
			submitted = true
			_, _ = w.Write([]byte(`{"code":0}`))
			return
		}
		id := 1
		if submitted {
			id = 2
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"quotePriceInfos": []any{map[string]any{"quotePriceId": id, "result": "0", "sumPreimum": "100"}}}})
	}))
	defer srv.Close()
	// Deadline is shorter than the configured interval: only an immediate
	// freshness-checked query can succeed. No timing percentile assertion.
	a := QuoteAPI{BaseURL: srv.URL, DefaultPhone: "test", DefaultVehicleNo: "test", PollEveryMS: 1000, TimeoutMS: 500}
	q, _, err := a.Quote(context.Background(), srv.Client(), QuoteRequest{Config: map[string]any{"交强险": true}})
	if err != nil || q == nil || q.QuotePriceID != 2 {
		t.Fatalf("ready result waited: quote=%+v err=%v", q, err)
	}
}
