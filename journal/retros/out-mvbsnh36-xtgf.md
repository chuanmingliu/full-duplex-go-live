# Simulated call out-mvbsnh36-xtgf — 2026-10-17 10:45, build d0bb53d

`kb-coverage-nerd` (run `sim-a10ea411509969fca6e6ab7c`): a first-time
buyer asking sixteen coverage questions with a follow-up each, written
from the business knowledge in the owner's prompt (the scenario lives on
the box, not here). The first run on Codex's `ae89b93` (a synthesizer
error no longer ends the call). 200 s, 54 assistant / 98 customer
utterances. **The first conversation with 40 or more measured turns.**

## Latency — the first real p95

| | |
| --- | --- |
| turns measured | **42** |
| last sound → first audio | **p50 915, p95 1162, max 1276; 1 over 1200** |
| console's definition | p50 535, max 896 |
| chat model first token, fastest of 8 | p50 472, p90 614, max 928 |
| synthesizer first audio | p50 194, p90 275, max 410 |
| guesses | 47 held, 10 abandoned; 14 answers cut by the customer |
| line | quiet 36 times, 179 s filled |

The goal is met on this sample by 38 ms, at 10:45 — not the fastest hour
(the 09:43 run had the chat model at 311) and not the slow one. The
margin is the chat model's p90 (614) and nothing else: synthesizer and
recognizer as always. One build, one sitting, 42 turns: this is the
measurement the simulated customer exists for.

## ISS-039 explained

The receiver's new termination attribution says why this call, and every
one before it, ended early: `local_hangup_requested: true, local_reason:
"remote turn limit"`. It counts a *remote turn* per speech burst — each
synthesized segment after a pause — so `max_remote_turns 80` was reached
at 54 answers (200 s), and the earlier scenarios' 40 at 135–145 s. A
limit stop was then reported as "Active Call control disconnected". Not
a crash. Asked of Codex (n21): count per debounced utterance, and end a
limit stop as `completed` with valid response gaps. On the box the
`kb-*` scenarios are at the cap, 100, so the 295 s duration binds.

## Open

- Codex n21; then a run that ends `completed` gives the receiver's own
  response-gap figures beside golive's for the first time.
- The same scenario in the 23:00 hour, for the other end of the range.
