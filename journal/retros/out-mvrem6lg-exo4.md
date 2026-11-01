# Simulated call out-mvrem6lg-exo4 — 2026-10-28 08:56, build 4131de7 (the lever half of the A/B)

`kb-coverage-nerd` through the PBX, ten minutes after the baseline on
27946cc (out-mvre9xxa-5gtr). 4131de7 is the telephony profile with
`stream_first_chunk_deadline_ms` 100 (was 160) and `llm_hedge_clause_chars:
4` (the copies raced on the first clause), plus 20cba5b (a revised turn
inherits the opener's stamps — a measurement change that touches at most
the one or two revised turns of a call). 20 answered turns, figures from
the caller's last sound, over every answered turn.

## From the caller's last sound

- **First audio: p50 380, p95 380, max 495 — 20 of 20 turns opened at the
  pause, 0 over 1000, no busy floor.** The best headline of any call.
- **The answer's own first word: p50 981, p95 1257, max 1269; 8 of 20 over
  1000** — against the baseline's 890 / 1039 / 1059. Opener-bound 5 of 20.
- The chat model was in a slow spell: first token from the close **301 p50 /
  667 p95**, against 171 / 381 on the baseline ten minutes earlier; the four
  slowest turns (1168–1269) all had the first token at 581–667.
- 13 openers became backchannels, 11 fragments opened with a phrase of
  another kind, 3 stripped duplicate openers; no first segment reached the
  deadline (the clause race hands the pipe four characters at once, and the
  first comma cuts them); first segments 4–27 characters; one synthesis at
  484 ms (the 1180 turn).

## What the levers were worth

Per turn, the share after the first token (content − pause − first token)
on the clause-bound turns: **~220 ms at the median on 4131de7 against ~290
on the baseline** (176–345 against 177–406). So the two levers took ~70 ms
off golive's own share, as expected, and the model's spell added 130 at the
median and 286 at p95. One call each; the difference in the model between
two calls ten minutes apart is larger than the levers.

## Against the goal

- **Headline (the owner's definition): met with margin on this build — 380
  at p95 over 20 of 20 turns; over the four kb-coverage-nerd calls since
  27946cc, 60 of 61 answered turns at the pause.**
- **The answer's own first word: not under 1000 at p95 on any honest call
  (1151, 1184, 1188, 1039, 1257).** The figure is close + the model's first
  token + ~220 now; it needs the first token under ~300 at p95 from the
  close in the call, which none of the five had (381 in the best). What is
  left on golive's side is small: `early_final_ms` 120 → 100 (−20, 18% more
  split recognitions), the opener cap 540 → 460 for the opener-bound turns
  (floor 920 → 840; the shortest phrases already render at 230–340). The
  rest is the chat model's tail — lever 3.3, the owner's.

The box stays on 4131de7; reverting the levers is the 27946cc tarball
(`golive-slim2-27946cc.tar.gz` on the Mac) through a deploy job.
