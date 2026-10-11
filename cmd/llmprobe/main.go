// Command llmprobe measures a chat backend's time to first token directly,
// with the rest of the cascade taken out of the picture.
//
// On a phone call the backend's first token is the largest single term in the
// wait, and the only one that changes by a factor rather than a percentage when
// the model behind it changes. Which model is fastest from a given machine, on
// a given account, at a given hour is not something a vendor's page can say.
// This asks each candidate the same questions in turn and reports what came
// back when:
//
//	llmprobe -env .env.local                      # the backend golive is configured with
//	llmprobe -env .env.local -runs 12 \
//	  -try 'qwen|https://dashscope.aliyuncs.com/compatible-mode|qwen-flash|DASHSCOPE_API_KEY'
//
// Each -try is label|base URL|model|name of the environment variable holding
// its key, for any chat-completions endpoint. Candidates are asked in rotation,
// so a slow minute on the network lands on all of them.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/provider/deepseek"
	_ "github.com/chuanmingliu/golive/internal/provider/mock"
)

type tries []string

func (t *tries) String() string     { return strings.Join(*t, ",") }
func (t *tries) Set(v string) error { *t = append(*t, v); return nil }

type candidate struct {
	label string
	model string
	llm   provider.LLM
}

type sample struct {
	first, clause, total time.Duration
	err                  error
}

// What a caller says, and so what the backend is asked. Short on purpose: the
// prompt a voice session sends is a persona and a sentence.
var questions = []string{
	"你好，今天天气怎么样？",
	"帮我查一下明天上午去上海的航班有哪些。",
	"你能给我讲一个简短的笑话吗？",
	"我想了解一下你们周末的营业时间。",
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "llmprobe:", err)
		os.Exit(1)
	}
}

func run() error {
	var extra tries
	var (
		envPath  = flag.String("env", ".env.local", "dotenv file with credentials")
		profile  = flag.String("profile", "", "golive profile to take the instructions and backend model from")
		name     = flag.String("llm", "deepseek", "the registered backend to probe as the baseline; empty skips it")
		runs     = flag.Int("runs", 8, "measured requests per candidate")
		warmup   = flag.Int("warmup", 1, "unmeasured requests per candidate first, to pay for the connection")
		thinking = flag.Bool("thinking", false, "leave reasoning on, where a model has it")
		pool     = flag.Bool("pool", false, "measure the baseline's key pool: one request down every lane at once, with a key per lane and then with every lane on one key")
	)
	flag.Var(&extra, "try", "another candidate, as label|base URL|model|KEY_ENV; repeatable")
	flag.Parse()

	if err := config.LoadDotEnv(*envPath); err != nil {
		fmt.Fprintf(os.Stderr, "llmprobe: %v (continuing with the process environment)\n", err)
	}
	instructions := "你是一个电话语音助手。用简短口语化的中文回答，每次一到两句话。"
	model := ""
	if *profile != "" {
		cfg, err := config.Load(*profile)
		if err != nil {
			return err
		}
		if cfg.Instructions != "" {
			instructions = cfg.Instructions
		}
		model = cfg.BackendModel
	}

	var cands []candidate
	if *name != "" {
		llm, err := provider.OpenLLM(*name)
		if err != nil {
			return fmt.Errorf("%s: %w", *name, err)
		}
		m := config.Env("GOLIVE_BACKEND_MODEL", model)
		cands = append(cands, candidate{label: *name, model: m, llm: llm})
	}
	for _, spec := range extra {
		parts := strings.Split(spec, "|")
		if len(parts) != 4 {
			return fmt.Errorf("-try %q: want label|base URL|model|KEY_ENV", spec)
		}
		llm, err := deepseek.NewNamed("", parts[3], parts[1], parts[2])
		if err != nil {
			return fmt.Errorf("-try %s: %w", parts[0], err)
		}
		// NewNamed lets the shared GOLIVE_BACKEND_* variables win; a candidate
		// named on the command line is exactly what was typed.
		llm.BaseURL = strings.TrimRight(parts[1], "/")
		llm.DefaultModel = parts[2]
		cands = append(cands, candidate{label: parts[0], model: parts[2], llm: llm})
	}
	if len(cands) == 0 {
		return fmt.Errorf("nothing to probe")
	}
	if *pool {
		return runPool(cands[0].llm, cands[0].label, cands[0].model, instructions, *runs, *warmup, !*thinking)
	}

	results := make([][]sample, len(cands))
	total := *warmup + *runs
	for i := 0; i < total; i++ {
		q := questions[i%len(questions)]
		for c, cand := range cands {
			s := ask(cand, instructions, q, !*thinking)
			if s.err != nil {
				fmt.Printf("  %-12s FAILED: %v\n", cand.label, s.err)
			}
			if i >= *warmup {
				results[c] = append(results[c], s)
			}
		}
	}

	fmt.Printf("\n%-12s %-28s %5s %6s   %s\n", "", "model", "ok", "", "first token ms: min / p50 / p90 / max    first clause p50    whole answer p50")
	for c, cand := range cands {
		var first, clause, whole []time.Duration
		for _, s := range results[c] {
			if s.err != nil {
				continue
			}
			first = append(first, s.first)
			clause = append(clause, s.clause)
			whole = append(whole, s.total)
		}
		m := cand.model
		if m == "" {
			m = "(provider default)"
		}
		if len(first) == 0 {
			fmt.Printf("%-12s %-28s %2d/%-2d  no successful request\n", cand.label, m, 0, len(results[c]))
			continue
		}
		fmt.Printf("%-12s %-28s %2d/%-2d %6s   %5d / %5d / %5d / %5d %19d %19d\n",
			cand.label, m, len(first), len(results[c]), "",
			pct(first, 0), pct(first, 0.5), pct(first, 0.9), pct(first, 1),
			pct(clause, 0.5), pct(whole, 0.5))
	}
	fmt.Println("\nfirst clause: six characters of answer, which is what golive waits for before it starts the synthesizer.")
	return nil
}

func ask(c candidate, instructions, question string, noThinking bool) sample {
	return askOn(context.Background(), c, instructions, question, noThinking)
}

func askOn(parent context.Context, c candidate, instructions, question string, noThinking bool) sample {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	started := time.Now()
	deltas, err := c.llm.Stream(ctx, provider.LLMRequest{
		Model: c.model,
		Messages: []provider.Message{
			{Role: "system", Content: instructions},
			{Role: "user", Content: question},
		},
		Temperature:     0.7,
		MaxTokens:       120,
		DisableThinking: noThinking,
	})
	if err != nil {
		return sample{err: err}
	}
	var s sample
	chars := 0
	for d := range deltas {
		if d.Err != nil {
			return sample{err: d.Err}
		}
		if d.Text == "" {
			continue
		}
		if s.first == 0 {
			s.first = time.Since(started)
		}
		chars += len([]rune(d.Text))
		if s.clause == 0 && chars >= 6 {
			s.clause = time.Since(started)
		}
	}
	s.total = time.Since(started)
	if s.first == 0 {
		return sample{err: fmt.Errorf("the answer was empty")}
	}
	if s.clause == 0 {
		s.clause = s.total
	}
	return s
}

func pct(v []time.Duration, p float64) int64 {
	s := append([]time.Duration(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	k := int(float64(len(s)-1)*p + 0.5)
	return s[k].Milliseconds()
}
