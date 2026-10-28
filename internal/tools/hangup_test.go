package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTheDefaultHangupRulesParseAndReachTheToolDescription(t *testing.T) {
	hr, errs := ParseHangupRules([]byte(DefaultHangupRules))
	if len(errs) != 0 {
		t.Fatalf("errors in the default set: %v", errs)
	}
	if len(hr.ModelRules()) < 3 || len(hr.EngineRules()) < 2 {
		t.Fatalf("model %d engine %d", len(hr.ModelRules()), len(hr.EngineRules()))
	}
	d := hr.EndCallTool().Description
	for _, want := range []string{"1. ", "再见", "告别语"} {
		if !strings.Contains(d, want) {
			t.Fatalf("description lacks %q: %s", want, d)
		}
	}
	var matched, goodbye bool
	for _, r := range hr.EngineRules() {
		matched = matched || (r.Kind == "keyword" && r.Matches("我要投诉你们"))
		goodbye = goodbye || (r.Kind == "assistant" && r.Matches("好的，再见。"))
	}
	if !matched || !goodbye {
		t.Fatalf("complaint matched %v, assistant goodbye matched %v", matched, goodbye)
	}
}

func TestBadRulesAreDroppedNotTheSet(t *testing.T) {
	off := false
	raw, _ := json.Marshal(HangupRules{Rules: []HangupRule{
		{ID: "a", Kind: "model", When: "x"},
		{ID: "b", Kind: "keyword", Pattern: "("},
		{ID: "c", Kind: "silence"},
		{ID: "d", Kind: "duration", Seconds: 5, Enabled: &off},
	}})
	hr, errs := ParseHangupRules(raw)
	if len(errs) != 2 || len(hr.Rules) != 2 {
		t.Fatalf("errs %v rules %d", errs, len(hr.Rules))
	}
	if n := len(hr.EngineRules()); n != 0 {
		t.Fatalf("a disabled rule is in force: %d", n)
	}
}

func TestHangupRulesAreReadFromTheLeadService(t *testing.T) {
	var auth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/configs/hangup-rules" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"config":{"name":"hangup-rules","version":3,"body":{"rules":[{"id":"q","kind":"silence","seconds":9,"farewell":"再见"}]}}}`))
	}))
	defer ts.Close()
	hr, errs := FetchHangupRules(ts.URL, "tok", time.Second)
	if len(errs) != 0 || hr.Version != 3 || len(hr.Rules) != 1 || hr.Rules[0].Seconds != 9 {
		t.Fatalf("got %+v errs %v", hr, errs)
	}
	if auth != "Bearer tok" {
		t.Fatalf("auth %q", auth)
	}
}
