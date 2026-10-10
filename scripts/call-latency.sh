#!/usr/bin/env bash
# Per-turn latency of the calls in a golive log, as a table you can paste.
#
#   scripts/call-latency.sh                       # golive-telephony.log
#   scripts/call-latency.sh golive.log            # some other log
#   scripts/call-latency.sh golive.log 380        # with vad.min_silence_ms
#
# Every figure is milliseconds from the moment golive judged the caller's turn
# over, which is vad.min_silence_ms after their last sound. "+pause" adds that
# silence back (the second argument, default 380), so it is the wait from the
# caller's last sound to the first audio leaving golive. A speculative turn
# spent the pause working, which is why its figures are smaller — the pause
# itself is still there. Neither column includes what the gateway, the PBX and
# the carrier add on the way to the handset.
set -euo pipefail
LOG="${1:-golive-telephony.log}"
PAUSE="${2:-380}"
[ -f "$LOG" ] || { echo "no such log: $LOG" >&2; exit 1; }

grep 'turn: response latency' "$LOG" | awk -v pause="$PAUSE" '
function field(name,   i, kv) {
  for (i = 1; i <= NF; i++) { split($i, kv, "="); if (kv[1] == name) return kv[2] }
  return ""
}
BEGIN {
  printf "%-9s %-8s %-5s %6s %6s %6s %6s %7s %7s\n", "time", "turn", "spec", "asr", "llm", "seg", "tts", "audio", "+pause"
}
{
  t = field("time"); sub(/^.*T/, "", t); t = substr(t, 1, 8)
  asr = field("asr_final_ms"); llm = field("llm_first_token_ms")
  if (asr == 0 && llm == 0) next            # an opening line: nobody was waiting
  out = field("first_audio_out_ms"); spec = field("speculative")
  heard = out + pause
  printf "%-9s %-8s %-5s %6d %6d %6d %6d %7d %7d\n", t, field("turn"), spec, asr, llm, field("first_segment_ms"), field("tts_first_audio_ms"), out, heard
  n++; a[n] = out + 0; b[n] = heard + 0; if (spec == "true") s++
}
function median(v, n,   i, j, t) {
  for (i = 1; i <= n; i++) for (j = i + 1; j <= n; j++) if (v[j] < v[i]) { t = v[i]; v[i] = v[j]; v[j] = t }
  return n % 2 ? v[(n + 1) / 2] : (v[n / 2] + v[n / 2 + 1]) / 2
}
END {
  if (!n) { print "no answered turns in this log"; exit }
  printf "\n%d turns, %d speculative.  first audio p50 %d ms;  with the pause %d ms.\n", n, s, median(a, n), median(b, n)
}'
