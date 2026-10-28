# Call out-mvrf5mdh-tfgz — 2026-10-28 09:11, build 4131de7 (live, the owner's page dial)

A real call dialled from the page (the owner's 12,840-character prompt,
kept for redialling), 09:11:52 to about 09:16:40, daytime; the first live
call on the two content levers (decision 17) and the first live call since
27946cc. It arrived between two simulated calls and the simulations were
stood down for it. 19 answered turns, figures from the caller's last sound,
over every answered turn; numbers only, from the log.

## From the caller's last sound

- **First audio: p50 380, p95 380, max 429 — 19 of 19 turns opened at the
  pause.** No busy floor, no turn without an opener.
- **The answer's own first word: p50 908, p95 1043, max 1086; 3 of 19 over
  1000** (1086, 1043, 1001 — the model's first token at 433, 405 and 338
  from the close). Opener-bound 5 of 19; openers p50 360 / max 540.
- The chat model: first token from the close 282 p50 / 405 p95 (the
  08:47 sim 171 / 381, the 09:06 sim 309 / 476).
- On the clause-bound turns the share after the first token (content −
  pause − first token) is 167–314, median ~230: the clause ≤100 (the new
  deadline), the synthesizer's first audio 164–271 (p50 ~185), the queue.
- What a human does that the simulator does not: 5 answers cut by the
  caller, 11 speculations abandoned (the words changed before the close),
  the model repeating the engine's opener 7 times (stripped; the first word
  after it a first segment again, 5 times "the first segment said nothing"
  → restart — the fba0387 path, working live), 3 sounds judged another
  voice's.

## Against the goal

**Headline (caller's last sound → first audio, the owner's definition):
met live, 380 at p95 on 19 of 19 turns.** Over the morning's four calls on
27946cc/4131de7 (three simulated, one live): 80 of 81 answered turns at the
pause, the one exception a real barge-in.

**The answer's own first word: p95 1043 live, 43 over.** The same picture
as every honest call since the 10-27 correction (sims 1039 / 1257 / 1065
this morning): content ≈ close + the model's first token + ~230–300 at the
tail. Every turn over 1000 had the first token at 338+ from the close; the
figure is under 1000 at p95 only on a call where the model's p95 is under
~320 (10-26 09:04 was 287). Nothing left in golive reaches that: the two
levers took golive's share from ~290 to ~230; what remains is
`early_final_ms` (−20 for 18% more split recognitions) and the opener cap
(opener-bound turns only). The rest is the chat model's tail — lever 3.3,
the owner's.
