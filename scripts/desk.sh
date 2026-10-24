#!/usr/bin/env bash
# Read and write the desk as one participant.
#
#   scripts/desk.sh me                         who this token belongs to, and the switches
#   scripts/desk.sh inbox [n]                  open tasks and questions addressed to me (or all)
#   scripts/desk.sh recent [n]                 the last n notes (20)
#   scripts/desk.sh thread <id>                one thread, oldest first
#   scripts/desk.sh post <to> <kind> <subject> [body-file|-] [ref ...]
#                                              a new note; body from a file or stdin ("-")
#   scripts/desk.sh reply <note id> [kind] [body-file|-] [ref ...]
#                                              a note in that thread, addressed to its author
#   scripts/desk.sh done <note id>             close a task or a question
#   scripts/desk.sh watch                      print notes as they arrive (the event stream)
#   scripts/desk.sh pause | resume             the owner's switch (owner token only)
#
#   DESK_URL=http://127.0.0.1:8792             the desk (on the box: 127.0.0.1; from the
#                                              LAN: the box's address, same port)
#   DESK_TOKEN_FILE=~/golive-desk/claude.token whose token: owner.token, claude.token or
#                                              codex.token; or DESK_AS=claude|codex|owner
#                                              to pick it in DESK_DIR
#
# The token is read from the file and sent in a header; it is never on a
# command line or in the output. Write numbers, shapes and ids; no
# transcripts, keys or phone numbers.
set -euo pipefail

DESK_URL=${DESK_URL:-http://127.0.0.1:8792}
DESK_DIR=${DESK_DIR:-$HOME/golive-desk}
DESK_AS=${DESK_AS:-claude}
DESK_TOKEN_FILE=${DESK_TOKEN_FILE:-$DESK_DIR/$DESK_AS.token}
[ -r "$DESK_TOKEN_FILE" ] || { echo "desk: no token at $DESK_TOKEN_FILE (DESK_TOKEN_FILE or DESK_AS)" >&2; exit 2; }

api() { # METHOD PATH [curl args]
  local m=$1 p=$2; shift 2
  curl -sS -m 30 -X "$m" -H @<(printf 'Authorization: Bearer %s\nContent-Type: application/json\n' "$(tr -d '[:space:]' < "$DESK_TOKEN_FILE")") "$@" "$DESK_URL$p"
}
show() { # notes as lines
  python3 -I -c '
import json, sys
d = json.load(sys.stdin)
if "error" in d:
    sys.exit("desk: " + d["error"])
notes = d.get("notes") or ([d] if "id" in d else [])
for n in notes:
    flag = "open" if n.get("open") else ("done" if n.get("done_by") else "")
    i, at, fr, to, kind, subj = n["id"], n["at"][5:16], n["from"], n["to"], n["kind"], n.get("subject", "")
    print(f"{i:>6}  {at}  {fr:>6} -> {to:<6} {kind:<8} {flag:<4} {subj}")
    if n.get("body"):
        for line in n["body"].splitlines(): print(" " * 9 + line)
    if n.get("refs"): print(" " * 9 + "refs: " + ", ".join(n["refs"]))
'
}
body_json() { # [file|-] → JSON string via python
  local src=${1:-}
  if [ -z "$src" ]; then printf '""'; return; fi
  if [ "$src" = "-" ]; then python3 -I -c 'import json,sys; print(json.dumps(sys.stdin.read()))'
  else python3 -I -c 'import json,sys; print(json.dumps(open(sys.argv[1], encoding="utf-8").read()))' "$src"; fi
}
refs_json() { python3 -I -c 'import json,sys; print(json.dumps(sys.argv[1:]))' "$@"; }

cmd=${1:-me}; shift || true
case "$cmd" in
  me) api GET /api/me ;;
  inbox) api GET "/api/notes?open=1&to=me&limit=${1:-50}" | show ;;
  recent) api GET "/api/notes?limit=${1:-20}" | show ;;
  thread) api GET "/api/notes?thread=${1:?note id}" | show ;;
  post)
    to=${1:?to}; kind=${2:?kind}; subject=${3:?subject}; body=$(body_json "${4:-}"); shift 4 2>/dev/null || shift $#
    refs=$(refs_json "$@")
    python3 -I -c 'import json,sys; print(json.dumps({"to":sys.argv[1],"kind":sys.argv[2],"subject":sys.argv[3],"body":json.loads(sys.argv[4]),"refs":json.loads(sys.argv[5])}))' \
      "$to" "$kind" "$subject" "$body" "$refs" > /tmp/desk-post.$$
    api POST /api/notes --data-binary @/tmp/desk-post.$$ | show; rm -f /tmp/desk-post.$$ ;;
  reply)
    id=${1:?note id}; kind=${2:-message}; body=$(body_json "${3:-}"); shift 3 2>/dev/null || shift $#
    refs=$(refs_json "$@")
    author=$(api GET "/api/notes?thread=$id" | python3 -I -c 'import json,sys; ns=[n for n in json.load(sys.stdin)["notes"] if n["id"]==sys.argv[1]]; print(ns[0]["from"] if ns else "")' "$id")
    [ -n "$author" ] || { echo "desk: no note $id" >&2; exit 1; }
    python3 -I -c 'import json,sys; print(json.dumps({"to":sys.argv[1],"kind":sys.argv[2],"reply_to":sys.argv[3],"body":json.loads(sys.argv[4]),"refs":json.loads(sys.argv[5])}))' \
      "$author" "$kind" "$id" "$body" "$refs" > /tmp/desk-post.$$
    api POST /api/notes --data-binary @/tmp/desk-post.$$ | show; rm -f /tmp/desk-post.$$ ;;
  done) api POST "/api/notes/${1:?note id}/done" -d '{}' | show ;;
  watch) api GET /api/events -N ;;
  pause) api PUT /api/control -d '{"paused":true}' ;;
  resume) api PUT /api/control -d '{"paused":false}' ;;
  *) sed -n '2,24p' "$0"; exit 2 ;;
esac
