// golive-simctl runs receiving-only suites and independent post-call evaluation.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/chuanmingliu/golive/internal/simclient"
	"github.com/chuanmingliu/golive/internal/simeval"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(code)
}

// Exit codes: 0 = every requested check passed, 1 = failed/indeterminate verdict,
// 2 = usage, suite input/configuration/transport, or persistence error.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, env func(string) string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: golive-simctl suite|evaluate [flags]")
		return 2
	}
	switch args[0] {
	case "suite":
		return suiteCommand(ctx, args[1:], stdout, stderr, env)
	case "evaluate":
		return evaluateCommand(ctx, args[1:], stdout, stderr, env)
	default:
		fmt.Fprintln(stderr, "unknown command")
		return 2
	}
}
func suiteCommand(ctx context.Context, args []string, stdout, stderr io.Writer, env func(string) string) int {
	fs := flag.NewFlagSet("suite", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	file := fs.String("file", "", "strict suite manifest")
	url := fs.String("url", "http://127.0.0.1:18865", "control origin")
	tokenEnv := fs.String("token-env", "SIM_CONTROL_TOKEN", "environment variable containing control token")
	output := fs.String("output", "", "private artifact directory")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *file == "" || *output == "" || !environmentName(*tokenEnv) {
		fmt.Fprintln(stderr, "suite requires --file PATH --output DIR [--url ORIGIN] [--token-env NAME]")
		return 2
	}
	suite, err := simclient.LoadSuite(*file)
	if err != nil {
		return errorCode(stderr, err)
	}
	client, err := simclient.New(*url, env(*tokenEnv))
	if err != nil {
		return errorCode(stderr, err)
	}
	runner := simclient.Runner{Client: client, Ready: stdout, Output: *output}
	summary, err := runner.Run(ctx, suite)
	if err != nil {
		return errorCode(stderr, err)
	}
	if summary.Counts.Fail > 0 || summary.Counts.Indeterminate > 0 {
		return 1
	}
	return 0
}
func evaluateCommand(ctx context.Context, args []string, stdout, stderr io.Writer, env func(string) string) int {
	fs := flag.NewFlagSet("evaluate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	scenario := fs.String("scenario", "", "scenario JSON")
	report := fs.String("report", "", "terminal report JSON")
	transcript := fs.String("transcript", "", "retained transcript envelope")
	output := fs.String("output", "", "separate private evaluation artifact")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *scenario == "" || *report == "" || *transcript == "" || *output == "" {
		fmt.Fprintln(stderr, "evaluate requires --scenario PATH --report PATH --transcript PATH --output PATH")
		return 2
	}
	for _, input := range []string{*scenario, *report, *transcript} {
		if sameFile(input, *output) {
			return errorCode(stderr, errors.New("evaluation output must be separate from inputs"))
		}
	}
	s, r, t, err := simeval.LoadEvidence(*scenario, *report, *transcript)
	if err != nil {
		// Invalid/missing evidence still produces an explicit indeterminate
		// artifact without trusting any partially decoded IDs or hashes.
		artifact := simeval.Artifact{SchemaVersion: 1, PromptVersion: simeval.PromptVersion, Grades: []simeval.Grade{}, Verdict: "indeterminate", Reason: "invalid_input_artifacts"}
		if saveErr := simclient.WritePrivate(*output, artifact); saveErr != nil {
			return errorCode(stderr, saveErr)
		}
		fmt.Fprintln(stderr, err.Error())
		return 1
	}
	config := simeval.Config{BaseURL: env("SIM_EVAL_BASE_URL"), Model: env("SIM_EVAL_MODEL"), APIKey: env("SIM_EVAL_API_KEY")}
	artifact, evalErr := simeval.Evaluate(ctx, s, r, t, config)
	if err = simclient.WritePrivate(*output, artifact); err != nil {
		return errorCode(stderr, err)
	}
	if evalErr != nil {
		fmt.Fprintln(stderr, evalErr.Error())
		return 1
	}
	fmt.Fprintln(stdout, artifact.Verdict)
	if artifact.Verdict != "pass" {
		return 1
	}
	return 0
}
func errorCode(w io.Writer, err error) int { fmt.Fprintln(w, err.Error()); return 2 }
func environmentName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func sameFile(a, b string) bool {
	aa, err := filepath.Abs(a)
	if err != nil {
		return true
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		return true
	}
	if aa == bb {
		return true
	}
	ai, aerr := os.Stat(aa)
	bi, berr := os.Stat(bb)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}
