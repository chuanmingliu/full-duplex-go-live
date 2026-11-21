#!/bin/bash
# Runs reviewed jobs in the local runner queue on the configured box over SSH,
# and puts what they print in ~/golive/.runner/out. Start it in a terminal on
# the Mac and leave it running; Ctrl-C stops it.
#
#   BOX=user@box bash scripts/local-ops/runner.sh
#
# What a job can do:
#   - run shell commands ON THE BOX, as your user there (no sudo: there is no
#     terminal for a password, so anything needing root simply fails);
#   - "#push <file under ~/golive> <path on the box>"  copy a file to the box;
#   - "#pull <path on the box> <name>"                 copy a file back into
#                                                      ~/golive/.runner/out/.
# Nothing in a job runs on this Mac. Every job is printed here before it runs,
# and kept afterwards in ~/golive/.runner/done.
: "${BOX:?Set BOX to the box SSH destination (user@host or SSH alias)}"
HERE="${GOLIVE_RUNNER_HOME:-$HOME/golive}"
DIR="$HERE/.runner"
mkdir -p "$DIR/in" "$DIR/out" "$DIR/done" "$HOME/.ssh"
SOCK="${GOLIVE_RUNNER_SOCKET:-$HOME/.ssh/golive-runner.sock}"
OPTS=(-o ControlMaster=auto -o "ControlPath=$SOCK" -o ControlPersist=6h -o ServerAliveInterval=30 -o ConnectTimeout=8)

echo "connecting to $BOX (it may ask for that machine's password once) ..."
if ! ssh "${OPTS[@]}" "$BOX" 'echo "connected: $(whoami)@$(hostname)"'; then
  echo "could not reach $BOX"; exit 1
fi
echo "waiting for jobs in $DIR/in    (Ctrl-C to stop)"
date +%s > "$DIR/alive"

while :; do
  date +%s > "$DIR/alive"
  for job in "$DIR"/in/*.job; do
    [ -f "$job" ] || continue
    id="$(basename "$job" .job)"
    part="$DIR/out/$id.part"; out="$DIR/out/$id.txt"
    echo; echo "──── job $id   $(date +%T) ────"; sed 's/^/  | /' "$job"
    : > "$part"

    grep '^#push ' "$job" | while read -r _ src dst; do
      case "$src" in
        /*|*..*) echo "[runner] push refused: $src" >> "$part" ;;
        *) if scp -q -o "ControlPath=$SOCK" "$HERE/$src" "$BOX:$dst" >> "$part" 2>&1; then
             echo "[runner] pushed $src -> $dst" >> "$part"
           else
             echo "[runner] push FAILED: $src" >> "$part"
           fi ;;
      esac
    done

    limit="$(grep -m1 '^#timeout ' "$job" | awk '{print $2}')"; limit="${limit:-900}"
    # Passed as an argument, not on stdin: a command inside the job that reads
    # its input would otherwise swallow the rest of the job.
    body="$(grep -v -e '^#push ' -e '^#pull ' -e '^#timeout ' "$job")"
    ssh "${OPTS[@]}" "$BOX" "bash -c $(printf '%q' "$body")" < /dev/null >> "$part" 2>&1 &
    pid=$!; t=0
    while kill -0 "$pid" 2>/dev/null; do
      sleep 1; t=$((t + 1))
      [ $((t % 20)) -eq 0 ] && date +%s > "$DIR/alive"
      if [ "$t" -ge "$limit" ]; then
        kill "$pid" 2>/dev/null
        echo "[runner] stopped after ${limit}s" >> "$part"
      fi
    done
    wait "$pid"; code=$?

    grep '^#pull ' "$job" | while read -r _ src name; do
      case "$name" in
        */*|*..*|"") echo "[runner] pull refused: $name" >> "$part" ;;
        *) if scp -q -o "ControlPath=$SOCK" "$BOX:$src" "$DIR/out/$name" >> "$part" 2>&1; then
             echo "[runner] pulled $src -> $name" >> "$part"
           else
             echo "[runner] pull FAILED: $src" >> "$part"
           fi ;;
      esac
    done

    echo "[runner] exit $code at $(date +%T)" >> "$part"
    mv "$part" "$out"
    mv "$job" "$DIR/done/"
    echo "──── finished (exit $code): $(wc -l < "$out" | tr -d ' ') lines in .runner/out/$id.txt"
  done
  sleep 2
done
