package main

// pool.go asks what more keys are worth.
//
// golive sends copies of each completion and keeps the first to answer. That
// only helps if the copies are slow independently, and on one key they were
// not: one turn in ten had every copy over 550 ms. More keys are the remedy if
// the slowness belongs to the account — and no remedy at all if it belongs to
// the vendor's whole service that minute. This measures which, from here, now:
//
//	llmprobe -env .env.local -pool -runs 12
//
// Each round sends one request down every lane at once, first with a key per
// lane and then with every lane on the first key, and reports the first token
// of each lane and of the fastest in the round.

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/deepseek"
)

func runPool(llm provider.LLM, label, model, instructions string, runs, warmup int, noThinking bool) error {
	pooled, ok := llm.(*deepseek.LLM)
	if !ok {
		return fmt.Errorf("-pool: %s has no key pool to measure", label)
	}
	keys, lanes := pooled.Keys(), pooled.Lanes()
	fmt.Printf("%s: %d key(s), %d lane(s) — a lane is one key on a connection of its own\n", label, keys, lanes)
	if keys == 1 {
		fmt.Println("only one key is configured, so both rows below are the same arrangement; add keys to compare")
	}
	fmt.Println()

	single := pooled.SingleKey()
	type arrangement struct {
		name string
		llm  provider.LLM
		per  [][]time.Duration // per lane
		best []time.Duration   // fastest of each round
		fail int
	}
	arr := []*arrangement{
		{name: fmt.Sprintf("a key per lane (%d keys)", min(keys, lanes)), llm: pooled, per: make([][]time.Duration, lanes)},
		{name: "every lane on key 1", llm: single, per: make([][]time.Duration, lanes)},
	}
	for round := 0; round < warmup+runs; round++ {
		q := questions[round%len(questions)]
		for _, a := range arr {
			firsts := make([]time.Duration, lanes)
			var wg sync.WaitGroup
			for lane := 0; lane < lanes; lane++ {
				wg.Add(1)
				go func(lane int) {
					defer wg.Done()
					s := askOn(provider.WithLane(context.Background(), lane), candidate{label: label, model: model, llm: a.llm}, instructions, q, noThinking)
					if s.err == nil {
						firsts[lane] = s.first
					}
				}(lane)
			}
			wg.Wait()
			if round < warmup {
				continue
			}
			var best time.Duration
			for lane, f := range firsts {
				if f == 0 {
					a.fail++
					continue
				}
				a.per[lane] = append(a.per[lane], f)
				if best == 0 || f < best {
					best = f
				}
			}
			if best > 0 {
				a.best = append(a.best, best)
			}
			time.Sleep(300 * time.Millisecond)
		}
	}

	for _, a := range arr {
		fmt.Printf("── %s\n", a.name)
		fmt.Printf("   %-22s %4s   %s\n", "", "ok", "first token ms: min / p50 / p90 / max")
		for lane, v := range a.per {
			key := lane%keys + 1
			if a.llm != provider.LLM(pooled) {
				key = 1
			}
			if len(v) == 0 {
				fmt.Printf("   lane %d (key %d)         %2d/%-2d  no successful request\n", lane+1, key, 0, runs)
				continue
			}
			fmt.Printf("   lane %d (key %d)         %2d/%-2d  %5d / %5d / %5d / %5d\n", lane+1, key, len(v), runs,
				pct(v, 0), pct(v, 0.5), pct(v, 0.9), pct(v, 1))
		}
		if len(a.best) > 0 {
			sort.Slice(a.best, func(i, j int) bool { return a.best[i] < a.best[j] })
			over := 0
			for _, b := range a.best {
				if b > 550*time.Millisecond {
					over++
				}
			}
			fmt.Printf("   fastest of each round  %2d/%-2d  %5d / %5d / %5d / %5d      rounds with every lane over 550 ms: %d\n",
				len(a.best), runs, pct(a.best, 0), pct(a.best, 0.5), pct(a.best, 0.9), pct(a.best, 1), over)
		}
		if a.fail > 0 {
			fmt.Printf("   failed requests: %d\n", a.fail)
		}
		fmt.Println()
	}
	fmt.Println("\"fastest of each round\" is what a caller waits for. If the first arrangement beats the second, the keys are earning their place.")
	return nil
}
