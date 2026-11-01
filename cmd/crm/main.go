// Command crm serves the lead service's API: templates, rendering, and
// placing a call on a rendered prompt.
//
//	crm -dir ~/golive-crm -addr 127.0.0.1:8791
//	crm -dir ~/golive-crm -verify       # check the event log and exit
//
// Every request but /healthz carries the token kept in <dir>/api.token:
//
//	curl -H "Authorization: Bearer $(cat ~/golive-crm/api.token)" \
//	     -X PUT --data-binary @prompt.j2 http://127.0.0.1:8791/api/templates/car-renewal
//	curl -H "Authorization: Bearer $(cat ~/golive-crm/api.token)" \
//	     -d '{"template":"car-renewal","vars":{"carNum":"...","start":"..."}}' \
//	     http://127.0.0.1:8791/api/render
//	curl ... -d '{"template":"car-renewal","vars":{...}}' http://127.0.0.1:8791/api/call
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chuanmingliu/golive/internal/crm"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "crm:", err)
		os.Exit(1)
	}
}

func run() error {
	home, _ := os.UserHomeDir()
	var (
		addr     = flag.String("addr", "127.0.0.1:8791", "addresses to listen on, comma-separated; never one a tunnel publishes")
		dir      = flag.String("dir", filepath.Join(home, "golive-crm"), "where everything is kept: the event log, templates, rendered prompts, the token")
		origin   = flag.String("originator", "http://127.0.0.1:3000", "the call originator")
		envFile  = flag.String("originator-env", filepath.Join(home, "aria", ".env"), "the originator's env file, which names the number it will dial")
		golive   = flag.String("golive", "http://127.0.0.1:8765", "the phone line's golive, asked whether a call is in progress; empty skips the question")
		quoteAPI = flag.String("quote-api", os.Getenv("CRM_QUOTE_API_BASE"), "the business's quotation service, up to and including /intranet (CRM_QUOTE_API_BASE); overrides the quote-api config's base_url; empty keeps the config's")
		logPath  = flag.String("log", "", "write the operational log here as well as to stderr (default <dir>/crm.log)")
		zone     = flag.String("timezone", "Asia/Shanghai", "the time zone nowDateTime is written in")
		verify   = flag.Bool("verify", false, "check the event log against its checksums, say how many events it holds, and exit")
		showVer  = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return nil
	}
	if *verify {
		n, err := crm.VerifyLog(*dir)
		if err != nil {
			return err
		}
		fmt.Printf("%d events, every checksum holds\n", n)
		return nil
	}
	loc, err := time.LoadLocation(*zone)
	if err != nil {
		loc = time.FixedZone("UTC+8", 8*3600)
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	if *logPath == "" {
		*logPath = filepath.Join(*dir, "crm.log")
	}
	lf, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	log := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, lf), &slog.HandlerOptions{Level: slog.LevelInfo}))

	svc, err := crm.Open(crm.Config{Dir: *dir, OriginatorURL: *origin, OriginatorEnv: *envFile, GoliveURL: *golive, QuoteAPIBase: *quoteAPI, Location: loc, Log: log})
	if err != nil {
		return err
	}
	defer svc.Close()

	srv := &http.Server{Handler: svc.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errs := make(chan error, 4)
	for _, a := range strings.Split(*addr, ",") {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		ln, err := net.Listen("tcp", a)
		if err != nil {
			return err
		}
		log.Info("crm: listening", "addr", a, "version", version)
		go func() { errs <- srv.Serve(ln) }()
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-stop:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}
