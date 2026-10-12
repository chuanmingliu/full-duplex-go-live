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
		addr  = flag.String("addr", "127.0.0.1:8790", "address to listen on; the home-network address, never one a tunnel publishes")
		logp  = flag.String("log", "golive-telephony.log", "golive's log")
		store = flag.String("store", "", "golive's store_dir, where recorded calls are kept; empty when calls are not recorded")
		data  = flag.String("data", "journal-data", "where the journal keeps calls as last read and the owner's notes")
		notes = flag.String("notes", "journal", "retros/<call id>.md, issues.json and decisions.json")
		repo  = flag.String("repo", "", "repository URL, for turning a commit hash into a link")
		keep  = flag.Int("retention-hours", 0, "how long golive keeps a recorded call, for the page to say")
		ui    = flag.String("ui", "", "serve the page from this directory instead of the one built in (for working on it)")
	)
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	srv := journal.NewServer(journal.Config{
		LogPath: *logp, StoreDir: *store, DataDir: *data, NotesDir: *notes, RepoURL: *repo, UIDir: *ui, RetentionHours: *keep, Log: log,
	})
	hs := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Info("journal listening", "addr", *addr, "log", *logp, "store", *store, "data", *data, "notes", *notes)
	if err := hs.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "journal:", err)
		os.Exit(1)
	}
}
