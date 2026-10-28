#!/usr/bin/env bash
# Start golive and serve the browser demo.
#
#   ./start.sh              mock providers, no credentials, full debug logging
#   ./start.sh real         Tencent + DeepSeek + MiniMax, reads .env.local
#   ./start.sh prod         production profile (auth required, json logs, no demo)
#   ./start.sh telephony    the voice agent behind a SIP gateway: OpenAI Realtime
#                           protocol on 127.0.0.1:8765, reads .env.local
#   PORT=9000 ./start.sh    listen somewhere else
#
# If a Go toolchain is present the binary is rebuilt from source first, so what
# you run is always what is in the tree. Otherwise the prebuilt binary for this
# platform in bin/ is used, which is what makes this project runnable on a
# machine with no Go installed.
#
# Everything the server logs goes to the terminal *and* to golive.log.
set -euo pipefail
cd "$(dirname "$0")"

MODE="${1:-mock}"
if [ "$MODE" = telephony ]; then
  # Where a media gateway looks for its agent, and nowhere else: this mode
  # serves a phone line, not a browser.
  PORT="${PORT:-8765}"
  BIND="${BIND:-127.0.0.1}"
  LOGFILE="${LOGFILE:-golive-telephony.log}"
else
  PORT="${PORT:-8080}"
  BIND="${BIND:-}"
  LOGFILE="${LOGFILE:-golive.log}"
fi

case "$(uname -s)" in
  Darwin) OS=darwin ;;
  Linux)  OS=linux ;;
  *) echo "unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  arm64|aarch64) ARCH=arm64 ;;
  x86_64|amd64)  ARCH=amd64 ;;
  *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;;
esac

BIN="bin/golive"
# Only where there is source to build: a deploy bundle carries binaries and no
# go.mod, and on a box that happens to have Go that used to fail the start.
if command -v go >/dev/null 2>&1 && [ -f go.mod ]; then
  echo "· building from source"
  GOPROXY="${GOPROXY:-direct}" \
    go build -ldflags "-s -w" -o bin/golive ./cmd/golive
  GOPROXY="${GOPROXY:-direct}" \
    go build -ldflags "-s -w" -o bin/golivectl ./cmd/golivectl
else
  BIN="bin/golive-$OS-$ARCH"
  if [ ! -x "$BIN" ]; then
    echo "no Go toolchain and no prebuilt binary at $BIN" >&2
    echo "install Go, or run 'make dist' on a machine that has it" >&2
    exit 1
  fi
  echo "· no Go toolchain found; using the prebuilt $OS/$ARCH binary"
fi

chmod +x bin/* 2>/dev/null || true

# macOS quarantines anything that arrived over the network. Without this the
# binary is killed on launch with a Gatekeeper dialog rather than an error.
if [ "$OS" = darwin ]; then
  xattr -dr com.apple.quarantine bin 2>/dev/null || true
fi

LOG_LEVEL="${GOLIVE_LOG_LEVEL:-}"
case "$MODE" in
  prod)
    if [ ! -f .env.local ]; then
      echo "prod mode needs .env.local — copy .env.example and fill it in" >&2
      echo "GOLIVE_AUTH_TOKEN is required" >&2
      exit 1
    fi
    PROFILE=configs/production.json
    ENVFILE=.env.local
    LOG_LEVEL="${LOG_LEVEL:-info}"
    ;;
  real)
    if [ ! -f .env.local ]; then
      echo "real mode needs .env.local — copy .env.example and fill it in" >&2
      exit 1
    fi
    PROFILE=configs/tencent-deepseek-minimax.json
    ENVFILE=.env.local
    LOG_LEVEL="${LOG_LEVEL:-info}"
    ;;
  telephony)
    ENVFILE="${ENVFILE:-.env.local}"
    if [ ! -f "$ENVFILE" ]; then
      echo "telephony mode needs provider keys — $ENVFILE not found" >&2
      echo "point ENVFILE at an existing one:  ENVFILE=~/golive/.env.local ./start.sh telephony" >&2
      exit 1
    fi
    PROFILE=configs/telephony.json
    # Debug: the first calls on a new line are the ones you will want to read
    # back turn by turn. Transcripts are in it; set GOLIVE_LOG_LEVEL=info once
    # it is behaving.
    LOG_LEVEL="${LOG_LEVEL:-debug}"
    ;;
  *)
    PROFILE=configs/mock.json
    ENVFILE=/dev/null
    LOG_LEVEL="${LOG_LEVEL:-debug}"
    MODE=mock
    ;;
esac

echo
if [ "$MODE" = telephony ]; then
  echo "  golive — voice agent for a SIP gateway"
  echo "  agent: ws://$BIND:$PORT/v1/realtime   (OpenAI Realtime protocol)"
  echo "  calls: curl -s http://$BIND:$PORT/v1/pool"
else
  echo "  golive — $MODE providers"
  echo "  open:  http://localhost:$PORT"
fi
echo "  log:   $(pwd)/$LOGFILE"
echo "  stop:  Ctrl-C"
echo

# Mock keeps debug so the demo is inspectable. Real/prod inherit info unless
# GOLIVE_LOG_LEVEL is set — debug logs contain transcripts.
GOLIVE_LOG_LEVEL="$LOG_LEVEL" \
  "./$BIN" -profile "$PROFILE" -env "$ENVFILE" -addr "$BIND:$PORT" 2>&1 | tee "$LOGFILE"
