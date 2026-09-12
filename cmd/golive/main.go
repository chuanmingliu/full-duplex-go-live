// Command golive runs the full-duplex realtime audio service.
//
// It speaks the gpt-live-1 event protocol over a WebSocket at /v1/live and
// simulates full duplex with a concurrent ASR/LLM/TTS cascade. See README.md
// for the protocol surface and the design notes in internal/duplex.
package main

import (
	"context"
	"encoding/json"
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
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/server"

	// Provider adapters register themselves. Each factory is lazy, so an
	// unconfigured vendor costs nothing until a session asks for it.
	_ "github.com/chuanmingliu/golive/internal/provider/deepseek"
	_ "github.com/chuanmingliu/golive/internal/provider/minimax"
	_ "github.com/chuanmingliu/golive/internal/provider/mock"
	_ "github.com/chuanmingliu/golive/internal/provider/tencent"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "golive:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		profilePath = flag.String("profile", "", "path to a JSON profile (see configs/)")
		envPath     = flag.String("env", ".env.local", "path to a dotenv file with credentials")
		addr        = flag.String("addr", "", "listen address (overrides the profile)")
		printConfig = flag.Bool("print-config", false, "print the resolved configuration and exit")
	)
	flag.Parse()

	if err := config.LoadDotEnv(*envPath); err != nil {
		return fmt.Errorf("loading %s: %w", *envPath, err)
	}

	cfg, err := config.Load(*profilePath)
	if err != nil {
		return err
	}
	if *addr != "" {
		cfg.Addr = *addr
	}

	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)

	if *printConfig {
		return printResolved(cfg)
	}

	log.Info("providers registered",
		"asr", provider.ASRNames(),
		"llm", provider.LLMNames(),
		"tts", provider.TTSNames())
	log.Info("provider selection", "asr", cfg.ASR, "llm", cfg.LLM, "tts", cfg.TTS)

	// Fail at start-up rather than on the first caller's session: a voice
	// service that accepts a connection and then cannot synthesize is worse
	// than one that refuses to boot.
	if err := preflight(cfg); err != nil {
		return err
	}

	srv := server.NewServer(cfg, log)
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: a live session is an open WebSocket for minutes.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "endpoint", "ws://"+cfg.Addr+"/v1/live")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

// preflight constructs each selected provider once so a missing credential is
// reported now, with the variable's name, instead of as a failed session later.
func preflight(cfg config.Config) error {
	if _, err := provider.OpenASR(cfg.ASR); err != nil {
		return fmt.Errorf("asr %q is not usable: %w", cfg.ASR, err)
	}
	if _, err := provider.OpenLLM(cfg.LLM); err != nil {
		return fmt.Errorf("llm %q is not usable: %w", cfg.LLM, err)
	}
	if _, err := provider.OpenTTS(cfg.TTS); err != nil {
		return fmt.Errorf("tts %q is not usable: %w", cfg.TTS, err)
	}
	return nil
}

func printResolved(cfg config.Config) error {
	enc := newIndentEncoder(os.Stdout)
	return enc.Encode(cfg)
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func newIndentEncoder(w *os.File) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc
}
