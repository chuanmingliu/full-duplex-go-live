package simclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chuanmingliu/golive/internal/simulator"
)

func scenario(t *testing.T) simulator.Scenario {
	t.Helper()
	f, err := os.Open("../../scenarios/simulator/cooperative.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, err := simulator.DecodeScenario(f)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func validReport(s simulator.Scenario, id string) simulator.Report {
	return simulator.Report{RunID: id, ScenarioID: s.ID, ScenarioHash: simulator.ScenarioHash(s), PromptHash: simulator.PromptHash(s), ExpectedOutcome: expectedOutcome(s), Phase: simulator.Completed, Checks: []simulator.Check{{Name: "answered_once", Result: "pass"}, {Name: "greeting_once", Result: "pass"}, {Name: "call_limits", Result: "pass"}, {Name: "clean_termination", Result: "pass"}, {Name: "appointment_facts", Result: "indeterminate"}}}
}
func TestManifestStrictAndPrevalidates(t *testing.T) {
	dir := t.TempDir()
	raw, _ := json.Marshal(scenario(t))
	if err := os.WriteFile(filepath.Join(dir, "s.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, manifest string
		valid          bool
	}{
		{"valid", `{"schema_version":1,"id":"suite","repeat":2,"scenarios":["s.json"]}`, true},
		{"duplicate", `{"schema_version":1,"id":"suite","id":"other","repeat":1,"scenarios":["s.json"]}`, false},
		{"unknown", `{"schema_version":1,"id":"suite","repeat":1,"scenarios":["s.json"],"hook":"x"}`, false},
		{"case", `{"schema_version":1,"ID":"suite","repeat":1,"scenarios":["s.json"]}`, false},
		{"zero", `{"schema_version":1,"id":"suite","repeat":0,"scenarios":["s.json"]}`, false},
		{"repeat", `{"schema_version":1,"id":"suite","repeat":11,"scenarios":["s.json"]}`, false},
		{"empty", `{"schema_version":1,"id":"suite","repeat":1,"scenarios":[]}`, false},
		{"missing later", `{"schema_version":1,"id":"suite","repeat":1,"scenarios":["s.json","missing.json"]}`, false},
		{"trailing", `{"schema_version":1,"id":"suite","repeat":1,"scenarios":["s.json"]} {}`, false},
		{"duplicate scenario", `{"schema_version":1,"id":"suite","repeat":1,"scenarios":["s.json","s.json"]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "suite.json")
			os.WriteFile(path, []byte(tc.manifest), 0600)
			_, err := LoadSuite(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
	var manifest Manifest
	names := make([]string, 11)
	for i := range names {
		names[i] = "s.json"
	}
	manifest = Manifest{1, "suite", 10, names}
	raw, _ = json.Marshal(manifest)
	os.WriteFile(filepath.Join(dir, "suite.json"), raw, 0600)
	if _, err := LoadSuite(filepath.Join(dir, "suite.json")); err == nil {
		t.Fatal("run cap accepted")
	}
}
func TestEndpointAndStrictJSON(t *testing.T) {
	for _, base := range []string{"http://127.0.0.1:18865", "http://[::1]:18865", "http://localhost:18865", "https://control.example"} {
		if _, err := New(base, "private-token"); err != nil {
			t.Errorf("rejected %s: %v", base, err)
		}
	}
	for _, base := range []string{"http://control.example", "http://127.0.0.1.example", "ftp://localhost", "https://user:pass@control.example", "https://control.example?key=secret", "https://control.example#secret", "https://control.example/sim/v1", "https://control.example?"} {
		if _, err := New(base, "private-token"); err == nil {
			t.Errorf("accepted %s", base)
		}
	}
	for _, raw := range []string{`{"run_id":"a","run_id":"b"}`, `{"RUN_ID":"a"}`, `{"binding":{"run_id":"a","run_id":"b"}}`, `{} {}`, `{"extra":1}`} {
		var s simulator.Snapshot
		if StrictJSON([]byte(raw), &s) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

type readyWriter struct {
	bytes.Buffer
	flushed int
	onWrite func(Ready)
}

func (w *readyWriter) Write(raw []byte) (int, error) {
	var r Ready
	if json.Unmarshal(raw, &r) != nil {
		return 0, fmt.Errorf("not JSON ready")
	}
	if w.onWrite != nil {
		w.onWrite(r)
	}
	return w.Buffer.Write(raw)
}
func (w *readyWriter) Flush() error { w.flushed++; return nil }
func TestSequentialSuiteAndPrivateSanitizedArtifacts(t *testing.T) {
	s := scenario(t)
	other := s
	other.ID = "other"
	var mu sync.Mutex
	var events []string
	var keys []string
	current := ""
	prepared := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer private-token" {
			t.Error("missing authentication")
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/sim/v1/runs":
			if current != "" {
				t.Error("overlapping reservations")
			}
			prepared++
			current = fmt.Sprintf("run-%d", prepared)
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			events = append(events, "prepare:"+current)
			json.NewEncoder(w).Encode(simulator.Snapshot{RunID: current, Binding: simulator.Binding{RunID: current}, Phase: simulator.Waiting, DialTargetReference: "sip:<run_id>@simulator.invalid"})
		case strings.HasSuffix(r.URL.Path, "/report"):
			events = append(events, "report:"+current)
			sc := s
			if prepared > 2 {
				sc = other
			}
			rep := validReport(sc, current)
			rep.Checks[0].Evidence = "private-token"
			rep.ProviderModel = "private-token"
			rep.TerminationReason = "private-token"
			rep.Failures = []string{}
			json.NewEncoder(w).Encode(rep)
			current = ""
		default:
			events = append(events, "terminal:"+current)
			json.NewEncoder(w).Encode(simulator.Snapshot{RunID: current, Binding: simulator.Binding{RunID: current}, Phase: simulator.Completed})
		}
	}))
	defer server.Close()
	client, _ := New(server.URL, "private-token")
	writer := &readyWriter{onWrite: func(r Ready) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, "ready:"+r.RunID)
		if r.CalledSIPURI != "sip:"+current+"@simulator.invalid" {
			t.Error("wrong exact target")
		}
	}}
	output := filepath.Join(t.TempDir(), "private", "nested")
	runner := Runner{Client: client, Ready: writer, Output: output}
	summary, err := runner.Run(context.Background(), Suite{Manifest: Manifest{1, "suite", 2, nil}, Scenarios: []simulator.Scenario{s, other}})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Complete || summary.Counts.Pass != 4 || summary.SemanticCounts.NotRequested != 4 || writer.flushed != 4 {
		t.Fatalf("summary=%+v flushes=%d", summary, writer.flushed)
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if key == "" || seen[key] {
			t.Fatal("nonunique key")
		}
		seen[key] = true
	}
	for i := 0; i < 4; i++ {
		want := []string{fmt.Sprintf("prepare:run-%d", i+1), fmt.Sprintf("ready:run-%d", i+1), fmt.Sprintf("terminal:run-%d", i+1), fmt.Sprintf("report:run-%d", i+1)}
		for j, e := range want {
			if events[i*4+j] != e {
				t.Fatalf("events=%v", events)
			}
		}
	}
	entries, _ := os.ReadDir(output)
	for _, e := range entries {
		info, _ := e.Info()
		if info.Mode().Perm() != 0600 {
			t.Fatal("artifact permissions")
		}
		raw, _ := os.ReadFile(filepath.Join(output, e.Name()))
		if bytes.Contains(raw, []byte("private-token")) {
			t.Fatal("secret persisted")
		}
	}
	info, _ := os.Stat(output)
	if info.Mode().Perm() != 0700 {
		t.Fatal("directory permissions")
	}
}
func TestPreparationFailuresNoRetryOrStop(t *testing.T) {
	for _, mode := range []string{"401", "409", "malformed", "oversized", "unsafe-id", "wrong-binding", "redirect", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			var targetRequests atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetRequests.Add(1) }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				switch mode {
				case "401":
					http.Error(w, "private-token", 401)
				case "409":
					http.Error(w, "private-token", 409)
				case "malformed":
					w.Write([]byte(`{"run_id":`))
				case "oversized":
					w.Write(bytes.Repeat([]byte("x"), MaxArtifactBytes+1))
				case "unsafe-id":
					json.NewEncoder(w).Encode(simulator.Snapshot{RunID: "../foreign", Binding: simulator.Binding{RunID: "../foreign"}})
				case "wrong-binding":
					json.NewEncoder(w).Encode(simulator.Snapshot{RunID: "foreign", Binding: simulator.Binding{RunID: "different"}})
				case "redirect":
					http.Redirect(w, r, target.URL, 307)
				case "timeout":
					time.Sleep(60 * time.Millisecond)
				}
			}))
			defer server.Close()
			client, _ := New(server.URL, "private-token")
			client.RequestTimeout = 20 * time.Millisecond
			writer := &readyWriter{}
			runner := Runner{Client: client, Ready: writer, Output: t.TempDir()}
			_, err := runner.Run(context.Background(), Suite{Manifest: Manifest{1, "suite", 2, nil}, Scenarios: []simulator.Scenario{scenario(t)}})
			if err == nil || strings.Contains(err.Error(), "private-token") || requests.Load() != 1 || targetRequests.Load() != 0 || writer.Len() != 0 {
				t.Fatalf("err=%v requests=%d redirects=%d ready=%s", err, requests.Load(), targetRequests.Load(), writer.String())
			}
		})
	}
}
func TestCancellationStopsOnlyOwnedRun(t *testing.T) {
	s := scenario(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/sim/v1/runs" {
			json.NewEncoder(w).Encode(simulator.Snapshot{RunID: "owned", Binding: simulator.Binding{RunID: "owned"}, Phase: simulator.Waiting, DialTargetReference: "sip:<run_id>@simulator.invalid"})
		} else if r.URL.Path == "/sim/v1/runs/owned/stop" {
			w.Write([]byte(`{}`))
		} else {
			t.Error("unexpected request")
		}
	}))
	defer server.Close()
	client, _ := New(server.URL, "private-token")
	runner := Runner{Client: client, Ready: &readyWriter{onWrite: func(Ready) { cancel() }}, Output: t.TempDir()}
	summary, err := runner.Run(ctx, Suite{Manifest: Manifest{1, "suite", 2, nil}, Scenarios: []simulator.Scenario{s}})
	if err == nil || len(paths) != 2 || paths[1] != "POST /sim/v1/runs/owned/stop" || summary.Complete {
		t.Fatalf("paths=%v summary=%+v err=%v", paths, summary, err)
	}
}
func TestMismatchedResponseAbortsSuite(t *testing.T) {
	for _, mode := range []string{"snapshot-id", "report-id", "report-hash"} {
		t.Run(mode, func(t *testing.T) {
			s := scenario(t)
			creates := 0
			stops := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/sim/v1/runs":
					creates++
					json.NewEncoder(w).Encode(simulator.Snapshot{RunID: "owned", Binding: simulator.Binding{RunID: "owned"}, Phase: simulator.Waiting, DialTargetReference: "sip:<run_id>@simulator.invalid"})
				case strings.HasSuffix(r.URL.Path, "/stop"):
					stops++
					w.Write([]byte(`{}`))
				case strings.HasSuffix(r.URL.Path, "/report"):
					rep := validReport(s, "owned")
					if mode == "report-id" {
						rep.RunID = "../foreign"
					} else {
						rep.ScenarioHash = "mismatch"
					}
					json.NewEncoder(w).Encode(rep)
				default:
					id := "owned"
					if mode == "snapshot-id" {
						id = "foreign"
					}
					json.NewEncoder(w).Encode(simulator.Snapshot{RunID: id, Binding: simulator.Binding{RunID: id}, Phase: simulator.Completed})
				}
			}))
			defer server.Close()
			client, _ := New(server.URL, "private-token")
			output := t.TempDir()
			runner := Runner{Client: client, Ready: &readyWriter{}, Output: output}
			_, err := runner.Run(context.Background(), Suite{Manifest: Manifest{1, "suite", 2, nil}, Scenarios: []simulator.Scenario{s}})
			if err == nil || creates != 1 {
				t.Fatal("did not stop suite")
			}
			if mode == "snapshot-id" && stops != 1 {
				t.Fatal("owned run not cleaned up")
			}
			entries, _ := os.ReadDir(output)
			if len(entries) != 1 {
				t.Fatal("mismatched report persisted")
			}
		})
	}
}
func TestProtocolSeparateFromSemanticsAndActions(t *testing.T) {
	s := scenario(t)
	rep := validReport(s, "run")
	if ProtocolVerdict(s, rep) != "pass" {
		t.Fatal("legacy semantic placeholders affected protocol")
	}
	s.Actions = []simulator.Action{{ID: "action"}}
	if ProtocolVerdict(s, rep) != "indeterminate" {
		t.Fatal("missing action passed")
	}
	rep.Actions = []simulator.ActionOutcome{{ID: "action", Result: "failed"}}
	if ProtocolVerdict(s, rep) != "fail" {
		t.Fatal("failed action passed")
	}
	s.Actions = nil
	s.ExpectedOutcome = simulator.OutcomeCancelBeforeAnswer
	rep.Checks = nil
	for _, name := range []string{"native_cancel_before_answer", "no_answer_or_greeting", "no_speech", "no_run_failures", "call_limits", "clean_termination"} {
		rep.Checks = append(rep.Checks, simulator.Check{Name: name, Result: "pass"})
	}
	if ProtocolVerdict(s, rep) != "pass" {
		t.Fatal("cancel protocol rejected")
	}
}
func TestRunWaitBound(t *testing.T) {
	s := scenario(t)
	stopped := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stop") {
			stopped = true
			w.Write([]byte(`{}`))
			return
		}
		json.NewEncoder(w).Encode(simulator.Snapshot{RunID: "owned", Binding: simulator.Binding{RunID: "owned"}, Phase: simulator.Waiting, DialTargetReference: "sip:<run_id>@simulator.invalid"})
	}))
	defer server.Close()
	c, _ := New(server.URL, "private-token")
	r := Runner{Client: c, Ready: &readyWriter{}, Output: t.TempDir(), RunWait: 20 * time.Millisecond, PollInterval: time.Millisecond}
	_, err := r.Run(context.Background(), Suite{Manifest: Manifest{1, "suite", 1, nil}, Scenarios: []simulator.Scenario{s}})
	if err == nil || !stopped {
		t.Fatal("timeout did not stop owned run")
	}
}

func TestAmbiguousReservationBodyIsNotReplayed(t *testing.T) {
	var creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(simulator.Snapshot{RunID: "warm", Binding: simulator.Binding{RunID: "warm"}, Phase: simulator.Completed})
			return
		}
		creates.Add(1)
		// Simulate a server that accepted preparation but lost the response on a
		// reused HTTP connection. A retry could create or expose another reservation.
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	client, _ := New(server.URL, "private-token")
	if _, err := client.Get(context.Background(), "warm"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Prepare(context.Background(), "unique-key", scenario(t)); err == nil || creates.Load() != 1 {
		t.Fatalf("ambiguous preparation replayed: count=%d err=%v", creates.Load(), err)
	}
}

func TestSanitizedActionLifecyclePreservesRequested(t *testing.T) {
	s := scenario(t)
	s.Actions = []simulator.Action{{ID: "speak-once", Trigger: "connected", Occurrence: 1, Kind: "speak", Text: "Synthetic reminder", MaxHoldMS: 1000}}
	report := validReport(s, "owned")
	report.Actions = []simulator.ActionOutcome{
		{ID: "speak-once", Result: "requested", AtMS: 10, Reason: "private scheduler detail"},
		{ID: "speak-once", Result: "started", AtMS: 20, Reason: "private engine detail"},
		{ID: "speak-once", Result: "completed", AtMS: 30, Reason: "private completion detail"},
	}
	if verdict := ProtocolVerdict(s, report); verdict != "pass" {
		t.Fatalf("completed raw action verdict=%s", verdict)
	}
	sanitized := sanitizeReport(s, report)
	if len(sanitized.Actions) != 3 {
		t.Fatalf("lost action lifecycle: %+v", sanitized.Actions)
	}
	for i, want := range []string{"requested", "started", "completed"} {
		got := sanitized.Actions[i]
		if got.ID != "speak-once" || got.Result != want || got.AtMS != int64((i+1)*10) || got.Reason != "" {
			t.Fatalf("action %d=%+v; expected preserved %s state/time with private reason removed", i, got, want)
		}
	}
	if verdict := ProtocolVerdict(s, sanitized); verdict != "pass" {
		t.Fatalf("sanitization changed passing lifecycle to %s", verdict)
	}
}
