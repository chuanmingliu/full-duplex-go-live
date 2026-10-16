// Command desk serves the desk: where Claude and Codex leave each other
// notes, tasks and results, with the owner watching.
//
//	desk -dir ~/golive-desk -addr 127.0.0.1:8792,192.168.71.9:8792
//	desk -dir ~/golive-desk -verify
//
// Each participant's token is in <dir>/<name>.token (owner, claude, codex),
// made on the first start. Every request but /healthz carries one:
//
//	curl -H "Authorization: Bearer $(cat ~/golive-desk/claude.token)" \
//	     -d '{"to":"codex","kind":"task","subject":"…","body":"…"}' http://127.0.0.1:8792/api/notes
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/chuanmingliu/golive/internal/desk"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "desk:", err)
		os.Exit(1)
	}
}

func run() error {
	home, _ := os.UserHomeDir()
	var (
		addr    = flag.String("addr", "127.0.0.1:8792", "addresses to listen on, comma-separated; never one a tunnel publishes")
		dir     = flag.String("dir", filepath.Join(home, "golive-desk"), "the log and the tokens")
		logPath = flag.String("log", "", "the operational log (default <dir>/desk.log)")
		dials   = flag.String("sim-dials", filepath.Join(home, "golive-sim", "data", "dials.jsonl"), "the record of calls placed to the simulated customer (phone-brain.sh call sim writes it); empty turns the calls page off")
		simURL  = flag.String("simulator", "http://127.0.0.1:18865", "the simulated customer's control URL")
		simTok  = flag.String("sim-tokens", filepath.Join(home, "golive-sim", "tokens.env"), "its tokens file, read for SIM_CONTROL_TOKEN; never sent to the page")
		journal = flag.String("journal", "http://127.0.0.1:8790", "the call journal, as this process reaches it")
		jport   = flag.Int("journal-port", 8790, "the journal's port as a browser on the home network reaches it, on this host; the page embeds its call view")
		verify  = flag.Bool("verify", false, "check the log and exit")
		showVer = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return nil
	}
	if *verify {
		n, err := desk.Verify(*dir)
		if err != nil {
			return err
		}
		fmt.Printf("%d entries, every checksum holds\n", n)
		return nil
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	if *logPath == "" {
		*logPath = filepath.Join(*dir, "desk.log")
	}
	lf, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	log := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, lf), nil))
	var calls *desk.Calls
	if *dials != "" {
		calls = &desk.Calls{Dials: *dials, Simulator: *simURL, SimTokens: *simTok, Journal: *journal, JournalPort: *jport}
	}
	s, err := desk.Open(desk.Config{Dir: *dir, Log: log, Calls: calls})
	if err != nil {
		return err
	}
	defer s.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	addrs := strings.Split(*addr, ",")
	log.Info("desk: serving", "addr", addrs, "dir", *dir, "version", version)
	return desk.Serve(ctx, s, addrs)
}
