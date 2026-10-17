package journal

import (
	"os"
	"strings"
	"testing"
	"time"
)

// fixtureCalls reads testdata/call.log: one real three-minute call, with every
// word said on it replaced by a filler of the same length.
func fixtureCalls(t *testing.T) []*Call {
	t.Helper()
	f, err := os.Open("testdata/call.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ix := NewIndexer()
	if err := ix.ReadLog(f); err != nil {
		t.Fatal(err)
	}
	return ix.Calls()
}

// The journal must say what scripts/call-latency.sh said about the same call,
// or two instruments disagree about one set of turns. These are that script's
// figures for this call, as reported on the day.
func TestACallReadsAsTheLatencyScriptReadIt(t *testing.T) {
	calls := fixtureCalls(t)
	if len(calls) != 1 {
		t.Fatalf("%d calls in a log of one", len(calls))
	}
	c := calls[0]
	if c.ID != "out-mv4kxcji-s18u" || c.Build != "c8a8477" {
		t.Errorf("call %q on build %q", c.ID, c.Build)
	}
	want := Stats{Turns: 21, P50: 806, P90: 979, P95: 1285, Max: 1396, Over: 2, ReplyP50: 426, ReplyP90: 599, ReplyP95: 905}
	got := c.Summary
	if got.Turns != want.Turns || got.P50 != want.P50 || got.P90 != want.P90 || got.P95 != want.P95 ||
		got.Max != want.Max || got.Over != want.Over || got.ReplyP50 != want.ReplyP50 ||
		got.ReplyP90 != want.ReplyP90 || got.ReplyP95 != want.ReplyP95 {
		t.Errorf("summary %+v\n   want %+v", got, want)
	}
	if c.PromptChars != 13116 || c.PromptSHA != "639dcecf24dd" || c.LLMCopies != 8 || c.Lanes != 8 || c.Keys != 2 {
		t.Errorf("prompt %d/%s, copies %d, lanes %d, keys %d", c.PromptChars, c.PromptSHA, c.LLMCopies, c.Lanes, c.Keys)
	}
	if c.DurationS < 170 || c.DurationS > 200 {
		t.Errorf("duration %.1f s for a three-minute call", c.DurationS)
	}
}

// A turn's stages are the wait between the caller's last sound and the first
// audio, cut up. If they do not add up to it, the waterfall is a drawing.
func TestEveryTurnsStagesAddUpToItsTotal(t *testing.T) {
	for _, turn := range fixtureCalls(t)[0].Turns {
		sum := 0
		for _, s := range turn.Stages {
			if s.MS < 0 {
				t.Errorf("turn %d: stage %s is %d ms", turn.N, s.Key, s.MS)
			}
			sum += s.MS
		}
		if sum != turn.TotalMS {
			t.Errorf("turn %d (%s): stages add to %d, total is %d: %+v", turn.N, turn.ID, sum, turn.TotalMS, turn.Stages)
		}
		if turn.TotalMS != turn.ReplyMS+turn.PauseMS {
			t.Errorf("turn %d: total %d is not reply %d + pause %d", turn.N, turn.TotalMS, turn.ReplyMS, turn.PauseMS)
		}
	}
}

// The two turns over target on this call were both the synthesizer's first
// audio (938 and 646 ms) with the second request refused by the budget. The
// journal's first guess should land on the stage that was argued for by hand.
func TestTheSlowTurnsAreLaidAtTheSynthesizersDoor(t *testing.T) {
	c := fixtureCalls(t)[0]
	var over []Turn
	for _, turn := range c.Turns {
		if turn.Over {
			over = append(over, turn)
		}
	}
	if len(over) != 2 {
		t.Fatalf("%d turns over target; want 2", len(over))
	}
	for _, turn := range over {
		if !strings.Contains(turn.Cause, "synthesizer") {
			t.Errorf("turn %d (%d ms): cause %q", turn.N, turn.TotalMS, turn.Cause)
		}
		if !turn.HedgeRefused {
			t.Errorf("turn %d: the refused second request is not recorded against it", turn.N)
		}
	}
	if c.Events.HedgeRefused != 4 || c.Events.Interruptions != 13 || c.Events.GuessesDropped != 16 ||
		c.Events.EarlyFinals != 45 || c.Events.ClosedOnFinal != 22 || c.Events.Utterances != 25 {
		t.Errorf("events %+v", c.Events)
	}
	wins := 0
	for _, n := range c.Events.CopyWins {
		wins += n
	}
	if len(c.Events.CopyWins) != 8 || wins != 40 {
		t.Errorf("copy wins %v", c.Events.CopyWins)
	}
	if len(c.Events.PausesInside) != 51 {
		t.Errorf("%d pauses inside utterances; the call logged 51", len(c.Events.PausesInside))
	}
}

// Turns are placed in the recording so that a click can play them. They must
// be in order, inside the call, and the answer must start after the caller
// stopped.
func TestTurnsArePlacedOnTheRecordingsTimeline(t *testing.T) {
	c := fixtureCalls(t)[0]
	last := 0.0
	for _, turn := range c.Turns {
		if turn.LastSoundS <= last || turn.LastSoundS > c.DurationS {
			t.Errorf("turn %d placed at %.2f s (previous %.2f, call %.1f s)", turn.N, turn.LastSoundS, last, c.DurationS)
		}
		gap := (turn.AudioOutS - turn.LastSoundS) * 1000
		if d := gap - float64(turn.TotalMS); d < -2 || d > 2 {
			t.Errorf("turn %d: placed %.0f ms apart, total is %d ms", turn.N, gap, turn.TotalMS)
		}
		last = turn.LastSoundS
	}
}

func TestParseLineReadsQuotedAndBareValues(t *testing.T) {
	l, ok := ParseLine(`time=2026-10-12T09:34:54.416+08:00 level=DEBUG msg="think: abandoning the speculative turn" session=sess_1 turn=item_3 reason="the transcript \"moved\" on" guess=字字字， n=-12`)
	if !ok {
		t.Fatal("not parsed")
	}
	if l.Msg != "think: abandoning the speculative turn" || l.Level != "DEBUG" || l.Get("turn") != "item_3" ||
		l.Get("reason") != `the transcript "moved" on` || l.Get("guess") != "字字字，" || l.N("n") != -12 {
		t.Errorf("%+v %v", l, l.kv)
	}
	if _, ok := ParseLine("--- 2026-10-12 09:32:36 phone-brain: starting golive ---"); ok {
		t.Error("the launcher's banner was read as a log line")
	}
}

func TestVerdictsFollowTheRule(t *testing.T) {
	mk := func(n, over int) []Turn {
		var out []Turn
		for i := 0; i < n; i++ {
			ms := 800
			if i < over {
				ms = 1500
			}
			out = append(out, Turn{TotalMS: ms, Over: ms > TargetMS})
		}
		return out
	}
	for _, tc := range []struct {
		n, over int
		want    string
	}{
		{4, 0, "too few"}, {21, 2, "too few"}, {25, 5, "not met"},
		{52, 1, "provisional"}, {93, 7, "not met"}, {72, 0, "met"}, {110, 1, "met"},
	} {
		if got := Summarise(mk(tc.n, tc.over)).Verdict; got != tc.want {
			t.Errorf("%d over in %d turns: %q, want %q", tc.over, tc.n, got, tc.want)
		}
	}
}

func TestTheMissIntervalIsTheOneTheRuleWasWrittenWith(t *testing.T) {
	for _, tc := range []struct {
		k, n      int
		low, high float64
	}{
		{0, 20, 0, 16.8}, {0, 40, 0, 8.8}, {2, 52, 0.5, 13.2}, {7, 93, 3.1, 14.9}, {0, 72, 0, 5.0}, {1, 110, 0, 5.0},
	} {
		low, high := missInterval(tc.k, tc.n)
		if d := low - tc.low; d < -0.15 || d > 0.15 {
			t.Errorf("%d of %d: low %.1f, want about %.1f", tc.k, tc.n, low, tc.low)
		}
		if d := high - tc.high; d < -0.15 || d > 0.15 {
			t.Errorf("%d of %d: high %.1f, want about %.1f", tc.k, tc.n, high, tc.high)
		}
	}
}

// The conversation, line by line: what the caller said and what the assistant
// said, in order, each answered turn's two lines tied to its number.
func TestTheConversationIsInOrderAndTiedToItsTurns(t *testing.T) {
	c := fixtureCalls(t)[0]
	if len(c.Transcript) < 2*len(c.Turns) {
		t.Fatalf("%d lines for %d answered turns", len(c.Transcript), len(c.Turns))
	}
	first := c.Transcript[0]
	if first.Who != "assistant" || first.Kind != "greeting" || first.Turn != 0 || first.Text == "" {
		t.Errorf("the call opens with %+v; want the assistant's greeting", first)
	}
	last := -1.0
	asked, answered := map[int]bool{}, map[int]bool{}
	for i, line := range c.Transcript {
		if line.TS < last {
			t.Errorf("line %d at %.2f s comes after one at %.2f s", i, line.TS, last)
		}
		last = line.TS
		if line.Text == "" || (line.Who != "caller" && line.Who != "assistant") {
			t.Errorf("line %d: %+v", i, line)
		}
		if line.Turn == 0 {
			continue
		}
		if line.Who == "caller" {
			if asked[line.Turn] {
				t.Errorf("turn %d has two caller lines", line.Turn)
			}
			asked[line.Turn] = true
			if answered[line.Turn] {
				t.Errorf("turn %d: the caller's words come after their answer", line.Turn)
			}
		} else {
			answered[line.Turn] = true
			if line.TotalMS != c.Turns[line.Turn-1].TotalMS {
				t.Errorf("turn %d: the answer is marked %d ms, the turn took %d", line.Turn, line.TotalMS, c.Turns[line.Turn-1].TotalMS)
			}
		}
	}
	for _, turn := range c.Turns {
		if !answered[turn.N] {
			t.Errorf("turn %d's answer is not in the conversation", turn.N)
		}
		if !asked[turn.N] {
			t.Errorf("turn %d's question is not in the conversation", turn.N)
		}
	}
	// The call had 13 answers cut short; what was heard of them is what is shown.
	cut := 0
	for _, line := range c.Transcript {
		if line.Interrupted {
			cut++
		}
	}
	if cut != 13 {
		t.Errorf("%d interrupted answers in the conversation; the call had 13", cut)
	}
}

// A caller's line is stamped where their words ended, which is where the wait
// is counted from, and says where they began when the log does; and the log's
// marks of how much audio had arrived are kept with the call.
func TestCallerLinesSayWhereTheyBegan(t *testing.T) {
	c := fixtureCalls(t)[0]
	callers, began := 0, 0
	for _, line := range c.Transcript {
		if line.Who != "caller" {
			if line.StartS != 0 {
				t.Errorf("an assistant's line was given a start of its own: %+v", line)
			}
			continue
		}
		callers++
		if line.StartS == 0 {
			continue
		}
		began++
		if line.StartS >= line.TS || line.TS-line.StartS > 30 {
			t.Errorf("a caller's line begins at %.2f and ends at %.2f", line.StartS, line.TS)
		}
	}
	if callers == 0 || began*10 < callers*8 {
		t.Errorf("%d of %d caller's lines say where they began", began, callers)
	}
	if n := len(c.AudioIn); n < 10 || c.AudioIn[0].Frames != 1 || c.AudioIn[0].Bytes != 640 || c.AudioIn[n-1].TS <= c.AudioIn[0].TS {
		t.Fatalf("audio marks: %+v", c.AudioIn)
	}
	first, last := c.AudioIn[0], c.AudioIn[len(c.AudioIn)-1]
	if per := (last.TS - first.TS) / float64(last.Frames-first.Frames) * 1000; per < 19.9 || per > 20.1 {
		t.Errorf("the fixture call's audio arrived every %.3f ms; it was a call on time", per)
	}
}

// The speaker-activity model's verdicts are kept with the turns they judged,
// and counted for the call, from the engine's focus lines.
func TestFocusVerdictsAreKeptWithTheirTurns(t *testing.T) {
	// A caller's line in the fixture that nobody answered: the veto goes
	// just after it, as it would on a call.
	var unanswered Said
	base := fixtureCalls(t)[0]
	for _, line := range base.Transcript {
		if line.Who == "caller" && line.Turn == 0 && line.TS > 5 {
			unanswered = line
			break
		}
	}
	if unanswered.Text == "" {
		t.Fatal("the fixture has no unanswered caller line")
	}
	vetoAt := base.StartedAt.Add(time.Duration((unanswered.TS + 0.6) * float64(time.Second)))
	stamp := func(tm time.Time) string { return tm.Format("2006-01-02T15:04:05.000-07:00") }

	f, err := os.Open("testdata/call.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ix := NewIndexer()
	if err := ix.ReadLog(f); err != nil {
		t.Fatal(err)
	}
	// Focus lines for the fixture's session, as the engine writes them.
	extra := `time=2026-10-12T09:34:40.000+08:00 level=INFO msg="focus: on" session=sess_811ccb6206d6013bacb8559a mode=veto lag_ms=345 veto_share=0.7 min_voiced_ms=240
time=2026-10-12T09:34:53.000+08:00 level=DEBUG msg="focus: utterance attributed" session=sess_811ccb6206d6013bacb8559a turn=item_2 primary_share=1 others_share=0 voiced_ms=960 coverage=0.92 primary_speaker=0 labelled_lag_ms=360 another_voice=false heard=false
time=` + stamp(vetoAt) + ` level=DEBUG msg="focus: utterance attributed" session=sess_811ccb6206d6013bacb8559a turn=item_99 primary_share=0.1 others_share=0.9 voiced_ms=520 coverage=0.88 primary_speaker=0 labelled_lag_ms=410 another_voice=true heard=false
time=` + stamp(vetoAt) + ` level=INFO msg="focus: vetoing the answer: the utterance was another voice's" session=sess_811ccb6206d6013bacb8559a turn=item_99 others_share=0.9 voiced_ms=520 waited_ms=700
time=2026-10-12T09:36:00.000+08:00 level=INFO msg="focus: this call" session=sess_811ccb6206d6013bacb8559a judged=2 vetoed=1 too_late=0 primary_speaker=0 handovers=0
`
	if err := ix.ReadLog(strings.NewReader(extra)); err != nil {
		t.Fatal(err)
	}
	c := ix.Calls()[0]
	ev := c.Events
	if ev.FocusMode != "veto" || ev.FocusJudged != 2 || ev.FocusOther != 1 || ev.FocusVetoed != 1 || ev.FocusLate != 0 || len(ev.FocusLagMS) != 2 {
		t.Fatalf("events %+v", ev)
	}
	judged := 0
	for _, turn := range c.Turns {
		if turn.Focus == nil {
			continue
		}
		judged++
		if turn.Focus.AnotherVoice || turn.Focus.PrimaryShare != 1 || turn.Focus.LagMS != 360 {
			t.Fatalf("judged turn %+v", turn.Focus)
		}
	}
	// A vetoed turn was never answered, so it is not among the answered turns;
	// its verdict lives on the caller's line in the conversation and in the
	// call's counts.
	if judged != 1 {
		t.Fatalf("judged %d on the answered turns", judged)
	}
	marked := 0
	for _, line := range c.Transcript {
		if line.Who == "caller" && line.Focus == "vetoed" {
			marked++
			if line.Text != unanswered.Text {
				t.Fatalf("the wrong caller line was marked: %.1f s, wanted %.1f s", line.TS, unanswered.TS)
			}
		}
		// item_2 is the first answered turn of the fixture.
		if line.Who == "caller" && line.Turn == 1 && line.Focus != "caller" {
			t.Fatalf("turn 1's caller line is %q; wanted the caller's own voice", line.Focus)
		}
	}
	if marked != 1 {
		t.Fatalf("%d caller lines marked vetoed in the conversation", marked)
	}
	found := false
	for _, m := range c.Markers {
		if m.Kind == "vetoed" {
			found = true
		}
	}
	if !found {
		t.Fatal("no marker for the vetoed answer")
	}
}

// The gate lane: every opening of the energy gate, on the audio clock, with
// how it closed, so a call on a noisy line shows where the gate stayed open.
func TestTheGateLaneReadsEveryOpeningAndHowItClosed(t *testing.T) {
	base := fixtureCalls(t)[0]
	opened, closed := 0, 0
	for _, u := range base.Utterances {
		opened++
		if u.EndS > 0 {
			closed++
			if u.EndS <= u.StartS {
				t.Fatalf("utterance ends before it starts: %+v", u)
			}
			if u.Close != "silence" && u.Close != "settled" {
				t.Fatalf("the fixture's utterances closed on silence or settled words; got %q", u.Close)
			}
		}
	}
	if opened == 0 || closed == 0 {
		t.Fatalf("utterances: %d opened, %d closed", opened, closed)
	}
	if base.Events.Utterances != opened {
		t.Fatalf("the count says %d utterances, the lane has %d", base.Events.Utterances, opened)
	}

	// A line that never went quiet: the settled close and the cap, as the
	// engine logs them, and a floor raise.
	sid := "sess_811ccb6206d6013bacb8559a"
	extra := `time=2026-10-12T09:40:00.000+08:00 level=DEBUG msg="listen: utterance opened" session=` + sid + ` start_ms=300000 active_ms=320 barge_in=true noise_floor_db=-60
time=2026-10-12T09:40:02.100+08:00 level=INFO msg="listen: the line never dipped under the bar; the floor is raised to its quietest" session=` + sid + ` from_db=-60 to_db=-37.4 over_ms=2000
time=2026-10-12T09:40:30.000+08:00 level=DEBUG msg="listen: utterance closed" session=` + sid + ` start_ms=300000 end_ms=330000 active_ms=29000 barge_in=true
time=2026-10-12T09:40:31.000+08:00 level=DEBUG msg="listen: utterance opened" session=` + sid + ` start_ms=330000 active_ms=200 barge_in=false noise_floor_db=-37.4
time=2026-10-12T09:40:34.000+08:00 level=INFO msg="listen: no new words and the line never went quiet; taking the turn" session=` + sid + ` words_unchanged_ms=600 voiced_since_ms=300
time=2026-10-12T09:40:34.000+08:00 level=DEBUG msg="listen: utterance closed" session=` + sid + ` start_ms=330000 end_ms=334920 active_ms=3560 barge_in=false
`
	f, err := os.Open("testdata/call.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ix := NewIndexer()
	if err := ix.ReadLog(f); err != nil {
		t.Fatal(err)
	}
	if err := ix.ReadLog(strings.NewReader(extra)); err != nil {
		t.Fatal(err)
	}
	c := ix.Calls()[0]
	n := len(c.Utterances)
	if n != opened+2 {
		t.Fatalf("utterances: %d, want %d", n, opened+2)
	}
	capped, settled := c.Utterances[n-2], c.Utterances[n-1]
	if capped.Close != "cap" || !capped.BargeIn || capped.StartS != 300 || capped.EndS != 330 || capped.ActiveMS != 29000 {
		t.Fatalf("the capped utterance: %+v", capped)
	}
	if settled.Close != "settled" || settled.BargeIn || settled.EndS != 334.92 {
		t.Fatalf("the settled utterance: %+v", settled)
	}
	if c.Events.CappedCloses != 1 || c.Events.SettledCloses != base.Events.SettledCloses+1 || c.Events.FloorRaises != 1 {
		t.Fatalf("counts: capped %d settled %d raises %d", c.Events.CappedCloses, c.Events.SettledCloses, c.Events.FloorRaises)
	}
	var raised int
	for _, m := range c.Markers {
		if m.Kind == "floor_raised" {
			raised++
			if !strings.Contains(m.Label, "-37.4") {
				t.Fatalf("the raise's marker does not say where the floor went: %q", m.Label)
			}
		}
	}
	if raised != 1 {
		t.Fatalf("floor_raised markers: %d", raised)
	}
}
