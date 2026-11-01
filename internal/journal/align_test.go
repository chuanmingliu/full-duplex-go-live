package journal

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// fastCall writes the two tracks of a call whose caller's audio arrived every
// everyMS instead of every 20: three seconds of speech, two of a quiet line,
// and again, for a minute, the first chunk arriving firstS in. Each speech
// chunk carries the moment it arrived, in 20 ms steps, so that a test can ask
// of any sample in the recording when it was really said. It returns the marks
// a log would hold.
func fastCall(t *testing.T, dir string, everyMS, firstS float64) []AudioMark {
	t.Helper()
	const rate, frame = 16000, 320
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	chunk := func(v int16) []byte {
		b := make([]byte, frame*2)
		for i := 0; i < frame; i++ {
			binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
		}
		return b
	}
	in := make([]byte, int(firstS*rate)*2) // the recorder put the first chunk where the clock stood
	var marks []AudioMark
	n := 0
	for at := firstS + 0.02; at < 60; at += everyMS / 1000 {
		n++
		if n%250 == 1 {
			marks = append(marks, AudioMark{TS: at, Frames: n, Bytes: frame * 2})
		}
		v := int16(8) // the quiet line's one frame
		if math.Mod(at, 5) < 3 {
			v = int16(1000 + int(at/0.02))
		}
		in = append(in, chunk(v)...)
	}
	out := make([]byte, 50*rate*2)
	for i := 5 * rate; i < 50*rate; i++ {
		binary.LittleEndian.PutUint16(out[i*2:], 0xF448) // -3000
	}
	for name, data := range map[string][]byte{inputTrack: in, outputTrack: out, recordFile: []byte(`{"id":"x","rate":16000,"closed_at":"2026-10-12T09:37:41+08:00"}`)} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return marks
}

// adrift reads the caller's side and says how far the worst speech sample is
// from the moment it was said, and how many speech chunks are there.
func adrift(side []byte) (worstS float64, chunks int) {
	const rate, frame = 16000, 320
	body := side[wavHeaderBytes:]
	last := int16(0)
	for i := 0; i+1 < len(body); i += 2 {
		v := int16(binary.LittleEndian.Uint16(body[i:]))
		if v < 1000 {
			continue
		}
		if v != last {
			chunks++
			last = v
			// The chunk's first sample belongs a chunk before it arrived.
			said := float64(v-1000)*0.02 - 0.02
			if d := math.Abs(float64(i/2)/rate - said); d > worstS {
				worstS = d
			}
		}
	}
	return worstS, chunks
}

// A recording made while the phone side delivered 2% more audio than time
// passed has a caller's side that ends more than a second late in a minute.
// Read through its alignment it is back on the clock, with every chunk of
// speech still there; and it reads the same from golive's two tracks as from
// the archive's one file.
func TestALateCallersSideIsPutBackOnTheClock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sess_1")
	marks := fastCall(t, dir, 19.6, 2)

	open := func() *Recording {
		rec, err := OpenRecording(filepath.Dir(dir), "sess_1")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(rec.Close)
		return rec
	}
	raw := open()
	rawSide, _ := io.ReadAll(raw.Side(false))
	rawWorst, speech := adrift(rawSide)
	if rawWorst < 1.0 || raw.DurationS() < 61 {
		t.Fatalf("the fixture is not late: worst %.2f s, %.1f s long", rawWorst, raw.DurationS())
	}

	rec := open()
	if !rec.Realign(marks) {
		t.Fatal("a late recording was left as it was")
	}
	side, _ := io.ReadAll(rec.Side(false))
	worst, kept := adrift(side)
	if worst > 0.25 {
		t.Errorf("after alignment the caller's speech is still up to %.2f s from when it was said (it was %.2f)", worst, rawWorst)
	}
	if kept != speech {
		t.Errorf("%d chunks of speech after alignment, %d before: speech must never be dropped", kept, speech)
	}
	if d := rec.DroppedS(); d < 0.9 || d > 1.4 {
		t.Errorf("%.2f s left out; about 1.16 s of a quiet line's repeats should have gone", d)
	}
	if d := rec.DurationS(); d < 59.5 || d > 60.3 {
		t.Errorf("the aligned recording is %.2f s long; the call was 60", d)
	}

	// The stereo file a browser plays: the same caller's side, the assistant's
	// untouched, and a range of it the same bytes as the whole.
	whole, _ := io.ReadAll(rec.WAV())
	if len(whole) != wavHeaderBytes+int(rec.Frames)*4 {
		t.Fatalf("stereo file is %d bytes for %d frames", len(whole), rec.Frames)
	}
	for _, f := range []int{16000 * 3, 16000 * 31, int(rec.Frames) - 5} {
		l := whole[wavHeaderBytes+f*4 : wavHeaderBytes+f*4+2]
		r := int16(binary.LittleEndian.Uint16(whole[wavHeaderBytes+f*4+2:]))
		if !bytes.Equal(l, side[wavHeaderBytes+f*2:wavHeaderBytes+f*2+2]) {
			t.Errorf("frame %d: the stereo file's left is not the aligned caller's side", f)
		}
		if want := int16(-3000); (f >= 5*16000 && f < 50*16000) != (r == want) {
			t.Errorf("frame %d: the assistant's side reads %d", f, r)
		}
	}
	part := make([]byte, 9001)
	wav := rec.WAV()
	wav.Seek(1234567, io.SeekStart)
	io.ReadFull(wav, part)
	if !bytes.Equal(part, whole[1234567:1234567+9001]) {
		t.Error("a range of the aligned file is not the same bytes as the whole")
	}

	// The archive keeps one stereo file, written as recorded. Aligned, it
	// reads as the tracks did.
	archived := filepath.Join(t.TempDir(), "audio.wav")
	rawWhole, _ := io.ReadAll(raw.WAV())
	os.WriteFile(archived, rawWhole, 0o600)
	arc, err := OpenArchivedWAV(archived)
	if err != nil {
		t.Fatal(err)
	}
	defer arc.Close()
	if !arc.Realign(marks) {
		t.Fatal("the archived copy was left as it was")
	}
	fromArchive, _ := io.ReadAll(arc.Side(false))
	if !bytes.Equal(fromArchive, side) {
		t.Error("the caller's side aligned from the archive is not the one aligned from golive's tracks")
	}
	if p, q := rec.Peaks(600), arc.Peaks(600); p.Caller[300] != q.Caller[300] || p.Assistant[300] != 91 || p.DurationS != rec.DurationS() {
		t.Errorf("peaks: caller %d against %d, assistant %d, %.2f s", p.Caller[300], q.Caller[300], p.Assistant[300], p.DurationS)
	}
}

// Audio that arrived on time is left alone, and so is a recording the marks
// are not about.
func TestARecordingOnTheClockIsLeftAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sess_1")
	marks := fastCall(t, dir, 20, 2)
	rec, err := OpenRecording(filepath.Dir(dir), "sess_1")
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()
	before := rec.Frames
	if rec.Realign(marks) || rec.DroppedS() != 0 || rec.Frames != before {
		t.Errorf("a recording on the clock was changed: %.2f s left out", rec.DroppedS())
	}
	if rec.Realign(marks[:2]) || rec.Realign([]AudioMark{{TS: 1, Frames: 1, Bytes: 640}, {TS: 2, Frames: 51, Bytes: 320}, {TS: 3, Frames: 101, Bytes: 640}}) {
		t.Error("marks too few, or of chunks of two sizes, were acted on")
	}
}

// Through the journal: a call whose log says the audio arrived fast is played
// aligned, says how much was left out, and can still be had exactly as it was
// recorded; one whose record says the recorder kept it on the clock is not
// touched.
func TestTheJournalPlaysALateRecordingOnTheClock(t *testing.T) {
	// The fixture call, with its marks rewritten to say 2% more audio arrived.
	src, err := os.ReadFile("testdata/call.log")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(msg="client audio".* frames=)(\d+)`)
	log := re.ReplaceAllStringFunc(string(src), func(m string) string {
		p := re.FindStringSubmatch(m)
		n, _ := strconv.Atoi(p[2])
		return p[1] + strconv.Itoa(1+int(float64(n-1)*1.02))
	})
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "call.log")
	os.WriteFile(logPath, []byte(log), 0o600)
	cfg := Config{LogPath: logPath, DataDir: filepath.Join(tmp, "data"), NotesDir: filepath.Join(tmp, "notes"), StoreDir: filepath.Join(tmp, "store")}
	s := NewServer(cfg)
	s.refresh()
	c := s.byID[fixtureCall]
	if c == nil || len(c.AudioIn) < 10 {
		t.Fatalf("the call's audio marks were not read: %+v", c)
	}
	// Its recording: chunks written as they arrived, a quiet line throughout
	// but for a second of speech every five.
	sess := filepath.Join(cfg.StoreDir, c.Session)
	os.MkdirAll(sess, 0o700)
	const frame = 320
	in := make([]byte, int(c.AudioIn[0].TS*16000-frame)*2)
	total := c.AudioIn[len(c.AudioIn)-1].Frames
	for n := 1; n <= total; n++ {
		v := int16(8)
		if n%250 < 50 {
			v = int16(1000 + n)
		}
		for i := 0; i < frame; i++ {
			in = binary.LittleEndian.AppendUint16(in, uint16(v))
		}
	}
	os.WriteFile(filepath.Join(sess, inputTrack), in, 0o600)
	os.WriteFile(filepath.Join(sess, outputTrack), bytes.Repeat([]byte{1, 1}, 16000*20), 0o600)
	os.WriteFile(filepath.Join(sess, recordFile), []byte(`{"id":"x","rate":16000,"closed_at":"2026-10-12T09:37:41+08:00"}`), 0o600)

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	get := func(q string) ([]byte, string) {
		resp, err := http.Get(ts.URL + "/api/calls/" + fixtureCall + "/audio.wav" + q)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return b, resp.Header.Get("Content-Disposition")
	}
	var detail struct {
		Audio struct {
			Available bool    `json:"available"`
			DurationS float64 `json:"duration_s"`
			TrimmedS  float64 `json:"caller_trimmed_s"`
		} `json:"audio"`
	}
	getJSON(t, ts.URL+"/api/calls/"+fixtureCall, &detail)
	wantTrim := float64(total) * 0.02 * (1 - 1/1.02)
	if !detail.Audio.Available || math.Abs(detail.Audio.TrimmedS-wantTrim) > 0.4 {
		t.Fatalf("the page is told %.1f s of the caller's side is left out; about %.1f s arrived that time had not passed for", detail.Audio.TrimmedS, wantTrim)
	}
	played, _ := get("")
	asRecorded, name := get("?raw=1&download=1")
	if got := float64(len(asRecorded)-len(played)) / 4 / 16000; math.Abs(got-detail.Audio.TrimmedS) > 0.3 {
		t.Errorf("what is played is %.2f s shorter than what was recorded; the page says %.1f", got, detail.Audio.TrimmedS)
	}
	if len(asRecorded) != wavHeaderBytes+len(in)*2 || !strings.Contains(name, "_as-recorded.wav") {
		t.Errorf("the recording as recorded is %d bytes, named %q", len(asRecorded), name)
	}
	if math.Abs(detail.Audio.DurationS-float64(len(played)-wavHeaderBytes)/4/16000) > 0.01 {
		t.Errorf("the page is told %.2f s; what is played is %d bytes", detail.Audio.DurationS, len(played))
	}

	// The same recording, with a record that says the recorder kept it on the
	// clock: nothing is left out.
	os.WriteFile(filepath.Join(sess, recordFile), []byte(`{"id":"x","rate":16000,"input_on_clock":true,"closed_at":"2026-10-12T09:37:41+08:00"}`), 0o600)
	getJSON(t, ts.URL+"/api/calls/"+fixtureCall, &detail)
	if again, _ := get(""); detail.Audio.TrimmedS != 0 || len(again) != len(asRecorded) {
		t.Errorf("a recording made on the clock was aligned again: %.1f s left out", detail.Audio.TrimmedS)
	}
}
