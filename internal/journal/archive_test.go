package journal

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recordedFixture is a store holding a recording for the fixture call — the
// caller's track a ramp, the assistant's its negative — which golive has
// finished writing or not.
func recordedFixture(t *testing.T, closed bool) (storeDir, sessionDir string) {
	t.Helper()
	storeDir = filepath.Join(t.TempDir(), "store")
	sessionDir = filepath.Join(storeDir, "sess_811ccb6206d6013bacb8559a")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	in, out := make([]byte, 2000*2), make([]byte, 1500*2)
	for i := 0; i < 2000; i++ {
		binary.LittleEndian.PutUint16(in[i*2:], uint16(int16(i)))
	}
	for i := 0; i < 1500; i++ {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(-i)))
	}
	record := `{"id":"x","rate":8000}`
	if closed {
		record = `{"id":"x","rate":8000,"closed_at":"2026-10-12T09:37:41+08:00"}`
	}
	for name, data := range map[string][]byte{"input.pcm": in, "output.pcm": out, "session.json": []byte(record)} {
		if err := os.WriteFile(filepath.Join(sessionDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return storeDir, sessionDir
}

func archivingJournal(t *testing.T, cfg Config) *Server {
	t.Helper()
	if cfg.LogPath == "" {
		cfg.LogPath = "testdata/call.log"
	}
	if cfg.ArchiveDir == "" {
		cfg.ArchiveDir = filepath.Join(t.TempDir(), "archive")
	}
	s := NewServer(cfg)
	s.refresh()
	return s
}

func again(s *Server) {
	s.mu.Lock()
	s.checked = time.Time{}
	s.mu.Unlock()
	s.refresh()
}

// A finished call is archived whole, without anyone opening the page: the
// recording as one stereo file, the conversation to read, everything measured,
// golive's own record of the session, and the call's own lines of the log.
func TestAFinishedCallIsArchivedWhole(t *testing.T) {
	store, _ := recordedFixture(t, true)
	s := archivingJournal(t, Config{StoreDir: store})

	e := s.archive.Entry(fixtureCall)
	if e == nil {
		t.Fatal("the call was not archived")
	}
	if !e.Audio || e.Turns != 21 || e.P50 != 806 || e.Over != 2 {
		t.Errorf("manifest: audio %v, %d turns, p50 %d, %d over; want audio, 21, 806, 2", e.Audio, e.Turns, e.P50, e.Over)
	}
	if want := filepath.Join("calls", "2026", "10", "2026-10-12_093435_"+fixtureCall); e.Dir != want {
		t.Errorf("archived in %s; want %s, so that a listing is in order", e.Dir, want)
	}
	dir := filepath.Join(s.archive.Dir(), e.Dir)
	for _, name := range []string{"audio.wav", "transcript.txt", "call.json", "session.json", "golive.log", "manifest.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s is not in the archive", name)
			continue
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s is mode %o; a call is its owner's alone", name, info.Mode().Perm())
		}
	}

	// The recording is the one the journal plays: stereo, caller left.
	wav, err := os.ReadFile(filepath.Join(dir, "audio.wav"))
	if err != nil || len(wav) != 44+2000*4 {
		t.Fatalf("audio.wav: %d bytes, %v; want a header and 2000 stereo frames", len(wav), err)
	}
	frame := wav[44+1200*4:]
	if l, r := int16(binary.LittleEndian.Uint16(frame)), int16(binary.LittleEndian.Uint16(frame[2:])); l != 1200 || r != -1200 {
		t.Errorf("frame 1200 is (%d, %d); want the caller's 1200 on the left and the assistant's -1200 on the right", l, r)
	}

	// The slice of the log is this call's lines and can be read again.
	logSlice, _ := os.ReadFile(filepath.Join(dir, "golive.log"))
	whole, _ := os.ReadFile("testdata/call.log")
	if len(logSlice) == 0 || !bytes.Contains(whole, logSlice) {
		t.Fatalf("golive.log (%d bytes) is not a run of lines from the log", len(logSlice))
	}
	ix := NewIndexer()
	if err := ix.ReadLog(bytes.NewReader(logSlice)); err != nil {
		t.Fatal(err)
	}
	if calls := ix.Calls(); len(calls) != 1 || calls[0].Summary.Turns != 21 || calls[0].Summary.P95 != 1285 {
		t.Errorf("read again from its archived lines the call is %d call(s); want the same 21 turns and p95 1285", len(calls))
	}

	// And the conversation is there to read, in order, with its waits.
	text, _ := os.ReadFile(filepath.Join(dir, "transcript.txt"))
	lines := strings.Split(strings.TrimSpace(string(text)), "\n")
	said := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "[") {
			said++
		}
	}
	if said != e.Lines || said < 40 {
		t.Errorf("transcript.txt has %d lines of speech; the call has %d", said, e.Lines)
	}
	if !strings.Contains(string(text), "(opening line)") || !strings.Contains(string(text), "(turn 1, after ") {
		t.Errorf("transcript.txt does not mark the opening line and the first answer's wait:\n%s", strings.Join(lines[:min(len(lines), 6)], "\n"))
	}

	if files, _, problems := s.archive.Verify(); len(problems) != 0 || files != 5 {
		t.Errorf("verify: %d files, problems %v; want 5 files and none", files, problems)
	}
}

// The archive outlives everything it was made from: golive's store expiring
// the recording, the log going, the journal's own copy going, a restart.
func TestAnArchivedCallOutlivesItsSources(t *testing.T) {
	store, _ := recordedFixture(t, true)
	work := t.TempDir()
	logPath := filepath.Join(work, "golive.log")
	whole, _ := os.ReadFile("testdata/call.log")
	os.WriteFile(logPath, whole, 0o600)
	cfg := Config{LogPath: logPath, StoreDir: store, DataDir: filepath.Join(work, "data"), ArchiveDir: filepath.Join(work, "archive")}
	archivingJournal(t, cfg)

	for _, gone := range []string{store, logPath, cfg.DataDir} {
		if err := os.RemoveAll(gone); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(NewServer(cfg).Handler())
	defer ts.Close()

	var d struct {
		Call  Call `json:"call"`
		Audio struct {
			Available bool    `json:"available"`
			DurationS float64 `json:"duration_s"`
			Rate      int     `json:"rate"`
		} `json:"audio"`
		Archive struct {
			Archived bool   `json:"archived"`
			Audio    bool   `json:"audio"`
			Dir      string `json:"dir"`
		} `json:"archive"`
	}
	if code := getJSON(t, ts.URL+"/api/calls/"+fixtureCall, &d); code != 200 {
		t.Fatalf("the call is gone with its sources: HTTP %d", code)
	}
	if len(d.Call.Turns) != 21 || len(d.Call.Transcript) < 40 {
		t.Errorf("%d turns and %d lines came back; want the whole call", len(d.Call.Turns), len(d.Call.Transcript))
	}
	if !d.Audio.Available || d.Audio.Rate != 8000 || d.Audio.DurationS != 0.25 {
		t.Errorf("audio %+v; want the quarter second at 8 kHz that was recorded", d.Audio)
	}
	if !d.Archive.Archived || !d.Archive.Audio || !strings.HasPrefix(d.Archive.Dir, cfg.ArchiveDir) {
		t.Errorf("archive %+v", d.Archive)
	}

	// It plays, and a browser can seek in it.
	req, _ := http.NewRequest("GET", ts.URL+"/api/calls/"+fixtureCall+"/audio.wav", nil)
	req.Header.Set("Range", "bytes=4844-4847") // frame 1200
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || len(body) != 4 {
		t.Fatalf("a range of the archived recording: HTTP %d, %d bytes", resp.StatusCode, len(body))
	}
	if l, r := int16(binary.LittleEndian.Uint16(body)), int16(binary.LittleEndian.Uint16(body[2:])); l != 1200 || r != -1200 {
		t.Errorf("frame 1200 of the archived recording is (%d, %d)", l, r)
	}
	var peaks Peaks
	if code := getJSON(t, ts.URL+"/api/calls/"+fixtureCall+"/peaks?n=10", &peaks); code != 200 || len(peaks.Caller) != 10 {
		t.Fatalf("peaks: HTTP %d, %d slots", code, len(peaks.Caller))
	}
	if peaks.Caller[9] <= peaks.Caller[0] || peaks.Assistant[9] != 0 || peaks.Assistant[5] == 0 {
		t.Errorf("peaks caller %v assistant %v; want the caller's ramp rising and the assistant's shorter track ending early", peaks.Caller, peaks.Assistant)
	}

	// And the conversation can be taken away as a file.
	resp, err = http.Get(ts.URL + "/api/calls/" + fixtureCall + "/transcript.txt?download=1")
	if err != nil {
		t.Fatal(err)
	}
	text, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Content-Disposition"), "2026-10-12_093435_"+fixtureCall+".txt") || !strings.Contains(string(text), "(opening line)") {
		t.Errorf("transcript download: disposition %q, %d bytes", resp.Header.Get("Content-Disposition"), len(text))
	}
}

// golive writes a recording for as long as the call lasts and says so when it
// has finished. A recording still being written is not archived — half a call
// kept for good is worse than none — and is picked up when it is closed.
func TestARecordingIsArchivedOnlyOnceGoliveHasClosedIt(t *testing.T) {
	store, sess := recordedFixture(t, false)
	s := archivingJournal(t, Config{StoreDir: store})

	e := s.archive.Entry(fixtureCall)
	if e == nil || e.Audio {
		t.Fatalf("with the recording still open the call's text should be archived and its audio not: %+v", e)
	}
	if !s.archiveDue {
		t.Fatal("the journal has stopped waiting for the recording to close")
	}
	os.WriteFile(filepath.Join(sess, "session.json"), []byte(`{"id":"x","rate":8000,"closed_at":"2026-10-12T09:37:41+08:00"}`), 0o600)
	again(s)
	if e := s.archive.Entry(fixtureCall); e == nil || !e.Audio {
		t.Fatalf("the recording was closed and is still not archived: %+v", e)
	}
	if s.archiveDue {
		t.Error("nothing is left to archive, and the journal is still making passes")
	}
}

// A call made with recording off is archived as text, and nothing is lost by
// that: the manifest says there is no audio.
func TestACallWithoutARecordingIsArchivedAsText(t *testing.T) {
	s := archivingJournal(t, Config{})
	e := s.archive.Entry(fixtureCall)
	if e == nil || e.Audio || e.Lines < 40 {
		t.Fatalf("manifest %+v; want the call, without audio, with its conversation", e)
	}
	if _, err := os.Stat(s.archive.Path(fixtureCall, "audio.wav")); err == nil {
		t.Error("an audio.wav was written for a call that was not recorded")
	}
	if s.archiveDue {
		t.Error("the journal is waiting for a recording that was never made")
	}
}

// What is archived is checked against what was archived: a file that has
// changed or gone is named.
func TestVerifyNamesAFileThatHasChanged(t *testing.T) {
	store, _ := recordedFixture(t, true)
	s := archivingJournal(t, Config{StoreDir: store})
	wav := s.archive.Path(fixtureCall, "audio.wav")
	data, _ := os.ReadFile(wav)
	data[len(data)-1] ^= 0xff
	os.WriteFile(wav, data, 0o600)
	os.Remove(s.archive.Path(fixtureCall, "session.json"))

	_, _, problems := s.archive.Verify()
	got := map[string]string{}
	for _, p := range problems {
		got[p.File] = p.What
	}
	if len(problems) != 2 || !strings.Contains(got["audio.wav"], "changed") || got["session.json"] != "missing" {
		t.Errorf("verify found %v; want the changed recording and the missing record, and nothing else", problems)
	}
}

// Audio is written once. A second pass, or a restart, or the journal reading
// the call differently, must never rewrite a recording that is already kept.
func TestAnArchivedRecordingIsNeverRewritten(t *testing.T) {
	store, sess := recordedFixture(t, true)
	cfg := Config{StoreDir: store, ArchiveDir: filepath.Join(t.TempDir(), "archive")}
	s := archivingJournal(t, cfg)
	wav := s.archive.Path(fixtureCall, "audio.wav")
	before, _ := os.Stat(wav)

	// The working copy changes under it (as it would if a session id were
	// ever reused), and a new journal starts.
	os.WriteFile(filepath.Join(sess, "input.pcm"), make([]byte, 64), 0o600)
	s2 := archivingJournal(t, cfg)
	after, err := os.Stat(wav)
	if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the archived recording was touched: %v -> %v (%v)", before, after, err)
	}
	if _, _, problems := s2.archive.Verify(); len(problems) != 0 {
		t.Errorf("verify after a restart: %v", problems)
	}
}

// The instructions a call ran on are kept once per distinct text, when golive
// still has them.
func TestTheCallsInstructionsAreKeptOnce(t *testing.T) {
	calls := fixtureCalls(t)
	var sha string
	for _, c := range calls {
		if c.ID == fixtureCall {
			sha = c.PromptSHA
		}
	}
	if sha == "" {
		t.Skip("the fixture call has no instructions hash")
	}
	last := filepath.Join(t.TempDir(), "last-call.json")
	kept, _ := json.Marshal(map[string]string{"instructions": "you are a helpful front desk", "instructions_sha": sha})
	os.WriteFile(last, kept, 0o600)
	s := archivingJournal(t, Config{LastCallFile: last})
	got, err := os.ReadFile(filepath.Join(s.archive.Dir(), "prompts", sha+".txt"))
	if err != nil || string(got) != "you are a helpful front desk" {
		t.Errorf("prompts/%s.txt: %q, %v", sha, got, err)
	}

	// Another prompt's text is not filed under this call's hash.
	other, _ := json.Marshal(map[string]string{"instructions": "something else", "instructions_sha": "ffffffffffff"})
	os.WriteFile(last, other, 0o600)
	s2 := archivingJournal(t, Config{LastCallFile: last})
	if _, err := os.Stat(filepath.Join(s2.archive.Dir(), "prompts", "ffffffffffff.txt")); err == nil {
		t.Error("instructions that no archived call ran on were filed")
	}
}

// The archive is fed on a clock, not by whoever opens the page — a recording
// has a retention, and a call nobody looked at in time would be lost. And the
// clock keeps out of a call's way: it does not read a log golive is writing.
func TestCallsAreArchivedByTheClockAndNotDuringACall(t *testing.T) {
	work := t.TempDir()
	logPath := filepath.Join(work, "golive.log")
	whole, _ := os.ReadFile("testdata/call.log")
	os.WriteFile(logPath, whole, 0o600) // written this instant: a call in progress
	s := NewServer(Config{LogPath: logPath, ArchiveDir: filepath.Join(work, "archive")})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Watch(ctx, 10*time.Millisecond)

	time.Sleep(150 * time.Millisecond)
	if e := s.archive.Entry(fixtureCall); e != nil {
		t.Fatal("the clock read a log that was being written")
	}
	still := time.Now().Add(-time.Minute)
	os.Chtimes(logPath, still, still) // the call has ended and the log gone quiet
	deadline := time.Now().Add(3 * time.Second)
	for s.archive.Entry(fixtureCall) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.archive.Entry(fixtureCall) == nil {
		t.Fatal("nobody opened the page, and the call was never archived")
	}
}
