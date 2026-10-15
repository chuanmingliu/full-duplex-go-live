// Command edge serves self-hosted speech models behind an OpenAI-compatible
// API. See docs/edge-api.md.
//
//	edge -addr 127.0.0.1:8030,192.168.1.9:8030 \
//	     -audio8-asr http://127.0.0.1:7860 \
//	     -audio8-tts http://127.0.0.1:8024
//
// Every request but /healthz carries the token in -key-file (made on first
// start, mode 600), unless -key-file is empty.
//
//	curl -H "Authorization: Bearer $(cat ~/golive-edge/api.token)" http://127.0.0.1:8030/v1/models
//	curl -H "Authorization: Bearer $(cat ~/golive-edge/api.token)" \
//	     -F model=audio8-asr -F file=@clip.wav http://127.0.0.1:8030/v1/audio/transcriptions
//	curl -H "Authorization: Bearer $(cat ~/golive-edge/api.token)" \
//	     -d '{"model":"audio8-tts","input":"你好","response_format":"wav"}' \
//	     http://127.0.0.1:8030/v1/audio/speech -o out.wav
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

	"github.com/chuanmingliu/golive/internal/edge"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "edge:", err)
		os.Exit(1)
	}
}

func run() error {
	home, _ := os.UserHomeDir()
	var (
		addr      = flag.String("addr", "127.0.0.1:8030", "addresses to listen on, comma-separated; never one a tunnel publishes")
		dir       = flag.String("dir", filepath.Join(home, "golive-edge"), "where the token and the log are kept")
		keyFile   = flag.String("key-file", "", "the bearer token's file (default <dir>/api.token); empty string for no token")
		noKey     = flag.Bool("no-key", false, "serve without a token (only on an address nothing but the LAN can reach)")
		asrURL    = flag.String("audio8-asr", "", "the Audio8-ASR ONNX service (http://127.0.0.1:7860); empty leaves it out")
		asrID     = flag.String("audio8-asr-id", "audio8-asr", "the model id it is served as")
		ttsURL    = flag.String("audio8-tts", "", "the Audio8 TTS 0.1B ONNX service (http://127.0.0.1:8024); empty leaves it out")
		ttsID     = flag.String("audio8-tts-id", "audio8-tts", "the model id it is served as")
		ttsTokens = flag.Int("audio8-tts-max-tokens", 512, "the most tokens one synthesis may run to (46 ms of audio each)")
		diarURL   = flag.String("diarize", "", "the streaming diarization service (http://127.0.0.1:8031); empty leaves it out")
		diarID    = flag.String("diarize-id", "nemotron-diar", "the model id it is served as")
		queueWait = flag.Duration("queue-wait", 10*time.Second, "how long a request waits for a busy model before it is refused with 429")
		logPath   = flag.String("log", "", "write the operational log here as well as to stderr (default <dir>/edge.log)")
		showVer   = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return nil
	}
	if *asrURL == "" && *ttsURL == "" && *diarURL == "" {
		return errors.New("nothing to serve: give -audio8-asr, -audio8-tts and/or -diarize")
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	if *logPath == "" {
		*logPath = filepath.Join(*dir, "edge.log")
	}
	lf, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	log := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, lf), &slog.HandlerOptions{Level: slog.LevelInfo}))

	srv := edge.NewServer()
	srv.Log = log
	srv.QueueWait = *queueWait
	if !*noKey {
		if *keyFile == "" {
			*keyFile = filepath.Join(*dir, "api.token")
		}
		key, err := loadOrMakeKey(*keyFile)
		if err != nil {
			return err
		}
		srv.Key = key
	}
	if *asrURL != "" {
		srv.Add(edge.NewAudio8ASR(*asrURL).Model(*asrID))
	}
	if *ttsURL != "" {
		t := edge.NewAudio8TTS(*ttsURL)
		t.MaxNewTokens = *ttsTokens
		srv.Add(t.Model(*ttsID))
	}
	if *diarURL != "" {
		srv.Add(edge.NewDiarizeUpstream(*diarURL).Model(*diarID))
	}
	for _, m := range srv.Models() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := m.Health(ctx)
		cancel()
		log.Info("edge: model", "id", m.ID, "kind", m.Kind, "upstream", m.Upstream, "licence", m.Licence, "status", statusOf(err))
	}

	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
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
		log.Info("edge: listening", "addr", a, "version", version, "token", srv.Key != "")
		go func() { errs <- hs.Serve(ln) }()
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return hs.Shutdown(ctx)
}

func statusOf(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

// loadOrMakeKey reads the token, or makes one and writes it mode 600.
func loadOrMakeKey(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if k := strings.TrimSpace(string(b)); k != "" {
			return k, nil
		}
	}
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	key := "edge-" + hex.EncodeToString(b[:])
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return "", err
	}
	return key, nil
}
