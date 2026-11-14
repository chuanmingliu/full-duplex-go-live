package crm

import (
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/quotesvc"
)

func TestMotorCombinationCacheSimulation(t *testing.T) {
	if os.Getenv("GOLIVE_MOTOR_CACHE_SIM") != "1" {
		t.Skip("opt-in exhaustive family and pairwise API simulation")
	}
	cases, families, space := motorCombinationMatrix()
	if space != 413677492904 {
		t.Fatalf("catalog space changed: %d", space)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	backend, err := quotesvc.Open(quotesvc.Options{Dir: t.TempDir(), Workers: 1, Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.AddVehicle(quotesvc.Vehicle{Plate: "测试车", Price: 150000, Age: 3, Seats: 5}); err != nil {
		t.Fatal(err)
	}
	var submissions atomic.Int64
	upstreamHandler := backend.Handler()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "vehicleApplyInquiryPriceAsync") {
			submissions.Add(1)
		}
		upstreamHandler.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	service, err := Open(Config{Dir: t.TempDir(), Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	save := func(name string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.configs.Save(name, raw, "synthetic cache simulation", "test"); err != nil {
			t.Fatal(err)
		}
	}
	save(QuotePlansName, QuotePlans{Default: "motor-full", Plans: map[string]QuotePlan{"motor-full": FullMotorPlan()}})
	api := FullMotorAPI()
	api.BaseURL = upstream.URL + "/intranet"
	api.PollEveryMS = 1
	save(QuoteAPIName, api)
	// Exercise the HTTP tool entry point, including normalization and overlays.
	crmServer := httptest.NewServer(service.Handler())
	defer crmServer.Close()
	request := func(selection map[string]any, call string, vars map[string]any) map[string]any {
		t.Helper()
		memory := copyVars(vars)
		memory["quote"] = copyVars(selection)
		raw, _ := json.Marshal(map[string]any{"tool": "quote_total", "call": call, "memory": memory, "memory_version": 1, "arguments": map[string]any{}})
		req, _ := http.NewRequest(http.MethodPost, crmServer.URL+"/api/tools/quote", strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+service.token)
		response, err := crmServer.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(response.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 || out["status"] != "quote" {
			t.Fatalf("quotation failed: HTTP %d %+v", response.StatusCode, out)
		}
		if !reflect.DeepEqual(out["selection"], selectedConfig(selection)) {
			t.Fatalf("selection mismatch: requested=%+v actual=%+v", selection, out["selection"])
		}
		sum := 0.0
		for _, raw := range out["lines"].([]any) {
			sum += raw.(map[string]any)["premium"].(float64)
		}
		if math.Abs(sum-out["total"].(float64)) > 0.011 {
			t.Fatalf("line sum %.2f differs from total %v", sum, out["total"])
		}
		return out
	}
	call := "synthetic-one-customer"
	seed := func(c motorCombination) bool {
		raw, _ := json.Marshal(c.Selection)
		hash := sha256.Sum256(raw)
		return hash[0]%4 == 0
	}
	seeded := 0
	for _, c := range cases {
		if seed(c) {
			request(c.Selection, call, motorVars())
			seeded++
		}
	}
	if submissions.Load() != int64(seeded) {
		t.Fatalf("preparation submissions=%d seeds=%d", submissions.Load(), seeded)
	}
	hits, misses, repeats := 0, 0, 0
	start := time.Now()
	for index, c := range cases {
		before := submissions.Load()
		out := request(c.Selection, call, motorVars())
		if seed(c) {
			if submissions.Load() != before || out["cached"] != true {
				t.Fatalf("%s cache hit used API", c.ID)
			}
			hits++
		} else {
			if submissions.Load() != before+1 || out["cached"] == true {
				t.Fatalf("%s miss did not use API exactly once", c.ID)
			}
			misses++
		}
		before = submissions.Load()
		again := request(c.Selection, call, motorVars())
		if submissions.Load() != before || again["cached"] != true || again["total"] != out["total"] {
			t.Fatalf("%s repeat missed cache or changed price", c.ID)
		}
		repeats++
		if (index+1)%1000 == 0 {
			t.Logf("verified %d/%d combinations", index+1, len(cases))
		}
	}
	if submissions.Load() != int64(len(cases)) {
		t.Fatalf("unexpected upstream work %d", submissions.Load())
	}
	// The same selection for another call and corrected pricing facts must miss.
	probe := cases[0].Selection
	before := submissions.Load()
	request(probe, "synthetic-other-customer", motorVars())
	if submissions.Load() != before+1 {
		t.Fatal("quotation crossed call scope")
	}
	vars := motorVars()
	vars["vehicleAge"] = 4
	before = submissions.Load()
	request(probe, call, vars)
	if submissions.Load() != before+1 {
		t.Fatal("changed pricing fact reused a cached quotation")
	}
	result := map[string]any{"kind": "synthetic HTTP API simulation", "family_states": families, "structural_policy_space": space, "tested_unique_combinations": len(cases), "seeded_combinations": seeded, "cache_hits": hits, "api_misses": misses, "repeat_cache_hits": repeats, "isolation_checks": 2, "total_api_submissions": submissions.Load(), "evaluation_ms": time.Since(start).Milliseconds(), "simulated_backend_delay_ms": 0, "real_audio": false}
	raw, _ := json.MarshalIndent(result, "", "  ")
	t.Log(string(raw))
	if path := os.Getenv("GOLIVE_MOTOR_CACHE_REPORT"); path != "" {
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
