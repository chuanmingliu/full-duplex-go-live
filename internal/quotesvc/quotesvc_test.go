package quotesvc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/crm"
)

// The service answers the lead service's adapter end to end: inquiry,
// polling, the priced quote in the document's shape.
func TestTheLeadServicesAdapterGetsAQuoteFromTheService(t *testing.T) {
	dir := t.TempDir()
	svc, err := Open(Options{Dir: dir, ComputeMS: 300, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AddVehicle(Vehicle{Plate: "测A00001", Price: 700000, Age: 5, Seats: 5, NCD: 0}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(svc.Handler())
	t.Cleanup(ts.Close)

	var api crm.QuoteAPI
	if err := jsonUnmarshal([]byte(crm.DefaultQuoteAPI), &api); err != nil {
		t.Fatal(err)
	}
	api.BaseURL, api.PollEveryMS, api.TimeoutMS = ts.URL+"/intranet", 100, 5000
	req := crm.QuoteRequest{Config: map[string]any{"交强险": true, "车损险": true, "三者险": "200万", "座位险": true},
		Vars: map[string]any{"phone": "13800000000", "carNum": "测A00001"}}
	q, _, err := api.Quote(context.Background(), &http.Client{Timeout: 5 * time.Second}, req)
	if err != nil {
		t.Fatal(err)
	}
	// 700000 × 0.012 = 8400 × 0.9 (age 5) = 7560; 三者 200万 1450 × 0.9 = 1305;
	// 乘客 2万 × 9 × 4 = 72 × 0.9 = 64.8; compulsory 950; tax 420.
	if q.Total != 7560+1305+64.8+950+420 {
		t.Fatalf("total %v; lines %+v", q.Total, q.Lines)
	}
	if q.Status != "quote" || len(q.Lines) != 5 || q.ComputeMS < 300 {
		t.Fatalf("quote %+v", q)
	}
	// A plate the service does not know is a failed quote the assistant explains.
	req.Vars["carNum"] = "测B99999"
	if _, _, err := api.Quote(context.Background(), &http.Client{Timeout: 5 * time.Second}, req); err == nil {
		t.Fatal("an unknown plate was priced")
	}
	// The latest quote survives a restart.
	svc2, err := Open(Options{Dir: dir, ComputeMS: 0})
	if err != nil {
		t.Fatal(err)
	}
	if svc2.Latest("13800000000", "测A00001") == nil {
		t.Fatal("the quote was not replayed from the log")
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
