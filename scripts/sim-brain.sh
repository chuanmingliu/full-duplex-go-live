#!/usr/bin/env bash
# The simulated customer on the box: start it, reserve a call, read the report.
#
#   scripts/sim-brain.sh init            make the three private tokens (once) and the
#                                        directories; prints nothing secret
#   scripts/sim-brain.sh start [mock|providers]
#                                        start the receiver on SIM_ADDR with that
#                                        profile (mock: synthetic tones, no keys;
#                                        providers: PROFILE_FILE with the keys in
#                                        PROVIDERS_ENV). Started under nohup; the
#                                        log is in LOG.
#   scripts/sim-brain.sh stop            stop it
#   scripts/sim-brain.sh status          whether it answers, whether its gateway is
#                                        reachable, which run is live
#   scripts/sim-brain.sh scenarios       the scenarios shipped with it
#   scripts/sim-brain.sh validate <file> check a scenario without reserving a call
#   scripts/sim-brain.sh prepare <scenario> [key]
#                                        reserve one call for that scenario (a name
#                                        under scenarios/simulator, or a file) and
#                                        print the run id and the SIP target the
#                                        assistant side must dial within 120 s
#   scripts/sim-brain.sh run [id]        the run's state (the last prepared one by
#                                        default); `events`, `report`, `transcript`
#                                        and `stop-run` likewise
#   scripts/sim-brain.sh package [update|status|logs [n]]
#                                        the Docker package (its gateway + the receiver
#                                        wired to it): `update` pulls the branch into
#                                        SRC_DIR, rebuilds the receiver's image and
#                                        restarts it with the overrides; `status` what
#                                        runs and from which commit; `logs` its lines
#   scripts/sim-brain.sh retro <call id> [build]
#                                        the retrospective of a simulated call in
#                                        numbers (scripts/sim-retro.py): golive's
#                                        stages per turn, the customer's report;
#                                        written to journal/retros/<call id>.md
#   scripts/sim-brain.sh suite <manifest> [output dir]
#                                        the sequential suite runner: reserves one
#                                        call at a time and waits for the caller
#
# Written for the layout the bundle unpacks to (scripts/sim-bundle.sh):
#
#   SIM_DIR=~/golive-sim      bin/ scenarios/ configs/ deploy/ docs/ scripts/
#   SIM_ADDR=127.0.0.1:18865  the receiver's HTTP control and media listener
#   TOKENS_ENV=$SIM_DIR/tokens.env       SIM_CONTROL_TOKEN SIM_WEBHOOK_TOKEN SIM_MEDIA_TOKEN
#   PROVIDERS_ENV=$SIM_DIR/providers.env the simulator's OWN provider keys, written by
#                                        the owner (TENCENT_ASR_*, DEEPSEEK_API_KEY,
#                                        MINIMAX_TTS_*); never the phone line's
#   PROFILE_FILE=$SIM_DIR/profile.json   the providers profile, copied from
#                                        configs/simulator/providers.example.json
#   SRC_DIR=$SIM_DIR/src                 a clone of the branch, which the Docker package
#                                        builds from; SIM_REF its branch
#   OVERRIDES=$SIM_DIR/compose.*.yml     compose overrides applied after the package's
#                                        own file, in name order (the receiver's SIP
#                                        domain, the providers profile)
#
# The receiver reserves a run and waits to be CALLED: nothing here dials. The
# assistant side (Active Call, through a route the owner sets up) must send its
# INVITE for sip:<run_id>@simulator.invalid to the simulator's dedicated gateway
# (deploy/compose.yml, SIP 25060 inside its own Docker network). Without that
# gateway `readyz` is 503 and a reserved run simply expires.
#
# Nothing in this script prints a token or a key: they are read from files of
# mode 600 and passed in the environment of the processes that need them.
set -euo pipefail

SIM_DIR=${SIM_DIR:-$HOME/golive-sim}
SIM_ADDR=${SIM_ADDR:-127.0.0.1:18865}
TOKENS_ENV=${TOKENS_ENV:-$SIM_DIR/tokens.env}
PROVIDERS_ENV=${PROVIDERS_ENV:-$SIM_DIR/providers.env}
PROFILE_FILE=${PROFILE_FILE:-$SIM_DIR/profile.json}
DATA_DIR=${DATA_DIR:-$SIM_DIR/data}
SRC_DIR=${SRC_DIR:-$SIM_DIR/src}
SIM_REF=${SIM_REF:-codex/inbound-user-simulator}
PROJECT=golive-user-simulator
LOG=${LOG:-$SIM_DIR/log/simulator.log}
PIDFILE=$SIM_DIR/simulator.pid
LAST_RUN=$SIM_DIR/data/last-run
BASE=http://$SIM_ADDR

die() { echo "sim-brain: $*" >&2; exit 2; }
have_tokens() { [ -f "$TOKENS_ENV" ]; }
load_tokens() {
  have_tokens || die "no tokens yet; run: sim-brain.sh init"
  set -a; . "$TOKENS_ENV"; set +a
  [ "${#SIM_CONTROL_TOKEN}" -ge 32 ] || die "SIM_CONTROL_TOKEN in $TOKENS_ENV is too short"
}
api() { # method path [curl args...]
  local m=$1 p=$2; shift 2
  curl -sS -X "$m" "$BASE$p" -H "Authorization: Bearer $SIM_CONTROL_TOKEN" "$@"
}
pretty() { python3 -I -m json.tool 2>/dev/null || cat; }
pid() { [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null && cat "$PIDFILE"; }
scenario_path() {
  local s=$1
  if [ -f "$s" ]; then echo "$s"; return; fi
  if [ -f "$SIM_DIR/scenarios/simulator/$s.json" ]; then echo "$SIM_DIR/scenarios/simulator/$s.json"; return; fi
  if [ -f "$SIM_DIR/scenarios/custom/$s.json" ]; then echo "$SIM_DIR/scenarios/custom/$s.json"; return; fi
  die "no scenario $s (see: sim-brain.sh scenarios)"
}
run_id() {
  local id=${1:-}
  if [ -z "$id" ]; then [ -f "$LAST_RUN" ] && id=$(cat "$LAST_RUN"); fi
  [ -n "$id" ] || die "which run? none prepared yet"
  echo "$id"
}

cmd=${1:-status}; shift || true
case "$cmd" in
  init)
    mkdir -p "$SIM_DIR/data" "$SIM_DIR/log"
    chmod 700 "$SIM_DIR/data"
    if have_tokens; then
      echo "tokens already in $TOKENS_ENV (kept)"
    else
      umask 077
      {
        echo "# Three independent private tokens for the simulated customer. Never"
        echo "# the phone line's keys. Made by sim-brain.sh init on $(date +%F)."
        for k in SIM_CONTROL_TOKEN SIM_WEBHOOK_TOKEN SIM_MEDIA_TOKEN; do
          echo "$k=$(openssl rand -hex 32)"
        done
        echo "SIM_SIP_DOMAIN=simulator.invalid"
      } > "$TOKENS_ENV"
      echo "tokens written to $TOKENS_ENV (mode 600)"
    fi
    if [ ! -f "$PROFILE_FILE" ]; then
      cp "$SIM_DIR/configs/simulator/providers.example.json" "$PROFILE_FILE"
      echo "providers profile copied to $PROFILE_FILE (edit language/voice there)"
    fi
    if [ ! -f "$PROVIDERS_ENV" ]; then
      umask 077
      cat > "$PROVIDERS_ENV" <<'EOF'
# The simulated customer's OWN provider accounts — dedicated ones, not the phone
# line's (its quotas are the phone line's). Fill in and keep mode 600.
TENCENT_ASR_APP_ID=
TENCENT_ASR_SECRET_ID=
TENCENT_ASR_SECRET_KEY=
DEEPSEEK_API_KEY=
MINIMAX_TTS_API_KEY=
MINIMAX_TTS_VOICE_ID=
#MINIMAX_TTS_WEBSOCKET_ENDPOINT=
EOF
      echo "provider env skeleton written to $PROVIDERS_ENV (mode 600; fill it in for the providers profile)"
    fi
    ;;

  start)
    which=${1:-mock}
    load_tokens
    if p=$(pid); then die "already running (pid $p); stop it first"; fi
    case "$which" in
      mock) profile=$SIM_DIR/configs/simulator/mock.json ;;
      providers)
        profile=$PROFILE_FILE
        [ -f "$PROVIDERS_ENV" ] || die "no $PROVIDERS_ENV"
        set -a; . "$PROVIDERS_ENV"; set +a
        for k in TENCENT_ASR_APP_ID TENCENT_ASR_SECRET_ID TENCENT_ASR_SECRET_KEY DEEPSEEK_API_KEY MINIMAX_TTS_API_KEY MINIMAX_TTS_VOICE_ID; do
          [ -n "${!k:-}" ] || die "$k is empty in $PROVIDERS_ENV"
        done
        ;;
      *) die "start mock|providers" ;;
    esac
    mkdir -p "$(dirname "$LOG")" "$DATA_DIR"
    SIM_DATA_DIR=$DATA_DIR nohup "$SIM_DIR/bin/golive-simulator" -profile "$profile" -addr "$SIM_ADDR" -data "$DATA_DIR" >> "$LOG" 2>&1 &
    echo $! > "$PIDFILE"
    sleep 1
    if ! kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then tail -5 "$LOG"; die "did not start"; fi
    echo "simulator ($which) pid $(cat "$PIDFILE") on $SIM_ADDR; source $(cat "$SIM_DIR/SOURCE_COMMIT" 2>/dev/null || echo ?)"
    "$0" status
    ;;

  stop)
    if p=$(pid); then kill "$p"; sleep 0.5; echo "stopped pid $p"; else echo "not running"; fi
    rm -f "$PIDFILE"
    ;;

  status)
    if p=$(pid); then echo "process: pid $p"; else echo "process: not running"; fi
    printf 'livez:  '; curl -s -o /dev/null -w '%{http_code}\n' "$BASE/livez" || echo unreachable
    printf 'readyz: '; curl -s -w ' %{http_code}\n' "$BASE/readyz" || echo unreachable
    if have_tokens; then
      load_tokens
      api GET /sim/v1/runs 2>/dev/null | python3 -I -c '
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit(0)
runs=d.get("runs",d) if isinstance(d,dict) else d
if not isinstance(runs,list): sys.exit(0)
live=[r for r in runs if r.get("phase") in ("prepared","waiting","ringing","answering","connected","ending")]
print(f"runs: {len(runs)} retained, {len(live)} live")
for r in live[:3]: print("  ",r.get("run_id"),r.get("phase"),r.get("dial_target_reference",""))
' || true
    fi
    ;;

  scenarios)
    for f in "$SIM_DIR"/scenarios/simulator/*.json; do
      n=$(basename "$f" .json)
      python3 -I -c '
import json,sys
d=json.load(open(sys.argv[1])); n=sys.argv[2]
lang=d.get("language","?"); out=d.get("expected_outcome",""); goal=d.get("goal") or d.get("persona") or ""
print(f"{n:24} {lang:3} {out:22} {goal[:60]}")' "$f" "$n"
    done
    echo "suites: $(ls "$SIM_DIR"/scenarios/suites/ | tr '\n' ' ')"
    ;;

  validate)
    load_tokens
    f=$(scenario_path "${1:?scenario}")
    api POST /sim/v1/validate -H 'Content-Type: application/json' --data-binary @"$f" | pretty
    ;;

  prepare)
    load_tokens
    f=$(scenario_path "${1:?scenario}")
    key=${2:-$(basename "$f" .json)-$(date +%Y%m%d-%H%M%S)}
    out=$(api POST /sim/v1/runs -H "Idempotency-Key: $key" -H 'Content-Type: application/json' --data-binary @"$f" -w '\n%{http_code}')
    code=${out##*$'\n'}; body=${out%$'\n'*}
    [ "$code" = 200 ] || { echo "$body"; die "prepare: HTTP $code"; }
    id=$(printf '%s' "$body" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["run_id"])')
    echo "$id" > "$LAST_RUN"
    printf '%s' "$body" | python3 -I -c '
import json,sys; d=json.load(sys.stdin)
print("run_id:      ", d["run_id"])
print("dial target: ", d.get("dial_target_reference"))
print("expires:     ", d.get("reservation_expires_at"), "(the INVITE must arrive before this)")
print("phase:       ", d.get("phase"))'
    ;;

  run|events|report|transcript|stop-run)
    load_tokens
    id=$(run_id "${1:-}")
    case "$cmd" in
      run)        api GET "/sim/v1/runs/$id" | pretty ;;
      events)     api GET "/sim/v1/runs/$id/events?after=${2:-0}" | pretty ;;
      report)     api GET "/sim/v1/runs/$id/report" | pretty ;;
      transcript) api GET "/sim/v1/runs/$id/transcript" | pretty ;;
      stop-run)   api POST "/sim/v1/runs/$id/stop" | pretty ;;
    esac
    ;;

  package)
    # The package needs docker without sudo (the docker group) and the tokens
    # in its environment; they are exported here from the file, never printed.
    have_tokens || die "no tokens yet; run: sim-brain.sh init"
    [ -d "$SRC_DIR/deploy/simulator" ] || die "no clone at $SRC_DIR: git clone -b $SIM_REF <repo> $SRC_DIR"
    compose() {
      local files=(-f "$SRC_DIR/deploy/simulator/compose.yml")
      for o in "$SIM_DIR"/compose.*.yml; do [ -f "$o" ] && files+=(-f "$o"); done
      ( set -a; . "$TOKENS_ENV"; set +a; SIM_SOURCE_COMMIT=$(git -C "$SRC_DIR" rev-parse --short HEAD) docker compose -p "$PROJECT" "${files[@]}" "$@" )
    }
    case "${1:-status}" in
      update)
        before=$(git -C "$SRC_DIR" rev-parse --short HEAD)
        # GitHub is reached through the box's tunnel and sometimes resets;
        # the clone as it stands is still worth building.
        git -C "$SRC_DIR" pull -q --ff-only 2>&1 | tail -1 || echo "git pull failed; building what the clone holds"
        after=$(git -C "$SRC_DIR" rev-parse --short HEAD)
        echo "source: $before -> $after ($SIM_REF)"
        if p=$(pid); then die "the host receiver is running (pid $p); stop it: the package takes its port"; fi
        compose up -d --build simulator 2>&1 | grep -E "Built|Started|Created|Recreated|Running|rror" | sed 's/^ *//' | tail -6
        sleep 3
        "$0" package status
        ;;
      status)
        docker ps --filter "label=com.docker.compose.project=$PROJECT" --format '{{.Names}}  {{.Status}}' | sed 's/^/  /'
        echo "  gateway: $(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$PROJECT-activecall-1" 2>/dev/null || echo '?') (SIP 25060)"
        echo "  receiver source: $(docker inspect -f '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$PROJECT-simulator-1" 2>/dev/null || true) clone: $(git -C "$SRC_DIR" rev-parse --short HEAD 2>/dev/null)"
        "$0" status | sed -n 2,3p
        ;;
      logs)
        docker logs --since "${3:-10m}" "$PROJECT-simulator-1" 2>&1 | sed -E 's/[0-9]{7,}/<num>/g' | tail -"${2:-40}"
        ;;
      *) die "package update | status | logs [n] [since]" ;;
    esac
    ;;

  retro)
    [ -n "${1:-}" ] || die "retro <call id> [build]"
    here=$(cd "$(dirname "$0")/.." && pwd)
    tele=${TELEPHONY_DIR:-$HOME/golive-telephony}
    python3 -I "$tele/scripts/sim-retro.py" "$1" --log "$tele/golive-telephony.log" --sim-dir "$SIM_DIR" --sim-url "$BASE" ${2:+--build "$2"} --md "$tele/journal/retros/$1.md"
    ;;

  suite)
    load_tokens
    m=${1:?manifest}; [ -f "$m" ] || m=$SIM_DIR/scenarios/suites/$1
    [ -f "$m" ] || die "no manifest $1"
    out=${2:-$SIM_DIR/suites/$(date +%Y%m%d-%H%M%S)}
    mkdir -p "$(dirname "$out")"
    exec "$SIM_DIR/bin/golive-simctl" suite --file "$m" --url "$BASE" --token-env SIM_CONTROL_TOKEN --output "$out"
    ;;

  *) sed -n '2,45p' "$0"; exit 2 ;;
esac
