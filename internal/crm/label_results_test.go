package crm

import (
	"fmt"
	"testing"
)

func TestLabelResultsSearchAndPagination(t *testing.T) {
	fx := newFixture(t)
	lead, err := fx.svc.saveLead("alice", leadEdit{Name: "Alice", Phone: lockedNumber, Purpose: "normal"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	var last int64
	for i := 0; i < 28; i++ {
		id := fmt.Sprintf("transcript-%02d", i)
		meta := labelReceipt{TranscriptID: id, CallID: "Call-" + id, VehicleID: "vehicle-a", User: labelUser{ID: lead.ID, Version: lead.Version}, OriginalSession: []byte("{}")}
		rev, err := fx.svc.commitLabels(lead.ID, id, "hangup", 0, meta, `{"vehicle":{"model":"Civic"}}`)
		if err != nil {
			t.Fatal(err)
		}
		last = rev.Seq
	}
	fx.api.Close()
	fx.svc.Close()
	fx.open("", "", "")
	type page struct {
		Results []labelSummary
		Next    int64 `json:"next_before"`
	}
	get := func(query string) page {
		t.Helper()
		var p page
		if code, b := fx.do("GET", "/api/label-results"+query, nil, &p); code != 200 {
			t.Fatal(b)
		}
		return p
	}
	p := get("")
	if len(p.Results) != 25 || p.Results[0].Revision != last || p.Next != p.Results[24].Revision {
		t.Fatal("incorrect first page")
	}
	if tail := get(fmt.Sprintf("?before=%d", p.Next)); len(tail.Results) != 3 || tail.Next != 0 || tail.Results[0].Revision >= p.Next {
		t.Fatal("incorrect second page")
	}
	for _, query := range []string{"CALL-transcript-27", "transcript-27", fmt.Sprint(last)} {
		result := get("?id=" + query)
		if len(result.Results) != 1 || result.Results[0].Archive != "pending" {
			t.Fatal(query)
		}
	}
	for _, query := range []string{"alice", "vehicle-a"} {
		if len(get("?id="+query).Results) != 25 {
			t.Fatal(query)
		}
	}
	if len(get("?id=unknown").Results) != 0 {
		t.Fatal("unexpected match")
	}
	var detail struct {
		Fields   map[string]map[string]string
		Labeling labelReceipt
	}
	if code, _ := fx.do("GET", fmt.Sprintf("/api/label-results/%d", last), nil, &detail); code != 200 || detail.Fields["vehicle"]["model"] != "Civic" || detail.Labeling.User.ID != "alice" {
		t.Fatal("result detail missing")
	}
}
