#!/usr/bin/env bash
# Put golive where the phone line's agent stands, and take it away again.
#
#   scripts/phone-brain.sh status     what holds the agent port, and the state of
#                                     the PBX and gateway around it. Changes nothing.
#   scripts/phone-brain.sh golive     stop the container on the agent port, start
#                                     golive there, check it answers
#   scripts/phone-brain.sh bench      measure this box's real-provider latency
#                                     through golive, with no phone involved
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
set -uo pipefail
cd "$(dirname "$0")/.."

PORT="${PORT:-8765}"
STATE=".phone-brain"
LOG="${LOGFILE:-golive-telephony.log}"
PROFILE="${PROFILE:-configs/telephony.json}"
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

stop_golive() {
  local pid=""
  [ -f "$STATE.pid" ] && pid=$(cat "$STATE.pid" 2>/dev/null)
  if [ -z "$pid" ] || [ ! -d "/proc/$pid" ]; then
    find_owner || true
    case "$OWNER_COMM" in golive*) pid="$OWNER_PID" ;; *) pid="" ;; esac
  fi
  if [ -n "$pid" ] && grep -q '^golive' "/proc/$pid/comm" 2>/dev/null; then
    kill "$pid" 2>/dev/null || "${SUDO[@]}" kill "$pid"
    for _ in $(seq 1 40); do [ -d "/proc/$pid" ] || break; sleep 0.25; done
    say "stopped golive (pid $pid)"
  fi
  rm -f "$STATE.pid"
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
  [ -x "$BIN" ] || chmod +x bin/* 2>/dev/null
  [ -x "$BIN" ] || { say "no binary at $BIN"; exit 1; }
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
  bind="${bind:-127.0.0.1}"
  for _ in $(seq 1 60); do find_owner || break; sleep 0.5; done
  if find_owner; then say ":$PORT is still held by ${OWNER_COMM:-?} pid=${OWNER_PID:-?}"; exit 1; fi

  printf '%s\n' "$bind" > "$STATE.bind"
  printf '\n--- %s phone-brain: starting golive %s on %s:%s ---\n' "$(date '+%F %T')" "$(cat VERSION 2>/dev/null | cut -d' ' -f1)" "$bind" "$PORT" >> "$LOG"
  mark=$(wc -l < "$LOG")
  GOLIVE_LOG_LEVEL="${GOLIVE_LOG_LEVEL:-debug}" setsid nohup "./$BIN" \
    -profile "$PROFILE" -env "$ENVFILE" -addr "$bind:$PORT" >> "$LOG" 2>&1 < /dev/null &
  echo $! > "$STATE.pid"

  # Up means golive itself answering: anything else that got the port back
  # first would answer a health check too.
  ok=""
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
  say "golive is up on $bind:$PORT (pid $(cat "$STATE.pid"))"
  say "/v1/pool : $(pool | cut -c1-300)"
  say "startup  :"
  tail -n +"$((mark + 1))" "$LOG" | grep -E 'level=(INFO|WARN|ERROR)' | grep -vE 'msg=http ' | cut -c1-240 | head -25 | sed 's/^/  /'
  say
  say "log      : $(pwd)/$LOG"
  say "next     : scripts/phone-brain.sh bench     (no phone needed)"
  ;;

bench)
  is_golive || { say "golive is not on :$PORT; run: scripts/phone-brain.sh golive"; exit 1; }
  chmod +x "$BENCH" 2>/dev/null
  # Things a caller says, short and long: a turn's length decides whether there
  # is a pause worth speculating in.
  SPEAK="${SPEAK:-你好，今天天气怎么样？|帮我查一下明天上午去上海的航班有哪些。|你能给我讲一个简短的笑话吗？|我想了解一下你们周末的营业时间。|好的，谢谢。}"
  "./$BENCH" -a "golive=ws://127.0.0.1:$PORT/v1/live" -speak "$SPEAK" -rate 16000 \
    -runs "${RUNS:-15}" -warmup 2 -v -md bench-phone.md -json bench-phone.json
  say
  say "== the same turns, stage by stage (from $LOG)"
  scripts/call-latency.sh "$LOG" | tail -n "$(( ${RUNS:-15} + 8 ))"
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
  sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
  ;;
esac
