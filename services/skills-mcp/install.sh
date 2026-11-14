#!/bin/sh
# Run from an extracted release directory on the ThinkPad.
set -eu
umask 077
cd "$(dirname "$0")"
: "${RELEASE:?set the immutable release tag}"
export STACK_DIR="${STACK_DIR:-$HOME/golive-skills-mcp}"
export CRM_TOKEN_FILE="${CRM_TOKEN_FILE:-$HOME/golive-crm/api.token}"
export SERVICE_UID="$(id -u)" SERVICE_GID="$(id -g)"
test -s "$CRM_TOKEN_FILE"
mkdir -p "$STACK_DIR/secrets" "$STACK_DIR/data"
for name in runtime admin; do
  key="$STACK_DIR/secrets/$name"
  if [ ! -e "$key" ]; then
    tmp="$(mktemp "$STACK_DIR/secrets/.new.XXXXXX")"
    openssl rand -hex 32 > "$tmp"
    ln "$tmp" "$key" || test -s "$key"
    rm -f "$tmp"
  fi
  test -s "$key"
done
docker compose build catalog
docker compose up --no-build --wait --wait-timeout 60 catalog
python3 smoke.py
