// golive-simulator is an isolated receiving-only simulated customer service.
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
	"github.com/chuanmingliu/golive/internal/provider"
	_ "github.com/chuanmingliu/golive/internal/provider/deepseek"
	_ "github.com/chuanmingliu/golive/internal/provider/minimax"
	_ "github.com/chuanmingliu/golive/internal/provider/mock"
	_ "github.com/chuanmingliu/golive/internal/provider/tencent"
	"github.com/chuanmingliu/golive/internal/server"
	"github.com/chuanmingliu/golive/internal/simulator"
	"github.com/chuanmingliu/golive/internal/simulatorui"
)

var commit = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "golive-simulator:", err)
		os.Exit(1)
	}
}
func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func run() error {
	profile := flag.String("profile", "configs/simulator/mock.json", "isolated provider profile")
	addr := flag.String("addr", env("SIM_ADDR", "127.0.0.1:18865"), "receiving HTTP listen address")
	data := flag.String("data", env("SIM_DATA_DIR", "deploy/simulator/data"), "private simulator artifacts directory")
	initGateway := flag.String("init-gateway-config", "", "render isolated gateway config then exit")
	flag.Parse()
	if *initGateway != "" {
		return writeGatewayConfig(*initGateway)
	}
	// Deliberately do not load the project's .env.local or existing deployment config.
	for _, key := range []string{"SIM_CONTROL_TOKEN", "SIM_WEBHOOK_TOKEN", "SIM_MEDIA_TOKEN"} {
		if len(os.Getenv(key)) < 32 {
			return fmt.Errorf("%s must contain at least 32 characters", key)
		}
	}
	cfg, err := config.Load(*profile)
	if err != nil {
		return err
	}
	cfg.AllowProviderOverride = false
	cfg.WebRoot = ""
	cfg.EnablePprof = false
	for _, check := range []func() error{
		func() error { _, e := provider.OpenASR(cfg.ASR); return e },
		func() error { _, e := provider.OpenLLM(cfg.LLM); return e },
		func() error { _, e := provider.OpenTTS(cfg.TTS); return e },
	} {
		if err = check(); err != nil {
			return fmt.Errorf("provider preflight: %w", err)
		}
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		// Provider logs can contain transcripts; retain only operational metadata.
		switch a.Key {
		case "text", "transcript", "instructions", "audio", "delta", "url", "err":
			a.Value = slog.StringValue("[redacted]")
		}
		return a
	}}))
	receiver, err := server.NewSimulatorReceiver(cfg, log, os.Getenv("SIM_MEDIA_TOKEN"))
	if err != nil {
		return err
	}
	destination := env("SIM_DESTINATION", "simulator-receiver")
	gatewayID := env("SIM_GATEWAY_ID", "isolated-activecall")
	domain := env("SIM_SIP_DOMAIN", "simulator.invalid")
	gateway, err := simulator.NewActiveCall(simulator.ActiveCallOptions{
		ControlURL: env("SIM_ACTIVECALL_URL", "http://127.0.0.1:18881"), RealtimeURL: env("SIM_REALTIME_URL", "ws://127.0.0.1:18865/v1/realtime"),
		WebhookToken: os.Getenv("SIM_WEBHOOK_TOKEN"), MediaToken: os.Getenv("SIM_MEDIA_TOKEN"), Destination: destination, GatewayIdentity: gatewayID, SIPDomain: domain,
	})
	if err != nil {
		return err
	}
	defer gateway.Close()
	controller, err := simulator.NewController(simulator.Options{Telephony: gateway, Voice: receiver, Token: os.Getenv("SIM_CONTROL_TOKEN"), Destination: destination, GatewayIdentity: gatewayID, DataDir: *data, SourceCommit: commit, ProviderModel: cfg.BackendModel, VoiceID: cfg.Voice, SetupTimeout: 25 * time.Second, DialTargetReference: "sip:<run_id>@" + domain})
	if err != nil {
		return err
	}
	receiver.SetController(controller)
	gateway.SetController(controller)
	mux := http.NewServeMux()
	workbench := simulatorui.New(cfg, os.Getenv("SIM_CONTROL_TOKEN"), gateway.Ready, controller.Handler())
	mux.Handle("/sim/", workbench)
	mux.Handle("/", workbench)
	mux.Handle("/active-call/incoming", gateway)
	mux.Handle("/v1/realtime", receiver)
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if gateway.Ready(ctx) != nil {
			http.Error(w, "gateway unavailable", 503)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ended := make(chan error, 1)
	go func() { ended <- srv.ListenAndServe() }()
	select {
	case err = <-ended:
	case <-ctx.Done():
	}
	_ = controller.Close()
	_ = gateway.Close()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
