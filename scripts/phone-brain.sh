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
  # How many chat-model keys the pool has. Counts only: a key is never printed.
  find_env || { say "no provider keys found; set ENVFILE"; exit 1; }
  one=$(grep -cE '^DEEPSEEK_API_KEY=.+' "$ENVFILE" || true)
  list=$(grep -m1 '^DEEPSEEK_API_KEYS=' "$ENVFILE" | cut -d= -f2- | tr ',; ' '\n\n\n' | grep -c . || true)
  file=$(grep -m1 '^DEEPSEEK_API_KEYS_FILE=' "$ENVFILE" | cut -d= -f2- | tr -d '"'"'"'"')
  infile=0
  [ -n "$file" ] && [ -f "$file" ] && infile=$(sed 's/#.*//' "$file" | grep -c '[^[:space:]]' || true)
  say "env file              : $ENVFILE"
  say "DEEPSEEK_API_KEY      : $one"
  say "DEEPSEEK_API_KEYS     : $list"
  say "DEEPSEEK_API_KEYS_FILE: ${file:-(not set)}${file:+ — $infile key(s)}"
  say "(a key listed twice counts once in the pool; golive logs the pool it built at debug level as key=N)"
  ;;

addkey)
  # Add one chat-model key to the pool without it reaching the screen or the
  # shell history. Run it once per key, then: scripts/phone-brain.sh restart
  find_env || { say "no provider keys found; set ENVFILE"; exit 1; }
  file=$(grep -m1 '^DEEPSEEK_API_KEYS_FILE=' "$ENVFILE" | cut -d= -f2- | tr -d '"'"'"'"')
  if [ -z "$file" ]; then
    file="$HOME/golive-deepseek.keys"
    printf '\nDEEPSEEK_API_KEYS_FILE=%s\n' "$file" >> "$ENVFILE"
    say "added DEEPSEEK_API_KEYS_FILE=$file to $ENVFILE"
  fi
  touch "$file" && chmod 600 "$file"
  printf 'paste one key and press Enter (it is not shown): '
  IFS= read -rs key; echo
  key=$(printf '%s' "$key" | tr -d '[:space:]"'"'"'')
  [ -n "$key" ] || { say "nothing entered; nothing changed"; exit 1; }
  if grep -qxF "$key" "$file" || grep -qF "DEEPSEEK_API_KEY=$key" "$ENVFILE"; then
    say "that key is already in the pool; nothing changed"
  else
    printf '%s\n' "$key" >> "$file"
    say "added. $file now holds $(sed 's/#.*//' "$file" | grep -c '[^[:space:]]') key(s)"
  fi
  unset key
  say "to use them: scripts/phone-brain.sh restart    to measure them: scripts/phone-brain.sh llm -pool"
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
