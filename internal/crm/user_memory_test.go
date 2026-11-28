package crm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestUserMemoryAcrossCallsAndRestart(t *testing.T) {
	fx := newFixture(t)
	request := func(method, path string, body, out any, want int) {
		t.Helper()
		if code, text := fx.do(method, path, body, out); code != want {
			t.Fatalf("%s %s: %d %s", method, path, code, text)
		}
	}
	for id, phone := range map[string]string{"alice": lockedNumber, "bob": "1234567890"} {
		request("PUT", "/api/leads/"+id, leadEdit{Name: id, Phone: phone, Purpose: "normal"}, nil, 200)
	}
	const alice = "/api/leads/alice/memory"
	const bob = "/api/leads/bob/memory"
	edit := map[string]any{"value": "evening", "source": "operator interview", "previous_seq": 0}
	var first, latest memoryRevision
	request("PUT", alice+"/callback", edit, &first, 200)
	request("PUT", alice+"/callback", edit, &latest, 200)
	if first.Seq == 0 || first.Seq != latest.Seq {
		t.Fatal("retry created a revision")
	}
	edit["value"] = "morning"
	request("PUT", bob+"/callback", edit, nil, 200)
	request("PUT", alice+"/callback", edit, nil, 409)
	edit["previous_seq"] = first.Seq
	request("PUT", alice+"/callback", edit, &latest, 200)
	request("PUT", "/api/templates/recall", "Prior customer facts: {{ user_memory }}", nil, 200)
	render := map[string]any{"template": "recall", "lead": "alice", "greeting": "hello"}
	var out Rendered
	request("POST", "/api/render", render, &out, 200)
	if !slices.Equal(out.MemorySeqs, []int64{latest.Seq}) || !strings.Contains(out.Instructions, "morning") {
		t.Fatalf("preview: %+v", out)
	}
	request("POST", "/api/call", render, nil, 200)
	render["lead"] = "bob"
	request("POST", "/api/call", render, nil, 502)
	if len(fx.dials) != 1 {
		t.Fatal("recipient mismatch reached fake originator")
	}
	request("PUT", alice+"/callback", map[string]any{"retracted": true, "source": "operator correction", "previous_seq": latest.Seq}, nil, 200)
	fx.api.Close()
	fx.svc.Close()
	fx.open("", "", "")
	var trace struct {
		Revisions []memoryRevision
		Calls     []CallRecord
	}
	request("GET", alice+"/trace", nil, &trace, 200)
	if len(trace.Revisions) != 3 || trace.Revisions[0] != first || trace.Revisions[1] != latest || !trace.Revisions[2].Retracted ||
		len(trace.Calls) != 1 || trace.Calls[0].CallID != "out-test-1" || !slices.Equal(trace.Calls[0].MemorySeqs, []int64{latest.Seq}) {
		t.Fatalf("replayed trace: %+v", trace)
	}
	var current struct{ Facts []memoryRevision }
	request("GET", alice, nil, &current, 200)
	if len(current.Facts) != 0 {
		t.Fatal("retracted fact is still active")
	}
	request("GET", bob, nil, &current, 200)
	if len(current.Facts) != 1 || current.Facts[0].Seq == latest.Seq {
		t.Fatal("lead memory mixed")
	}
	request("GET", "/api/leads/missing/memory", nil, nil, 404)
}

func TestOpenVikingMemoryImport(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 required for importer integration")
	}
	fx := newFixture(t)
	if _, err := fx.svc.saveLead("alice", leadEdit{Name: "Alice", Phone: lockedNumber, Purpose: "normal"}, "tester"); err != nil {
		t.Fatal(err)
	}
	const uri = "viking://user/alice/memories/preferences/language.md"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/content/read" || r.URL.Query().Get("uri") != uri || r.Header.Get("X-API-Key") != "fixture-key" {
			http.Error(w, "bad request", 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "result": "Prefers Mandarin."})
	}))
	defer upstream.Close()
	cmd := exec.Command(python, "../../scripts/import-openviking-memory.py", "alice", "language", "preferences/language.md", "--previous-seq", "0")
	cmd.Env = append(os.Environ(), "OPENVIKING_URL="+upstream.URL, "OPENVIKING_API_KEY=fixture-key", "CRM_URL="+fx.api.URL, "CRM_TOKEN="+fx.token())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("import: %v %s", err, output)
	}
	var current struct{ Facts []memoryRevision }
	if code, _ := fx.do("GET", "/api/leads/alice/memory", nil, &current); code != 200 || len(current.Facts) != 1 || current.Facts[0].Source != uri || current.Facts[0].Value != "Prefers Mandarin." {
		t.Fatalf("imported facts: %+v", current)
	}
}

func TestMemoryRecallSelection(t *testing.T) {
	fx := newFixture(t)
	if _, err := fx.svc.saveLead("alice", leadEdit{Name: "Alice", Phone: lockedNumber, Purpose: "normal"}, "tester"); err != nil {
		t.Fatal(err)
	}
	var want []int64
	for _, key := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		rev, err := fx.svc.saveMemory("alice", key, "tester", 0, memoryFact{Value: "fact-" + key, Source: "sample"})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, rev.Seq)
	}
	slices.Reverse(want)
	if code, body := fx.do("PUT", "/api/templates/mem", "Memory: {{ user_memory }}", nil); code != 200 {
		t.Fatal(body)
	}
	for _, test := range []struct {
		name     string
		lead     string
		vars     map[string]string
		wantSeqs []int64
		wantText string
	}{
		{"most recent", "alice", nil, want[:8], "fact-i"},
		{"unidentified", "", nil, nil, "Memory:"},
		{"explicit variable", "alice", map[string]string{"user_memory": "supplied"}, nil, "supplied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out Rendered
			code, body := fx.do("POST", "/api/render", map[string]any{"template": "mem", "lead": test.lead, "vars": test.vars}, &out)
			if code != 200 || !slices.Equal(out.MemorySeqs, test.wantSeqs) || !strings.Contains(out.Instructions, test.wantText) || strings.Contains(out.Instructions, "fact-a") {
				t.Fatal(body)
			}
		})
	}
}
