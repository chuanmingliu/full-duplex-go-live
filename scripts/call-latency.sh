#!/usr/bin/env bash
# Per-turn latency of the calls in a golive log, as a table you can paste.
#
#   scripts/call-latency.sh                       # golive-telephony.log
#   scripts/call-latency.sh golive.log            # some other log
#   scripts/call-latency.sh golive.log 380        # with vad.min_silence_ms
#
# Every figure but ttft is milliseconds from the moment golive judged the
# caller's turn over, which is vad.min_silence_ms after their last sound.
#
#   start   when the answer was begun. Negative on a speculative turn: the
#           backend was started that long before the turn was judged over.
#   asr     the final transcript
#   llm     the backend's first token
#   ttft    llm − start: the backend's own time to first token
#   seg     the first clause handed to the synthesizer
#   tts     the first synthesized audio
#   audio   the first audio written towards the caller
#   +pause  audio plus the pause the turn was judged on (from the log; the
#           second argument, default 380, for logs that predate it): the
#           caller's last sound → first audio leaving golive
#
# Nothing here includes what the gateway, the PBX and the carrier add on the
# way to the handset.
set -euo pipefail
LOG="${1:-golive-telephony.log}"
PAUSE="${2:-380}"
[ -f "$LOG" ] || { echo "no such log: $LOG" >&2; exit 1; }

{ grep 'turn: response latency' "$LOG" || true; } | awk -v pause="$PAUSE" '
function field(name,   i, kv) {
  for (i = 1; i <= NF; i++) { split($i, kv, "="); if (kv[1] == name) return kv[2] }
  return ""
}
function median(v, n,   i, j, t) {
  if (!n) return 0
  for (i = 1; i <= n; i++) for (j = i + 1; j <= n; j++) if (v[j] < v[i]) { t = v[i]; v[i] = v[j]; v[j] = t }
  return n % 2 ? v[(n + 1) / 2] : (v[n / 2] + v[n / 2 + 1]) / 2
}
function pct(v, n, p,   k) {       # v already sorted by median()
  if (!n) return 0
  k = int(n * p + 0.999); if (k < 1) k = 1; if (k > n) k = n
  return v[k]
}
BEGIN {
  printf "%-9s %-8s %-5s %6s %6s %6s %6s %6s %6s %7s %7s\n", "time", "turn", "spec", "start", "asr", "llm", "ttft", "seg", "tts", "audio", "+pause"
}
{
  t = field("time"); sub(/^.*T/, "", t); t = substr(t, 1, 8)
  asr = field("asr_final_ms"); llm = field("llm_first_token_ms")
  if (asr == 0 && llm == 0) next            # an opening line: nobody was waiting
  out = field("first_audio_out_ms"); spec = field("speculative")
  seg = field("first_segment_ms"); tts = field("tts_first_audio_ms")
  start = field("turn_start_ms")
  # The pause this turn was judged on, where the log has it; the assumed one
  # for a log written before it did.
  sil = field("end_silence_ms"); p = (sil == "" || sil + 0 <= 0) ? pause : sil + 0
  heard = out + p
  if (start == "") { st = "-"; tf = "-" } else { st = start + 0; tf = llm - start; m++; F[m] = tf; S[m] = st }
  printf "%-9s %-8s %-5s %6s %6d %6d %6s %6d %6d %7d %7d\n", t, field("turn"), spec, st, asr, llm, tf, seg, tts, out, heard
  n++; a[n] = out + 0; b[n] = heard + 0; P[n] = p; if (spec == "true") s++
  L[n] = llm + 0; G[n] = seg - llm; T[n] = tts - seg; A[n] = asr + 0
}
END {
  if (!n) { print "no answered turns in this log"; exit }
  printf "\n%d turns, %d speculative.\n", n, s
  p50 = median(b, n)
  over = 0; for (i = 1; i <= n; i++) if (b[i] > 1200) over++
  printf "last sound → first audio:  p50 %d   p90 %d   p95 %d   max %d ms;  %d of %d over 1200\n", p50, pct(b, n, 0.9), pct(b, n, 0.95), b[n], over, n
  ma = median(a, n)
  printf "turn judged over → first audio (what the latency console calls the reply):  p50 %d   p90 %d   p95 %d ms;  pause p50 %d ms\n", ma, pct(a, n, 0.9), pct(a, n, 0.95), median(P, n)
  printf "medians:  asr final %d   llm first token %d", median(A, n), median(L, n)
  if (m) printf " (started %d, ttft %d)", median(S, m), median(F, m)
  printf "   wait for a clause %d   tts first audio %d ms\n", median(G, n), median(T, n)
}'
