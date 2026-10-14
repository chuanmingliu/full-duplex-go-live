#!/usr/bin/env bash
# Put golive where the phone line's agent stands, and take it away again.
#
#   scripts/phone-brain.sh status     what holds the agent port, and the state of
#                                     the PBX and gateway around it. Changes nothing.
#   scripts/phone-brain.sh golive     stop the container on the agent port, start
#                                     golive there, check it answers
#   scripts/phone-brain.sh restart    replace a running golive with the build in bin/;
#                                     needs no sudo
#   scripts/phone-brain.sh bench      measure this box's real-provider latency
#                                     through golive, with no phone involved
#   scripts/phone-brain.sh net        how this box reaches the providers: its
#                                     egress, tunnels, and connect times
#   scripts/phone-brain.sh log [n] [re]   warnings, errors and turn timings from the
#                                     last n lines of golive's log (default 400);
#                                     with a regex, every engine line matching it
#   scripts/phone-brain.sh llm [args] the backend's time to first token from this
#                                     box; extra arguments go to llmprobe, e.g.
#                                     -try 'label|base URL|model|KEY_ENV'
#   scripts/phone-brain.sh tts        the synthesizer's time to first audio, on its
#                                     standard and its bidirectional endpoint
#   scripts/phone-brain.sh call       place the last answered call again: the same
#                                     instructions and opening line, to the one
#                                     number the originator allows. `call show`
#                                     says what is kept without dialling.
#   scripts/phone-brain.sh record on|off   keep each call's audio — caller and assistant,
#                                     two tracks — under CALLS_DIR for a day, or stop.
#                                     Takes effect at the next `restart`. Off unless
#                                     turned on; `record` alone says which.
#   scripts/phone-brain.sh echo       time the phone leg from the last recorded call:
#                                     how long the assistant's voice takes to come
#                                     back on the line. Prints numbers only.
#   scripts/phone-brain.sh journal [start|stop|status]
#                                     the call journal: every call timed stage by
#                                     stage, its recording, what was written about
#                                     it, the issues found and fixed. A page on the
#                                     home network (JOURNAL_ADDR), with no login.
#                                     It also archives every finished call, and
#                                     is started with golive.
#   scripts/phone-brain.sh demo [on|off|status]
#                                     the browser demo: a second golive of this
#                                     build with the test page, on DEMO_PORT
#                                     (9080), apart from the phone line's. For a
#                                     public hostname to point at. Once on, it
#                                     is started with golive.
#   scripts/phone-brain.sh archive [status|verify|mirror <dest>]
#                                     the permanent record of calls (ARCHIVE_DIR):
#                                     what it holds, whether every file still
#                                     matches its checksum, a second copy elsewhere
#   scripts/phone-brain.sh sync       run the gateway port sync now
#   scripts/phone-brain.sh restore    stop golive, start the container again
#
# Written for the stack on 192.168.71.9: an agent container on :8765 that Active
# Call and Aria both expect to find there. Nothing is removed — the container is
# stopped, its name is written down, and `restore` starts it again.
#
#   PORT=8765       the agent port
#   ENVFILE=…       provider keys; looked for in the usual places otherwise
#   BIND=…          listen address; defaults to what the previous agent used
#   PROFILE=…       golive profile; configs/telephony.json otherwise
#   LAST_CALL=…     where golive keeps the last answered call's instructions
#                   and opening line (~/golive-last-call.json), for `call`
#   CALLS_DIR=…     where recorded calls are kept (~/golive-calls), mode 700
#   CALLS_TTL_HOURS=…  how long golive's working copy of a recorded call is
#                   kept (168: a week); the archive has the call for good
#   ARCHIVE_DIR=…   the archive: every finished call, recording and text, kept
#                   for good (~/golive-archive, mode 700). Fed by the journal.
#   ARCHIVE_MIRROR=…  a second copy, for `archive mirror`: a directory on
#                   another disk, or host:/path for rsync over ssh
#   JOURNAL_ADDR=…  where the call journal listens (this box's home-network
#                   address, port 8790); never an address or port
#                   a tunnel publishes. JOURNAL_DATA=… its own files (~/golive-journal)
#   ARIA_ENV=…      the originator's env file, read for the number it allows
#                   (~/aria/.env); ARIA_URL=… its address (http://127.0.0.1:3000)
set -uo pipefail
cd "$(dirname "$0")/.."

PORT="${PORT:-8765}"
STATE=".phone-brain"
LOG="${LOGFILE:-golive-telephony.log}"
PROFILE="${PROFILE:-configs/telephony.json}"
LAST_CALL="${LAST_CALL:-$HOME/golive-last-call.json}"
CALLS_DIR="${CALLS_DIR:-$HOME/golive-calls}"
CALLS_TTL_HOURS="${CALLS_TTL_HOURS:-168}"
# The journal shows what was said on calls, so it listens on one address: the
# one this box has on the home network (the interface the default route
# leaves by) - not on every interface, which here would include a VPN tunnel
# and container bridges.
lan_ip() {
  local dev
  dev="$(ip -4 route show default 2>/dev/null | sed -n 's/.* dev \([^ ]*\).*/\1/p' | head -1)"
  [ -n "$dev" ] && ip -o -4 addr show dev "$dev" 2>/dev/null | awk '{sub(/\/.*/, "", $4); print $4; exit}'
}
JOURNAL_PORT="${JOURNAL_PORT:-8790}"
if [ -z "${JOURNAL_ADDR:-}" ]; then _ip="$(lan_ip)"; JOURNAL_ADDR="${_ip:-127.0.0.1}:$JOURNAL_PORT"; fi
JOURNAL_DATA="${JOURNAL_DATA:-$HOME/golive-journal}"
# Where finished calls are kept for good: recording, conversation, measurements
# and the log they came from. Nothing deletes from it. CALLS_DIR above is
# golive's working copy, which expires; this does not.
ARCHIVE_DIR="${ARCHIVE_DIR:-$HOME/golive-archive}"
# The browser demo: a second golive, this build, with the test page, on a port
# of its own. It is what a public hostname is pointed at; the phone line's
# golive is never that.
DEMO_PORT="${DEMO_PORT:-9080}"
# The lead service: templates, rendering, and placing a call on a rendered
# prompt. It listens on this machine and on the home network, and every
# request needs the token in CRM_DIR/api.token.
CRM_PORT="${CRM_PORT:-8791}"
CRM_DIR="${CRM_DIR:-$HOME/golive-crm}"
if [ -z "${CRM_ADDR:-}" ]; then _ip="$(lan_ip)"; CRM_ADDR="127.0.0.1:$CRM_PORT${_ip:+,$_ip:$CRM_PORT}"; fi
CRM_URL="http://127.0.0.1:$CRM_PORT"
# The edge gateway: self-hosted speech models (under EDGE_HOME) behind an
# OpenAI-compatible API, on this machine and the home network, token in
# EDGE_DIR/api.token. See EDGE.md.
EDGE_PORT="${EDGE_PORT:-8030}"
EDGE_DIR="${EDGE_DIR:-$HOME/golive-edge}"
EDGE_HOME="${EDGE_HOME:-$HOME/edge}"
EDGE_ASR_PORT="${EDGE_ASR_PORT:-7860}"
EDGE_TTS_PORT="${EDGE_TTS_PORT:-8024}"
if [ -z "${EDGE_ADDR:-}" ]; then _ip="$(lan_ip)"; EDGE_ADDR="127.0.0.1:$EDGE_PORT${_ip:+,$_ip:$EDGE_PORT}"; fi
EDGE_URL="http://127.0.0.1:$EDGE_PORT"
DEMO_PROFILE="${DEMO_PROFILE:-configs/tencent-deepseek-minimax.json}"
DEMO_MAX_SESSIONS="${DEMO_MAX_SESSIONS:-2}"
JOURNAL_REPO="${JOURNAL_REPO:-https://github.com/chuanmingliu/full-duplex-go-live}"
CMD="${1:-status}"

case "$(uname -m)" in
  arm64|aarch64) ARCH=arm64 ;;
  *)             ARCH=amd64 ;;
esac
BIN="bin/golive-linux-$ARCH"
BENCH="bin/golivebench-linux-$ARCH"

say()  { printf '%s\n' "$*"; }
head1() { printf '\n== %s\n' "$*"; }

SUDO=()
if [ "$(id -u)" != 0 ]; then SUDO=(sudo); fi
DOCKER=(docker)
need_docker() {
  if docker ps >/dev/null 2>&1; then DOCKER=(docker); return 0; fi
  # Not in the docker group: everything about the containers needs root.
  "${SUDO[@]}" -v || { say "this needs sudo, for docker"; exit 1; }
  DOCKER=("${SUDO[@]}" docker)
}

# Who listens on the port: sets OWNER_PID, OWNER_COMM, OWNER_ADDR, OWNER_CONTAINER.
find_owner() {
  OWNER_PID="" OWNER_COMM="" OWNER_ADDR="" OWNER_CONTAINER=""
  local line=""
  if command -v ss >/dev/null 2>&1; then
    line=$("${SUDO[@]}" ss -lntpH "sport = :$PORT" 2>/dev/null | head -1)
    if [ -n "$line" ]; then
      OWNER_ADDR=$(awk '{print $4}' <<<"$line")
      OWNER_PID=$(grep -oE 'pid=[0-9]+' <<<"$line" | head -1 | cut -d= -f2)
      OWNER_COMM=$(grep -oE '\(\("[^"]+"' <<<"$line" | head -1 | tr -d '("')
    fi
  elif command -v lsof >/dev/null 2>&1; then
    line=$("${SUDO[@]}" lsof -nP -iTCP:"$PORT" -sTCP:LISTEN -Fpcn 2>/dev/null)
    if [ -n "$line" ]; then
      OWNER_PID=$(sed -n 's/^p//p' <<<"$line" | head -1)
      OWNER_COMM=$(sed -n 's/^c//p' <<<"$line" | head -1)
      OWNER_ADDR=$(sed -n 's/^n//p' <<<"$line" | head -1)
    fi
  fi
  [ -n "$line" ] || return 1
  if [ "$OWNER_COMM" = docker-proxy ]; then
    OWNER_CONTAINER=$("${DOCKER[@]}" ps --filter "publish=$PORT" --format '{{.Names}}' 2>/dev/null | head -1)
  elif [ -n "$OWNER_PID" ]; then
    local cid
    cid=$(grep -oE '[0-9a-f]{64}' "/proc/$OWNER_PID/cgroup" 2>/dev/null | head -1)
    if [ -n "$cid" ]; then
      OWNER_CONTAINER=$("${DOCKER[@]}" inspect -f '{{.Name}}' "$cid" 2>/dev/null | sed 's|^/||')
    fi
  fi
  return 0
}

# What a log line may carry that should not be pasted anywhere.
scrub() { sed -E 's/(secretid|signature|secret|api[_-]?key|key|token|authorization)=[^& "]+/\1=…/Ig; s/(Bearer )[A-Za-z0-9._-]+/\1…/g'; }

env_value() { grep -E "^$1=" "${ENVFILE:-/dev/null}" 2>/dev/null | tail -1 | cut -d= -f2- | tr -d "\"'" ; }
url_host() { sed -E 's|^[a-z]+://||; s|[/:?].*$||' <<<"$1"; }

pool() { curl -s -m 3 "http://127.0.0.1:$PORT/v1/pool" 2>/dev/null; }
is_golive() { pool | grep -q '"service":"golive"'; }

find_env() {
  local c
  for c in "${ENVFILE:-}" .env.local "$HOME/golive-telephony.env" "$HOME/golive/.env.local" \
           "$HOME/gpt-live-deploy/full-duplex-current/.env.local"; do
    if [ -n "$c" ] && [ -f "$c" ]; then ENVFILE="$c"; return 0; fi
  done
  return 1
}

# golive runs under a small loop that starts it again if it dies: a phone line
# whose agent crashed once should not stay silent until someone notices.
stop_golive() {
  local sup="" kids="" pid=""
  : > "$STATE.stop"
  [ -f "$STATE.pid" ] && sup=$(cat "$STATE.pid" 2>/dev/null)
  if [ -n "$sup" ] && [ -d "/proc/$sup" ]; then
    kids=$(pgrep -P "$sup" 2>/dev/null | tr '\n' ' ')
    # shellcheck disable=SC2086
    [ -n "$kids" ] && kill $kids 2>/dev/null
    for _ in $(seq 1 40); do [ -d "/proc/$sup" ] || break; sleep 0.25; done
    kill "$sup" 2>/dev/null
  fi
  # Whatever of ours is still on the port: one started by hand, or by an
  # earlier version of this script.
  if find_owner; then
    case "$OWNER_COMM" in golive*) pid="$OWNER_PID" ;; esac
    if [ -n "$pid" ]; then
      kill "$pid" 2>/dev/null || "${SUDO[@]}" kill "$pid"
      for _ in $(seq 1 40); do [ -d "/proc/$pid" ] || break; sleep 0.25; done
    fi
  fi
  [ -n "$sup$pid" ] && say "stopped golive"
  rm -f "$STATE.pid" "$STATE.stop"
}

# start_golive <bind>: run golive under its keep-alive loop and wait for it to
# answer. Everything in here runs as the invoking user; none of it needs root.
# The call journal runs beside golive and reads what golive writes. It is its
# own process so that changing it never restarts a service that may be in the
# middle of a call. It is also what feeds the archive, so golive is never
# started without it.
JBIN="./bin/journal-linux-$ARCH"; JPID="$STATE.journal.pid"; JLOG="journal.log"
jrunning() { [ -f "$JPID" ] && kill -0 "$(cat "$JPID")" 2>/dev/null; }
jstop() {
  if jrunning; then kill "$(cat "$JPID")" 2>/dev/null; for _ in 1 2 3 4 5 6; do jrunning || break; sleep 0.5; done; fi
  rm -f "$JPID"
}
jstart() {
  chmod +x "$JBIN" 2>/dev/null
  [ -x "$JBIN" ] || { say "no binary at $JBIN"; return 1; }
  jstop
  mkdir -p "$JOURNAL_DATA" "$ARCHIVE_DIR" && chmod 700 "$JOURNAL_DATA" "$ARCHIVE_DIR"
  local store=""; [ -d "$CALLS_DIR" ] && store="$CALLS_DIR"
  setsid nohup "$JBIN" -addr "$JOURNAL_ADDR" -log "$LOG" -store "$store" -data "$JOURNAL_DATA" \
    -archive "$ARCHIVE_DIR" -last-call "$LAST_CALL" \
    -notes journal -repo "$JOURNAL_REPO" -retention-hours "$CALLS_TTL_HOURS" >> "$JLOG" 2>&1 < /dev/null &
  echo $! > "$JPID"
  sleep 1
  if jrunning; then say "the call journal is up on $JOURNAL_ADDR (pid $(cat "$JPID")); archive: $ARCHIVE_DIR"
  else say "the call journal did not start; see $JLOG"; tail -3 "$JLOG"; return 1; fi
}

# The lead service is its own process too, and is left running across a
# restart of golive: it holds no call, only what calls are placed with.
CBIN="./bin/crm-linux-$ARCH"; CPID="$STATE.crm.pid"; CLOG="crm.out"
crunning() { [ -f "$CPID" ] && kill -0 "$(cat "$CPID")" 2>/dev/null; }
cstop() {
  if crunning; then kill "$(cat "$CPID")" 2>/dev/null; for _ in 1 2 3 4 5 6 7 8; do crunning || break; sleep 0.5; done; fi
  rm -f "$CPID"
}
cstart() {
  chmod +x "$CBIN" 2>/dev/null
  [ -x "$CBIN" ] || { say "no binary at $CBIN"; return 1; }
  cstop
  mkdir -p "$CRM_DIR" && chmod 700 "$CRM_DIR"
  setsid nohup "$CBIN" -addr "$CRM_ADDR" -dir "$CRM_DIR" -originator "${ARIA_URL:-http://127.0.0.1:3000}" \
    -originator-env "${ARIA_ENV:-$HOME/aria/.env}" -golive "http://127.0.0.1:$PORT" >> "$CLOG" 2>&1 < /dev/null &
  echo $! > "$CPID"
  sleep 1
  if crunning; then say "the lead service is up on $CRM_ADDR (pid $(cat "$CPID")); data: $CRM_DIR"
  else say "the lead service did not start; see $CLOG"; tail -3 "$CLOG"; return 1; fi
}
# capi METHOD PATH [curl args]: a request to the lead service with its token.
# The token goes to curl in a header file descriptor, not on the command line.
capi() {
  local method="$1" path="$2"; shift 2
  [ -r "$CRM_DIR/api.token" ] || { say "no token at $CRM_DIR/api.token: scripts/phone-brain.sh crm start"; return 1; }
  curl -sS -m 60 -X "$method" -H @<(printf 'Authorization: Bearer %s\nX-Operator: %s\n' "$(cat "$CRM_DIR/api.token")" "${USER:-box}") "$@" "$CRM_URL$path"
}

# The edge gateway and the two model runtimes it fronts. The runtimes are
# Python services in their own virtual environments under EDGE_HOME (EDGE.md
# says how they were set up); the gateway is ours. All three are left alone by
# a restart of golive.
EBIN="./bin/edge-linux-$ARCH"; EPID="$STATE.edge.pid"; ELOG="edge.out"
ASR_HOME="$EDGE_HOME/asr"; TTS_HOME="$EDGE_HOME/Audio8_TTS/onnx_runtime_0_1b_int8"
erunning() { [ -f "$EPID" ] && kill -0 "$(cat "$EPID")" 2>/dev/null; }
asr_up() { curl -sf -m 3 "http://127.0.0.1:$EDGE_ASR_PORT/health" >/dev/null 2>&1; }
tts_up() { curl -sf -m 3 "http://127.0.0.1:$EDGE_TTS_PORT/api/health" >/dev/null 2>&1; }
runtimes_start() {
  if [ -x "$ASR_HOME/.venv/bin/python" ] && ! asr_up; then
    ( cd "$ASR_HOME" && setsid nohup env PYTHON=.venv/bin/python HOST=127.0.0.1 PORT="$EDGE_ASR_PORT" bash run_local.sh >> service.log 2>&1 < /dev/null & )
    for _ in $(seq 1 60); do asr_up && break; sleep 2; done
    asr_up && say "audio8-asr is up on 127.0.0.1:$EDGE_ASR_PORT" || say "audio8-asr did not come up; see $ASR_HOME/service.log"
  fi
  if [ -x "$TTS_HOME/.venv/bin/python" ] && ! tts_up; then
    ( cd "$TTS_HOME" && PORT="$EDGE_TTS_PORT" ARKTTS_THREADS="${EDGE_TTS_THREADS:-5}" bash start_server.sh 2>&1 | tail -1 | sed 's/^/  /' )
  fi
}
runtimes_stop() {
  [ -x "$TTS_HOME/stop_server.sh" ] && ( cd "$TTS_HOME" && bash stop_server.sh >/dev/null 2>&1 )
  pkill -f "server.py --bundle_dir $ASR_HOME" 2>/dev/null || true
}
estop() {
  if erunning; then kill "$(cat "$EPID")" 2>/dev/null; for _ in 1 2 3 4 5 6 7 8; do erunning || break; sleep 0.5; done; fi
  rm -f "$EPID"
}
estart() {
  chmod +x "$EBIN" 2>/dev/null
  [ -x "$EBIN" ] || { say "no binary at $EBIN"; return 1; }
  runtimes_start
  estop
  mkdir -p "$EDGE_DIR" && chmod 700 "$EDGE_DIR"
  local args=()
  asr_up && args+=(-audio8-asr "http://127.0.0.1:$EDGE_ASR_PORT")
  tts_up && args+=(-audio8-tts "http://127.0.0.1:$EDGE_TTS_PORT")
  [ ${#args[@]} -gt 0 ] || { say "neither runtime is up; nothing to serve"; return 1; }
  setsid nohup "$EBIN" -addr "$EDGE_ADDR" -dir "$EDGE_DIR" "${args[@]}" >> "$ELOG" 2>&1 < /dev/null &
  echo $! > "$EPID"
  sleep 1
  if erunning; then say "the edge gateway is up on $EDGE_ADDR (pid $(cat "$EPID")): $(curl -s -m 3 "$EDGE_URL/healthz" | cut -c1-160)"
  else say "the edge gateway did not start; see $ELOG"; tail -3 "$ELOG"; return 1; fi
}
# eapi METHOD PATH [curl args]: a request to the gateway with its token.
eapi() {
  local method="$1" path="$2"; shift 2
  [ -r "$EDGE_DIR/api.token" ] || { say "no token at $EDGE_DIR/api.token: scripts/phone-brain.sh edge start"; return 1; }
  curl -sS -m 300 -X "$method" -H @<(printf 'Authorization: Bearer %s\n' "$(cat "$EDGE_DIR/api.token")") "$@" "$EDGE_URL$path"
}

# The browser demo is a separate process from the phone line's golive, with its
# own port, profile and log, so that a stranger on the demo page cannot be
# confused with a call, does not appear in the journal, and is not recorded.
# It does share the provider accounts: DEMO_MAX_SESSIONS bounds how much of
# them a public page can take.
DPID="$STATE.demo.pid"; DLOG="golive-demo.log"
drunning() { [ -f "$DPID" ] && kill -0 "$(cat "$DPID")" 2>/dev/null; }
dstop() {
  touch "$STATE.demo.stop"
  if drunning; then
    pkill -TERM -P "$(cat "$DPID")" 2>/dev/null; kill "$(cat "$DPID")" 2>/dev/null
    for _ in 1 2 3 4 5 6 7 8; do drunning || break; sleep 0.5; done
  fi
  rm -f "$DPID"
}
dstart() {
  find_env || { say "no provider keys found; set ENVFILE"; return 1; }
  [ -x "$BIN" ] || chmod +x bin/* 2>/dev/null
  [ -d web ] || { say "no web/ directory beside the binaries: this bundle has no test page"; return 1; }
  dstop; rm -f "$STATE.demo.stop"
  printf '\n--- %s phone-brain: starting the browser demo %s on 127.0.0.1:%s ---\n' "$(date '+%F %T')" "$(cut -d' ' -f1 VERSION 2>/dev/null)" "$DEMO_PORT" >> "$DLOG"
  env -u GOLIVE_STORE_DIR -u GOLIVE_REALTIME_STORE_CALLS -u GOLIVE_LAST_CALL_FILE -u GOLIVE_TELEMETRY_URL \
    GOLIVE_LOG_LEVEL="${DEMO_LOG_LEVEL:-info}" GOLIVE_MAX_SESSIONS="$DEMO_MAX_SESSIONS" GOLIVE_WEB_ROOT=web \
    setsid nohup bash -c '
    stop="$1"; shift
    quick=0
    while :; do
      began=$(date +%s)
      "$@"; code=$?
      [ -f "$stop" ] && exit 0
      if [ $(( $(date +%s) - began )) -lt 5 ]; then quick=$((quick + 1)); else quick=0; fi
      [ "$quick" -ge 5 ] && { printf "\n--- %s the demo keeps exiting at once (status %s); giving up ---\n" "$(date "+%F %T")" "$code"; exit 1; }
      printf "\n--- %s the demo exited with status %s; starting it again ---\n" "$(date "+%F %T")" "$code"
      sleep 1
    done' keepalive "$STATE.demo.stop" "./$BIN" -profile "$DEMO_PROFILE" -env "$ENVFILE" -addr "127.0.0.1:$DEMO_PORT" \
    >> "$DLOG" 2>&1 < /dev/null &
  echo $! > "$DPID"
  for _ in $(seq 1 30); do
    curl -s -m 2 "http://127.0.0.1:$DEMO_PORT/healthz" 2>/dev/null | grep -q '"status":"ok"' && break
    sleep 0.5
  done
  if curl -s -m 2 "http://127.0.0.1:$DEMO_PORT/healthz" 2>/dev/null | grep -q '"status":"ok"'; then
    say "the browser demo is up on 127.0.0.1:$DEMO_PORT (at most $DEMO_MAX_SESSIONS sessions at once); log: $DLOG"
  else
    say "the browser demo did not come up; see $DLOG"; tail -5 "$DLOG" | cut -c1-200; return 1
  fi
}

start_golive() {
  local bind="$1" mark ok=""
  [ -x "$BIN" ] || chmod +x bin/* 2>/dev/null
  [ -x "$BIN" ] || { say "no binary at $BIN"; exit 1; }
  for _ in $(seq 1 60); do find_owner || break; sleep 0.5; done
  if find_owner; then say ":$PORT is still held by ${OWNER_COMM:-?} pid=${OWNER_PID:-?}"; exit 1; fi

  printf '%s\n' "$bind" > "$STATE.bind"
  printf '\n--- %s phone-brain: starting golive %s on %s:%s ---\n' "$(date '+%F %T')" "$(cat VERSION 2>/dev/null | cut -d' ' -f1)" "$bind" "$PORT" >> "$LOG"
  mark=$(wc -l < "$LOG")
  rm -f "$STATE.stop"
  # Calls are recorded only when somebody has said so: `record on`.
  if [ -f "$STATE.record" ]; then
    mkdir -p "$CALLS_DIR" && chmod 700 "$CALLS_DIR"
    export GOLIVE_STORE_DIR="$CALLS_DIR" GOLIVE_REALTIME_STORE_CALLS=1
    export GOLIVE_STORE_TTL_HOURS="${GOLIVE_STORE_TTL_HOURS:-$CALLS_TTL_HOURS}"
  fi
  # A lane — a connection of its own — for each copy of a completion the
  # profile asks for; the chat-model adapter keeps four unless told otherwise.
  local copies
  copies=$(grep -oE '"llm_hedge": *[0-9]+' "$PROFILE" 2>/dev/null | grep -oE '[0-9]+$' | head -1)
  GOLIVE_LOG_LEVEL="${GOLIVE_LOG_LEVEL:-debug}" GOLIVE_LAST_CALL_FILE="${GOLIVE_LAST_CALL_FILE:-$LAST_CALL}" \
  DEEPSEEK_LANES="${DEEPSEEK_LANES:-${GOLIVE_LLM_HEDGE:-${copies:-4}}}" setsid nohup bash -c '
    stop="$1"; shift
    quick=0
    while :; do
      began=$(date +%s)
      "$@"; code=$?
      [ -f "$stop" ] && exit 0
      if [ $(( $(date +%s) - began )) -lt 5 ]; then quick=$((quick + 1)); else quick=0; fi
      if [ "$quick" -ge 5 ]; then
        printf "\n--- %s golive keeps exiting at once (status %s); giving up ---\n" "$(date "+%F %T")" "$code"
        exit 1
      fi
      printf "\n--- %s golive exited with status %s; starting it again ---\n" "$(date "+%F %T")" "$code"
      sleep 1
    done' keepalive "$STATE.stop" "./$BIN" -profile "$PROFILE" -env "$ENVFILE" -addr "$bind:$PORT" \
    >> "$LOG" 2>&1 < /dev/null &
  echo $! > "$STATE.pid"

  # Up means golive itself answering: anything else that got the port back
  # first would answer a health check too.
  for _ in $(seq 1 40); do
    if is_golive; then ok=1; break; fi
    [ -d "/proc/$(cat "$STATE.pid")" ] || break
    sleep 0.5
  done
  if [ -z "$ok" ]; then
    say "golive is not answering on :$PORT. Its log:"
    tail -n +"$((mark + 1))" "$LOG" | tail -25
    find_owner && say "the port is held by ${OWNER_COMM:-?} pid=${OWNER_PID:-?} container=${OWNER_CONTAINER:-none}"
    stop_golive
    if [ -f "$STATE.container" ]; then say; say "put the old agent back with: scripts/phone-brain.sh restore"; fi
    exit 1
  fi
  # Long enough for the startup warm-up to have reached the synthesizer.
  sleep 4
  say "golive is up on $bind:$PORT (kept running by pid $(cat "$STATE.pid"))"
  say "/v1/pool : $(pool | cut -c1-300)"
  say "startup  :"
  tail -n +"$((mark + 1))" "$LOG" | grep -E 'level=(INFO|WARN|ERROR)' | grep -vE 'msg=http ' | cut -c1-240 | head -25 | sed 's/^/  /'
  say
  say "log      : $(pwd)/$LOG"
  # With this build's journal: it is what archives the calls golive records.
  jstart || say "golive is up, but without the journal nothing is being archived"
  # And the browser demo, if it has been turned on: `demo on`.
  if [ -f "$STATE.demo" ]; then dstart || say "golive is up, but the browser demo is not"; fi
  # The lead service, if it has been turned on and is not already running.
  if [ -f "$STATE.crm" ] && ! crunning; then cstart || say "golive is up, but the lead service is not"; fi
}

case "$CMD" in

status)
  need_docker
  head1 "agent port :$PORT"
  if find_owner; then
    say "listener  : $OWNER_ADDR  ${OWNER_COMM:-?} pid=${OWNER_PID:-?}  container=${OWNER_CONTAINER:-none}"
    say "/v1/pool  : $(pool | cut -c1-300)"
    if is_golive; then say "agent     : golive"; else say "agent     : not golive"; fi
  else
    say "nothing is listening on :$PORT"
  fi
  [ -f "$STATE.container" ] && say "restore   : would start container '$(cat "$STATE.container")'"

  head1 "containers"
  "${DOCKER[@]}" ps -a --format '{{.Names}}\t{{.Image}}\t{{.Status}}'
  for c in $("${DOCKER[@]}" ps --format '{{.Names}}'); do
    printf '  %-22s network=%s\n' "$c" "$("${DOCKER[@]}" inspect -f '{{.HostConfig.NetworkMode}}' "$c" 2>/dev/null)"
  done

  head1 "provider keys"
  if find_env; then
    say "env file  : $ENVFILE"
    say "keys set  : $(grep -oE '^[A-Z_]+=.' "$ENVFILE" | sed 's/=.$//' | tr '\n' ' ')"
  else
    say "no env file found (looked at ./.env.local, ~/golive-telephony.env, ~/golive/.env.local)"
  fi

  head1 "aria"
  say "brain     : $(curl -s -m 3 http://127.0.0.1:3000/api/realtime/brain | cut -c1-300)"

  head1 "gateway port sync"
  systemctl list-timers --all --no-pager 2>/dev/null | grep -i gateway || say "no gateway timer is scheduled"
  "${SUDO[@]}" journalctl -u gateway-port-sync --no-pager -n 12 2>/dev/null | cut -c1-200
  PBX=$("${DOCKER[@]}" ps --format '{{.Names}}' | grep -i -m1 pbx || true)
  if [ -n "$PBX" ]; then
    say "pbx       : $PBX"
    for f in $("${DOCKER[@]}" inspect -f '{{range .Mounts}}{{.Source}}{{"\n"}}{{end}}' "$PBX" 2>/dev/null | grep -i '\.toml$'); do
      say "trunk dest in $f:"
      "${SUDO[@]}" grep -nE '^\s*dest\s*=' "$f" 2>/dev/null | sed 's/^/    /'
    done
    say "last registrations (30 min):"
    "${DOCKER[@]}" logs "$PBX" --since 30m 2>&1 | grep -i 'registered location' | grep -viE 'password|secret' | tail -4 | cut -c1-260 | sed 's/^/    /'
    head1 "pbx: calls in the last 3 hours"
    "${DOCKER[@]}" logs "$PBX" --since 3h 2>&1 | grep -viE 'password|secret' \
      | grep -E 'INVITE|Request Timeout| 408| 480| 486| 487| 50[0-9]|hangup|dial' | tail -30 | cut -c1-260
  else
    say "no PBX container is running"
  fi
  AC=$("${DOCKER[@]}" ps --format '{{.Names}}' | grep -i -m1 'active' || true)
  if [ -n "$AC" ]; then
    head1 "$AC: last lines"
    "${DOCKER[@]}" logs "$AC" --since 3h 2>&1 | grep -viE 'password|secret|api[_-]?key|authorization' | tail -20 | cut -c1-260
  fi
  ;;

golive)
  need_docker
  find_env || { say "no provider keys: put them in ./.env.local or ~/golive-telephony.env, or set ENVFILE"; exit 1; }
  say "provider keys from $ENVFILE"

  bind="${BIND:-}"
  if find_owner; then
    if is_golive; then
      say "golive already holds :$PORT; restarting it with this build"
      [ -z "$bind" ] && [ -f "$STATE.bind" ] && bind=$(cat "$STATE.bind")
      stop_golive
    elif [ -n "$OWNER_CONTAINER" ]; then
      # Listen where the agent before us did: whatever reached it there must be
      # able to reach us.
      case "$OWNER_ADDR" in 127.*|"[::1]"*) : "${bind:=127.0.0.1}" ;; *) : "${bind:=0.0.0.0}" ;; esac
      say "stopping container '$OWNER_CONTAINER' (was on $OWNER_ADDR); 'restore' starts it again"
      printf '%s\n' "$OWNER_CONTAINER" > "$STATE.container"
      "${DOCKER[@]}" stop -t 20 "$OWNER_CONTAINER" >/dev/null || { say "could not stop it"; exit 1; }
    else
      say ":$PORT is held by ${OWNER_COMM:-a process} (pid ${OWNER_PID:-?}), which is not a container."
      say "Stop it yourself, then run this again."
      exit 1
    fi
  fi
  # Nothing there: golive was here before and is gone. Listen where it did.
  [ -z "$bind" ] && [ -f "$STATE.bind" ] && bind=$(cat "$STATE.bind")
  start_golive "${bind:-127.0.0.1}"
  say "next     : scripts/phone-brain.sh bench     (no phone needed)"
  ;;

restart)
  # golive is ours, so replacing it with the build in bin/ needs no root and no
  # docker. For a box where it already holds the port; otherwise use 'golive'.
  SUDO=()
  find_env || { say "no provider keys: put them in ./.env.local or ~/golive-telephony.env, or set ENVFILE"; exit 1; }
  bind="${BIND:-}"
  if find_owner && ! is_golive; then
    say ":$PORT is held by ${OWNER_COMM:-something} that is not golive; use: scripts/phone-brain.sh golive"
    exit 1
  fi
  [ -z "$bind" ] && [ -f "$STATE.bind" ] && bind=$(cat "$STATE.bind")
  stop_golive
  start_golive "${bind:-127.0.0.1}"
  ;;

bench)
  is_golive || { say "golive is not on :$PORT; run: scripts/phone-brain.sh golive"; exit 1; }
  chmod +x "$BENCH" 2>/dev/null
  # Things a caller says, short and long: a turn's length decides whether there
  # is a pause worth speculating in.
  SPEAK="${SPEAK:-你好，今天天气怎么样？|帮我查一下明天上午去上海的航班有哪些。|你能给我讲一个简短的笑话吗？|我想了解一下你们周末的营业时间。|好的，谢谢。}"
  mark=$(wc -l < "$LOG" 2>/dev/null || echo 0)
  # The end-of-turn silence is the server's own unless SILENCE says otherwise:
  # that is what a phone call gets, since the gateway's session.update has been
  # seen to carry no turn detection this server acts on. SILENCE=200 asks for
  # the shortest a client may have.
  silence="${SILENCE:-0}"
  if [ "$silence" = 0 ]; then say "· end-of-turn silence: the server's own (vad.min_silence_ms in the profile)"
  else say "· end-of-turn silence asked for: ${silence} ms"; fi
  "./$BENCH" -a "golive=ws://127.0.0.1:$PORT/v1/live" -speak "$SPEAK" -rate 16000 -min-silence-ms "$silence" \
    ${KEEP_CLIPS:+-keep-clips "$KEEP_CLIPS"} -runs "${RUNS:-15}" -warmup 2 -v -md bench-phone.md -json bench-phone.json
  say
  say "== the same turns, stage by stage (from $LOG)"
  tail -n +"$((mark + 1))" "$LOG" > "$STATE.benchlog"
  scripts/call-latency.sh "$STATE.benchlog"
  say
  say "== warnings and errors during the run"
  grep -E 'level=(WARN|ERROR)|^panic|^fatal error|^goroutine |golive exited|answered without speculating' "$STATE.benchlog" \
    | scrub | cut -c1-320 | sed -E 's/^time=[0-9-]+T//' | tail -40
  rm -f "$STATE.benchlog"
  if is_golive; then say "golive is still up"; else say "golive is NOT answering on :$PORT"; fi
  ;;

net)
  find_env || true
  head1 "how this box leaves the LAN"
  say "default route : $(ip route show default 2>/dev/null | tr '\n' ';' | cut -c1-200)"
  say "tunnels       : $(ip -br link 2>/dev/null | grep -Ei '^(tun|tap|wg|utun|tailscale|lantern|ppp|zt)' | awk '{print $1"("$2")"}' | tr '\n' ' ')"
  say "vpn services  : $(for u in lanternd tailscaled openvpn wg-quick@wg0; do printf '%s=%s ' "$u" "$(systemctl is-active "$u" 2>/dev/null)"; done)"
  say "proxy env     : $(env | grep -iE '^(https?|all|no)_proxy=' | cut -d= -f1 | tr '\n' ' ')"
  say "policy rules  : $(ip rule 2>/dev/null | grep -vcE 'lookup (local|main|default)') beyond the defaults"
  # The address is left out: where the box appears to be is the question.
  say "seen from a mainland service : $(curl -4 -s -m 6 https://myip.ipip.net 2>/dev/null | sed -E 's/[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/x.x.x.x/' | tr -d '\n' | cut -c1-120)"
  say "seen from abroad             : $(curl -4 -s -m 6 https://ipinfo.io/json 2>/dev/null | tr -d '\n' | grep -oE '"(city|region|country|org)": *"[^"]*"' | tr '\n' ' ')"

  head1 "the three providers"
  llm=$(url_host "$(env_value GOLIVE_BACKEND_BASE_URL)"); llm="${llm:-api.deepseek.com}"
  tts=$(url_host "$(env_value MINIMAX_TTS_WEBSOCKET_ENDPOINT)"); tts="${tts:-api.minimaxi.com}"
  printf '%-6s %-28s %-16s %-10s %s\n' "" host address via "dns/tcp/tls ms, three tries"
  for pair in "asr:asr.cloud.tencent.com" "llm:$llm" "tts:$tts"; do
    name=${pair%%:*}; host=${pair#*:}
    ip4=$(getent ahostsv4 "$host" 2>/dev/null | awk 'NR==1{print $1}')
    dev=$(ip route get "${ip4:-0.0.0.0}" 2>/dev/null | grep -oE 'dev [^ ]+' | head -1 | cut -d' ' -f2)
    times=""
    for _ in 1 2 3; do
      times="$times $(curl -4 -s -m 8 -o /dev/null -w '%{time_namelookup}/%{time_connect}/%{time_appconnect}' "https://$host/" 2>/dev/null \
        | awk -F/ '{printf "%.0f/%.0f/%.0f", $1*1000, $2*1000, $3*1000}')"
    done
    printf '%-6s %-28s %-16s %-10s%s\n' "$name" "$host" "${ip4:-unresolved}" "${dev:-?}" "$times"
    v6=$(getent ahostsv6 "$host" 2>/dev/null | awk 'NR==1{print $1}' | grep -v '^::ffff:' || true)
    [ -n "$v6" ] && printf '%-6s %-28s %s\n' "" "(also IPv6)" "$v6"
  done
  ;;

log)
  n="${2:-400}"
  [ -f "$LOG" ] || { say "no $LOG here"; exit 1; }
  if [ -n "${3:-}" ]; then
    # Everything the engine said in the lines that match, e.g. a minute:
    #   scripts/phone-brain.sh log 100000 'T22:21:'
    tail -n "$n" "$LOG" | grep -E -- "$3" \
      | grep -vE 'msg="?(http|server event|server audio|client event|client audio)' | scrub | cut -c1-360
  else
    tail -n "$n" "$LOG" \
      | grep -E 'level=(WARN|ERROR)|^panic|^fatal error|^goroutine |^[[:space:]]+[^[:space:]]|golive exited|golive keeps|phone-brain:|msg="(turn: response latency|session: response latency summary|think: answered[^"]*|session started|realtime:[^"]*)"|msg=listening' \
      | scrub | cut -c1-360
  fi
  ;;

llm)
  find_env || { say "no provider keys found; set ENVFILE"; exit 1; }
  shift
  chmod +x "bin/llmprobe-linux-$ARCH" 2>/dev/null
  "./bin/llmprobe-linux-$ARCH" -env "$ENVFILE" -profile "$PROFILE" -runs "${RUNS:-10}" "$@"
  ;;

tts)
  find_env || { say "no provider keys found; set ENVFILE"; exit 1; }
  shift
  chmod +x "bin/ttsprobe-linux-$ARCH" 2>/dev/null
  if [ $# -gt 0 ]; then "./bin/ttsprobe-linux-$ARCH" -env "$ENVFILE" "$@"
  else "./bin/ttsprobe-linux-$ARCH" -env "$ENVFILE" -compare-endpoints -runs "${RUNS:-6}"; fi
  ;;

keys)
  # How many keys each pool has. Counts only: a key is never printed.
  find_env || { say "no provider keys found; set ENVFILE"; exit 1; }
  say "env file: $ENVFILE"
  for var in DEEPSEEK_API_KEY MINIMAX_TTS_API_KEY FISH_TTS_API_KEY SILICONFLOW_API_KEY OPENROUTER_API_KEY; do
    one=$(grep -cE "^${var}=.+" "$ENVFILE" || true)
    list=$(grep -m1 "^${var}S=" "$ENVFILE" | cut -d= -f2- | tr ',; ' '\n\n\n' | grep -c . || true)
    file=$(grep -m1 "^${var}S_FILE=" "$ENVFILE" | cut -d= -f2- | tr -d '"'"'"'"')
    infile=0
    [ -n "$file" ] && [ -f "$file" ] && infile=$(sed 's/#.*//' "$file" | grep -c '[^[:space:]]' || true)
    numbered=$(grep -cE "^${var}_[2-9]=.+" "$ENVFILE" || true)
    say "$var: $one    ${var}_2…9: $numbered    ${var}S: $list    ${var}S_FILE: ${file:-(not set)}${file:+ — $infile key(s)}"
  done
  say "MINIMAX_TTS_VOICE_ID: $(grep -cE '^MINIMAX_TTS_VOICE_ID=.+' "$ENVFILE" || true)    MINIMAX_TTS_VOICE_ID_2…9: $(grep -cE '^MINIMAX_TTS_VOICE_ID_[2-9]=.+' "$ENVFILE" || true)   (a numbered synthesis account speaks in the voice id of the same number)"
  say "requests a minute each synthesis account is trusted with: first $(grep -m1 -E '^MINIMAX_TTS_RPM=' "$ENVFILE" | cut -d= -f2 | tr -d '[:space:]' | grep . || echo 'not set')$(grep -E '^MINIMAX_TTS_RPM_[2-9]=.+' "$ENVFILE" | sed -E 's/^MINIMAX_TTS_RPM_([2-9])=(.*)$/, account \1: \2/' | tr -d '\n')   (not set: the profile's tts_account_rpm)"
  say "(a key listed twice counts once in a pool; the first key is the account used when nothing else is asked for)"
  ;;

addkey)
  # Add one key to a pool without it reaching the screen or the shell history:
  #   addkey            a chat-model key (DeepSeek)
  #   addkey minimax    a synthesis account (MiniMax) that can use the first
  #                     account's voice id. One with its own copy of the voice
  #                     is written into the env file instead, as a pair:
  #                     MINIMAX_TTS_API_KEY_2=…  MINIMAX_TTS_VOICE_ID_2=…
  #   addkey siliconflow  a key for a candidate chat-model vendor
  #   addkey openrouter   (SiliconFlow, OpenRouter), for `llm -try` to measure
  #   addkey fish         a Fish Audio synthesis key, for `tts -fish` to measure
  # Run it once per key, then: scripts/phone-brain.sh restart
  find_env || { say "no provider keys found; set ENVFILE"; exit 1; }
  case "${2:-deepseek}" in
    deepseek|llm) var=DEEPSEEK_API_KEY; file_default="$HOME/golive-deepseek.keys"; measure="scripts/phone-brain.sh llm -pool" ;;
    minimax|tts)  var=MINIMAX_TTS_API_KEY; file_default="$HOME/golive-minimax.keys"; measure="scripts/phone-brain.sh tts -pool 30 -every 8s" ;;
    siliconflow|sf) var=SILICONFLOW_API_KEY; file_default="$HOME/golive-siliconflow.keys"; measure="scripts/phone-brain.sh llm -runs 20 -try 'siliconflow|https://api.siliconflow.cn/v1|<model>|SILICONFLOW_API_KEY|enable_thinking=false'" ;;
    fish) var=FISH_TTS_API_KEY; file_default="$HOME/golive-fish.keys"; measure="scripts/phone-brain.sh tts -fish s2.1-pro,drama-3 -runs 12   and   scripts/phone-brain.sh tts -fish s2.1-pro,drama-3 -fish-only -call 20 -every 6s" ;;
    openrouter|or) var=OPENROUTER_API_KEY; file_default="$HOME/golive-openrouter.keys"; measure="scripts/phone-brain.sh llm -runs 20 -try 'openrouter|https://openrouter.ai/api/v1|<model>|OPENROUTER_API_KEY|reasoning={\"enabled\":false}'" ;;
    *) say "addkey [deepseek|minimax|siliconflow|openrouter|fish]"; exit 2 ;;
  esac
  file=$(grep -m1 "^${var}S_FILE=" "$ENVFILE" | cut -d= -f2- | tr -d '"'"'"'"')
  if [ -z "$file" ]; then
    file="$file_default"
    printf '\n%sS_FILE=%s\n' "$var" "$file" >> "$ENVFILE"
    say "added ${var}S_FILE=$file to $ENVFILE"
  fi
  touch "$file" && chmod 600 "$file"
  printf 'paste one key and press Enter (it is not shown): '
  IFS= read -rs key; echo
  key=$(printf '%s' "$key" | tr -d '[:space:]"'"'"'')
  [ -n "$key" ] || { say "nothing entered; nothing changed"; exit 1; }
  if grep -qxF "$key" "$file" || grep -qF "${var}=$key" "$ENVFILE"; then
    say "that key is already in the pool; nothing changed"
  else
    printf '%s\n' "$key" >> "$file"
    say "added. $file now holds $(sed 's/#.*//' "$file" | grep -c '[^[:space:]]') key(s)"
  fi
  unset key
  if [ "$var" = SILICONFLOW_API_KEY ] || [ "$var" = OPENROUTER_API_KEY ] || [ "$var" = FISH_TTS_API_KEY ]; then
    say "to measure it: $measure    (a candidate only: no call uses it until the profile says so)"
  else
    say "to measure them first: $measure    to use them: scripts/phone-brain.sh restart"
  fi
  ;;

call)
  # Place the last answered call again. The prompt is composed in the
  # originator's web page and lives in that browser; golive writes down what
  # each answered call was given (last_call_file), and this sends it back to
  # the originator with the one number it will dial. Neither the number nor the
  # instructions are printed.
  [ -s "$LAST_CALL" ] || { say "nothing kept yet at $LAST_CALL: place one call from the originator's page first"; exit 1; }
  python3 - "$LAST_CALL" "${ARIA_ENV:-$HOME/aria/.env}" "${ARIA_URL:-http://127.0.0.1:3000}" "${2:-}" <<'PY'
import json, re, sys, urllib.request
kept_path, env_path, base, mode = sys.argv[1:5]
kept = json.load(open(kept_path, encoding="utf-8"))
print("kept at %s from call %s: instructions %d characters (sha %s), opening line %d characters" % (
    kept.get("saved_at", "?"), kept.get("call") or "?", len(kept.get("instructions", "")),
    kept.get("instructions_sha", "?"), len(kept.get("greeting", ""))))
if mode == "show":
    sys.exit(0)
to = ""
try:
    for line in open(env_path, encoding="utf-8"):
        if line.startswith("ALLOWED_CALLEE="):
            to = line.split("=", 1)[1].strip().strip("\"'")
except OSError as err:
    sys.exit("cannot read the originator's env file: %s" % err.strerror)
if not to:
    sys.exit("no ALLOWED_CALLEE in the originator's env file")
body = {"to": to, "instructions": kept.get("instructions", ""), "greeting": kept.get("greeting", "")}
req = urllib.request.Request(base.rstrip("/") + "/api/outbound/dial", data=json.dumps(body).encode(),
                             headers={"Content-Type": "application/json"}, method="POST")
def hide(text):
    return re.sub(r"\+?\d{7,}", "<number>", text)
try:
    with urllib.request.urlopen(req, timeout=45) as resp:
        status, text = resp.status, resp.read().decode("utf-8", "replace")
except urllib.error.HTTPError as err:
    status, text = err.code, err.read().decode("utf-8", "replace")
except OSError as err:
    sys.exit("the originator did not answer at %s: %s" % (base, err))
print("originator answered %d: %s" % (status, hide(text)[:400]))
sys.exit(0 if status == 200 else 1)
PY
  ;;

record)
  case "${2:-}" in
    on)  : > "$STATE.record"; say "calls will be recorded to $CALLS_DIR from the next restart, and kept a day" ;;
    off) rm -f "$STATE.record"; say "calls will not be recorded from the next restart; what is in $CALLS_DIR stays until it expires or is removed" ;;
    "")  ;;
    *)   say "record on | record off"; exit 2 ;;
  esac
  if [ -f "$STATE.record" ]; then say "recording: on (setting)"; else say "recording: off (setting)"; fi
  n=$(find "$CALLS_DIR" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | wc -l)
  say "recorded calls kept: $n in $CALLS_DIR ($(du -sh "$CALLS_DIR" 2>/dev/null | cut -f1))"
  ;;

echo)
  # The phone leg, from the audio of the last recorded call. Numbers only.
  chmod +x "bin/echoprobe-linux-$ARCH" 2>/dev/null
  [ -d "$CALLS_DIR" ] || { say "no recorded calls: scripts/phone-brain.sh record on, restart, and place a call"; exit 1; }
  shift
  "./bin/echoprobe-linux-$ARCH" -store "$CALLS_DIR" "$@"
  ;;

journal)
  case "${2:-status}" in
    start|restart) jstart || exit 1 ;;
    stop) jstop; say "the call journal is stopped; calls are not being archived until it is started" ;;
    status)
      if jrunning; then say "the call journal is up on $JOURNAL_ADDR (pid $(cat "$JPID"))"; else say "the call journal is not running"; fi
      ;;
    *) say "journal start | stop | status"; exit 2 ;;
  esac
  if jrunning; then say "  http://$JOURNAL_ADDR/"; fi
  ;;

crm)
  # The lead service: templates, rendering, and calls on rendered prompts.
  #   crm start | stop | status | verify
  #   crm token                         print the API token (to your terminal only)
  #   crm put NAME FILE [NOTE]          save FILE as the next version of template NAME
  #   crm templates                     what is saved
  #   crm render NAME 'JSON' [VERSION]  render with variables; prints the prompt
  #   crm call NAME 'JSON' [dry]        render and place the call (dry: stop before dialling)
  #   crm greeting ['LINE']             show or set the default opening line
  #   crm calls                         the calls placed
  case "${2:-status}" in
    start|restart) touch "$STATE.crm"; cstart || exit 1 ;;
    stop) rm -f "$STATE.crm"; cstop; say "the lead service is stopped, and stays off when golive is restarted" ;;
    status)
      if crunning; then say "the lead service is up on $CRM_ADDR (pid $(cat "$CPID")): $(curl -s -m 3 "$CRM_URL/healthz" | cut -c1-120)"
      else say "the lead service is not running$([ -f "$STATE.crm" ] && echo ' (it is turned on: scripts/phone-brain.sh crm start starts it)')"; fi
      ;;
    verify) chmod +x "$CBIN" 2>/dev/null; "$CBIN" -dir "$CRM_DIR" -verify ;;
    token)
      [ -t 1 ] || { say "the token is printed to a terminal only"; exit 1; }
      cat "$CRM_DIR/api.token" ;;
    put)
      [ -n "${3:-}" ] && [ -r "${4:-}" ] || { say "crm put NAME FILE [NOTE]"; exit 2; }
      note="$(python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1]))' "${5:-}")"
      capi PUT "/api/templates/$3?note=$note" -H 'Content-Type: text/plain; charset=utf-8' --data-binary "@$4" \
        | python3 -c 'import json,sys; d=json.load(sys.stdin); t=d.get("template"); print(d.get("error") if not t else "saved %s v%d (sha %s): %d characters, %d before its first variable; reads %s" % (t["name"], t["version"], t["sha"], t["chars"], t["static_chars"], ", ".join(t["names"]))); sys.exit(0 if t else 1)'
      ;;
    templates)
      capi GET /api/templates | python3 -c 'import json,sys
for t in json.load(sys.stdin).get("templates", []): print("%-24s v%-3d sha %s  %6d characters, %d before its first variable; reads %s" % (t["name"], t["version"], t["sha"], t["chars"], t["static_chars"], ", ".join(t["names"])))'
      ;;
    render|call)
      [ -n "${3:-}" ] || { say "crm $2 NAME 'JSON variables' [${2/render/VERSION}${2/call/dry}]"; exit 2; }
      body="$(python3 -c 'import json,sys
kind, name, raw, extra = sys.argv[1:5]
try: v = json.loads(raw or "{}")
except ValueError as e: sys.exit("the variables are not JSON: %s" % e)
req = {"template": name, "vars": v}
if kind == "render" and extra: req["version"] = int(extra)
if kind == "call" and extra == "dry": req["dry_run"] = True
print(json.dumps(req))' "$2" "$3" "${4:-}" "${5:-}")" || exit 2
      capi POST "/api/$2" -H 'Content-Type: application/json' --data-binary "$body" | python3 -c 'import json,sys
kind = sys.argv[1]; d = json.load(sys.stdin)
if "error" in d: sys.exit(d["error"])
r = d if kind == "render" else d["render"]
print("%s v%d (sha %s) -> %d characters (sha %s)" % (r["template"], r["version"], r["template_sha"], r["chars"], r["sha"]))
print("supplied: %s" % (", ".join(r["supplied"]) or "nothing"))
if r["undefined"]: print("not supplied, rendered as missing: %s" % ", ".join(r["undefined"]))
if r["unused"]: print("supplied and never read by the template (a misspelt name?): %s" % ", ".join(r["unused"]))
if kind == "render":
    print("---"); print(r["instructions"])
elif d.get("dry_run"): print("dry run: nothing dialled. Opening line: %d characters" % len(d["greeting"]))
else: print("call placed: %s on %s" % (d["call_id"], d["line"]))' "$2"
      ;;
    greeting)
      if [ -n "${3:-}" ]; then capi PUT /api/greeting -H 'Content-Type: text/plain; charset=utf-8' --data-binary "$3" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("error") or "default opening line set: %d characters" % len(d["greeting"]))'
      else capi GET /api/greeting | python3 -c 'import json,sys; g=json.load(sys.stdin).get("greeting",""); print("default opening line: %s" % (("%d characters" % len(g)) if g else "not set"))'; fi
      ;;
    calls)
      capi GET "/api/calls?n=${3:-20}" | python3 -c 'import json,sys
for c in json.load(sys.stdin).get("calls", []): print("%s  %-22s %s v%d  prompt %s (%d characters)  %s" % (c["at"][:19], c.get("call_id") or "-", c["template"], c["version"], c["render"], c["chars"], "ok" if c["ok"] else "FAILED: " + c.get("error", "")))'
      ;;
    *) say "crm start | stop | status | verify | token | put NAME FILE | templates | greeting [LINE] | render NAME JSON | call NAME JSON [dry] | calls"; exit 2 ;;
  esac
  ;;

edge)
  # The edge gateway: self-hosted speech models behind an OpenAI-compatible API.
  #   edge start | stop | status       the runtimes under EDGE_HOME, then the gateway
  #   edge token                       print the API token (to your terminal only)
  #   edge models                      GET /v1/models
  #   edge probe [WAV]                 one transcription (of WAV, or of what the synthesis made) and one synthesis, timed
  #   edge say 'TEXT' [OUT.wav]        synthesize to a file
  case "${2:-status}" in
    start|restart) estart || exit 1 ;;
    stop) estop; runtimes_stop; say "the edge gateway and its runtimes are stopped" ;;
    status)
      if erunning; then say "the edge gateway is up on $EDGE_ADDR (pid $(cat "$EPID")): $(curl -s -m 5 "$EDGE_URL/healthz" | cut -c1-160)"
      else say "the edge gateway is not running"; fi
      asr_up && say "  audio8-asr runtime: up on 127.0.0.1:$EDGE_ASR_PORT" || say "  audio8-asr runtime: down"
      tts_up && say "  audio8-tts runtime: up on 127.0.0.1:$EDGE_TTS_PORT" || say "  audio8-tts runtime: down"
      ;;
    token) cat "$EDGE_DIR/api.token" ;;
    models) eapi GET /v1/models | python3 -c 'import json,sys
for m in json.load(sys.stdin)["data"]: e=m["edge"]; print("%-12s %-14s %-12s %6s Hz  %s  %s" % (m["id"], e["kind"], e["status"], e.get("sample_rate",""), e.get("licence",""), e.get("note","")[:70]))' ;;
    say)
      [ -n "${3:-}" ] || { say "edge say 'TEXT' [OUT.wav]"; exit 2; }
      out="${4:-$EDGE_DIR/say.wav}"
      eapi POST /v1/audio/speech -H 'Content-Type: application/json' -o "$out" -w "first audio %{time_starttransfer}s, done %{time_total}s, %{size_download} bytes -> $out\n" \
        -d "$(python3 -c 'import json,sys; print(json.dumps({"model":"audio8-tts","input":sys.argv[1],"response_format":"wav","sample_rate":16000}))' "$3")" ;;
    probe)
      wav="${3:-}"
      if [ -z "$wav" ]; then
        wav="$EDGE_DIR/probe.wav"
        eapi POST /v1/audio/speech -H 'Content-Type: application/json' -o "$wav" -w "synthesis: first audio %{time_starttransfer}s, done %{time_total}s, %{size_download} bytes\n" \
          -d '{"model":"audio8-tts","input":"你好，这是一个测试。","response_format":"wav","sample_rate":16000}' || exit 1
      fi
      eapi POST /v1/audio/transcriptions -F model=audio8-asr -F language=zh -F response_format=verbose_json -F "file=@$wav" -w "\ntranscription: %{time_total}s\n" | cut -c1-400 ;;
    *) say "edge start | stop | status | token | models | probe [WAV] | say 'TEXT' [OUT.wav]"; exit 2 ;;
  esac
  ;;

demo)
  # The browser demo on DEMO_PORT, for a public hostname to point at.
  case "${2:-status}" in
    on|start|restart) touch "$STATE.demo"; dstart || exit 1 ;;
    off|stop) rm -f "$STATE.demo"; dstop; say "the browser demo is stopped, and stays off when golive is restarted" ;;
    status)
      if drunning; then say "the browser demo is up on 127.0.0.1:$DEMO_PORT: $(curl -s -m 2 "http://127.0.0.1:$DEMO_PORT/healthz" | cut -c1-120)"
      else say "the browser demo is not running$([ -f "$STATE.demo" ] && echo ' (it is turned on: scripts/phone-brain.sh demo on starts it)')"; fi
      ;;
    *) say "demo on | off | status"; exit 2 ;;
  esac
  ;;

archive)
  # The permanent record of every finished call. Numbers only: nothing that
  # was said is printed.
  chmod +x "$JBIN" 2>/dev/null
  case "${2:-status}" in
    status)
      "$JBIN" -archive "$ARCHIVE_DIR" -archive-status
      jrunning || say "the journal is not running, so new calls are not being archived: scripts/phone-brain.sh journal start"
      df -h "$ARCHIVE_DIR" 2>/dev/null | awk 'NR==2 {print "disk: " $4 " free of " $2}'
      ;;
    verify) "$JBIN" -archive "$ARCHIVE_DIR" -verify ;;
    mirror)
      # A second copy somewhere else. Nothing is ever deleted from the mirror
      # either: a call removed here by mistake is still there.
      dest="${3:-${ARCHIVE_MIRROR:-}}"
      [ -n "$dest" ] || { say "archive mirror <directory | host:/path>   (or set ARCHIVE_MIRROR)"; exit 2; }
      command -v rsync >/dev/null || { say "rsync is not installed"; exit 1; }
      rsync -a --chmod=D700,F600 --stats "$ARCHIVE_DIR"/ "$dest"/ | grep -E 'Number of (regular )?files transferred|Total transferred file size|Total file size' | sed 's/^/  /'
      say "mirrored $ARCHIVE_DIR to $dest"
      ;;
    *) say "archive status | verify | mirror <directory | host:/path>"; exit 2 ;;
  esac
  ;;

sync)
  "${SUDO[@]}" systemctl start gateway-port-sync.service
  "${SUDO[@]}" journalctl -u gateway-port-sync --no-pager -n 12 | cut -c1-200
  ;;

restore)
  need_docker
  stop_golive
  if [ -f "$STATE.container" ]; then
    name=$(cat "$STATE.container")
    say "starting container '$name'"
    "${DOCKER[@]}" start "$name" >/dev/null || { say "could not start it"; exit 1; }
    for _ in $(seq 1 90); do
      if [ -n "$(pool)" ]; then say "/v1/pool : $(pool | cut -c1-300)"; rm -f "$STATE.container" "$STATE.bind"; exit 0; fi
      sleep 2
    done
    say "'$name' is started but :$PORT does not answer yet; its models can take a while"
  else
    say "no container was recorded as stopped; nothing to start"
  fi
  ;;

*)
  sed -n '2,23p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
  ;;
esac
