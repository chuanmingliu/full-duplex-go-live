## What happened

Seven minutes, dialled from the originator's page, the first call after three fixes (`4b029b3`): short turns get a head start, opening clauses are remembered, a second synthesis connection is asked when the first is slow. 37 answered turns: p50 841, p90 982, p95 1058, one over 1200.

## The one slow turn: 4603 ms

The synthesizer stalled for four seconds on both connections at once. The second connection was asked 260 ms in and had nothing for 3.7 s; both spoke at the same moment. On the next segment the first connection stalled again and the second won — 611 ms against over 1.4 s — the only time it has. ISS-017, open.

## What the fixes did

- All 37 turns had an answer prepared before the turn closed; on the two calls before, 12 turns did not. ISS-016 verified.
- 4 ordinary turns opened from the phrase cache ("好的，" twice, two others) and were among the fastest of the call, 515–756 ms. ISS-019 verified.

## What I got wrong

I had told the owner the second connection would cover the stalls. A probe an hour earlier had already shown a stall hitting both connections; this call confirmed it. The second connection is insurance against one bad connection and nothing more.
