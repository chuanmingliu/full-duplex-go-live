## What happened

Nearly three minutes at 16:27 on 10-14, through the lead service, first call on build `6e313fc` (the murmur fix). 22 answered turns: p50 992, p90 1085, p95 1107, worst 1308, one over 1200. Console: p50 612, p95 727.

## Systematic

Nothing new. The one turn over (1308) was an ordinary turn a little slow at every stage; no stage was a pattern. The chat model was back to its daytime self (first token 319 ms after the headers at the median, against 384 and 290 on the two earlier afternoon calls).

## Also

13 interruptions, every one on real words outside the murmur list; the fix in this build was not exercised (no listed murmur was said over the assistant). 14 first segments asked of both synthesis accounts (9 and 4 first), two moves between accounts, nothing refused. Four phrase-cache plays. Recorder on the clock, nothing left out.

## What I changed

Nothing.
