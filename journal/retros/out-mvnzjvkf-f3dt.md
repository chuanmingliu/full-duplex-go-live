# Call out-mvnzjvkf-f3dt — 2026-10-25 23:31, build 8166f1b

The first real call on the opener (`duplex.opener`, 好的，/嗯，), the owner
on the line, ~3 minutes, 23:31 — the start of the chat model's slow hour.

## Both figures, from the caller's last sound (25 turns with a pause)

- **First audio** (the opener at the pause): **p50 380, p90 381, p95 381,
  max 820** (one turn on a 740 ms patience wait). 25 of 25 answers opened
  from the cache; the model's own 好的 dropped as a duplicate 5 times.
- **The answer's own first word**: **p50 890, p90 1048, p95 1150, max
  1353**; 7 of 25 over 1000, 2 over 1200. Every turn over 1000 had the chat
  model's first token 493–723 ms after the turn closed (the hour: the
  archive's 23h p95 is 1594); the 18 under 1000 sit at 820–891, which is
  380 + the opener's 440/510 ms — on those the model's clause was ready
  before the opener finished, so the opener's own length (voice, no pause
  left to trim) set the content figure.

## The rest

- 13 of the 25 answers were cut by the owner speaking (10 barge-ins judged,
  4 kept as backchannels, 6 yielded) — a fast conversation. 0 quiet closes,
  0 vetoes.
- Quotation: 3 tool calls, 2 async, 2 delivered, memory v1.
- Hangup by the goodbye net (12 chars) after the owner wound up; the
  farewell whole.

## Verdict against the 1000 ms goal

- By the headline definition (caller's last sound → first audio, p95): **381
  ms — met**, with the opener as the first audio.
- By the answer's own first word: **1150 at p95 — not met in this hour**;
  the same figure was 969–992 on 127 bench and simulated turns an hour
  earlier. The difference is the chat model's hour, as on every evening
  call since 10-11, and no setting on the box reaches it (RETRO-1000 §3.3,
  §8). A daytime call on this build is the fair test of the content figure.
