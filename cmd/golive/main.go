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
	"github.com/chuanmingliu/golive/internal/metrics"
	"github.com/chuanmingliu/golive/internal/provider"
	"github.com/chuanmingliu/golive/internal/server"

	// Provider adapters register themselves. Each factory is lazy, so an
	// unconfigured vendor costs nothing until a session asks for it.
	_ "github.com/chuanmingliu/golive/internal/provider/deepseek"
	_ "github.com/chuanmingliu/golive/internal/provider/minimax"
	_ "github.com/chuanmingliu/golive/internal/provider/mock"
	_ "github.com/chuanmingliu/golive/internal/provider/tencent"
)

// Set at link time: -ldflags "-X main.version=... -X main.commit=..."
var (
	version = "dev"
	commit  = ""
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
		showVersion = flag.Bool("version", false, "print the build version and exit")
	)
	flag.Parse()

	if *showVersion {
		if commit != "" {
			fmt.Printf("golive %s (%s)\n", version, commit)
		} else {
			fmt.Printf("golive %s\n", version)
		}
		return nil
	}

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

	log := newLogger(cfg.LogLevel, cfg.LogFormat)
	slog.SetDefault(log)

	if *printConfig {
		return printResolved(cfg)
	}

	log.Info("providers registered",
		"asr", provider.ASRNames(),
		"llm", provider.LLMNames(),
		"tts", provider.TTSNames())
	log.Info("provider selection", "asr", cfg.ASR, "llm", cfg.LLM, "tts", cfg.TTS)
	if cfg.AuthToken != "" || cfg.AuthRequired {
		log.Info("auth enabled", "origins", len(cfg.AllowedOrigins), "max_sessions", cfg.MaxSessions)
	} else {
		log.Warn("auth is off; this process will accept unauthenticated sessions")
	}

	// Fail at start-up rather than on the first caller's session: a voice
	// service that accepts a connection and then cannot synthesize is worse
	// than one that refuses to boot.
	if err := preflight(cfg); err != nil {
		return err
	}
	prewarm(context.Background(), cfg, log)

	metrics.Default().SetBuild(version, commit)
	srv := server.NewServer(cfg, log)
	srv.Version = version
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		// No WriteTimeout: a live session is an open WebSocket for minutes.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		scheme := "ws"
		if cfg.TLSCertFile != "" {
			scheme = "wss"
		}
		log.Info("listening",
			"addr", cfg.Addr,
			"endpoint", scheme+"://"+cfg.Addr+"/v1/live",
			"version", version)
		var err error
		if cfg.TLSCertFile != "" {
			err = httpSrv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			err = httpSrv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	timeout := time.Duration(cfg.ShutdownTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	srv.Drain(drainCtx)
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutCancel()
	return httpSrv.Shutdown(shutCtx)
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

// prewarm opens the selected providers' idle connections so the first caller's
// first syllable does not pay a TLS handshake. A failure is not fatal: the
// same work simply happens later, on the critical path.
func prewarm(ctx context.Context, cfg config.Config, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	try := func(name string, v any) {
		p, ok := v.(provider.Prewarmer)
		if !ok {
			return
		}
		if err := p.Prewarm(ctx); err != nil {
			log.Debug("prewarm skipped", "provider", name, "err", err)
			return
		}
		log.Info("prewarmed", "provider", name)
	}
	if asr, err := provider.OpenASR(cfg.ASR); err == nil {
		try(cfg.ASR, asr)
	}
	if llm, err := provider.OpenLLM(cfg.LLM); err == nil {
		try(cfg.LLM, llm)
	}
	if tts, err := provider.OpenTTS(cfg.TTS); err == nil {
		try(cfg.TTS, tts)
	}
}

func printResolved(cfg config.Config) error {
	enc := newIndentEncoder(os.Stdout)
	return enc.Encode(cfg)
}

func newLogger(level, format string) *slog.Logger {
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
	opts := &slog.HandlerOptions{Level: lvl}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

func newIndentEncoder(w *os.File) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc
}
