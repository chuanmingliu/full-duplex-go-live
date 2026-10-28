// Command journal serves the call journal: every phone call golive has
// answered, timed stage by stage from golive's log, with the recording when
// one was kept, what was written about it afterwards, and the list of what
// has gone wrong and been fixed.
//
// It reads golive's log and store and takes no part in a call. It has no
// login, and the page can change nothing but the owner's notes on a call: it
// is meant for a home network, where it listens, and not for a public address.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/chuanmingliu/golive/internal/journal"
)

func main() {
	var (
		addr   = flag.String("addr", "127.0.0.1:8790", "address to listen on; the home-network address, never one a tunnel publishes")
		target = flag.Int("target", journal.TargetMS, "the latency goal in ms, caller's last sound to first audio at p95")
		logp   = flag.String("log", "golive-telephony.log", "golive's log")
		store  = flag.String("store", "", "golive's store_dir, where recorded calls are kept; empty when calls are not recorded")
		data   = flag.String("data", "journal-data", "where the journal keeps calls as last read and the owner's notes")
		notes  = flag.String("notes", "journal", "retros/<call id>.md, issues.json and decisions.json")
		repo   = flag.String("repo", "", "repository URL, for turning a commit hash into a link")
		keep   = flag.Int("retention-hours", 0, "how long golive keeps a recorded call, for the page to say")
		ui     = flag.String("ui", "", "serve the page from this directory instead of the one built in (for working on it)")
		arch   = flag.String("archive", "", "where finished calls are kept for good: recording, conversation, measurements, log; empty archives nothing")
		last   = flag.String("last-call", "", "golive's last_call_file; the archive keeps a copy of each distinct set of instructions")
		dials  = flag.String("sim-dials", "", "the record of calls placed to the simulated customer (phone-brain.sh call sim writes it); such calls are marked, the rest are real")
		check  = flag.Bool("verify", false, "read every archived file again, compare it with its manifest, print what is wrong, and exit")
		list   = flag.Bool("archive-status", false, "print what the archive holds, in numbers, and exit")
	)
	flag.Parse()
	journal.TargetMS = *target
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *check || *list {
		os.Exit(archiveReport(*arch, *check))
	}
	srv := journal.NewServer(journal.Config{
		LogPath: *logp, StoreDir: *store, DataDir: *data, NotesDir: *notes, RepoURL: *repo, UIDir: *ui, RetentionHours: *keep,
		ArchiveDir: *arch, LastCallFile: *last, SimDials: *dials, Log: log,
	})
	// The archive is fed on a clock, not by whoever opens the page.
	go srv.Watch(context.Background(), 20*time.Second)
	hs := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Info("journal listening", "addr", *addr, "log", *logp, "store", *store, "data", *data, "notes", *notes, "archive", *arch)
	if err := hs.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "journal:", err)
		os.Exit(1)
	}
}

// archiveReport prints the archive in numbers — never a word of what was said
// — and, with verify, checks every file against its manifest.
func archiveReport(dir string, verify bool) int {
	a, err := journal.OpenArchive(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "journal:", err)
		return 1
	}
	t := a.Totals()
	fmt.Printf("archive: %s\n", t.Dir)
	fmt.Printf("calls: %d, of which %d with audio (%.1f minutes); %.1f MB\n", t.Calls, t.WithAudio, t.AudioS/60, float64(t.Bytes)/1e6)
	if entries := a.Entries(); len(entries) > 0 {
		fmt.Printf("from %s to %s\n", entries[len(entries)-1].StartedAt.Format("2006-01-02 15:04"), entries[0].StartedAt.Format("2006-01-02 15:04"))
	}
	if !verify {
		return 0
	}
	files, bytes, problems := a.Verify()
	fmt.Printf("verified: %d files, %.1f MB read\n", files, float64(bytes)/1e6)
	for _, p := range problems {
		fmt.Printf("PROBLEM  %s  %s: %s\n", p.ID, p.File, p.What)
	}
	if len(problems) > 0 {
		return 2
	}
	fmt.Println("every file matches its manifest")
	return 0
}
