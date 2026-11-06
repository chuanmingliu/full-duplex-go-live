#!/usr/bin/env bash
# Report last sound -> first audio AND first content leaving golive.
# Existing use: scripts/call-latency.sh [LOG [LEGACY_PAUSE_MS]]
# Acceptance: scripts/call-latency.sh LOG --gate --target-ms 1200 --min-turns 20
# See --help and TELEPHONY.md. The PBX/carrier/handset leg is not measured.
set -euo pipefail
exec python3 -B "$(dirname "$0")/call_latency.py" "$@"
