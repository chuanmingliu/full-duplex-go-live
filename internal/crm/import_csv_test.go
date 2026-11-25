package crm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCSVUpload(t *testing.T) {
	fx := newFixture(t)
	path := "/api/leads/import"
	header := "\ufeffname,phone,purpose,call_time,repeat_days,outbound_consent\r\n"
	first := "\"Chen, Yu\",13800000001,Normal call,09:15,Mon|Wed,true\r\n"
	for _, invalid := range []string{
		header + first + "Error,13800000002,survey,25:00,Fri,false",
		header + first + "Error,13800000002,survey,12:00,Fri,maybe",
		"name,phone,unknown\na,1234567,x",
	} {
		if status, body := fx.do("POST", path, invalid, nil); status != 422 {
			t.Fatal(status, body)
		}
	}
	content := header + first + "Other,+8613800000001,reminder,,,false\r\n小王,13800000002,survey,,,\r\n"
	var result struct {
		Created, Skipped int
		Queued           int `json:"registration_queued"`
	}
	if code, body := fx.do("POST", path, content, &result); code != 200 || result.Created != 2 || result.Skipped != 1 || result.Queued != 1 {
		t.Fatal(code, body)
	}
	if code, body := fx.do("POST", path, content, &result); code != 200 || result.Created != 0 || result.Skipped != 3 || result.Queued != 0 {
		t.Fatal(code, body)
	}
	var rows struct{ Leads []Lead }
	if code, body := fx.do("GET", "/api/leads?tag=schedule", nil, &rows); code != 200 || len(rows.Leads) != 1 || rows.Leads[0].Values["name"] != "Chen, Yu" || rows.Leads[0].Values["purpose"] != "normal" || rows.Leads[0].CallPlan.Cron != "15 9 * * 1,3" {
		t.Fatal(code, body)
	}
	files, err := os.ReadDir(filepath.Join(fx.dir, registrationDir))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
}
