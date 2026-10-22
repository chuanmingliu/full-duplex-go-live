#!/bin/bash
# Runs, ON THIS MAC, the jobs Claude leaves in ~/golive/.macrunner/in, and puts
# what they print in ~/golive/.macrunner/out. The sibling of runner.sh (which
# runs jobs on the test box over ssh): Claude's shell on this machine is a
# Linux VM that cannot see Metal, so anything that needs MLX runs here.
#
#   bash ~/golive/mac-runner.sh
#
# Every job is printed here before it runs and kept in .macrunner/done.
# A job is a bash script; it runs with cwd ~/golive/omni and these set:
#   OMNI=~/golive/omni   HF_HOME=$OMNI/hf   MODELSCOPE_CACHE=$OMNI/ms
# A job that needs longer than the default 2 h puts "#timeout <seconds>" on a line.
HERE="$(cd "$(dirname "$0")" && pwd)"
DIR="$HERE/.macrunner"
OMNI="$HERE/omni"
mkdir -p "$DIR/in" "$DIR/out" "$DIR/done" "$OMNI"
echo "mac-runner: $(sysctl -n machdep.cpu.brand_string 2>/dev/null) · $(($(sysctl -n hw.memsize)/1073741824)) GB · $(sw_vers -productVersion)"
echo "waiting for jobs in $DIR/in    (Ctrl-C to stop)"
date +%s > "$DIR/alive"
while :; do
  date +%s > "$DIR/alive"
  for job in "$DIR"/in/*.job; do
    [ -f "$job" ] || continue
    id="$(basename "$job" .job)"
    part="$DIR/out/$id.part"; out="$DIR/out/$id.txt"
    echo; echo "──── job $id   $(date +%T) ────"; sed 's/^/  | /' "$job"
    to="$(grep -m1 '^#timeout ' "$job" | awk '{print $2}')"; to="${to:-7200}"
    (
      cd "$OMNI" && export OMNI="$OMNI" HF_HOME="$OMNI/hf" MODELSCOPE_CACHE="$OMNI/ms" \
        PYTORCH_ENABLE_MPS_FALLBACK=1 PATH="$OMNI/.venv/bin:$HOME/.local/bin:/opt/homebrew/bin:$PATH"
      perl -e 'alarm shift; exec @ARGV' "$to" bash "$job"
      echo "exit: $?"
    ) > "$part" 2>&1
    mv "$part" "$out"; mv "$job" "$DIR/done/$id.job"
    echo "──── done $id   $(date +%T)   → .macrunner/out/$id.txt"
    tail -5 "$out" | sed 's/^/  > /'
  done
  sleep 2
done
