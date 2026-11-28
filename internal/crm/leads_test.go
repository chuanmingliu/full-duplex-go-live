package crm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLeadConfiguration(t *testing.T) {
	fx := newFixture(t)
	edit := leadEdit{Name: "Test lead", Phone: "+12025550101", Purpose: "survey", Tagged: true}
	save := func(want int) {
		t.Helper()
		if status, body := fx.do("PUT", "/api/leads/test", edit, nil); status != want {
			t.Fatalf("save: %d %s", status, body)
		}
	}
	tagged := func() []Lead {
		t.Helper()
		var out struct {
			Leads       []Lead
			OutboundTag string `json:"outbound_tag"`
		}
		if status, body := fx.do("GET", "/api/leads?tag=speko-outbound", nil, &out); status != 200 {
			t.Fatalf("list: %d %s", status, body)
		}
		if out.OutboundTag != "speko-outbound" {
			t.Fatalf("missing built-in tag: %q", out.OutboundTag)
		}
		return out.Leads
	}
	if len(tagged()) != 0 {
		t.Fatal("new CRM contains leads")
	}
	save(200)
	save(409)
	fx.api.Close()
	fx.svc.Close()
	fx.open("", "", "")
	rows := tagged()
	if len(rows) != 1 || rows[0].Version != 1 || rows[0].OutboundConsent || rows[0].Values["purpose"] != "survey" {
		t.Fatalf("after replay: %+v", rows)
	}
	edit.Version = 1
	edit.Phone = "invalid"
	save(422)
	edit.Phone = "+12025550101"
	edit.Tagged = false
	edit.Consent = true
	save(200)
	if len(tagged()) != 0 {
		t.Fatal("removed tag still matches")
	}
	var all struct{ Leads []Lead }
	if status, _ := fx.do("GET", "/api/leads", nil, &all); status != 200 || len(all.Leads) != 1 || !all.Leads[0].OutboundConsent || all.Leads[0].Version != 2 {
		t.Fatalf("after edit: %+v", all)
	}
}

func TestScheduleTag(t *testing.T) {
	fx := newFixture(t)
	on := true
	edit := leadEdit{Name: "Fixture", Phone: "+12025550101", Purpose: "reminder", Schedule: &on}
	check := func(count int) {
		t.Helper()
		var result struct {
			Leads       []Lead
			ScheduleTag string `json:"schedule_tag"`
		}
		status, body := fx.do("GET", "/api/leads?tag=schedule", nil, &result)
		if status != 200 || result.ScheduleTag != "schedule" || len(result.Leads) != count {
			t.Fatalf("schedule filter: %d %s", status, body)
		}
	}
	save := func() {
		t.Helper()
		if status, body := fx.do("PUT", "/api/leads/example", edit, nil); status != 200 {
			t.Fatalf("save: %d %s", status, body)
		}
		edit.Version++
	}
	check(0)
	save()
	check(1)
	edit.Schedule = nil // Older clients omit the schedule field.
	edit.Tagged = true
	save()
	fx.api.Close()
	fx.svc.Close()
	fx.open("", "", "")
	check(1)
	on = false
	edit.Schedule = &on
	save()
	check(0)
	var result struct{ Leads []Lead }
	if status, body := fx.do("GET", "/api/leads?tag=speko-outbound", nil, &result); status != 200 || len(result.Leads) != 1 {
		t.Fatalf("other tag: %d %s", status, body)
	}
}

func TestLeadCallPlan(t *testing.T) {
	fx := newFixture(t)
	enabled := true
	edit := leadEdit{Name: "Sample", Phone: "+12025550101", Purpose: "follow-up", Schedule: &enabled, CallPlan: &CallPlan{Time: "09:05", Days: []int{5, 1, 5}}}
	save := func(status int) *CallPlan {
		t.Helper()
		var result struct{ Lead Lead }
		code, body := fx.do("PUT", "/api/leads/recurring", edit, &result)
		if code != status {
			t.Fatalf("save: %d %s", code, body)
		}
		if status == 200 {
			edit.Version = result.Lead.Version
		}
		return result.Lead.CallPlan
	}
	plan := save(200)
	if plan == nil || plan.Cron != "5 9 * * 1,5" || plan.Timezone != "Asia/Shanghai" {
		t.Fatalf("saved plan: %+v", plan)
	}
	fx.api.Close()
	fx.svc.Close()
	fx.open("", "", "")
	edit.CallPlan = nil
	if p := save(200); p == nil || p.Cron != plan.Cron {
		t.Fatal("older client lost plan after replay")
	}
	edit.CallPlan = &CallPlan{Time: "25:01", Days: []int{0}}
	save(422)
	edit.CallPlan.Time = "00:00"
	edit.CallPlan.Days = nil
	save(422)
	edit.CallPlan.Days = []int{0, 1, 2, 3, 4, 5, 6}
	if p := save(200); p == nil || p.Cron != "0 0 * * *" {
		t.Fatal("daily midnight schedule failed")
	}
	edit.CallPlan = &CallPlan{}
	if save(200) != nil {
		t.Fatal("clear failed")
	}
}

func TestPurposeContent(t *testing.T) {
	fx := newFixture(t)
	for _, p := range []string{"follow-up", "reminder", "survey"} {
		var result purposeContent
		path := "/api/call-purposes/" + p
		if status, _ := fx.do("GET", path, nil, &result); status != 200 || result.Content != "" {
			t.Fatal("initial slot", p, status, result)
		}
		if status, _ := fx.do("PUT", path, purposeContent{Content: "Brief: " + p}, &result); status != 200 {
			t.Fatal("save", p, status)
		}
	}
	fx.api.Close()
	fx.svc.Close()
	fx.open("", "", "")
	for _, p := range []string{"follow-up", "reminder", "survey"} {
		var result purposeContent
		path := "/api/call-purposes/" + p
		if status, _ := fx.do("GET", path, nil, &result); status != 200 || result.Content != "Brief: "+p {
			t.Fatal("replay", p, status, result)
		}
		if status, _ := fx.do("PUT", path, purposeContent{}, &result); status != 200 || result.Content != "" {
			t.Fatal("clear", p, status, result)
		}
	}
}

func TestSaveQueuesOnlyCommittedEligibleLeadVersions(t *testing.T) {
	fx := newFixture(t)
	enabled := true
	edit := leadEdit{Name: "Fixture", Phone: "13800000001", Purpose: "normal", Schedule: &enabled, CallPlan: &CallPlan{Time: "11:15", Days: []int{3}}}
	path := filepath.Join(fx.dir, registrationDir)
	count := func(want int) {
		t.Helper()
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != want {
			t.Fatalf("tickets %v, %v; want %d", entries, err, want)
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "sample.2.") || !strings.HasSuffix(entry.Name(), ".job") {
				t.Fatal(entry.Name())
			}
		}
	}
	var response struct {
		Queued bool `json:"registration_queued"`
	}
	if code, _ := fx.do("PUT", "/api/leads/sample", edit, &response); code != 200 || response.Queued {
		t.Fatal("nonconsenting", code, response)
	}
	count(0)
	edit.Version = 1
	edit.Consent = true
	if code, _ := fx.do("PUT", "/api/leads/sample", edit, &response); code != 200 || !response.Queued {
		t.Fatal("eligible", code, response)
	}
	count(1)
	if code, _ := fx.do("PUT", "/api/leads/sample", edit, nil); code != 409 {
		t.Fatal(code)
	}
	count(1)
	fx.api.Close()
	fx.svc.Close()
	fx.open("", "", "")
	count(1)
}
