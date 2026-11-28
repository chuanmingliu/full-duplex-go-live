package crm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/deepseek"
)

func TestTranscriptLabelsEndToEnd(t *testing.T) {
	fx := newFixture(t)
	for _, id := range []string{"alice", "bob"} {
		if _, err := fx.svc.saveLead(id, leadEdit{Name: id, Phone: lockedNumber, Purpose: "normal"}, "operator"); err != nil {
			t.Fatal(err)
		}
	}
	do := func(method, path string, body, out any, want int) {
		t.Helper()
		if status, text := fx.do(method, path, body, out); status != want {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, status, want, text)
		}
	}
	schema := labelSchema{Enabled: true, Prompt: "Extract the requested details.", Dimensions: []labelDimension{
		{Key: "intent", Group: "labels", Type: "enum", Options: []string{"callback"}, Description: "Action"},
		{Key: "vehicle_model", Group: "vehicle", Type: "string", Description: "Vehicle model"},
		{Key: "after_five", Group: "user", Type: "boolean", Description: "Wants evening calls"},
	}}
	do("PUT", "/api/configs/transcript-labels", schema, nil, 200)
	bad := schema
	bad.Dimensions = append(bad.Dimensions, bad.Dimensions[0])
	do("PUT", "/api/configs/transcript-labels", bad, nil, 422)
	var requests atomic.Int32
	var unsupported atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var body struct {
			Messages []provider.Message
			Model    string
			Stream   bool
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.URL.Path != "/v1/chat/completions" || !body.Stream || body.Model != "classifier" || len(body.Messages) != 2 || !strings.Contains(body.Messages[0].Content, schema.Prompt) || !strings.Contains(body.Messages[0].Content, "after_five") || strings.Contains(body.Messages[1].Content, lockedNumber) {
			t.Error("unexpected model request")
			w.WriteHeader(400)
			return
		}
		result := `{"observations":[{"key":"intent","value":"callback","turn":1,"quote":"call after five"},{"key":"after_five","value":true,"turn":1,"quote":"call after five"},{"key":"vehicle_model","value":"Civic","turn":1,"quote":"my Civic"}]}`
		if unsupported.Load() {
			result = strings.Replace(result, "my Civic", "an invented quote", 1)
		}
		delta, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": result}}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", delta)
	}))
	defer upstream.Close()
	fx.svc.cfg.LabelLLM = &deepseek.LLM{BaseURL: upstream.URL, APIKey: "fixture"}
	fx.svc.cfg.LabelModel = "classifier"
	in := labelInput{TranscriptID: "chat-1", VehicleID: "vehicle-1", SchemaVersion: 2, Turns: []labelTurn{{Role: "user", Content: "Please call after five about my Civic."}}}
	const path = "/api/leads/alice/labels"
	var first memoryRevision
	do("POST", path, in, &first, 200)
	if first.Labeling == nil || first.Labeling.User.ID != "alice" || first.Labeling.User.Phone != lockedNumber || first.Labeling.User.Version != 1 || first.Labeling.VehicleID != "vehicle-1" || first.Labeling.SchemaVersion != 2 || len(first.Labeling.Observations) != 3 || !strings.Contains(first.Value, `"user":{"after_five":true}`) || !strings.Contains(first.Value, `"vehicle":{"vehicle_model":"Civic"}`) {
		t.Fatalf("saved %+v", first)
	}
	do("POST", path, in, nil, 200)
	in.CallID = "unrelated-call"
	do("POST", path, in, nil, 422)
	in.CallID = ""
	in.Turns[0].Content += " Thank you."
	do("POST", path, in, nil, 409)
	if requests.Load() != 1 {
		t.Fatal("retry or rejected request ran inference")
	}
	in.PreviousSeq = first.Seq
	unsupported.Store(true)
	do("POST", path, in, nil, 502)
	unsupported.Store(false)
	var second memoryRevision
	do("POST", path, in, &second, 200)
	if second.Seq <= first.Seq || second.Labeling.TranscriptSHA == first.Labeling.TranscriptSHA {
		t.Fatal("missing revised source")
	}
	fx.api.Close()
	fx.svc.Close()
	fx.open("", "", "")
	var trace struct{ Revisions []memoryRevision }
	do("GET", "/api/leads/alice/memory/trace", nil, &trace, 200)
	if len(trace.Revisions) != 2 || trace.Revisions[1].Labeling.Observations[1].Turn != 1 || trace.Revisions[1].Labeling.SchemaSHA != second.Labeling.SchemaSHA {
		t.Fatal("incomplete replayed trace")
	}
	do("GET", "/api/leads/bob/memory/trace", nil, &trace, 200)
	if len(trace.Revisions) != 0 {
		t.Fatal("cross-lead memory")
	}
	do("PUT", "/api/templates/labels", "Prior facts {{ user_memory }}", nil, 200)
	var render Rendered
	do("POST", "/api/render", map[string]string{"lead": "alice", "template": "labels"}, &render, 200)
	if len(render.MemorySeqs) != 1 || render.MemorySeqs[0] != second.Seq || !strings.Contains(render.Instructions, "Civic") {
		t.Fatalf("recall %+v", render)
	}
}

func TestLabelOutputValidation(t *testing.T) {
	schema := labelSchema{Dimensions: []labelDimension{{Key: "year", Group: "vehicle", Type: "number"}}}
	turns := []labelTurn{{Role: "user", Content: "2021"}}
	valid := `{"key":"year","value":2021,"turn":1,"quote":"2021"}`
	for _, item := range []string{strings.Replace(valid, "2021,", `"2021",`, 1), strings.Replace(valid, "year", "other", 1), valid + "," + valid, strings.Replace(valid, `"turn":1`, `"turn":2`, 1)} {
		if _, _, err := validateLabels(`{"observations":[`+item+`]}`, schema, turns, ""); err == nil {
			t.Fatalf("accepted %s", item)
		}
	}
	if _, value, err := validateLabels(`{"observations":[`+valid+`]}`, schema, turns, ""); err != nil || value != `{"vehicle":{"year":2021}}` {
		t.Fatal(value, err)
	}
	if _, value, err := validateLabels(`{"observations":[]}`, schema, turns, ""); err != nil || value != "{}" {
		t.Fatal(value, err)
	}
}

func TestLabelCommitRaces(t *testing.T) {
	fx := newFixture(t)
	lead, err := fx.svc.saveLead("alice", leadEdit{Name: "Alice", Phone: lockedNumber, Purpose: "normal"}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	meta := labelReceipt{TranscriptID: "a", TranscriptSHA: "original", Model: "fixture", SchemaVersion: 1, User: labelUser{ID: lead.ID, Version: lead.Version}}
	const key = "test-key"
	first, err := fx.svc.commitLabels(lead.ID, key, "operator", 0, meta, "{}")
	if err != nil {
		t.Fatal(err)
	}
	for _, same := range []bool{true, false} {
		competing := meta
		if !same {
			competing.TranscriptSHA = "updated"
		}
		result, err := fx.svc.commitLabels(lead.ID, key, "operator", 0, competing, "{}")
		if same && (err != nil || result.Seq != first.Seq) || !same && err == nil {
			t.Fatal("lost commit precondition", result, err)
		}
	}
	if _, err := fx.svc.saveLead("alice", leadEdit{Version: lead.Version, Name: "Updated", Phone: lockedNumber, Purpose: "normal"}, "operator"); err != nil {
		t.Fatal(err)
	}
	meta.TranscriptSHA = "updated"
	if _, err := fx.svc.commitLabels(lead.ID, key, "operator", first.Seq, meta, "{}"); err == nil {
		t.Fatal("accepted stale lead snapshot")
	}
}
