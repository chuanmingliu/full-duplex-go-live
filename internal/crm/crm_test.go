package crm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- the log ---

func TestTheLogReadsBackWhatWasWrittenAndNumbersOn(t *testing.T) {
	dir := t.TempDir()
	l, _, err := OpenLog(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := l.Append(Event{By: "tester", Type: "call.placed", Ref: fmt.Sprint("call-", i), Call: &CallRecord{Template: "t", Version: i, Vars: []string{"a"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := OpenLog(dir, nil); err == nil {
		t.Error("a second process was let into a log that is open")
	}
	l.Close()

	var seen []string
	l, torn, err := OpenLog(dir, func(ev Event) error { seen = append(seen, ev.Ref); return nil })
	if err != nil || torn != 0 {
		t.Fatalf("reopening: %v, %d torn", err, torn)
	}
	defer l.Close()
	if strings.Join(seen, ",") != "call-0,call-1,call-2,call-3,call-4" {
		t.Errorf("replayed %v", seen)
	}
	ev, err := l.Append(Event{Type: "call.placed", Ref: "call-5"})
	if err != nil || ev.Seq != 6 {
		t.Errorf("the next event is numbered %d (%v), not 6", ev.Seq, err)
	}
	if n, err := VerifyLog(dir); n != 6 || err != nil {
		t.Errorf("verify: %d events, %v", n, err)
	}
}

// A line changed, removed or added by hand breaks the chain, and the service
// will not start on it: an audit trail that can be edited is not one.
func TestALogThatHasBeenChangedIsRefused(t *testing.T) {
	write := func(t *testing.T) (string, []string) {
		dir := t.TempDir()
		l, _, err := OpenLog(dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 4; i++ {
			l.Append(Event{By: "tester", Type: "call.placed", Ref: fmt.Sprint("call-", i), Note: "as it happened"})
		}
		l.Close()
		data, _ := os.ReadFile(filepath.Join(dir, logFile))
		return dir, strings.SplitAfter(strings.TrimSuffix(string(data), "\n"), "\n")
	}
	for name, change := range map[string]func(lines []string) []string{
		"a line edited": func(l []string) []string {
			l[1] = strings.Replace(l[1], "as it happened", "as it did not", 1)
			return l
		},
		"a line removed":         func(l []string) []string { return append(l[:1], l[2:]...) },
		"a line repeated":        func(l []string) []string { return append(l[:2], l[1:]...) },
		"two lines swapped":      func(l []string) []string { l[1], l[2] = l[2], l[1]; return l },
		"the first line removed": func(l []string) []string { return l[1:] },
	} {
		dir, lines := write(t)
		for i := range lines {
			lines[i] = strings.TrimSuffix(lines[i], "\n")
		}
		changed := strings.Join(change(lines), "\n") + "\n"
		os.WriteFile(filepath.Join(dir, logFile), []byte(changed), 0o600)
		if l, _, err := OpenLog(dir, nil); err == nil {
			l.Close()
			t.Errorf("%s: the log was accepted", name)
		} else if !strings.Contains(err.Error(), "line") {
			t.Errorf("%s: the refusal does not say where: %v", name, err)
		}
		if _, err := VerifyLog(dir); err == nil {
			t.Errorf("%s: verify passed it", name)
		}
	}
}

// The one damage an append can do is leave the last line half written. That
// line was never acknowledged, so it is cut off and nothing else is lost.
func TestAHalfWrittenLastLineIsCutOff(t *testing.T) {
	dir := t.TempDir()
	l, _, _ := OpenLog(dir, nil)
	for i := 0; i < 3; i++ {
		l.Append(Event{Type: "call.placed", Ref: fmt.Sprint("call-", i)})
	}
	l.Close()
	path := filepath.Join(dir, logFile)
	whole, _ := os.ReadFile(path)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"seq":4,"at":"2026-10-13T23:00:00+08:00","by":"x","type":"call.pla`)
	f.Close()

	n := 0
	l, torn, err := OpenLog(dir, func(Event) error { n++; return nil })
	if err != nil || torn == 0 || n != 3 {
		t.Fatalf("reopening after a torn write: %v, %d bytes cut, %d events", err, torn, n)
	}
	if ev, err := l.Append(Event{Type: "call.placed", Ref: "call-3"}); err != nil || ev.Seq != 4 {
		t.Errorf("the event after the cut is numbered %d (%v)", ev.Seq, err)
	}
	l.Close()
	after, _ := os.ReadFile(path)
	if !bytes.HasPrefix(after, whole) {
		t.Error("cutting the torn line changed what was before it")
	}
	if n, err := VerifyLog(dir); n != 4 || err != nil {
		t.Errorf("verify: %d, %v", n, err)
	}
}

// --- the service ---

const testPrompt = `你是{% if not is_sh %}甲乙联合{% else %}甲方{% endif %}的客服。` +
	`{%- if carNum is defined and carNum is not none and carNum != "" -%} 车牌号:{{carNum}}， {%- else -%} 车牌号:未获取 {%- endif -%} ` +
	`{%- if vehicleAge is defined and vehicleAge is not none and vehicleAge != "" -%} 年限:{% set years = (vehicleAge | int // 12) %}{% set months = (vehicleAge | int % 12) %}{{ years }}年{{ months }}个月， {%- endif -%} ` +
	`{%- set day = nowDateTime.split(' ')[0] if nowDateTime is defined and nowDateTime is not none and ' ' in nowDateTime else nowDateTime -%} 当前时间:{{day}}， 开场白:"{{start}}"`

// fixture is a service with a stand-in originator and golive beside it.
type fixture struct {
	t        *testing.T
	svc      *Service
	api      *httptest.Server
	dir      string
	logs     *bytes.Buffer
	mu       sync.Mutex
	dials    []map[string]string
	sessions int
	refuse   bool
}

const lockedNumber = "00000001234"

func newFixture(t *testing.T) *fixture {
	t.Helper()
	fx := &fixture{t: t, dir: filepath.Join(t.TempDir(), "crm"), logs: &bytes.Buffer{}}
	originator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		fx.mu.Lock()
		defer fx.mu.Unlock()
		if fx.refuse {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"ok":false,"error":"%s is not the number this line may dial"}`, body["to"])
			return
		}
		fx.dials = append(fx.dials, body)
		fmt.Fprintf(w, `{"ok":true,"call_id":"out-test-%d","destination":"sip:%s@pbx"}`, len(fx.dials), body["to"])
	}))
	t.Cleanup(originator.Close)
	golive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fx.mu.Lock()
		defer fx.mu.Unlock()
		fmt.Fprintf(w, `{"model":"golive-1","sessions":%d,"status":"ok"}`, fx.sessions)
	}))
	t.Cleanup(golive.Close)
	env := filepath.Join(t.TempDir(), "aria.env")
	os.WriteFile(env, []byte("PORT=3000\nALLOWED_CALLEE=\""+lockedNumber+"\"\n"), 0o600)
	fx.open(originator.URL, env, golive.URL)
	return fx
}

func (fx *fixture) open(originator, env, golive string) {
	svc, err := Open(Config{Dir: fx.dir, OriginatorURL: originator, OriginatorEnv: env, GoliveURL: golive,
		Location: time.FixedZone("CST", 8*3600), Log: slog.New(slog.NewTextHandler(fx.logs, nil))})
	if err != nil {
		fx.t.Fatal(err)
	}
	fx.svc = svc
	fx.api = httptest.NewServer(svc.Handler())
	fx.t.Cleanup(func() { fx.api.Close(); svc.Close() })
}

func (fx *fixture) token() string {
	data, err := os.ReadFile(filepath.Join(fx.dir, tokenFile))
	if err != nil {
		fx.t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// do sends a request with the token and decodes a JSON answer into out.
func (fx *fixture) do(method, path string, body any, out any) (int, string) {
	fx.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		data, _ := json.Marshal(b)
		rd = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, fx.api.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+fx.token())
	req.Header.Set("X-Operator", "tester")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fx.t.Fatal(err)
	}
	defer resp.Body.Close()
	text, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(text, out); err != nil {
			fx.t.Fatalf("%s %s answered %d: %s", method, path, resp.StatusCode, text)
		}
	}
	return resp.StatusCode, string(text)
}

func TestNothingIsServedWithoutTheToken(t *testing.T) {
	fx := newFixture(t)
	fx.do("PUT", "/api/templates/car", testPrompt, nil)
	for _, c := range []struct{ method, path, auth string }{
		{"GET", "/api/templates", ""}, {"GET", "/api/templates/car", "Bearer wrong"}, {"POST", "/api/render", "Bearer "},
		{"POST", "/api/call", fx.token()}, {"GET", "/api/calls", "Basic " + fx.token()}, {"PUT", "/api/templates/x", ""},
	} {
		req, _ := http.NewRequest(c.method, fx.api.URL+c.path, strings.NewReader(`{"template":"car"}`))
		if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s with %q answered %d", c.method, c.path, c.auth, resp.StatusCode)
		}
	}
	if len(fx.dials) != 0 {
		t.Error("a call was placed without the token")
	}
	if info, _ := os.Stat(filepath.Join(fx.dir, tokenFile)); info.Mode().Perm() != 0o600 {
		t.Errorf("the token file is %v", info.Mode().Perm())
	}
	if info, _ := os.Stat(fx.dir); info.Mode().Perm() != 0o700 {
		t.Errorf("the data directory is %v", info.Mode().Perm())
	}
}

func TestATemplateIsSavedInVersionsAndRendered(t *testing.T) {
	fx := newFixture(t)
	var saved struct{ Template Template }
	if code, text := fx.do("PUT", "/api/templates/car", testPrompt, &saved); code != 200 {
		t.Fatalf("saving: %d %s", code, text)
	}
	tp := saved.Template
	if tp.Version != 1 || strings.Join(tp.Names, ",") != "is_sh,carNum,vehicleAge,nowDateTime,start" || tp.StaticChars != 2 || tp.Body != "" {
		t.Errorf("saved as %+v", tp)
	}
	// The same text again is the same version; different text is the next.
	fx.do("PUT", "/api/templates/car", testPrompt, &saved)
	if saved.Template.Version != 1 {
		t.Errorf("saving the same text made version %d", saved.Template.Version)
	}
	fx.do("PUT", "/api/templates/car?note=shorter", testPrompt+" 完。", &saved)
	if saved.Template.Version != 2 || saved.Template.Note != "shorter" {
		t.Errorf("saving new text made %+v", saved.Template)
	}

	var r Rendered
	code, text := fx.do("POST", "/api/render", map[string]any{"template": "car", "version": 1, "vars": map[string]any{
		"is_sh": true, "carNum": "沪A12345", "vehicleAge": "38", "start": "您好。", "plate": "typo"}}, &r)
	if code != 200 {
		t.Fatalf("render: %d %s", code, text)
	}
	day := time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
	want := `你是甲方的客服。车牌号:沪A12345，年限:3年2个月，当前时间:` + day + `， 开场白:"您好。"`
	if r.Instructions != want {
		t.Errorf("rendered\n %q\nwant\n %q", r.Instructions, want)
	}
	if r.Version != 1 || r.Chars != len([]rune(want)) || len(r.SHA) != 12 || strings.Join(r.Unused, ",") != "plate" || len(r.Undefined) != 0 {
		t.Errorf("render says %+v", r)
	}
	// Version 0 is the latest; a variable not supplied is reported.
	fx.do("POST", "/api/render", map[string]any{"template": "car", "vars": map[string]any{"nowDateTime": "2026-01-02 03:04:05"}}, &r)
	if r.Version != 2 || !strings.HasSuffix(r.Instructions, `当前时间:2026-01-02， 开场白:"" 完。`) || strings.Join(r.Undefined, ",") != "is_sh,carNum,vehicleAge,start" {
		t.Errorf("latest version rendered %+v", r)
	}

	// What cannot be rendered is refused where it is saved, and says where.
	if code, text := fx.do("PUT", "/api/templates/bad", "fine {% include 'x' %}", nil); code != 422 || !strings.Contains(text, "character 5") {
		t.Errorf("an unsupported template: %d %s", code, text)
	}
	if code, _ := fx.do("PUT", "/api/templates/Bad Name", "x", nil); code != 422 && code != 404 && code != 400 {
		t.Errorf("a bad name: %d", code)
	}
	if code, _ := fx.do("POST", "/api/render", map[string]any{"template": "nope"}, nil); code != 404 {
		t.Errorf("a template that is not there: %d", code)
	}
	if code, _ := fx.do("POST", "/api/render", map[string]any{"template": "car", "version": 9}, nil); code != 404 {
		t.Errorf("a version that is not there: %d", code)
	}
	// A render that fails as Jinja2 would fail says so.
	fx.do("PUT", "/api/templates/strict", "{{ months | int }}", nil)
	if code, text := fx.do("POST", "/api/render", map[string]any{"template": "strict"}, nil); code != 422 || !strings.Contains(text, "months") {
		t.Errorf("a render Jinja2 fails on: %d %s", code, text)
	}
}

func TestACallIsPlacedWithTheRenderedPromptAndRecorded(t *testing.T) {
	fx := newFixture(t)
	fx.do("PUT", "/api/templates/car", testPrompt, nil)
	vars := map[string]any{"is_sh": false, "carNum": "沪A12345", "vehicleAge": 143, "start": "您好，这里是车险客服。", "nowDateTime": "2026-10-13 23:30:00"}

	// A dry run renders, checks, and dials nothing.
	var res CallResult
	if code, text := fx.do("POST", "/api/call", map[string]any{"template": "car", "vars": vars, "dry_run": true}, &res); code != 200 || !res.OK || !res.DryRun || res.CallID != "" {
		t.Fatalf("dry run: %d %s", code, text)
	}
	if len(fx.dials) != 0 {
		t.Fatal("a dry run dialled")
	}

	code, text := fx.do("POST", "/api/call", map[string]any{"template": "car", "vars": vars, "lead": "L000007"}, &res)
	if code != 200 || !res.OK || res.CallID != "out-test-1" {
		t.Fatalf("call: %d %s", code, text)
	}
	want := `你是甲乙联合的客服。车牌号:沪A12345，年限:11年11个月，当前时间:2026-10-13， 开场白:"您好，这里是车险客服。"`
	if len(fx.dials) != 1 || fx.dials[0]["to"] != lockedNumber || fx.dials[0]["instructions"] != want || fx.dials[0]["greeting"] != "您好，这里是车险客服。" {
		t.Fatalf("the originator was sent %v", fx.dials)
	}
	if res.Render.Instructions != want || res.Greeting != "您好，这里是车险客服。" {
		t.Errorf("the answer says %+v", res)
	}
	// The exact prompt is kept and can be read back by its name.
	if code, kept := fx.do("GET", "/api/renders/"+res.Render.SHA, nil, nil); code != 200 || kept != want {
		t.Errorf("the kept prompt: %d %q", code, kept)
	}
	// The number is the originator's business: it is in no answer and no log.
	if strings.Contains(text, lockedNumber) || strings.Contains(fx.logs.String(), lockedNumber) {
		t.Error("the number dialled is in an answer or in the log")
	}
	events, _ := os.ReadFile(filepath.Join(fx.dir, logFile))
	if strings.Contains(string(events), lockedNumber) {
		t.Error("the number dialled is in the event log")
	}
	// Nor does the operational log hold what was said to whom.
	if strings.Contains(fx.logs.String(), "沪A12345") || strings.Contains(fx.logs.String(), "车险客服") {
		t.Errorf("the operational log holds a lead's details:\n%s", fx.logs.String())
	}

	// An explicit greeting wins over the variable.
	fx.do("POST", "/api/call", map[string]any{"template": "car", "vars": vars, "greeting": "喂，您好。"}, &res)
	if fx.dials[1]["greeting"] != "喂，您好。" {
		t.Errorf("greeting sent: %q", fx.dials[1]["greeting"])
	}

	// It is all still there after a restart.
	var list struct{ Calls []CallRecord }
	fx.do("GET", "/api/calls", nil, &list)
	fx.api.Close()
	fx.svc.Close()
	fx.open(fx.svc.cfg.OriginatorURL, fx.svc.cfg.OriginatorEnv, fx.svc.cfg.GoliveURL)
	var again struct{ Calls []CallRecord }
	fx.do("GET", "/api/calls", nil, &again)
	if len(list.Calls) != 2 || len(again.Calls) != 2 || again.Calls[1].CallID != "out-test-1" || again.Calls[1].Lead != "L000007" ||
		again.Calls[1].Render != list.Calls[1].Render || strings.Join(again.Calls[1].Vars, ",") != "carNum,is_sh,nowDateTime,start,vehicleAge" {
		t.Errorf("calls before the restart %+v, after %+v", list.Calls, again.Calls)
	}
	var tpl struct{ Template Template }
	if code, _ := fx.do("GET", "/api/templates/car", nil, &tpl); code != 200 || tpl.Template.Body != testPrompt {
		t.Errorf("the template after a restart: %d", code)
	}
}

func TestACallIsNotPlacedWhenItShouldNotBe(t *testing.T) {
	fx := newFixture(t)
	fx.do("PUT", "/api/templates/car", testPrompt, nil)
	body := map[string]any{"template": "car", "vars": map[string]any{"start": "您好。"}}

	fx.sessions = 1
	if code, text := fx.do("POST", "/api/call", body, nil); code != http.StatusConflict || !strings.Contains(text, "in progress") {
		t.Errorf("with a call on the line: %d %s", code, text)
	}
	fx.sessions = 0
	fx.refuse = true
	code, text := fx.do("POST", "/api/call", body, nil)
	if code != http.StatusBadGateway || strings.Contains(text, lockedNumber) {
		t.Errorf("when the originator refuses: %d %s", code, text)
	}
	fx.refuse = false
	if len(fx.dials) != 0 {
		t.Fatal("a call went out that should not have")
	}
	// The refusal is on record, without the number.
	var list struct{ Calls []CallRecord }
	fx.do("GET", "/api/calls", nil, &list)
	if len(list.Calls) != 1 || list.Calls[0].OK || list.Calls[0].Error == "" || strings.Contains(list.Calls[0].Error, lockedNumber) {
		t.Errorf("the record of the refusal: %+v", list.Calls)
	}
	// A template that renders to nothing is not a prompt.
	fx.do("PUT", "/api/templates/empty", "{% if x %}something{% endif %}", nil)
	if code, _ := fx.do("POST", "/api/call", map[string]any{"template": "empty"}, nil); code != 422 {
		t.Errorf("an empty prompt: %d", code)
	}
	// With golive not answering, nothing is dialled blind.
	fx.api.Close()
	fx.svc.Close()
	fx.open(fx.svc.cfg.OriginatorURL, fx.svc.cfg.OriginatorEnv, "http://127.0.0.1:1")
	if code, _ := fx.do("POST", "/api/call", body, nil); code != http.StatusServiceUnavailable || len(fx.dials) != 0 {
		t.Errorf("with golive down: %d, %d dials", code, len(fx.dials))
	}
}

// A template version is a file nothing rewrites. One that has been edited in
// place is not the text a past call ran on, and is refused.
func TestATemplateFileChangedOnDiskIsRefused(t *testing.T) {
	fx := newFixture(t)
	fx.do("PUT", "/api/templates/car", testPrompt, nil)
	path := fx.svc.prompts.versionPath("car", 1)
	os.WriteFile(path, []byte(testPrompt+" 被改过。"), 0o600)
	if code, text := fx.do("POST", "/api/render", map[string]any{"template": "car"}, nil); code != 500 || !strings.Contains(text, "changed on disk") {
		t.Errorf("a template edited in place: %d %s", code, text)
	}
}

// The default opening line is what a call opens with when the request gives
// neither greeting nor start, and is start when the template reads it.
func TestTheDefaultOpeningLineIsUsedWhenNoneIsGiven(t *testing.T) {
	fx := newFixture(t)
	fx.do("PUT", "/api/templates/car", testPrompt, nil)
	body := map[string]any{"template": "car", "vars": map[string]any{"carNum": "沪A12345"}}
	if code, text := fx.do("POST", "/api/call", body, nil); code != 422 || !strings.Contains(text, "opening line") {
		t.Errorf("with no opening line anywhere: %d %s", code, text)
	}
	if code, _ := fx.do("PUT", "/api/greeting", "  您好，我是小翠儿。 ", nil); code != 200 {
		t.Fatalf("setting the line: %d", code)
	}
	var r Rendered
	fx.do("POST", "/api/render", body, &r)
	if !strings.Contains(r.Instructions, `开场白:"您好，我是小翠儿。"`) || strings.Join(r.Defaulted, ",") != "start" || len(r.Undefined) != 2 {
		t.Errorf("rendered %+v", r)
	}
	var res CallResult
	fx.do("POST", "/api/call", body, &res)
	if res.Greeting != "您好，我是小翠儿。" || fx.dials[0]["greeting"] != "您好，我是小翠儿。" {
		t.Errorf("the call opened with %q", fx.dials[0]["greeting"])
	}
	// A start of its own still wins, and so does an explicit greeting.
	fx.do("POST", "/api/call", map[string]any{"template": "car", "vars": map[string]any{"start": "喂。"}}, &res)
	if fx.dials[1]["greeting"] != "喂。" || !strings.Contains(fx.dials[1]["instructions"], `开场白:"喂。"`) {
		t.Errorf("start given: %q", fx.dials[1]["greeting"])
	}
	// It survives a restart.
	fx.api.Close()
	fx.svc.Close()
	fx.open(fx.svc.cfg.OriginatorURL, fx.svc.cfg.OriginatorEnv, fx.svc.cfg.GoliveURL)
	var g struct{ Greeting string }
	fx.do("GET", "/api/greeting", nil, &g)
	if g.Greeting != "您好，我是小翠儿。" {
		t.Errorf("after a restart the line is %q", g.Greeting)
	}
}

// The quotation subtask: rules are a versioned config the service starts
// with, a quote is priced under them and recorded, and new rules are a new
// version that later quotes name.
func TestAQuoteIsPricedUnderVersionedRules(t *testing.T) {
	fx := newFixture(t)
	var list struct {
		Configs []ConfigVersion `json:"configs"`
	}
	if code, body := fx.do("GET", "/api/configs", nil, &list); code != 200 || len(list.Configs) != 1 || list.Configs[0].Name != QuoteRulesName || list.Configs[0].Version != 1 {
		t.Fatalf("configs: %d %s", code, body)
	}
	req := QuoteRequest{Config: map[string]any{"交强险": true, "车损险": true, "三者险": "200万", "座位险": true, "划痕险": false, "玻璃险": true},
		Vars: map[string]any{"vehiclePrice": "12.8万", "seats": 5, "vehicleAge": 2, "ncd": 1}, MemoryVersion: 7, Call: "out-test"}
	var q Quote
	if code, body := fx.do("POST", "/api/tools/quote", req, &q); code != 200 {
		t.Fatalf("quote: %d %s", code, body)
	}
	// 交强险 950 (no factor); 车损 128000×0.012=1536 ×0.95×0.85=1240.32; 三者 1450×0.8075=1170.88;
	// 座位 45×5=225 ×0.8075=181.69; 玻璃险 unknown → skipped.
	want := 950 + 1240.32 + 1170.88 + 181.69
	if q.Total < want-0.05 || q.Total > want+0.05 || len(q.Lines) != 4 || len(q.Skipped) != 1 || q.Skipped[0] != "玻璃险" || q.RulesVersion != 1 || q.MemoryVersion != 7 {
		t.Fatalf("quote %+v (want total %.2f)", q, want)
	}
	// An option the table does not have is an error the caller can act on.
	bad := req
	bad.Config = map[string]any{"三者险": "800万"}
	if code, body := fx.do("POST", "/api/tools/quote", bad, nil); code != 422 || !strings.Contains(body, "800万") {
		t.Fatalf("bad option: %d %s", code, body)
	}
	// New rules: a new version, used from then on.
	rules := strings.Replace(DefaultQuoteRules, `"flat": 950`, `"flat": 1000`, 1)
	var saved struct {
		Config ConfigVersion `json:"config"`
	}
	if code, body := fx.do("PUT", "/api/configs/"+QuoteRulesName+"?note=test", rules, &saved); code != 200 || saved.Config.Version != 2 {
		t.Fatalf("save: %d %s", code, body)
	}
	if code, body := fx.do("PUT", "/api/configs/"+QuoteRulesName, `{"not":"rules"}`, nil); code != 422 {
		t.Fatalf("bad rules accepted: %d %s", code, body)
	}
	req.Config = map[string]any{"交强险": true}
	if code, _ := fx.do("POST", "/api/tools/quote", req, &q); code != 200 || q.Total != 1000 || q.RulesVersion != 2 {
		t.Fatalf("under v2: %+v", q)
	}
	req.RulesVersion = 1
	if code, _ := fx.do("POST", "/api/tools/quote", req, &q); code != 200 || q.Total != 950 || q.RulesVersion != 1 {
		t.Fatalf("under v1: %+v", q)
	}
	// Every quote is in the log, choices only.
	n, err := VerifyLog(fx.dir)
	if err != nil {
		t.Fatal(err)
	}
	if n < 5 {
		t.Fatalf("%d events; expected the rules, the quotes and the new rules", n)
	}
}

// golive's tool executor posts the call's working memory; the plan under
// "quote" is the coverage selection and the rest are the variables.
func TestAQuoteFromTheAssistantsWorkingMemory(t *testing.T) {
	fx := newFixture(t)
	call := map[string]any{"tool": "quote_total", "call_id": "c1", "session": "sess_x", "arguments": map[string]any{},
		"memory":         map[string]any{"quote": map[string]any{"交强险": true, "三者险": "200万"}, "vehiclePrice": "12.8万", "vehicleAge": 2, "ncd": 1, "seats": 5},
		"memory_version": 4}
	var q Quote
	if code, body := fx.do("POST", "/api/tools/quote", call, &q); code != 200 {
		t.Fatalf("quote: %d %s", code, body)
	}
	want := 950 + 1450*0.95*0.85
	if q.Total < want-0.05 || q.Total > want+0.05 || q.MemoryVersion != 4 || len(q.Lines) != 2 {
		t.Fatalf("quote %+v want %.2f", q, want)
	}
	// Arguments lay over the memory.
	call["arguments"] = map[string]any{"config": map[string]any{"三者险": "300万"}}
	if code, _ := fx.do("POST", "/api/tools/quote", call, &q); code != 200 || q.Lines[len(q.Lines)-1].Choice != "300万" && q.Lines[0].Choice != "300万" {
		t.Fatalf("override: %+v", q)
	}
	// Nothing settled yet: a clear error for the model.
	call["memory"] = map[string]any{}
	call["arguments"] = map[string]any{}
	if code, body := fx.do("POST", "/api/tools/quote", call, nil); code != 400 || !strings.Contains(body, "quote") {
		t.Fatalf("empty: %d %s", code, body)
	}
}
