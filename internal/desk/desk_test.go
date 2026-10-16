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
