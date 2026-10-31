// golive-telephony is an experimental, explicitly invoked outbound SIP adapter.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/chuanmingliu/golive/internal/telephony"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	configPath := flag.String("config", "", "private adapter JSON configuration")
	callPath := flag.String("call", "", "private call JSON: call_id, instructions, greeting, voice")
	dial := flag.Bool("dial", false, "place ONE real call to the configured destination; otherwise only check readiness")
	flag.Parse()
	if *configPath == "" || flag.NArg() != 0 {
		return fmt.Errorf("usage: golive-telephony -config private.json [-call call.json -dial]")
	}
	f, err := os.Open(*configPath)
	if err != nil {
		return err
	}
	defer f.Close()
	cfg, err := telephony.LoadConfig(f)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if !*dial {
		if err := telephony.Check(ctx, cfg); err != nil {
			return err
		}
		fmt.Println("Ready: dedicated agent is idle. No call placed.")
		return nil
	}
	fcall, err := os.Open(*callPath)
	if err != nil {
		return err
	}
	defer fcall.Close()
	var call telephony.Call
	d := json.NewDecoder(io.LimitReader(fcall, 128<<10))
	d.DisallowUnknownFields()
	if err = d.Decode(&call); err != nil {
		return err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("call file must contain one JSON object")
	}
	stats, err := telephony.Run(ctx, cfg, call)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"call_id": call.ID, "media": stats, "failed": err != nil})
	return err
}
