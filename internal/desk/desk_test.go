package desk

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T, dir string) *Service {
	t.Helper()
	s, err := Open(Config{Dir: dir, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func token(t *testing.T, dir, who string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, who+".token"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func call(t *testing.T, h http.Handler, tok, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestATokenSaysWhoYouAreAndTheOwnerAloneHoldsTheSwitches(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	h := s.Handler()
	for _, who := range []string{Owner, Claude, Codex} {
		code, me := call(t, h, token(t, dir, who), "GET", "/api/me", "")
		if code != 200 || me["who"] != who {
			t.Fatalf("%s: %d %v", who, code, me)
		}
	}
	if code, _ := call(t, h, "", "GET", "/api/me", ""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := call(t, h, strings.Repeat("x", 48), "GET", "/api/me", ""); code != 401 {
		t.Fatalf("a wrong token: %d", code)
	}
	if code, _ := call(t, h, token(t, dir, Claude), "PUT", "/api/control", `{"paused":true}`); code != 403 {
		t.Fatalf("an agent pausing: %d", code)
	}
	if code, c := call(t, h, token(t, dir, Owner), "PUT", "/api/control", `{"paused":true,"mode":"auto"}`); code != 200 || c["paused"] != true || c["mode"] != "auto" {
		t.Fatalf("the owner pausing: %d %v", code, c)
	}
}

func TestNotesThreadAndTasksAreClosedByThoseConcerned(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	h := s.Handler()
	claude, codex, owner := token(t, dir, Claude), token(t, dir, Codex), token(t, dir, Owner)
	code, task := call(t, h, claude, "POST", "/api/notes", `{"to":"codex","kind":"task","subject":"build the gateway","body":"see the route","refs":["b8aa681"]}`)
	if code != 201 || task["from"] != "claude" || task["open"] != true || task["thread"] != task["id"] {
		t.Fatalf("posting a task: %d %v", code, task)
	}
	id := task["id"].(string)
	code, reply := call(t, h, codex, "POST", "/api/notes", `{"to":"claude","kind":"result","body":"done, see run","reply_to":"`+id+`"}`)
	if code != 201 || reply["thread"] != id {
		t.Fatalf("a reply joins the thread: %d %v", code, reply)
	}
	if code, _ := call(t, h, owner, "POST", "/api/notes", `{"to":"owner","body":"x"}`); code != 400 {
		t.Fatalf("a note to oneself: %d", code)
	}
	// The owner is not the addressee and not the author, but may close anything;
	// a third agent may not close what is not theirs.
	code, _ = call(t, h, claude, "POST", "/api/notes/"+reply["id"].(string)+"/done", "{}")
	if code != 200 {
		t.Fatalf("closing a result (not a task) is harmless: %d", code)
	}
	code, v := call(t, h, codex, "POST", "/api/notes/"+id+"/done", "{}")
	if code != 200 || v["open"] != false || v["done_by"] != "codex" {
		t.Fatalf("the addressee closing: %d %v", code, v)
	}
	_, list := call(t, h, owner, "GET", "/api/notes?open=1", "")
	if n := list["notes"]; n != nil && len(n.([]any)) != 0 {
		t.Fatalf("still open: %v", n)
	}
	_, list = call(t, h, owner, "GET", "/api/notes?thread="+id, "")
	if len(list["notes"].([]any)) != 2 {
		t.Fatalf("the thread: %v", list)
	}
	_, st := call(t, h, owner, "GET", "/api/state", "")
	seen := st["seen"].(map[string]any)
	if seen["claude"] == nil || seen["codex"] == nil {
		t.Fatalf("who was heard from: %v", st)
	}
}

func TestPausedAgentsReadButDoNotWrite(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	h := s.Handler()
	owner, claude := token(t, dir, Owner), token(t, dir, Claude)
	call(t, h, owner, "PUT", "/api/control", `{"paused":true}`)
	if code, _ := call(t, h, claude, "POST", "/api/notes", `{"to":"codex","body":"hello"}`); code != 423 {
		t.Fatalf("an agent writing while paused: %d", code)
	}
	if code, _ := call(t, h, claude, "GET", "/api/notes", ""); code != 200 {
		t.Fatalf("an agent reading while paused: %d", code)
	}
	if code, _ := call(t, h, owner, "POST", "/api/notes", `{"to":"all","body":"hold on"}`); code != 201 {
		t.Fatalf("the owner writing while paused: %d", code)
	}
	call(t, h, owner, "PUT", "/api/control", `{"paused":false}`)
	if code, _ := call(t, h, claude, "POST", "/api/notes", `{"to":"codex","body":"hello"}`); code != 201 {
		t.Fatalf("after resume: %d", code)
	}
}

func TestTheLogSurvivesARestartAndNoticesAChange(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	h := s.Handler()
	owner, claude := token(t, dir, Owner), token(t, dir, Claude)
	_, task := call(t, h, claude, "POST", "/api/notes", `{"to":"owner","kind":"question","subject":"may I?"}`)
	call(t, h, owner, "PUT", "/api/control", `{"paused":true}`)
	s.Close()

	s2 := openTest(t, dir)
	if !s2.Control().Paused {
		t.Fatal("the switch did not survive")
	}
	notes := s2.Notes(Query{Open: true})
	if len(notes) != 1 || notes[0].ID != task["id"] || notes[0].From != Claude {
		t.Fatalf("after restart: %+v", notes)
	}
	s2.Close()

	// A line edited by hand breaks the chain.
	path := filepath.Join(dir, logFile)
	b, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(b), "may I?", "may I not?", 1)), 0o600)
	if _, err := Verify(dir); err == nil || !strings.Contains(err.Error(), "does not follow") {
		t.Fatalf("a changed line went unnoticed: %v", err)
	}
}

func TestTheStreamCarriesEachNoteAsItIsWritten(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+token(t, dir, Claude))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	r := bufio.NewReader(resp.Body)
	line, _ := r.ReadString('\n')
	if !strings.HasPrefix(line, "event: hello") {
		t.Fatalf("first line: %q", line)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.Post(Codex, Note{To: Claude, Kind: Status, Body: "the gateway is up"})
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data: {") && strings.Contains(line, `"from":"codex"`) && strings.Contains(line, `"kind":"status"`) {
			return
		}
	}
	t.Fatal("the note did not arrive on the stream")
}

func TestASimulatedCallIsPairedWithItsRunAndItsJournalEntry(t *testing.T) {
	// The simulator knows the run; the journal knows the call; the dials
	// file is the only place the two ids meet. A real call is not listed.
	sim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 64) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/sim/v1/runs/sim-aaaaaaaaaaaaaaaaaaaaaaaa":
			io.WriteString(w, `{"run_id":"sim-aaaaaaaaaaaaaaaaaaaaaaaa","phase":"failed"}`)
		case "/sim/v1/runs/sim-aaaaaaaaaaaaaaaaaaaaaaaa/report":
			io.WriteString(w, `{"scenario_id":"appointment-cooperative","termination_reason":"Active Call control disconnected","failures":["Active Call control disconnected"],"checks":[{"name":"answered_once","result":"pass"},{"name":"clean_termination","result":"indeterminate"}],"assistant_response_gaps":[{"value_ms":0,"valid":false},{"value_ms":910,"valid":true}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer sim.Close()
	journal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/calls/out-sim1" {
			io.WriteString(w, `{"call":{"id":"out-sim1","duration_s":19.1,"build":"abc1234","summary":{"turns":1,"p50":0,"p95":0,"over":0}},"audio":{"available":true,"duration_s":19.1}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer journal.Close()
	dir := t.TempDir()
	dials := filepath.Join(dir, "dials.jsonl")
	os.WriteFile(dials, []byte(`{"at":"2026-10-16T21:38:40+08:00","call":"out-sim1","run":"sim-aaaaaaaaaaaaaaaaaaaaaaaa"}`+"\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "tokens.env"), []byte("# tokens\nSIM_CONTROL_TOKEN="+strings.Repeat("s", 64)+"\nSIM_SIP_DOMAIN=simulator.invalid\n"), 0o600)
	c := &Calls{Dials: dials, Simulator: sim.URL, SimTokens: filepath.Join(dir, "tokens.env"), Journal: journal.URL, JournalPort: 8790}

	s := openTest(t, filepath.Join(dir, "desk"))
	s.cfg.Calls = c
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/calls", nil)
	req.Header.Set("Authorization", "Bearer "+s.tokens["owner"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		Calls       []CallRow `json:"calls"`
		JournalPort int       `json:"journal_port"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/calls: %d %v", resp.StatusCode, err)
	}
	if len(got.Calls) != 1 || got.JournalPort != 8790 {
		t.Fatalf("rows: %+v port %d", got.Calls, got.JournalPort)
	}
	r := got.Calls[0]
	if r.Call != "out-sim1" || r.Phase != "failed" || r.Scenario != "appointment-cooperative" || r.Checks["answered_once"] != "pass" || len(r.Failures) != 1 {
		t.Fatalf("simulator side: %+v", r)
	}
	if !r.Journal || r.Build != "abc1234" || r.Turns != 1 || !r.HasAudio || r.DurationS != 19.1 {
		t.Fatalf("journal side: %+v", r)
	}
	if len(r.GapsMS) != 1 || r.GapsMS[0] != 910 {
		t.Fatalf("only the valid gap counts: %v", r.GapsMS)
	}
	// Without a token the page gets nothing, like everything else here.
	resp2, _ := http.Get(srv.URL + "/api/calls")
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", resp2.StatusCode)
	}
	resp2.Body.Close()
}

func TestAScenarioIsCheckedByTheSimulatorSavedAndReservedAndDraftedFromARealCall(t *testing.T) {
	var reserved []byte
	sim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/sim/v1/validate":
			if strings.Contains(string(body), `"_drafted_from"`) {
				http.Error(w, `{"error":"unknown field"}`, http.StatusBadRequest)
				return
			}
			if strings.Contains(string(body), `"max_remote_turns":1000`) {
				http.Error(w, `{"error":"max_remote_turns out of range"}`, http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"scenario":{"id":"ok"}}`)
		case "/sim/v1/runs":
			reserved = body
			if r.Header.Get("Idempotency-Key") == "" {
				http.Error(w, "no key", http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"run_id":"sim-feedfacefeedfacefeedface","phase":"waiting","dial_target_reference":"sip:sim-feedfacefeedfacefeedface@simulator.invalid"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer sim.Close()
	journal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/overview":
			io.WriteString(w, `{"calls":[{"id":"out-real1","started_at":"2026-10-16T09:16:00+08:00","duration_s":200,"summary":{"turns":12}},{"id":"out-sim1","kind":"simulated","summary":{"turns":3}}]}`)
		case "/api/calls/out-real1":
			io.WriteString(w, `{"call":{"id":"out-real1","duration_s":200,"transcript":[{"who":"assistant","text":"a"},{"who":"caller","text":"b"},{"who":"assistant","text":"c"},{"who":"caller","text":"d"}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer journal.Close()
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test-only" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "客户：b") {
			t.Errorf("the caller's words were not sent: %s", body)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"persona\":\"P\",\"goal\":\"G\",\"behavior\":[\"b1\",\"b2\"],\"facts\":{\"plate\":\"沪X00000\"},\"greeting\":\"喂\",\"expectations\":[{\"id\":\"e1\",\"description\":\"D\"}],\"interruptions\":[\"等一下\",\"多少钱\"],\"backchannels\":[\"嗯\"]}"}}]}`)
	}))
	defer chat.Close()

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "scenarios", "simulator"), 0o700)
	os.WriteFile(filepath.Join(dir, "scenarios", "simulator", "cooperative.json"), []byte(`{"id":"appointment-cooperative","language":"zh-CN","max_remote_turns":12,"max_duration_ms":120000}`), 0o600)
	os.WriteFile(filepath.Join(dir, "providers.env"), []byte("DEEPSEEK_API_KEY=sk-test-only\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "tokens.env"), []byte("SIM_CONTROL_TOKEN="+strings.Repeat("s", 64)+"\n"), 0o600)
	calls := &Calls{Dials: filepath.Join(dir, "dials.jsonl"), Simulator: sim.URL, SimTokens: filepath.Join(dir, "tokens.env"), Journal: journal.URL, JournalPort: 8790}
	sc := &Scenarios{Dir: filepath.Join(dir, "scenarios"), LastRun: filepath.Join(dir, "data", "last-run"), ProvidersEnv: filepath.Join(dir, "providers.env"), ChatURL: chat.URL, ChatModel: "test"}
	s := openTest(t, filepath.Join(dir, "desk"))
	s.cfg.Calls, s.cfg.Scenarios = calls, sc
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	do := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+s.tokens["owner"])
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	// The list: shipped ones are there, read-only.
	if code, m := do("GET", "/api/scenarios", ""); code != 200 || len(m["scenarios"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, m)
	}
	if code, _ := do("PUT", "/api/scenarios/cooperative", `{"id":"x"}`); code != http.StatusForbidden {
		t.Fatalf("a shipped scenario was overwritten: %d", code)
	}
	// A scenario the simulator refuses is not saved.
	if code, _ := do("PUT", "/api/scenarios/long-one", `{"id":"long-one","max_remote_turns":1000}`); code != http.StatusBadRequest {
		t.Fatalf("refused scenario: %d", code)
	}
	if fileExists(filepath.Join(dir, "scenarios", "custom", "long-one.json")) {
		t.Fatal("a refused scenario was saved")
	}
	// A good one is saved, pretty, without the desk's private keys, and can be reserved.
	if code, m := do("PUT", "/api/scenarios/long-one", `{"id":"long-one","max_remote_turns":60,"_drafted_from":{"call":"out-real1"}}`); code != 200 || m["saved"] != "long-one" {
		t.Fatalf("save: %d %v", code, m)
	}
	saved, _ := os.ReadFile(filepath.Join(dir, "scenarios", "custom", "long-one.json"))
	if strings.Contains(string(saved), "_drafted_from") || !strings.Contains(string(saved), "\n  \"id\"") {
		t.Fatalf("saved file: %s", saved)
	}
	if code, m := do("POST", "/api/scenarios/long-one/prepare", "{}"); code != 200 || m["run_id"] != "sim-feedfacefeedfacefeedface" {
		t.Fatalf("prepare: %d %v", code, m)
	}
	if last, _ := os.ReadFile(filepath.Join(dir, "data", "last-run")); strings.TrimSpace(string(last)) != "sim-feedfacefeedfacefeedface" {
		t.Fatalf("last-run not written for call sim: %q", last)
	}
	if !strings.Contains(string(reserved), `"max_remote_turns": 60`) {
		t.Fatalf("the reservation did not carry the scenario: %s", reserved)
	}
	// Real calls only are offered to draft from, and a draft is synthetic and marked.
	if code, m := do("GET", "/api/scenarios/calls", ""); code != 200 || len(m["calls"].([]any)) != 1 {
		t.Fatalf("real calls: %d %v", code, m)
	}
	code, m := do("POST", "/api/scenarios/draft", `{"call":"out-real1","turns":50}`)
	if code != 200 {
		t.Fatalf("draft: %d %v", code, m)
	}
	d := m["scenario"].(map[string]any)
	if d["persona"] != "P" || d["max_remote_turns"].(float64) != 50 || d["id"] != "from-call-real1" || d["_drafted_from"] == nil || d["retain_transcript"] != true {
		t.Fatalf("draft: %v", d)
	}
	if code, _ := do("POST", "/api/scenarios/draft", `{"call":"out-sim1"}`); code != http.StatusNotFound && code != http.StatusBadRequest {
		t.Fatalf("a simulated call was drafted from: %d", code)
	}
	// An effect is actions in the caller's own words on top of the draft.
	code, m = do("POST", "/api/scenarios/draft", `{"call":"out-real1","effect":"barge_in"}`)
	d = m["scenario"].(map[string]any)
	acts, _ := d["actions"].([]any)
	if code != 200 || len(acts) != 2 || d["id"] != "from-call-real1-barge-in" {
		t.Fatalf("barge_in draft: %d %v", code, d)
	}
	if a := acts[0].(map[string]any); a["text"] != "等一下" || a["trigger"] != "remote_speech_started" || a["occurrence"].(float64) != 2 {
		t.Fatalf("first cut-in: %v", a)
	}
	if code, _ := do("POST", "/api/scenarios/draft", `{"call":"out-real1","effect":"earthquake"}`); code != http.StatusBadRequest {
		t.Fatalf("unknown effect: %d", code)
	}
	// A set: one call, plain and with two effects, and a suite that names them.
	code, m = do("POST", "/api/scenarios/synthesize", `{"calls":["out-real1"],"effects":["barge_in","busy"],"turns":30,"prefix":"set-t","repeat":2}`)
	if code != 200 || m["count"].(float64) != 3 {
		t.Fatalf("synthesize: %d %v", code, m)
	}
	for _, n := range []string{"set-t-01", "set-t-01-barge-in", "set-t-01-busy"} {
		if !fileExists(filepath.Join(dir, "scenarios", "custom", n+".json")) {
			t.Fatalf("%s not saved", n)
		}
	}
	busy, _ := os.ReadFile(filepath.Join(dir, "scenarios", "custom", "set-t-01-busy.json"))
	if !strings.Contains(string(busy), `"max_duration_ms": 90000`) || !strings.Contains(string(busy), `"kind": "hangup"`) || strings.Contains(string(busy), "_interruptions") {
		t.Fatalf("busy variant: %s", busy)
	}
	manifest, _ := os.ReadFile(filepath.Join(dir, "scenarios", "suites", "set-t.json"))
	if !strings.Contains(string(manifest), `"../custom/set-t-01-busy.json"`) || !strings.Contains(string(manifest), `"repeat": 2`) {
		t.Fatalf("suite: %s", manifest)
	}
}
