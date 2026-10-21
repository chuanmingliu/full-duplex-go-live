// golivefdb is the full-duplex benchmark: a scripted caller who murmurs,
// hesitates, interrupts, talks to someone else and pauses mid-sentence,
// scored the way τ-voice (arXiv 2603.13686) and Full-Duplex-Bench (arXiv
// 2503.04721) score a voice agent's turn-taking. See FULLDUPLEX-BENCH.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "golivefdb:", err)
		os.Exit(1)
	}
}

type scenarioResult struct {
	Name     string
	About    string
	Variant  string
	Panel    *panel
	Sessions int
	Episodes int
	Elapsed  time.Duration
}

func run() error {
	var (
		url       = flag.String("url", "ws://127.0.0.1:8089/v1/live", "golive's gpt-live endpoint")
		token     = flag.String("token", os.Getenv("GOLIVE_AUTH_TOKEN"), "bearer token, if the service wants one")
		rate      = flag.Int("rate", 16000, "session PCM rate")
		names     = flag.String("scenarios", "", "comma-separated scenario names; empty runs the whole suite")
		episodes  = flag.Int("episodes", 6, "question-and-answer rounds per session")
		sessions  = flag.Int("sessions", 2, "sessions per scenario (each a fresh connection)")
		parallel  = flag.Int("parallel", 3, "sessions run at once")
		pauses    = flag.String("pause-ms", "400,600,800,1000", "intra-utterance pauses for the pause scenario, one variant each")
		waitMS    = flag.Int("wait-ms", 1000, "how long the caller waits after the answer ends before speaking again")
		intAt     = flag.Int("interrupt-at-ms", 1500, "how far into the answer an over-the-agent event is said")
		timeoutS  = flag.Int("answer-timeout-s", 12, "give up waiting for an answer after this long")
		outJSON   = flag.String("json", "", "write every panel and the windows here")
		outMD     = flag.String("md", "", "write the report here as markdown")
		printEnv  = flag.Bool("print-env", false, "print the GOLIVE_MOCK_PHRASES line the mock recognizer needs, and exit")
		listOnly  = flag.Bool("list", false, "list the scenarios and exit")
		verbose   = flag.Bool("v", false, "print each session's events as they are scored")
		toolDelay = flag.Int("tool-delay-ms", 600, "how long the application takes to answer a tool call")
		speak     = flag.Bool("speak", false, "have the service say each phrase in its own voice first and play those as the caller: real speech for a real recognizer")
		clipsDir  = flag.String("clips-dir", "", "read spoken_NN.wav clips from here (and write the ones -speak makes)")
		instr     = flag.String("instructions", "", "the session's instructions; empty leaves the service's profile to supply them")
	)
	flag.Parse()

	if *printEnv {
		fmt.Println(envLine())
		return nil
	}
	if *listOnly {
		for _, sc := range suite {
			fmt.Printf("%-16s %s\n", sc.Name, sc.About)
		}
		return nil
	}

	var chosen []scenario
	if *names == "" {
		chosen = suite
	} else {
		for _, n := range strings.Split(*names, ",") {
			sc, ok := scenarioByName(strings.TrimSpace(n))
			if !ok {
				return fmt.Errorf("no scenario %q (-list shows them)", n)
			}
			chosen = append(chosen, sc)
		}
	}

	if *clipsDir != "" {
		n, err := loadClips(*clipsDir, *rate)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%d clips from %s\n", n, *clipsDir)
	}
	if *speak {
		if err := speakClips(context.Background(), *url, *token, *rate, *clipsDir); err != nil {
			return err
		}
	}

	opt := runOptions{
		WaitToRespond: time.Duration(*waitMS) * time.Millisecond,
		InterruptAt:   time.Duration(*intAt) * time.Millisecond,
		AnswerTimeout: time.Duration(*timeoutS) * time.Second,
	}

	// Every (scenario, variant, session) is one job; variants exist for the
	// pause scenario, one per pause length.
	type job struct {
		sc      scenario
		variant string
		opt     runOptions
		index   int
	}
	var jobs []job
	for _, sc := range chosen {
		if sc.Name == "pause" {
			for _, p := range strings.Split(*pauses, ",") {
				ms, err := strconv.Atoi(strings.TrimSpace(p))
				if err != nil {
					return fmt.Errorf("pause-ms: %q", p)
				}
				o := opt
				o.PauseMS = ms
				for i := 0; i < *sessions; i++ {
					jobs = append(jobs, job{sc: sc, variant: fmt.Sprintf("%d ms", ms), opt: o, index: i})
				}
			}
			continue
		}
		for i := 0; i < *sessions; i++ {
			jobs = append(jobs, job{sc: sc, opt: opt, index: i})
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := map[string]*scenarioResult{}
	var order []string
	var mu sync.Mutex
	sem := make(chan struct{}, *parallel)
	var wg sync.WaitGroup
	started := time.Now()
	for _, j := range jobs {
		key := j.sc.Name
		if j.variant != "" {
			key += " @ " + j.variant
		}
		mu.Lock()
		if _, ok := results[key]; !ok {
			results[key] = &scenarioResult{Name: j.sc.Name, About: j.sc.About, Variant: j.variant, Panel: &panel{}}
			order = append(order, key)
		}
		mu.Unlock()
		wg.Add(1)
		sem <- struct{}{}
		go func(j job, key string) {
			defer wg.Done()
			defer func() { <-sem }()
			t0 := time.Now()
			p, err := runSession(ctx, *url, *token, *rate, j.sc, j.opt, *episodes, *toolDelay, *instr, *verbose)
			mu.Lock()
			defer mu.Unlock()
			r := results[key]
			if err != nil {
				r.Panel.Errors = append(r.Panel.Errors, err.Error())
			}
			if p != nil {
				r.Panel.merge(p)
			}
			r.Sessions++
			r.Episodes += *episodes
			r.Elapsed += time.Since(t0)
			if *verbose {
				fmt.Fprintf(os.Stderr, "%s session %d: %s\n", key, j.index+1, oneLine(p))
			}
		}(j, key)
	}
	wg.Wait()

	var ordered []*scenarioResult
	for _, k := range order {
		ordered = append(ordered, results[k])
	}
	report := summarize(ordered, defaultWindows, opt, *episodes, *sessions, time.Since(started))
	fmt.Print(report)
	if *outMD != "" {
		if err := os.WriteFile(*outMD, []byte(report), 0o644); err != nil {
			return err
		}
	}
	if *outJSON != "" {
		out := map[string]any{
			"windows":   defaultWindows,
			"options":   opt,
			"scenarios": ordered,
			"phrases":   phrases,
			"generated": time.Now().Format(time.RFC3339),
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		if err := os.WriteFile(*outJSON, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// runSession plays one scenario for `episodes` rounds on a fresh connection
// and scores what happened.
func runSession(ctx context.Context, url, token string, rate int, sc scenario, opt runOptions, episodes, toolDelay int, instructions string, verbose bool) (*panel, error) {
	s, err := dial(ctx, url, token, rate, sc.Tools, instructions)
	if err != nil {
		return nil, err
	}
	s.toolDelayMS = toolDelay
	runCtx, stop := context.WithCancel(ctx)
	go s.run(runCtx)
	// A moment of line silence first: the detector learns the floor, and the
	// backend is warm by the time the caller speaks.
	sleepCtx(ctx, 600*time.Millisecond)
	for ep := 0; ep < episodes && ctx.Err() == nil; ep++ {
		sc.Run(ctx, s, ep, opt)
	}
	sleepCtx(ctx, 500*time.Millisecond)
	stop()
	s.close()
	p := score(s.tl, defaultWindows)
	if verbose {
		dumpTimeline(s.tl)
	}
	return p, nil
}

func oneLine(p *panel) string {
	if p == nil {
		return "no panel"
	}
	return fmt.Sprintf("response %d/%d yield %d/%d bc %d/%d tic %d/%d nd %d/%d pause %d/%d trunc %d err %d",
		p.Response.Good, p.Response.Total, p.Yield.Good, p.Yield.Total, p.Backchannel.Good, p.Backchannel.Total,
		p.TicOver.Good+p.TicSilent.Good, p.TicOver.Total+p.TicSilent.Total, p.NDOver.Good+p.NDSilent.Good, p.NDOver.Total+p.NDSilent.Total,
		p.PauseHold.Good, p.PauseHold.Total, p.Truncated, len(p.Errors))
}

func dumpTimeline(tl *timeline) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	o := tl.Origin
	for _, u := range tl.User {
		extra := ""
		if u.HasPause && !u.PauseAt.IsZero() {
			extra = fmt.Sprintf(" pause %d–%d", u.PauseAt.Sub(o).Milliseconds(), u.PauseEnd.Sub(o).Milliseconds())
		}
		fmt.Fprintf(os.Stderr, "  caller %6d–%6d %-12s over=%v %s%s\n", u.Start.Sub(o).Milliseconds(), u.End.Sub(o).Milliseconds(), u.Kind, u.OverAgent, u.Text, extra)
	}
	for _, a := range tl.Agent {
		fmt.Fprintf(os.Stderr, "  agent  %6d–%6d truncated=%v\n", a.Start.Sub(o).Milliseconds(), a.End.Sub(o).Milliseconds(), a.Truncated)
	}
	for _, t := range tl.SpeechStarted {
		fmt.Fprintf(os.Stderr, "  golive.speech.started %6d\n", t.Sub(o).Milliseconds())
	}
	for _, t := range tl.SpeechStopped {
		fmt.Fprintf(os.Stderr, "  golive.speech.stopped %6d\n", t.Sub(o).Milliseconds())
	}
	for _, m := range tl.TurnMetrics {
		fmt.Fprintf(os.Stderr, "  turn %s speech_end=%d end_silence=%d start=%d first_token=%d first_audio_out=%d truncated=%v\n", m.TurnID, m.SpeechEndMS, m.EndSilenceMS, m.TurnStartMS, m.LLMFirstTokenMS, m.FirstAudioOutMS, m.Truncated)
	}
	for _, e := range tl.Errors {
		fmt.Fprintf(os.Stderr, "  error %s\n", e)
	}
}
