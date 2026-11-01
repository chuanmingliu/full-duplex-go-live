package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// The vocabulary the harness speaks and the one the mock recognizer is told
// to hear must be the same list in the same order, or every scenario is
// scoring a different conversation from the one it thinks it ran.
func TestSimEnvCarriesTheVocabulary(t *testing.T) {
	data, err := os.ReadFile("../../configs/fdb-sim.env")
	if err != nil {
		t.Skip("configs/fdb-sim.env not beside the source:", err)
	}
	if !strings.Contains(string(data), envLine()+"\n") {
		t.Fatalf("configs/fdb-sim.env does not carry the current vocabulary; regenerate it with `golivefdb -print-env`")
	}
}

func at(ms int) time.Time { return time.Unix(0, 0).Add(time.Duration(ms) * time.Millisecond) }

// A hand-built timeline exercises every rule in score: one answered turn,
// one real interruption yielded to, one murmur talked through, one
// hesitation that cut the answer, an aside answered into silence, and a
// pause answered into.
func TestScoreAppliesTheWindows(t *testing.T) {
	tl := &timeline{Origin: at(0)}
	tl.User = []segment{
		{Start: at(0), End: at(1000), Kind: kindTurn},                                                               // answered at 1800
		{Start: at(3000), End: at(3800), Kind: kindTurn, OverAgent: true},                                           // interruption; agent cut at 3500
		{Start: at(7000), End: at(7400), Kind: kindBackchannel, OverAgent: true},                                    // agent carries on
		{Start: at(9000), End: at(9400), Kind: kindTic, OverAgent: true},                                            // agent stops at 9500: error
		{Start: at(13000), End: at(14000), Kind: kindNonDirected},                                                   // answered at 15000: error
		{Start: at(20000), End: at(23000), Kind: kindTurn, HasPause: true, PauseAt: at(21000), PauseEnd: at(21900)}, // agent at 22000: took the pause
	}
	// The agent is audible 4600–9500, so the tic at 9000 "stops" it within
	// 1 s; its answer in the pause begins at 22000, over the resumed caller.
	tl.Agent = []segment{
		{Start: at(1800), End: at(3500), Truncated: true},
		{Start: at(4600), End: at(9500), Truncated: true},
		{Start: at(15000), End: at(16000)},
		{Start: at(22000), End: at(22300), Truncated: true},
		{Start: at(24000), End: at(26000)},
	}
	p := score(tl, defaultWindows)

	if p.Response.Total != 3 || p.Response.Good != 3 {
		t.Errorf("response = %d/%d, want 3/3 (the first turn, the interruption, the paused turn)", p.Response.Good, p.Response.Total)
	}
	if p.Yield.Total != 1 || p.Yield.Good != 1 || p.Yield.LatenciesMS[0] != 500 {
		t.Errorf("yield = %+v, want 1/1 at 500 ms", p.Yield)
	}
	if p.Backchannel.Total != 1 || p.Backchannel.Good != 1 {
		t.Errorf("backchannel = %+v, want 1/1", p.Backchannel)
	}
	if p.TicOver.Total != 1 || p.TicOver.Good != 0 {
		t.Errorf("tic over = %+v, want 0/1 (the answer stopped 500 ms after it)", p.TicOver)
	}
	if p.NDSilent.Total != 1 || p.NDSilent.Good != 0 {
		t.Errorf("non-directed silent = %+v, want 0/1 (answered 1 s later)", p.NDSilent)
	}
	if p.PauseHold.Total != 1 || p.PauseHold.Good != 0 {
		t.Errorf("pause held = %+v, want 0/1", p.PauseHold)
	}
	if p.AgentInt != 1 {
		t.Errorf("agent interruptions = %d, want 1 (the answer begun in the pause, over the resumed caller)", p.AgentInt)
	}
	if d := describe(p.LastSoundMS); d.N != 2 || d.P50 != 800 {
		t.Errorf("last sound → first audio = %+v, want n=2 p50 800 (800 and 1000)", d)
	}
	if got := passPowK(12, 10, 2); got < 0.68 || got > 0.69 {
		t.Errorf("pass^2 with 10 of 12 = %.3f, want C(10,2)/C(12,2) = 0.682", got)
	}
}
