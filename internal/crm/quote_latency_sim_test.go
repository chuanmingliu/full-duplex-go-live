package crm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

// Matched HTTP simulator; no real pricing service, caller data or dial path.
// Run the same file on baseline/candidate to expose polling overhead separately
// from the unchanged simulated upstream work and HTTP round-trip time.
func TestQuoteLatencySimulation(t *testing.T) {
	if os.Getenv("GOLIVE_TOOL_LATENCY_SIM") != "1" {
		t.Skip("opt-in latency comparison")
	}
	for _, readyMS := range []int{0, 350, 800, 1350} {
		for trial := 0; trial < 3; trial++ {
			var mu sync.Mutex
			var submitted time.Time
			queries := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/vehicle/innerProcess/vehicleApplyInquiryPriceAsync" {
					mu.Lock()
					submitted = time.Now()
					mu.Unlock()
					_, _ = w.Write([]byte(`{"code":0}`))
					return
				}
				time.Sleep(80 * time.Millisecond)
				mu.Lock()
				id := 1
				if !submitted.IsZero() {
					queries++
					if time.Since(submitted) >= time.Duration(readyMS)*time.Millisecond {
						id = 2
					}
				}
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"quotePriceInfos": []any{map[string]any{"quotePriceId": id, "result": "0", "sumPreimum": "100"}}}})
			}))
			a := QuoteAPI{BaseURL: srv.URL, DefaultPhone: "test", DefaultVehicleNo: "test", PollEveryMS: 500, TimeoutMS: 5000}
			began := time.Now()
			q, _, err := a.Quote(context.Background(), srv.Client(), QuoteRequest{Config: map[string]any{"交强险": true}})
			elapsed := time.Since(began).Milliseconds()
			srv.Close()
			if err != nil || q == nil || q.QuotePriceID != 2 {
				t.Fatalf("quote=%+v err=%v", q, err)
			}
			t.Logf("LATENCY_SIM upstream_ms=%d http_ms=80 poll_ms=500 trial=%d tool_ms=%d queries=%d", readyMS, trial, elapsed, queries)
		}
	}
}
