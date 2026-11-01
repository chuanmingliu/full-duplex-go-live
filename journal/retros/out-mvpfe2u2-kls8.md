# Call out-mvpfe2u2-kls8 — 2026-10-26 23:42, build 9414aa1

A real call, the owner on the line, quiet, 3 min 43 s, in the chat model's
usual slow hour — which was moderate tonight (first token 302 p50 / 425 p95
from the turn's close). The first live call on the shorter opener (tail 80,
cap 540). Figures from the caller's last sound (log + `end_silence_ms`).

## From the caller's last sound

- **First audio**: p50 380, p95 380, max 381 — 12 of 12 answered turns
  opened at the pause from the cache.
- **The answer's own first word**: **p50 860, p95 920, max 962; 0 of 12
  over 1000.** All twelve: 810 810 826 843 858 860 860 870 871 905 920 962.
  On 6 of 12 the content figure is pause + the opener (460–540); on the
  other 6 the clause was 30–170 ms behind the opener's end.
- Openers p50 460 / max 540 (the cap). 3 became backchannels (the owner went
  on), 2 answers cut by the owner, 10 sounds judged another voice's and let
  go, 0 patience waits.
- Quotation: default at 0 ms, read directly; two changes (三者险 500万, then
  300万) both **cache hits at 0 ms** (10884.8, 10524.8), each preceded by the
  ack "请您稍等" (3.0 s and 4.3 s before the figure — the 10-23 rule, open);
  one readout cut by the owner and noted beside the memory
  (`noteReadoutCut` fired for the first time live). The owner's goodbye
  ended the call through `end_call`.

## Against the goal

**p95 920 on 12 live turns, 80 ms inside.** Twelve turns is a small sample
(p95 is the second-worst turn); pooled with the 22:40 simulated call on the
same build (13 turns, p95 920) it is 25 turns at p95 920, max 962. The
floor of this build is 920 (380 + the 540 cap); turns that hit it are the
ones that drew the longest opener. The next 60–100 ms would come from the
pause (`min_silence_ms`, the owner's call) or an even shorter opener.
