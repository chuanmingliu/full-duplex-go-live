// Command quotesvc is the vehicle quotation service with the business's
// interface (internal/quotesvc), run on the test box.
//
//	quotesvc -dir ~/golive-quotesvc -addr 127.0.0.1:8793 -compute-ms 2500 -jitter-ms 2000
//	quotesvc -dir ~/golive-quotesvc -add-vehicle '{"plate":"…","price":700000,"age":5,"seats":5,"ncd":0}'
//
// The lead service reaches it as quote-api base_url http://127.0.0.1:8793/intranet.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chuanmingliu/golive/internal/quotesvc"
)

var version = "dev"

func main() {
	home, _ := os.UserHomeDir()
	var (
		addr    = flag.String("addr", "127.0.0.1:8793", "addresses to listen on, comma-separated")
		dir     = flag.String("dir", filepath.Join(home, "golive-quotesvc"), "where the vehicle table, the rating table, the token and the quote log are kept")
		compute = flag.Int("compute-ms", 2500, "how long pricing takes, before jitter (the real service's seconds)")
		jitter  = flag.Int("jitter-ms", 2000, "random extra on top")
		workers = flag.Int("workers", 2, "inquiries priced at once")
		add     = flag.String("add-vehicle", "", "add a vehicle (JSON: plate, price, age, seats, ncd, phone) and exit")
		showVer = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	svc, err := quotesvc.Open(quotesvc.Options{Dir: *dir, ComputeMS: *compute, JitterMS: *jitter, Workers: *workers, Log: log})
	if err != nil {
		fmt.Fprintln(os.Stderr, "quotesvc:", err)
		os.Exit(1)
	}
	if *add != "" {
		var v quotesvc.Vehicle
		if err := json.Unmarshal([]byte(*add), &v); err != nil || v.Plate == "" {
			fmt.Fprintln(os.Stderr, "quotesvc: -add-vehicle wants JSON with plate, price, age, seats, ncd")
			os.Exit(2)
		}
		if err := svc.AddVehicle(v); err != nil {
			fmt.Fprintln(os.Stderr, "quotesvc:", err)
			os.Exit(1)
		}
		fmt.Printf("vehicle added; %d known\n", svc.Vehicles())
		return
	}
	handler := svc.Handler()
	var servers []*http.Server
	for _, a := range strings.Split(*addr, ",") {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		ln, err := net.Listen("tcp", a)
		if err != nil {
			fmt.Fprintln(os.Stderr, "quotesvc:", err)
			os.Exit(1)
		}
		srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
		servers = append(servers, srv)
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("quotesvc: serve", "err", err)
			}
		}()
	}
	log.Info("quotesvc: up", "version", version, "addr", *addr, "dir", *dir, "vehicles", svc.Vehicles(), "compute_ms", *compute, "jitter_ms", *jitter)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	for _, srv := range servers {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = srv.Shutdown(c)
		cancel()
	}
}
