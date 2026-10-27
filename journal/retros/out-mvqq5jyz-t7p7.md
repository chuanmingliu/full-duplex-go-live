# Simulated call out-mvqq5jyz-t7p7 — 2026-10-27 21:31, build ddf25e2

`kb-coverage-nerd` through the PBX, the same scenario as the 22:40 run of
10-26 on 9414aa1; 19 turns, 14 answered with an opener; the chat model's
slow hour, moderate (first token 224 p50 / 455 p95 from the turn's close,
against 302 / 425 the night before). The first call on two changes of
ddf25e2: **the content figure is stamped on the answer's first audio chunk,
not on the request for it**, and the quiet head of the opener and of the
answer's first segment is cut. Figures from the caller's last sound.

## From the caller's last sound

- **First audio**: p50 380, p95 380, max 561 — every answered turn opened
  at the pause from the cache.
- **The answer's own first word**: **p50 950, p95 1151, max 1186; 5 of 14
  over 1000.** All fourteen: 820 848 875 896 920 927 950 952 965 1065 1096
  1101 1151 1186.
- Opener-bound turns (content = pause + the opener's length): **3 of 14**,
  against 9 of 13 on 10-26. Openers p50 430 / max 540.
- 9 openers became backchannels, 1 answer cut, 5 sounds judged another
  voice's, 0 rate limits.

## What the heads were worth

`lead_ms` on this call: the opener's rendering opened with **110–220 ms of
near-silence** (好的，-class phrases 140–220, the scenario openers 110–140),
now cut to 20; the answer's first segment with **84–172 ms** (p50 ~118),
now cut the same way. Before this build a caller heard the pause run on by
that much before the opener's word, and again before the answer's first
word on an opener-bound turn. The opener is shorter by its head, which is
why the opener binds on 3 turns instead of 9: the floor moved down and the
clause is now what the caller waits for.

## The content figure, corrected

Until this build `first_content_ms` was stamped when the first segment was
handed to the synthesizer. On an opener-bound turn the max with the
opener's end hid that; on a clause-bound turn the figure was short by the
synthesizer's first-audio time, ~200 ms. **This call's 950 / 1151 is the
first honest content figure, and it is not comparable with 10-26's 860 /
920 (live) and 841 / 920 (simulated), which would read ~150–200 higher on
their clause-bound turns under the new stamp.** The headline figure (first
audio) was never affected.

Per turn, from the first token to the content audio: 244–367 ms on 11 of
14 (the clause ~100 + the synthesizer ~200 + the queue), and 637 and 840 on
two (turns with content 1096 and 1101; first token 79 and −119 from the
close — the model was quick and something after it was not: under study,
job 624). So the content figure is `close + first token + ~320`: 380 + 224
+ 320 = 924 at p50, 380 + 455 + 320 = 1155 at p95 — the observed figures.

## Against the goal

**Not met on the honest figure in this hour: p95 1151 on 14 turns.** The
headline (first audio) is 380 at p95. What would reach 1000 on the
answer's own first word: the chat model's first token under ~320 from the
close at p95 (a good hour, or lever 3.3 — the owner's), or ~150 ms taken
out of the ~320 between the first token and the audio (the first-clause
race, a shorter first chunk, `early_final_ms`), or both. The two slow turns
first.
