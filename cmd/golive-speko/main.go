// Command golive-speko serves gpt-live-1 through Speko's hosted voice router.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chuanmingliu/golive/internal/config"
	"github.com/chuanmingliu/golive/internal/speko"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "golive-speko:", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:8095", "listen address")
	env := flag.String("env", ".env.local", "dotenv file with server-side credentials")
	max := flag.Int("max-sessions", 32, "maximum concurrent sessions")
	duration := flag.Duration("session-timeout", time.Hour, "maximum session duration")
	flag.Parse()
	if err := config.LoadDotEnv(*env); err != nil {
		return err
	}
	cfg := speko.FromEnv()
	cfg.MaxSessions, cfg.SessionTimeout = *max, *duration
	relay, err := speko.New(cfg)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr: *addr, Handler: relay.Handler(),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() { result <- srv.ListenAndServe() }()
	slog.Info("Speko relay listening", "addr", *addr, "model", speko.Model,
		"credentials_configured", cfg.APIKey != "" && cfg.AuthToken != "")
	select {
	case err = <-result:
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	relayErr := relay.Shutdown(shutdown)
	httpErr := srv.Shutdown(shutdown)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return errors.Join(relayErr, httpErr)
}
