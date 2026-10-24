package main

// The tests are the evidence that the method works, and the only evidence so
// far: every call here is synthetic (synth_test.go). Levels are given the way
// a line would be described — an echo so many dB below the assistant, noise
// so many dB above or below the echo.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testMaxLag = 1500 * time.Millisecond
	// echoBelow is how far under the assistant the echo comes back.
	echoBelow = 25.0
	echoDB    = voiceDB - echoBelow
)

func at(delay time.Duration) []echo { return []echo{{delay, echoBelow}} }

// frames is a duration in hops of the test's size.
func frames(d time.Duration) int { return int(d / testHop) }

func lagOf(p peak) time.Duration { return time.Duration(p.lag) * testHop }

// lines are the kinds of line a call is tried on.
var lines = []struct {
	name            string
	noiseDB, wander float64
}{
	{"a quiet line", echoDB - 10, 0},
	{"a metro", echoDB + 5, 3},
	// A gateway that suppresses silence sends exact zeros between words, and
	// the input's envelope sits on its floor: the case with no noise at all
	// for a coincidence to be compared with.
	{"a line that is silent between words", 0, 0},
}

func TestAnEchoAt320msOnAQuietLineIsFoundToWithinTwoHops(t *testing.T) {
	f := probe(scene{seed: 1, rate: 16000, length: 90 * time.Second, echoes: at(320 * time.Millisecond), noiseDB: echoDB - 10}, testMaxLag)

	if len(f.peaks) != 1 {
		t.Fatalf("found %d echoes, want the one at 320 ms: %+v", len(f.peaks), f.peaks)
	}
	p := f.peaks[0]
	if off := lagOf(p) - 320*time.Millisecond; off < -2*testHop || off > 2*testHop {
		t.Errorf("echo at %v, want 320ms within two hops", lagOf(p))
	}
	if p.significance < significant {
		t.Errorf("significance %.1f, under the bar of %.1f", p.significance, significant)
	}

	// The per-stretch look has to say the same thing by itself.
	if len(f.stretchLags) < f.eligible/2 {
		t.Fatalf("%d of %d stretches significant by themselves, want most on a quiet line", len(f.stretchLags), f.eligible)
	}
	median := f.stretchLags[len(f.stretchLags)/2]
	if abs(median-p.lag) > frames(agreeWithin) {
		t.Errorf("stretches' median %v, whole call %v: they should agree", time.Duration(median)*testHop, lagOf(p))
	}
	if p.agreeing < f.eligible*9/10 {
		t.Errorf("%d of %d stretches have their best lag at the echo, want nearly all", p.agreeing, f.eligible)
	}
}

// The metro: noise 5 dB louder than the echo, its level wandering by 3 dB on
// the timescale of a syllable so that it has onsets of its own.
//
// Where this stops, found by running it (TestSweepOfCallsWithAndWithoutEcho,
// 20 calls a cell, 8 kHz, the assistant speaking for half of each call):
//
//	noise over echo   15 s   30 s   45 s   60 s   90 s   180 s   300 s
//	metro    0 dB      7     19     20     20     20     20      20
//	metro   +5 dB      0      4     11     19     20     20      20
//	metro  +10 dB      0      0      0      0      0      5      18
//	metro  +15 dB      0      0      0      0      0      0       0
//	hiss    +5 dB      6     20     20     20     20     20      20
//	hiss   +10 dB      1      3      5     14     20     20      20
//	hiss   +15 dB      0      0      0      0      0      1       7
//
// So at the level asked for, 90 s is enough, 60 s nearly always, 45 s is a
// coin toss and 30 s is not. Another 5 dB of metro needs five minutes, and
// 15 dB over the echo is out of reach at any length tried. When it failed it
// said "no echo"; it never gave a wrong lag. A real metro is not this noise.
func TestAnEchoUnderAMetroLouderThanItselfIsStillFoundInNinetySeconds(t *testing.T) {
	t.Parallel()
	for seed := int64(1); seed <= 4; seed++ {
		f := probe(scene{seed: seed, rate: 16000, length: 90 * time.Second, echoes: at(320 * time.Millisecond),
			noiseDB: echoDB + 5, wanderDB: 3}, testMaxLag)
		if len(f.peaks) != 1 {
			t.Errorf("seed %d: found %d echoes (strongest significance %.1f), want the one at 320 ms", seed, len(f.peaks), f.strongest)
			continue
		}
		if off := lagOf(f.peaks[0]) - 320*time.Millisecond; off < -2*testHop || off > 2*testHop {
			t.Errorf("seed %d: echo at %v, want 320ms within two hops", seed, lagOf(f.peaks[0]))
		}
		// Hardly any stretch is significant alone under this much noise, but
		// far more of them point at the echo than chance would send there.
		// (Chance: one stretch in forty. Over 60 such calls it was half.)
		if f.peaks[0].agreeing < 3 {
			t.Errorf("seed %d: %d of %d stretches have their best lag at the echo, no more than chance", seed, f.peaks[0].agreeing, f.eligible)
		}
	}
}

func TestAnEchoTooFarUnderTheNoiseIsNotGuessedAt(t *testing.T) {
	for seed := int64(1); seed <= 3; seed++ {
		f := probe(scene{seed: seed, rate: 8000, length: 60 * time.Second, echoes: at(320 * time.Millisecond),
			noiseDB: echoDB + 15, wanderDB: 3}, testMaxLag)
		if len(f.peaks) != 0 {
			t.Errorf("seed %d: reported %+v from an echo 15 dB under a metro; the sweep says that cannot be heard", seed, f.peaks)
		}
	}
}

func TestACallWithNoEchoNeverReportsOne(t *testing.T) {
	t.Parallel()
	for _, line := range lines {
		for seed := int64(1); seed <= 12; seed++ {
			f := probe(scene{seed: seed, rate: 8000, length: 90 * time.Second, noiseDB: line.noiseDB, wanderDB: line.wander}, testMaxLag)
			if len(f.peaks) != 0 {
				t.Errorf("%s, seed %d: reported an echo where there is none: %+v", line.name, seed, f.peaks)
			}
			if f.eligible == 0 {
				t.Fatalf("%s, seed %d: no stretch of assistant speech; the scene is not testing anything", line.name, seed)
			}
		}
	}
}

func TestTwoEchoesAreBothReported(t *testing.T) {
	t.Parallel()
	near, far := 60*time.Millisecond, 420*time.Millisecond
	for seed := int64(1); seed <= 3; seed++ {
		f := probe(scene{seed: seed, rate: 16000, length: 90 * time.Second, noiseDB: echoDB - 10,
			echoes: []echo{{near, echoBelow}, {far, echoBelow}}}, testMaxLag)
		if len(f.peaks) != 2 {
			t.Fatalf("seed %d: found %d echoes, want two: %+v", seed, len(f.peaks), f.peaks)
		}
		got := []time.Duration{lagOf(f.peaks[0]), lagOf(f.peaks[1])}
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		for i, want := range []time.Duration{near, far} {
			if off := got[i] - want; off < -2*testHop || off > 2*testHop {
				t.Errorf("seed %d: echo at %v, want %v within two hops", seed, got[i], want)
			}
		}
	}
}

// A far echo much fainter than the gateway's own is the likely shape of a real
// line, and the one where the near echo's shoulders could hide it.
func TestAFaintFarEchoIsFoundBesideALoudNearOne(t *testing.T) {
	f := probe(scene{seed: 1, rate: 16000, length: 90 * time.Second, noiseDB: echoDB - 10,
		echoes: []echo{{60 * time.Millisecond, 15}, {420 * time.Millisecond, 30}}}, testMaxLag)
	if len(f.peaks) != 2 || lagOf(f.peaks[0]) != 60*time.Millisecond || lagOf(f.peaks[1]) != 420*time.Millisecond {
		t.Fatalf("want the echo at 60 ms and then the one at 420 ms, got %+v", f.peaks)
	}
	// Every stretch's best lag is the loud echo's. Set that aside and most
	// of them should point at the faint one, or it has only the whole call's
	// word for it.
	if f.peaks[1].agreeing < f.eligible/2 {
		t.Errorf("%d of %d stretches vouch for the faint echo, want most", f.peaks[1].agreeing, f.eligible)
	}
}

// Speech has a rhythm, so one echo also correlates a syllable either side of
// itself, and taking it out leaves the line's noise showing in its pauses.
// Neither is a second echo. Each seed here is one that says so when a guard
// in analyse is taken away: without remove, seed 10 reports a second echo at
// 615 ms; without the test against the call as recorded, seeds 2, 19 and 23
// report one at 250–255 ms.
func TestOneEchoIsReportedOnce(t *testing.T) {
	t.Parallel()
	for _, line := range lines {
		for _, seed := range []int64{1, 2, 3, 10, 19, 23} {
			f := probe(scene{seed: seed, rate: 16000, length: 90 * time.Second, echoes: at(320 * time.Millisecond),
				noiseDB: line.noiseDB, wanderDB: line.wander}, testMaxLag)
			if len(f.peaks) != 1 {
				t.Errorf("%s, seed %d: one echo reported as %d: %+v", line.name, seed, len(f.peaks), f.peaks)
			}
		}
	}
}

func TestTheCallerTalkingOverTheAssistantDoesNotMakeAnEcho(t *testing.T) {
	t.Parallel()
	for _, line := range lines {
		for seed := int64(1); seed <= 12; seed++ {
			f := probe(scene{seed: seed, rate: 16000, length: 60 * time.Second, overlap: 0.7,
				noiseDB: line.noiseDB, wanderDB: line.wander}, testMaxLag)
			if len(f.peaks) != 0 {
				t.Errorf("%s, seed %d: the caller's own voice was reported as an echo: %+v", line.name, seed, f.peaks)
			}
		}
	}
}

func TestAnEchoIsStillFoundWhileTheCallerTalksOverTheAssistant(t *testing.T) {
	f := probe(scene{seed: 1, rate: 16000, length: 90 * time.Second, overlap: 0.7, echoes: at(320 * time.Millisecond),
		noiseDB: echoDB - 10}, testMaxLag)
	if len(f.peaks) != 1 || lagOf(f.peaks[0]) != 320*time.Millisecond {
		t.Fatalf("want the echo at 320 ms, got %+v", f.peaks)
	}
}

// session writes a scene to dir the way the store would have: two raw tracks,
// the assistant's ending with its last sound, and a record if one is given.
func session(t *testing.T, dir string, sc scene, record string) string {
	t.Helper()
	out, in := render(sc)
	track := pcm16(out)
	for len(track) >= 2 && track[len(track)-1] == 0 && track[len(track)-2] == 0 {
		track = track[:len(track)-2]
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{outputFile: track, inputFile: pcm16(in)}
	if record != "" {
		files[metaFile] = []byte(record)
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// echoprobe runs the command as a shell would.
func echoprobe(args ...string) (code int, stdout, stderr string) {
	var o, e strings.Builder
	code = run(args, &o, &e)
	return code, o.String(), e.String()
}

var quietCall = scene{seed: 3, rate: 16000, length: 45 * time.Second, echoes: at(320 * time.Millisecond), noiseDB: echoDB - 10}

func TestTheReportNamesTheEchoAndWhoseItIs(t *testing.T) {
	dir := session(t, filepath.Join(t.TempDir(), "sess_0123abcd"), quietCall, `{"id":"sess_0123abcd","rate":16000}`)
	code, stdout, stderr := echoprobe("-dir", dir)
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"rate 16000 Hz, duration 45.0 s",
		"assistant speech ",
		"lag ms",
		"echo at 320 ms: consistent with the far handset's echo: the full round trip of the phone leg",
		" stretches significant by themselves have a median of 320 ms, which agrees",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not say %q:\n%s", want, stdout)
		}
	}
	if !strings.HasSuffix(strings.TrimRight(stdout, "\n"), "agrees") {
		t.Errorf("the interpretation should be the last line:\n%s", stdout)
	}
}

func TestAnEchoUnder150msIsReadAsTheGatewaysOwn(t *testing.T) {
	sc := quietCall
	sc.echoes = at(60 * time.Millisecond)
	dir := session(t, t.TempDir(), sc, `{"rate":16000}`)
	_, stdout, _ := echoprobe("-dir", dir)
	if want := "echo at 60 ms: consistent with an echo inside the gateway or PBX (the local part of the phone leg)"; !strings.Contains(stdout, want) {
		t.Errorf("the report does not say %q:\n%s", want, stdout)
	}
}

func TestNoEchoIsSaidPlainlyAndNoLagIsPrinted(t *testing.T) {
	sc := quietCall
	sc.echoes = nil
	dir := session(t, t.TempDir(), sc, `{"rate":16000}`)

	code, stdout, stderr := echoprobe("-dir", dir)
	if code != 0 || stderr != "" {
		t.Fatalf("a call with no echo was still read and measured: exit %d, stderr %q", code, stderr)
	}
	if want := "no echo found; this line returns too little of the assistant's voice to time"; !strings.HasSuffix(strings.TrimRight(stdout, "\n"), want) {
		t.Errorf("the report should end %q:\n%s", want, stdout)
	}
	for _, not := range []string{"lag ms", "echo at", "best lags", "median"} {
		if strings.Contains(stdout, not) {
			t.Errorf("a call with no echo has no lag to print, but the report says %q:\n%s", not, stdout)
		}
	}

	_, stdout, _ = echoprobe("-dir", dir, "-json")
	var rep map[string]any
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("-json did not print JSON: %v\n%s", err, stdout)
	}
	if rep["echo_found"] != false || len(rep["echoes"].([]any)) != 0 {
		t.Errorf("-json reports an echo: %s", stdout)
	}
	if strings.Contains(stdout, `"lag_ms"`) || strings.Contains(stdout, "best_lag") {
		t.Errorf("-json prints a lag for a call with no echo: %s", stdout)
	}
}

func TestJSONCarriesWhatTheTextDoes(t *testing.T) {
	dir := session(t, t.TempDir(), quietCall, `{"rate":16000}`)
	code, stdout, _ := echoprobe("-json", "-dir", dir)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var rep report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("-json did not print one JSON object: %v\n%s", err, stdout)
	}
	if !rep.EchoFound || len(rep.Echoes) != 1 || rep.Echoes[0].LagMS != 320 {
		t.Fatalf("want one echo at 320 ms, got %+v", rep.Echoes)
	}
	if rep.RateHz != 16000 || rep.DurationMS != 45000 || rep.AssistantStretches == 0 || rep.AssistantSpeechMS == 0 {
		t.Errorf("the call is misdescribed: %+v", rep)
	}
	if rep.Echoes[0].Significance < significant || rep.Echoes[0].Correlation <= 0 {
		t.Errorf("the echo is misdescribed: %+v", rep.Echoes[0])
	}
	if s := rep.PerStretch.BestLagMS; s == nil || s.Median != 320 || s.Min > s.Q1 || s.Q1 > s.Median || s.Median > s.Q3 || s.Q3 > s.Max {
		t.Errorf("the per-stretch spread is wrong: %+v", s)
	}
	if !strings.Contains(rep.Interpretation, "echo at 320 ms") {
		t.Errorf("interpretation %q", rep.Interpretation)
	}
}

func TestTheRateIsTheSessionRecordsAnd16kHzWithoutOne(t *testing.T) {
	phone := quietCall
	phone.rate = 8000
	dir := session(t, t.TempDir(), phone, `{"id":"sess_0123abcd","rate":8000,"session":{}}`)
	_, stdout, _ := echoprobe("-dir", dir)
	if !strings.Contains(stdout, "rate 8000 Hz, duration 45.0 s") || !strings.Contains(stdout, "echo at 320 ms") {
		t.Errorf("an 8 kHz call should be read at 8 kHz and its echo found at 320 ms:\n%s", stdout)
	}

	// Without the record the tracks are taken for 16 kHz, and the report says
	// that it assumed so: at the wrong rate every lag is wrong by the ratio.
	dir = session(t, t.TempDir(), quietCall, "")
	code, stdout, _ := echoprobe("-dir", dir)
	if code != 0 || !strings.Contains(stdout, "rate 16000 Hz (assumed") || !strings.Contains(stdout, "echo at 320 ms") {
		t.Errorf("exit %d; a call with no record should be read at 16 kHz and say so:\n%s", code, stdout)
	}
}

func TestOnlyNumbersArePrintedNeverTheConversation(t *testing.T) {
	record := `{"id":"sess_0123abcd","rate":16000,
		"session":{"instructions":"PERSONA-TEXT"},
		"history":[{"role":"user","content":"CALLER-WORDS"},{"role":"assistant","content":"ASSISTANT-WORDS"}]}`
	dir := session(t, t.TempDir(), quietCall, record)
	for _, args := range [][]string{{"-dir", dir}, {"-dir", dir, "-json"}} {
		_, stdout, stderr := echoprobe(args...)
		for _, word := range []string{"PERSONA", "CALLER-WORDS", "ASSISTANT-WORDS", "history", "instructions"} {
			if strings.Contains(stdout+stderr, word) {
				t.Errorf("%v printed %q from the session record", args[1:], word)
			}
		}
	}
}

func TestADirectoryMissingATrackIsAClearErrorAndANonZeroExit(t *testing.T) {
	for _, missing := range []string{inputFile, outputFile} {
		dir := session(t, t.TempDir(), scene{seed: 1, rate: 16000, length: 5 * time.Second}, `{"rate":16000}`)
		if err := os.Remove(filepath.Join(dir, missing)); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := echoprobe("-dir", dir)
		if code == 0 {
			t.Errorf("without %s: exit 0, want a failure", missing)
		}
		if stdout != "" {
			t.Errorf("without %s: a report was printed anyway:\n%s", missing, stdout)
		}
		if !strings.Contains(stderr, "no "+missing) || !strings.Contains(stderr, dir) {
			t.Errorf("without %s: the error should name the file and the directory, got %q", missing, stderr)
		}
	}
}

func TestARecordThatIsNotJSONIsAnErrorNotAGuessAtTheRate(t *testing.T) {
	dir := session(t, t.TempDir(), scene{seed: 1, rate: 16000, length: 5 * time.Second}, `{"rate":`)
	if code, _, stderr := echoprobe("-dir", dir); code == 0 || !strings.Contains(stderr, metaFile) {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

func TestTheStoreFlagMeasuresTheMostRecentlyModifiedSession(t *testing.T) {
	store := t.TempDir()
	short := scene{seed: 5, rate: 8000, length: 30 * time.Second, noiseDB: echoDB - 10}
	stamp := func(dir string, age time.Duration) {
		when := time.Now().Add(-age)
		for _, name := range []string{inputFile, outputFile, metaFile, "."} {
			if err := os.Chtimes(filepath.Join(dir, name), when, when); err != nil {
				t.Fatal(err)
			}
		}
	}
	short.echoes = at(320 * time.Millisecond)
	stamp(session(t, filepath.Join(store, "sess_00000001"), short, `{"rate":8000}`), 3*time.Hour)
	short.echoes = at(60 * time.Millisecond)
	latest := session(t, filepath.Join(store, "sess_00000002"), short, `{"rate":8000}`)
	stamp(latest, time.Hour)
	short.echoes = at(500 * time.Millisecond)
	stamp(session(t, filepath.Join(store, "sess_00000003"), short, `{"rate":8000}`), 2*time.Hour)
	// Newer than all of them, and not sessions.
	if err := os.Mkdir(filepath.Join(store, "lost+found"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := echoprobe("-store", store)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, latest) || !strings.Contains(stdout, "echo at 60 ms") {
		t.Errorf("want the session written to last (%s, echo at 60 ms):\n%s", latest, stdout)
	}

	if code, _, stderr := echoprobe("-store", t.TempDir()); code == 0 || !strings.Contains(stderr, "no stored session") {
		t.Errorf("an empty store: exit %d, stderr %q", code, stderr)
	}
}

func TestASilentAssistantIsNoEchoAndNotAnError(t *testing.T) {
	dir := session(t, t.TempDir(), scene{seed: 1, rate: 16000, length: 10 * time.Second, noiseDB: -50}, `{"rate":16000}`)
	if err := os.WriteFile(filepath.Join(dir, outputFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := echoprobe("-dir", dir)
	if code != 0 || !strings.Contains(stdout, "no echo found; the assistant's track is silent") {
		t.Errorf("exit %d:\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "duration 10.0 s") {
		t.Errorf("the call is as long as its longer track:\n%s", stdout)
	}
}

func TestACommandLineThatMakesNoSenseIsRefused(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{},
		{"-dir", dir, "-store", dir},
		{"-dir", dir, "-hop", "0"},
		{"-dir", dir, "-hop", "1ms"},
		{"-dir", dir, "-hop", "50ms"},
		{"-dir", dir, "-max-lag", "20ms"},
		{"-dir", dir, "-max-lag", "1m"},
		{"-dir", dir, "extra"},
	} {
		if code, stdout, stderr := echoprobe(args...); code != 2 || stdout != "" || stderr == "" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want exit 2 and a reason", args, code, stdout, stderr)
		}
	}
}

func TestACoarserHopAndAShorterSearchFindTheSameEcho(t *testing.T) {
	dir := session(t, t.TempDir(), quietCall, `{"rate":16000}`)
	_, stdout, _ := echoprobe("-dir", dir, "-hop", "10ms", "-max-lag", "600ms")
	if !strings.Contains(stdout, "echo at 320 ms") || !strings.Contains(stdout, "0 to 600 ms in steps of 10 ms") {
		t.Errorf("want the echo at 320 ms on a 10 ms grid:\n%s", stdout)
	}
}

// The figures quoted in the comments of this package come from here. It takes
// some minutes, so it runs only when asked:
//
//	ECHOPROBE_SWEEP=1 go test ./cmd/echoprobe -run Sweep -v -timeout 30m
func TestSweepOfCallsWithAndWithoutEcho(t *testing.T) {
	if os.Getenv("ECHOPROBE_SWEEP") == "" {
		t.Skip("set ECHOPROBE_SWEEP=1 to run the sweep")
	}
	many := func(n int, mk func(seed int64) scene) []finding {
		out := make([]finding, n)
		var wg sync.WaitGroup
		busy := make(chan struct{}, 4)
		for i := range out {
			wg.Add(1)
			go func() {
				defer wg.Done()
				busy <- struct{}{}
				out[i] = probe(mk(int64(i+1)), testMaxLag)
				<-busy
			}()
		}
		wg.Wait()
		return out
	}

	// No echo: how high does significance get by accident?
	t.Log("no echo, 300 calls each: largest significance at any lag; stretches significant alone")
	for _, line := range lines {
		for _, c := range []struct {
			name    string
			rate    int
			length  time.Duration
			overlap float64
		}{
			{"90 s at 16 kHz", 16000, 90 * time.Second, 0},
			{"90 s at 8 kHz, caller talking over", 8000, 90 * time.Second, 0.5},
			{"30 s at 16 kHz", 16000, 30 * time.Second, 0},
		} {
			worst, alone, eligible, found := 0.0, 0, 0, 0
			for _, f := range many(300, func(seed int64) scene {
				return scene{seed: seed, rate: c.rate, length: c.length, overlap: c.overlap, noiseDB: line.noiseDB, wanderDB: line.wander}
			}) {
				worst = max(worst, f.strongest)
				alone += len(f.stretchLags)
				eligible += f.eligible
				found += len(f.peaks)
			}
			t.Logf("  %-36s %-36s largest %.1f, false echoes %d, stretches %d of %d", line.name, c.name, worst, found, alone, eligible)
		}
	}

	// An echo at 320 ms, 25 dB down: how much noise and how short a call?
	t.Log("echo at 320 ms: calls of 20 in which it was found (and found at 320 ms ± 10), weakest and median significance")
	for _, wander := range []float64{3, 0} {
		for _, over := range []float64{0, 5, 10, 15} {
			for _, secs := range []int{15, 30, 45, 60, 90, 180, 300} {
				found, right := 0, 0
				var z []float64
				for _, f := range many(20, func(seed int64) scene {
					return scene{seed: seed, rate: 8000, length: time.Duration(secs) * time.Second,
						echoes: at(320 * time.Millisecond), noiseDB: echoDB + over, wanderDB: wander}
				}) {
					z = append(z, f.strongest)
					if len(f.peaks) > 0 {
						found++
						if abs(f.peaks[0].lag-frames(320*time.Millisecond)) <= 2 {
							right++
						}
					}
				}
				sort.Float64s(z)
				t.Logf("  noise %+3.0f dB over the echo, wandering %.0f dB, %3d s: %2d (%2d)  %.1f  %.1f", over, wander, secs, found, right, z[0], z[len(z)/2])
			}
		}
	}
}
