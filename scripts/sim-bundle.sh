#!/usr/bin/env bash
# Build the simulated customer from its own branch into one archive for a box
# with no Go: dist/golive-sim.tar.gz.
#
#   scripts/sim-bundle.sh [ref]     ref defaults to origin/codex/inbound-user-simulator
#
# The simulator (cmd/golive-simulator, cmd/golive-simctl) lives on the branch
# `codex/inbound-user-simulator` and is a service of its own: it answers a call
# as a synthetic customer and never touches the phone line's golive. Nothing
# from it is merged here; this script checks the ref out into a temporary
# worktree, builds its two commands for linux/amd64, and packs them with the
# scenarios, provider profiles, deployment package and the box runbook
# (scripts/sim-brain.sh, from this branch).
set -euo pipefail
cd "$(dirname "$0")/.."

ref=${1:-origin/codex/inbound-user-simulator}
out=dist/golive-sim.tar.gz
work=$(mktemp -d)
trap 'git worktree remove --force "$work/src" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

git worktree add --detach "$work/src" "$ref" >/dev/null
commit=$(git -C "$work/src" rev-parse --short HEAD)
echo "simulator source: $ref ($commit)"

stage="$work/golive-sim"
mkdir -p "$stage/bin" "$stage/scenarios" "$stage/configs" "$stage/scripts" "$stage/docs"
(
  cd "$work/src"
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' \
    -o "$stage/bin/golive-simulator" ./cmd/golive-simulator
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' \
    -o "$stage/bin/golive-simctl" ./cmd/golive-simctl
  cp -r scenarios/simulator scenarios/suites "$stage/scenarios/"
  rm -f "$stage"/scenarios/simulator/*.go
  cp -r configs/simulator "$stage/configs/"
  cp -r deploy/simulator "$stage/deploy"
  rm -rf "$stage/deploy/data"
  cp README.simulator.md docs/simulator-carrier-acceptance.md cmd/golive-simctl/README.md "$stage/docs/" 2>/dev/null || true
  mv "$stage/docs/README.md" "$stage/docs/simctl.md" 2>/dev/null || true
)
cp scripts/sim-brain.sh "$stage/scripts/"
chmod +x "$stage/scripts/sim-brain.sh" "$stage"/bin/*
echo "$commit" > "$stage/SOURCE_COMMIT"

mkdir -p dist
tar -C "$work" -czf "$out" golive-sim
ls -l "$out"
