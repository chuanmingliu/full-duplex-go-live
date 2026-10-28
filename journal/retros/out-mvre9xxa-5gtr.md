# Simulated call out-mvre9xxa-5gtr — 2026-10-28 08:47, build 27946cc (the daytime baseline)

`kb-coverage-nerd` through the PBX, the same scenario as the three calls of
the 10-27 evening, in a good hour: the chat model's first token 171 p50 /
381 p95 from the close (239 / 491 the night before). The baseline half of
the daytime A/B for the two content levers (decision 17); the box
unchanged from 22:34. 20 answered turns, figures from the caller's last
sound, over every answered turn.

## From the caller's last sound

- **First audio: p50 380, p95 381, max 1059** — 19 of 20 turns opened at
  the pause; the one that did not logged `the floor is busy` (job 638 looks
  at what held it).
- **The answer's own first word: p50 890, p95 1039, max 1059; 3 of 20 over
  1000**: 1039 (a 700 ms patience wait, the opener said at the base pause,
  the clause after the wait), 1059 (the busy-floor turn, first token 394),
  1020 (clause-bound, first token 381). Opener-bound 6 of 19; openers p50
  380 / max 540.
- 7 openers became backchannels, 5 fragments opened with a phrase of
  another kind, 7 first segments flushed on the 160 ms deadline (10–18
  characters), synthesizer first audio 146–259 (p50 ~165).

## What the hour is worth

Against the same build at 22:34 (947 / 1188): −57 at p50 and −149 at p95,
from the chat model's hour alone. The content figure at p95 is now 39 ms
over the goal, which is the size of the levers under test.
