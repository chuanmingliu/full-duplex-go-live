package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chuanmingliu/golive/internal/simclient"
	"github.com/chuanmingliu/golive/internal/simeval"
	"github.com/chuanmingliu/golive/internal/simulator"
)

func inputScenario(t *testing.T) simulator.Scenario {
	t.Helper()
	raw, err := os.ReadFile("../../scenarios/simulator/cooperative.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := simulator.DecodeScenario(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestUsageDoesNotRevealArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown-secret"}, {"suite", "--unknown=private-secret"}, {"evaluate", "--unknown=private-secret"}, {"suite", "--file=x", "--output=y", "--token-env=bad\nsecret"}} {
		var stdout, stderr bytes.Buffer
		if run(context.Background(), args, &stdout, &stderr, func(string) string { return "private-secret" }) != 2 {
			t.Fatal("usage should fail")
		}
		if strings.Contains(stderr.String(), "secret") {
			t.Fatal("arguments leaked")
		}
	}
}
func TestSuitePrevalidatesAndNeverReadsJudgeCredentials(t *testing.T) {
	dir := t.TempDir()
	s := inputScenario(t)
	simclient.WritePrivate(filepath.Join(dir, "scenario.json"), s)
	simclient.WritePrivate(filepath.Join(dir, "suite.json"), simclient.Manifest{SchemaVersion: 1, ID: "suite", Repeat: 1, Scenarios: []string{"scenario.json", "missing.json"}})
	reads := 0
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"suite", "--file", filepath.Join(dir, "suite.json"), "--output", filepath.Join(dir, "output")}, &stdout, &stderr, func(string) string { reads++; return "private-secret" })
	if code != 2 || reads != 0 || stdout.Len() != 0 {
		t.Fatalf("code=%d envreads=%d", code, reads)
	}
}
func TestSuiteExitCodesAndExactReady(t *testing.T) {
	for _, semantic := range []bool{false, true} {
		t.Run(map[bool]string{false: "protocol", true: "semantic pending"}[semantic], func(t *testing.T) {
			dir := t.TempDir()
			s := inputScenario(t)
			if semantic {
				s.Expectations = []simulator.Expectation{{ID: "identity", Description: "Identify the caller."}}
			}
			simclient.WritePrivate(filepath.Join(dir, "scenario.json"), s)
			simclient.WritePrivate(filepath.Join(dir, "suite.json"), simclient.Manifest{SchemaVersion: 1, ID: "suite", Repeat: 1, Scenarios: []string{"scenario.json"}})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "POST":
					json.NewEncoder(w).Encode(simulator.Snapshot{RunID: "owned", Phase: simulator.Waiting, Binding: simulator.Binding{RunID: "owned"}, DialTargetReference: "sip:owned@simulator.invalid"})
				case strings.HasSuffix(r.URL.Path, "/report"):
					json.NewEncoder(w).Encode(simulator.Report{RunID: "owned", ScenarioID: s.ID, ScenarioHash: simulator.ScenarioHash(s), PromptHash: simulator.PromptHash(s), ExpectedOutcome: simulator.OutcomeConversation, Phase: simulator.Completed, Checks: []simulator.Check{{Name: "answered_once", Result: "pass"}, {Name: "greeting_once", Result: "pass"}, {Name: "call_limits", Result: "pass"}, {Name: "clean_termination", Result: "pass"}, {Name: "appointment_facts", Result: "indeterminate"}}})
				default:
					json.NewEncoder(w).Encode(simulator.Snapshot{RunID: "owned", Phase: simulator.Completed, Binding: simulator.Binding{RunID: "owned"}})
				}
			}))
			defer server.Close()
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{"suite", "--file", filepath.Join(dir, "suite.json"), "--url", server.URL, "--output", filepath.Join(dir, "output")}, &stdout, &stderr, func(name string) string {
				if name != "SIM_CONTROL_TOKEN" {
					t.Fatal("suite read independent judge credentials")
				}
				return "control-secret"
			})
			want := 0
			if semantic {
				want = 1
			}
			if code != want || !strings.Contains(stdout.String(), `"called_sip_uri":"sip:owned@simulator.invalid"`) {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
		})
	}
}
func TestEvaluateProducesIndependentArtifactAndPreservesReport(t *testing.T) {
	dir := t.TempDir()
	s := inputScenario(t)
	s.RetainTranscript = true
	s.Expectations = []simulator.Expectation{{ID: "identity", Description: "Identify yourself."}}
	r := simulator.Report{RunID: "owned", ScenarioID: s.ID, ScenarioHash: simulator.ScenarioHash(s), PromptHash: simulator.PromptHash(s), ExpectedOutcome: simulator.OutcomeConversation, Phase: simulator.Completed, TranscriptRetained: true}
	tr := simulator.TranscriptArtifact{SchemaVersion: 1, RunID: r.RunID, ScenarioHash: r.ScenarioHash, PromptHash: r.PromptHash, Entries: []simulator.TranscriptEntry{{Role: "customer", AtMS: 0, Text: "Who is calling?"}, {Role: "assistant", AtMS: 1, Text: "I am the appointment assistant."}}}
	sp, rp, tp, ep := filepath.Join(dir, "s.json"), filepath.Join(dir, "r.json"), filepath.Join(dir, "t.json"), filepath.Join(dir, "e.json")
	simclient.WritePrivate(sp, s)
	simclient.WritePrivate(rp, r)
	simclient.WritePrivate(tp, tr)
	original, _ := os.ReadFile(rp)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": `{"grades":[{"id":"identity","result":"pass","reason":"private-provider-secret","evidence_turns":[2]}]}`}}}})
	}))
	defer server.Close()
	env := func(name string) string {
		switch name {
		case "SIM_EVAL_BASE_URL":
			return server.URL + "/v1"
		case "SIM_EVAL_MODEL":
			return "fake-judge"
		case "SIM_EVAL_API_KEY":
			return "independent-key"
		default:
			t.Fatal("unexpected shared credential read")
			return ""
		}
	}
	var stdout, stderr bytes.Buffer
	args := []string{"evaluate", "--scenario", sp, "--report", rp, "--transcript", tp, "--output", ep}
	if code := run(context.Background(), args, &stdout, &stderr, env); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	raw, _ := os.ReadFile(ep)
	var artifact simeval.Artifact
	if simclient.StrictJSON(raw, &artifact) != nil || artifact.Verdict != "pass" || bytes.Contains(raw, []byte("private-provider-secret")) {
		t.Fatal("bad artifact")
	}
	after, _ := os.ReadFile(rp)
	if !bytes.Equal(original, after) {
		t.Fatal("raw report overwritten")
	}
	args[len(args)-1] = rp
	if code := run(context.Background(), args, &stdout, &stderr, env); code != 2 {
		t.Fatal("overwriting report accepted")
	}
	after, _ = os.ReadFile(rp)
	if !bytes.Equal(original, after) {
		t.Fatal("report modified")
	}
	alias := filepath.Join(dir, "report-alias.json")
	if os.Link(rp, alias) != nil {
		t.Fatal("cannot link")
	}
	args[len(args)-1] = alias
	if code := run(context.Background(), args, &stdout, &stderr, env); code != 2 {
		t.Fatal("report hardlink overwrite accepted")
	}
}
func TestEvaluateNoConfigurationIsIndeterminate(t *testing.T) {
	dir := t.TempDir()
	s := inputScenario(t)
	s.RetainTranscript = true
	s.Expectations = []simulator.Expectation{{ID: "identity", Description: "Identify yourself."}}
	r := simulator.Report{RunID: "owned", ScenarioID: s.ID, ScenarioHash: simulator.ScenarioHash(s), PromptHash: simulator.PromptHash(s), ExpectedOutcome: simulator.OutcomeConversation, Phase: simulator.Completed, TranscriptRetained: true}
	tr := simulator.TranscriptArtifact{SchemaVersion: 1, RunID: r.RunID, ScenarioHash: r.ScenarioHash, PromptHash: r.PromptHash, Entries: []simulator.TranscriptEntry{{Role: "customer", Text: "Hello"}, {Role: "assistant", AtMS: 1, Text: "Hello"}}}
	sp, rp, tp, ep := filepath.Join(dir, "s.json"), filepath.Join(dir, "r.json"), filepath.Join(dir, "t.json"), filepath.Join(dir, "e.json")
	simclient.WritePrivate(sp, s)
	simclient.WritePrivate(rp, r)
	simclient.WritePrivate(tp, tr)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"evaluate", "--scenario", sp, "--report", rp, "--transcript", tp, "--output", ep}, &stdout, &stderr, func(string) string { return "" }); code != 1 {
		t.Fatalf("code=%d", code)
	}
	raw, _ := os.ReadFile(ep)
	if !bytes.Contains(raw, []byte(`"verdict": "indeterminate"`)) {
		t.Fatal("missing indeterminate artifact")
	}
}

func TestEvaluateMissingInputWritesIndeterminateWithoutConfiguration(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "evaluation.json")
	var stdout, stderr bytes.Buffer
	reads := 0
	code := run(context.Background(), []string{"evaluate", "--scenario", filepath.Join(dir, "missing.json"), "--report", filepath.Join(dir, "r.json"), "--transcript", filepath.Join(dir, "t.json"), "--output", output}, &stdout, &stderr, func(string) string { reads++; return "private-secret" })
	raw, _ := os.ReadFile(output)
	var artifact simeval.Artifact
	if code != 1 || reads != 0 || simclient.StrictJSON(raw, &artifact) != nil || artifact.Verdict != "indeterminate" || artifact.Reason != "invalid_input_artifacts" || artifact.RunID != "" {
		t.Fatalf("code=%d reads=%d artifact=%+v", code, reads, artifact)
	}
}
