package crm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chuanmingliu/golive/internal/labeljob"
	"github.com/chuanmingliu/golive/internal/provider"
)

type fixtureLabeler struct{ requests int }

func (*fixtureLabeler) Name() string { return "fixture" }
func (f *fixtureLabeler) Stream(context.Context, provider.LLMRequest) (<-chan provider.LLMDelta, error) {
	f.requests++
	ch := make(chan provider.LLMDelta, 1)
	ch <- provider.LLMDelta{Text: `{"observations":[{"key":"make_model","value":"Civic","quote":"my Civic","turn":1}]}`}
	close(ch)
	return ch, nil
}

func TestHangupLabelArchiveAndSwitch(t *testing.T) {
	fx := newFixture(t)
	dir := t.TempDir()
	llm := &fixtureLabeler{}
	fx.svc.cfg.LabelSpoolDir = dir
	fx.svc.cfg.LabelLLM = llm
	fx.svc.cfg.LabelModel = "fixture"
	if _, err := fx.svc.saveLead("alice", leadEdit{Name: "Alice", Phone: lockedNumber, Purpose: "normal"}, "tester"); err != nil {
		t.Fatal(err)
	}
	in := labelInput{TranscriptID: "manual", Turns: []labelTurn{{Role: "user", Content: "my Civic"}}}
	if status, _ := fx.do("POST", "/api/leads/alice/labels", in, nil); status != 409 || llm.requests != 0 {
		t.Fatal("disabled task invoked model")
	}
	var task labelSchema
	if err := json.Unmarshal(defaultLabelConfig, &task); err != nil {
		t.Fatal(err)
	}
	task.Enabled = true
	if status, body := fx.do("PUT", "/api/configs/transcript-labels", task, nil); status != 200 {
		t.Fatal(body)
	}
	job := labeljob.Session{ID: "session_001", CallID: "call-123", Reason: "remote_hangup", Turns: []labeljob.Turn{{Role: "user", Content: "my Civic", StartMS: 100, EndMS: 800}}}
	if err := job.Enqueue(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-"+job.ID+".json")
	if err := fx.svc.processHangup(context.Background(), path); err == nil || llm.requests != 0 {
		t.Fatal("used unowned session")
	}
	owners := filepath.Join(fx.dir, "scheduled-owners")
	if err := os.Mkdir(owners, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owners, fmt.Sprintf("%x", sha256.Sum256([]byte(job.CallID)))), []byte("alice"), 0600); err != nil {
		t.Fatal(err)
	}
	fx.svc.runLabelJobs(context.Background())
	if llm.requests != 1 || len(fx.svc.labelJobs.pending) != 1 {
		t.Fatal("missing durable archive job")
	}
	// Restart with an unavailable Viking engine, then restore it. Only the
	// archive delivery repeats. The source is the committed original session.
	fx.api.Close()
	fx.svc.Close()
	fx.open("", "", "")
	var attempts int
	var uri string
	viking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.URL.Path != "/api/v1/content/write" || r.Header.Get("X-API-Key") != "fixture-key" {
			t.Error("wrong OpenViking request")
		}
		var req struct{ URI, Content, Mode string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		var bundle struct {
			Fields  struct{ Vehicle map[string]string }
			User    labelUser
			Session labeljob.Session `json:"original_session"`
		}
		if err := json.Unmarshal([]byte(req.Content), &bundle); err != nil || bundle.Fields.Vehicle["make_model"] != "Civic" || bundle.User.ID != "alice" || bundle.Session.Reason != job.Reason || bundle.Session.Turns[0] != job.Turns[0] {
			t.Error("incomplete Viking bundle")
		}
		if attempts == 1 {
			uri = req.URI
			w.WriteHeader(503)
			return
		}
		if uri != req.URI || req.Mode != "replace" {
			t.Error("non-idempotent retry")
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "result": map[string]string{"uri": uri}})
	}))
	defer viking.Close()
	fx.svc.cfg.VikingURL = viking.URL
	fx.svc.cfg.VikingAPIKey = "fixture-key"
	fx.svc.runLabelJobs(context.Background())
	if len(fx.svc.labelJobs.pending) != 1 {
		t.Fatal("lost failed write")
	}
	task.Enabled = false
	if status, _ := fx.do("PUT", "/api/configs/transcript-labels", task, nil); status != 200 {
		t.Fatal(status)
	}
	fx.svc.runLabelJobs(context.Background())
	if attempts != 1 {
		t.Fatal("disabled worker delivered new job")
	}
	task.Enabled = true
	if status, _ := fx.do("PUT", "/api/configs/transcript-labels", task, nil); status != 200 {
		t.Fatal(status)
	}
	fx.svc.runLabelJobs(context.Background())
	if len(fx.svc.labelJobs.pending) != 0 || attempts != 2 {
		t.Fatal("archive not acknowledged")
	}
	fx.api.Close()
	fx.svc.Close()
	fx.open("", "", "")
	if len(fx.svc.labelJobs.pending) != 0 {
		t.Fatal("replayed a delivered job")
	}
}
