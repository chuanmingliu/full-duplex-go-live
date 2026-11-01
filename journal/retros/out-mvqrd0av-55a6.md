# Simulated call out-mvqrd0av-55a6 — 2026-10-27 22:05, build a4cb23a

`kb-coverage-nerd` through the PBX again, half an hour after the 21:31 call
on ddf25e2; the same hour (first token 257 p50 / 419 p95 from the close).
a4cb23a adds the fragment opener (`opener_on_fragments`) and the fix for the
stripped duplicate opener (fba0387). 21 answered turns. Figures from the
caller's last sound, **over every answered turn** — the 21:31 retro counted
only the turns that had an opener, which hid the ones that did not.

## From the caller's last sound

- **First audio**: p50 380, p95 1063, max 1131. 16 turns opened at the
  pause; **5 had no opener and their first audio was 742, 947, 990, 1063,
  1131**.
- **The answer's own first word**: p50 955, p95 1184, max 1274; 7 of 21
  over 1000. Opener-bound: 3. Openers p50 410 / max 540.
- 13 openers became backchannels; 8 fragments after a backchannel opened
  with a phrase of another kind (the new rule, as designed); 1 stripped
  duplicate opener, and its next segment was a first segment again (fba0387
  did its job — no 640/840 ms tail this time).

## The five turns with no opener (job 628)

All five have the same shape: the previous opener became a backchannel at
the instant the fragment's words arrived (the early final), and the
fragment's turn closed 170–200 ms later. `sayOpener` returned before any of
its logging: the player was still **Active**, because
`FinishOpenerAsBackchannel` drops the turn's queue but lets the paced audio
already in the air — up to `playback_lead_ms`, 300 — drain, and counts the
turn active meanwhile. The fragment rule never got to run on them. Fixed in
27946cc: `Player.FreeForOpener` ignores a draining backchanneled turn, and
the skip, when it happens, is logged.

## The content figure

955 / 1184 against 950 / 1151 on ddf25e2, the same hour: unchanged within
noise, as expected — the fragment opener moves the first-audio figure, not
the content figure, and the clause-bound tail is the chat model's first
token (419 p95 from the close) + ~320 ms (the clause, the synthesizer's
first audio, the queue).

## From the review of the day's diff (27946cc)

An adversarial review of 031a062..a4cb23a found two defects before a live
call did: with one model delta carrying several segments, the first-segment
restart re-pushed the buffer before the cut segments were spoken (words out
of order; fixed 7e404a0), and a first segment rendered wholly under the
silence floor was taken for "no audio" and dropped the synthesis session
(fixed 27946cc, with three smaller ones: an unspeakable remainder after the
strip, a cached first clause as the pipe's first, the fragment planner never
repeating the backchannel's phrase).

## Against the goal

Not met in this hour on either figure: first audio p95 1063 (the five
no-opener turns, fixed since), the answer's first word p95 1184 (the chat
model's hour). The next call on 27946cc should show first audio at 380 on
every answered turn; the content figure needs a better hour or lever 3.3.
