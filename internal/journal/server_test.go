package journal

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// journalOver serves the fixture call, with a short recording made for it:
// the caller's track a ramp, the assistant's its negative, so that a test can
// tell the channels apart and say where in the call a sample came from.
func journalOver(t *testing.T, withAudio bool) (*httptest.Server, Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{LogPath: "testdata/call.log", DataDir: filepath.Join(dir, "data"), NotesDir: filepath.Join(dir, "notes")}
	if withAudio {
		cfg.StoreDir = filepath.Join(dir, "store")
		sess := filepath.Join(cfg.StoreDir, "sess_811ccb6206d6013bacb8559a")
		if err := os.MkdirAll(sess, 0o700); err != nil {
			t.Fatal(err)
		}
		in, out := make([]byte, 2000*2), make([]byte, 1500*2)
		for i := 0; i < 2000; i++ {
			binary.LittleEndian.PutUint16(in[i*2:], uint16(int16(i)))
		}
		for i := 0; i < 1500; i++ {
			binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(-i)))
		}
		os.WriteFile(filepath.Join(sess, "input.pcm"), in, 0o600)
		os.WriteFile(filepath.Join(sess, "output.pcm"), out, 0o600)
		os.WriteFile(filepath.Join(sess, "session.json"), []byte(`{"id":"x","rate":8000}`), 0o600)
	}
	os.MkdirAll(filepath.Join(cfg.NotesDir, "retros"), 0o700)
	ts := httptest.NewServer(NewServer(cfg).Handler())
	t.Cleanup(ts.Close)
	return ts, cfg
}

func getJSON(t *testing.T, url string, into any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if into != nil {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			t.Fatalf("%s: %v", url, err)
		}
	}
	return resp.StatusCode
}

const fixtureCall = "out-mv4kxcji-s18u"

func TestTheOverviewListsTheCallAndPoolsItsBuild(t *testing.T) {
	ts, _ := journalOver(t, false)
	var o struct {
		TargetMS int        `json:"target_ms"`
		Build    string     `json:"build"`
		Calls    []CallRow  `json:"calls"`
		Builds   []BuildRow `json:"builds"`
		Recent   Stats      `json:"recent"`
	}
	if code := getJSON(t, ts.URL+"/api/overview", &o); code != 200 {
		t.Fatalf("status %d", code)
	}
	if o.TargetMS != 1200 || o.Build != "c8a8477" || len(o.Calls) != 1 || len(o.Builds) != 1 {
		t.Fatalf("%+v", o)
	}
	row := o.Calls[0]
	if row.ID != fixtureCall || row.Summary.Turns != 21 || row.HasAudio || row.HasRetro {
		t.Errorf("row %+v", row)
	}
	if !strings.Contains(row.Worst, "synthesizer") {
		t.Errorf("the list does not say what the worst turn was laid to: %q", row.Worst)
	}
	if o.Builds[0].Summary.Turns != 21 || o.Recent.Turns != 21 {
		t.Errorf("pooled %+v, recent %+v", o.Builds[0].Summary, o.Recent)
	}
}

// A call's recording is served as one stereo file a browser can seek in:
// caller left, assistant right, the shorter track padded with silence.
func TestARecordingIsServedAsSeekableStereo(t *testing.T) {
	ts, _ := journalOver(t, true)
	resp, err := http.Get(ts.URL + "/api/calls/" + fixtureCall + "/audio.wav")
	if err != nil {
		t.Fatal(err)
	}
	whole, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(whole) != 44+2000*4 {
		t.Fatalf("status %d, %d bytes; want a 44-byte header and 2000 stereo frames", resp.StatusCode, len(whole))
	}
	if string(whole[0:4]) != "RIFF" || binary.LittleEndian.Uint16(whole[22:]) != 2 || binary.LittleEndian.Uint32(whole[24:]) != 8000 {
		t.Errorf("header says %q, %d channels, %d Hz", whole[0:4], binary.LittleEndian.Uint16(whole[22:]), binary.LittleEndian.Uint32(whole[24:]))
	}
	frame := func(b []byte, i int) (l, r int16) {
		return int16(binary.LittleEndian.Uint16(b[44+i*4:])), int16(binary.LittleEndian.Uint16(b[44+i*4+2:]))
	}
	if l, r := frame(whole, 700); l != 700 || r != -700 {
		t.Errorf("frame 700 is (%d, %d); want the caller left and the assistant right", l, r)
	}
	if l, r := frame(whole, 1800); l != 1800 || r != 0 {
		t.Errorf("frame 1800 is (%d, %d); past the end of the shorter track should be silence", l, r)
	}

	// A range that begins and ends in the middle of frames.
	req, _ := http.NewRequest("GET", ts.URL+"/api/calls/"+fixtureCall+"/audio.wav", nil)
	req.Header.Set("Range", "bytes=2847-3901")
	part, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(part.Body)
	part.Body.Close()
	if part.StatusCode != http.StatusPartialContent || string(got) != string(whole[2847:3902]) {
		t.Errorf("a range read gave status %d and %d bytes that are not the same bytes of the whole file", part.StatusCode, len(got))
	}

	var p Peaks
	getJSON(t, ts.URL+"/api/calls/"+fixtureCall+"/peaks?n=10", &p)
	if len(p.Caller) != 10 || len(p.Assistant) != 10 || p.Rate != 8000 || p.Caller[9] <= p.Caller[0] || p.Assistant[9] != 0 {
		t.Errorf("peaks %+v", p)
	}
}

// Most calls have no recording: it is something the owner turns on, and it
// expires. That is said, not served as an empty file.
func TestACallWithoutARecordingSaysSo(t *testing.T) {
	ts, _ := journalOver(t, false)
	var detail struct {
		Audio struct {
			Available bool `json:"available"`
		} `json:"audio"`
		Call Call `json:"call"`
	}
	getJSON(t, ts.URL+"/api/calls/"+fixtureCall, &detail)
	if detail.Audio.Available || detail.Call.ID != fixtureCall || len(detail.Call.Turns) != 21 {
		t.Errorf("%+v", detail.Audio)
	}
	if code := getJSON(t, ts.URL+"/api/calls/"+fixtureCall+"/audio.wav", nil); code != 404 {
		t.Errorf("audio for a call with none: status %d", code)
	}
	if code := getJSON(t, ts.URL+"/api/calls/..%2f..%2fetc", nil); code != 404 {
		t.Errorf("a call id that is a path: status %d", code)
	}
}

// What is written about a call comes from files beside the code; what the
// owner says about it is the one thing the page can write.
func TestRetrospectivesAreReadAndTheOwnersNoteIsKept(t *testing.T) {
	ts, cfg := journalOver(t, false)
	os.WriteFile(filepath.Join(cfg.NotesDir, "retros", fixtureCall+".md"), []byte("## What happened\n\nTwo slow turns.\n"), 0o600)
	os.WriteFile(filepath.Join(cfg.NotesDir, "issues.json"), []byte(`{"issues":[{"id":"ISS-001"}]}`), 0o600)

	post := func(body string) (int, Feedback) {
		resp, err := http.Post(ts.URL+"/api/calls/"+fixtureCall+"/feedback", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var f Feedback
		_ = json.NewDecoder(resp.Body).Decode(&f)
		return resp.StatusCode, f
	}
	if code, f := post(`{"rating":4,"note":"  One long silence.  "}`); code != 200 || f.Rating != 4 || f.Note != "One long silence." {
		t.Fatalf("status %d, %+v", code, f)
	}
	if code, _ := post(`{"rating":9}`); code != 400 {
		t.Errorf("a rating of 9: status %d", code)
	}
	if code, _ := post(`{"rating":3,"note":"` + strings.Repeat("字", 4001) + `"}`); code != 400 {
		t.Errorf("a note of 4001 characters: status %d", code)
	}

	var detail struct {
		Retrospective *Note     `json:"retrospective"`
		Feedback      *Feedback `json:"feedback"`
	}
	getJSON(t, ts.URL+"/api/calls/"+fixtureCall, &detail)
	if detail.Retrospective == nil || !strings.Contains(detail.Retrospective.Text, "Two slow turns") {
		t.Errorf("retrospective %+v", detail.Retrospective)
	}
	if detail.Feedback == nil || detail.Feedback.Rating != 4 {
		t.Errorf("feedback %+v", detail.Feedback)
	}
	info, err := os.Stat(filepath.Join(cfg.DataDir, "feedback", fixtureCall+".json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the note's file: %v, %v", info, err)
	}
	var issues struct {
		Issues []map[string]any `json:"issues"`
	}
	getJSON(t, ts.URL+"/api/issues", &issues)
	if len(issues.Issues) != 1 {
		t.Errorf("issues %+v", issues)
	}
	var overview struct {
		Calls []CallRow `json:"calls"`
	}
	getJSON(t, ts.URL+"/api/overview", &overview)
	if !overview.Calls[0].HasRetro || overview.Calls[0].Rating != 4 {
		t.Errorf("the list does not show the retrospective and the rating: %+v", overview.Calls[0])
	}
}

// A call outlives the log it was read from: the log is one file that grows
// and will one day be rotated or lost.
func TestACallIsKeptAfterItsLogIsGone(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "golive.log")
	data, _ := os.ReadFile("testdata/call.log")
	os.WriteFile(logPath, data, 0o600)
	cfg := Config{LogPath: logPath, DataDir: filepath.Join(dir, "data")}
	first := httptest.NewServer(NewServer(cfg).Handler())
	getJSON(t, first.URL+"/api/overview", nil)
	first.Close()
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "calls", fixtureCall+".json")); err != nil {
		t.Fatalf("the call was not written down: %v", err)
	}

	os.WriteFile(logPath, []byte("time=2026-10-13T08:00:00.000+08:00 level=INFO msg=listening addr=127.0.0.1:8765 version=fffffff\n"), 0o600)
	second := httptest.NewServer(NewServer(cfg).Handler())
	defer second.Close()
	var o struct {
		Calls []CallRow `json:"calls"`
	}
	getJSON(t, second.URL+"/api/overview", &o)
	if len(o.Calls) != 1 || o.Calls[0].ID != fixtureCall || o.Calls[0].Summary.Turns != 21 {
		t.Errorf("after the log was replaced: %+v", o.Calls)
	}
}

// One side of a call can be taken away alone: mono, the samples golive stored
// for it, on the call's timeline. It is the same file whether the recording is
// still golive's two tracks or only the archive's one — which is where every
// call ends up once golive's own copy has expired.
func TestOneSideOfARecordingIsServedAloneFromTracksAndFromTheArchive(t *testing.T) {
	store, _ := recordedFixture(t, true)
	s := archivingJournal(t, Config{StoreDir: store})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	get := func(q string) (int, []byte, http.Header) {
		resp, err := http.Get(ts.URL + "/api/calls/" + fixtureCall + "/audio.wav" + q)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b, resp.Header
	}
	sample := func(b []byte, i int) int16 { return int16(binary.LittleEndian.Uint16(b[44+i*2:])) }
	check := func(from string) (caller, assistant []byte) {
		t.Helper()
		code, caller, h := get("?side=caller&download=1")
		if code != 200 || len(caller) != 44+2000*2 {
			t.Fatalf("%s: caller side: status %d, %d bytes; want a 44-byte header and 2000 mono samples", from, code, len(caller))
		}
		if ch, rate, data := binary.LittleEndian.Uint16(caller[22:]), binary.LittleEndian.Uint32(caller[24:]), binary.LittleEndian.Uint32(caller[40:]); ch != 1 || rate != 8000 || data != 4000 {
			t.Errorf("%s: header says %d channels, %d Hz, %d bytes of data", from, ch, rate, data)
		}
		if cd := h.Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "_caller.wav") {
			t.Errorf("%s: a download of the caller's side is named %q", from, cd)
		}
		if sample(caller, 700) != 700 || sample(caller, 1800) != 1800 {
			t.Errorf("%s: the caller's side is not the caller's samples: %d, %d", from, sample(caller, 700), sample(caller, 1800))
		}
		code, assistant, _ = get("?side=assistant")
		if code != 200 || len(assistant) != len(caller) {
			t.Fatalf("%s: assistant side: status %d, %d bytes; both sides are one length", from, code, len(assistant))
		}
		if sample(assistant, 700) != -700 || sample(assistant, 1800) != 0 {
			t.Errorf("%s: the assistant's side is not the assistant's samples, padded with silence: %d, %d", from, sample(assistant, 700), sample(assistant, 1800))
		}
		// A range that begins and ends in the middle of samples.
		req, _ := http.NewRequest("GET", ts.URL+"/api/calls/"+fixtureCall+"/audio.wav?side=assistant", nil)
		req.Header.Set("Range", "bytes=1047-2900")
		part, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(part.Body)
		part.Body.Close()
		if part.StatusCode != http.StatusPartialContent || string(got) != string(assistant[1047:2901]) {
			t.Errorf("%s: a range of one side gave status %d and %d bytes that are not the same bytes of the whole", from, part.StatusCode, len(got))
		}
		return caller, assistant
	}

	// The archive is preferred once it has the call; take it away to read the
	// two tracks, then take the tracks away to read the archive.
	wav := s.archive.Path(fixtureCall, "audio.wav")
	if err := os.Rename(wav, wav+".aside"); err != nil {
		t.Fatal(err)
	}
	c1, a1 := check("golive's two tracks")
	if err := os.Rename(wav+".aside", wav); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(store); err != nil {
		t.Fatal(err)
	}
	c2, a2 := check("the archive's stereo file")
	if string(c1) != string(c2) || string(a1) != string(a2) {
		t.Error("a side read from the archive is not the side read from golive's tracks")
	}

	if code, _, _ := get("?side=both"); code != http.StatusBadRequest {
		t.Errorf("an unknown side gave status %d", code)
	}
	if code, whole, h := get("?download=1"); code != 200 || len(whole) != 44+2000*4 || strings.Contains(h.Get("Content-Disposition"), "_caller") {
		t.Errorf("the whole recording: status %d, %d bytes, named %q", code, len(whole), h.Get("Content-Disposition"))
	}
}

// A call placed to the simulated customer looks like any other to golive;
// the dials file is what tells the journal, and the page, which it was.
func TestACallToTheSimulatedCustomerIsMarkedAsSuch(t *testing.T) {
	dir := t.TempDir()
	dials := filepath.Join(dir, "dials.jsonl")
	os.WriteFile(dials, []byte(`{"at":"2026-10-12T09:34:00+08:00","call":"`+fixtureCall+`","run":"sim-0123456789abcdef01234567"}`+"\n"), 0o600)
	cfg := Config{LogPath: "testdata/call.log", DataDir: filepath.Join(dir, "data"), NotesDir: filepath.Join(dir, "notes"), SimDials: dials}
	ts := httptest.NewServer(NewServer(cfg).Handler())
	defer ts.Close()
	var o struct {
		Calls []CallRow `json:"calls"`
	}
	if code := getJSON(t, ts.URL+"/api/overview", &o); code != 200 || len(o.Calls) != 1 {
		t.Fatalf("status %d, %d calls", code, len(o.Calls))
	}
	if o.Calls[0].Kind != "simulated" || o.Calls[0].RunID != "sim-0123456789abcdef01234567" {
		t.Fatalf("row %+v", o.Calls[0])
	}
	var d struct {
		SimRun string `json:"sim_run"`
	}
	if code := getJSON(t, ts.URL+"/api/calls/"+fixtureCall, &d); code != 200 || d.SimRun != "sim-0123456789abcdef01234567" {
		t.Fatalf("detail: %d %+v", code, d)
	}
	// And without the file, the same call is a real one.
	ts2 := httptest.NewServer(NewServer(Config{LogPath: "testdata/call.log", DataDir: filepath.Join(dir, "data2"), NotesDir: filepath.Join(dir, "notes")}).Handler())
	defer ts2.Close()
	var o2 struct {
		Calls []CallRow `json:"calls"`
	}
	if getJSON(t, ts2.URL+"/api/overview", &o2); o2.Calls[0].Kind != "" {
		t.Fatalf("a real call marked: %+v", o2.Calls[0])
	}
}
