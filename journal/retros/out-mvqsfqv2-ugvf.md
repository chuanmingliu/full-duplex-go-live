# Simulated call out-mvqsfqv2-ugvf — 2026-10-27 22:34, build 27946cc

`kb-coverage-nerd` through the PBX, the third call of the evening on the
same scenario (21:31 on ddf25e2, 22:05 on a4cb23a), the same hour — the chat
model a little slower at the tail (first token 239 p50 / 491 p95 from the
close, against 224/455 and 257/419). 27946cc adds `Player.FreeForOpener`
(the opener is said while a backchanneled turn's last paced audio drains)
and the fixes from the review of the day's diff. 21 answered turns, figures
from the caller's last sound, over every answered turn.

## From the caller's last sound

- **First audio: p50 380, p95 381, max 1286 — 20 of 21 turns opened at the
  pause.** The one that did not logged `speak: no opener; the floor is busy`
  (first audio 1286, first token 565 from the close). On the 22:05 call five
  turns had no opener (first audio 742–1131, p95 1063): the drain fix did
  what it was for.
- **The answer's own first word: p50 947, p95 1188, max 1286; 8 of 21 over
  1000.** Opener-bound 3 of 20; openers p50 340 / max 540.
- 13 openers became backchannels; 11 fragments opened with a phrase of
  another kind; 5 of the model's openers were stripped and the segment after
  each was a first segment again (7, 20 and 12 characters — no 66-character
  wait; the first segment said nothing 4 times, the restart followed).
- Lead silence cut from the openers: 80–210 ms (p50 ~160).

## Against the goal

**The headline figure — the caller's last sound to the first audio, the
owner's definition of 10-16 — is under 1000 at p95 on this build: 381 on 21
turns, 380 on 20 of them.** Pooled with the 22:05 call's opened turns it is
380 on 36 of 42 answered turns, and the six that were not are the drain
cases fixed here.

**The answer's own first word is not under 1000 in this hour: p95 1188.**
Across the three calls tonight it is 1151 / 1184 / 1188 — the same figure
on three builds, because its tail is the chat model's: content ≈ close +
first token + ~320 (the clause ~100, the synthesizer's first audio ~190,
the queue). With the first token at 455–491 from the close at p95, the
figure is ~1150–1190; it would need ~320 at p95 (a good daytime hour) to
sit at 1000. What golive can still take off it, each a profile value and
the owner's to set, measured here as a next A/B in the same hour:
`stream_first_chunk_deadline_ms` 160 → 100 (many first clauses waited the
whole 160: −40 to −60 on clause-bound turns), `llm_hedge_clause_chars: 4`
(the first-clause race, −28 at the median on the bench), `early_final_ms`
120 → 100 (−20, 18% more split recognitions). Together ~−100 at p95;
not the 190 this hour needs, which is lever 3.3 (a second chat-model
vendor), the owner's standing decision.

## Measurement notes

The content figure here is the honest one (stamped on the answer's first
audio chunk since ddf25e2); the 10-26 figures are not comparable. Every
answered turn is counted, not only those with an opener; the per-call
analysis job (`630-an-all`) is the template.
