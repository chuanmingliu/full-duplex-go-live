# Simulated call out-mvbqffwa-1f4w — 2026-10-17 09:43, build d0bb53d

`set-a-01-barge-in` (run `sim-a92cd75ac88dab9e77970ca3`): the persona of a
real call with scripted cut-ins over the 2nd and 5th answers. The first
run outside the 23:00 hour. 82 s, ended by the customer's gateway after
three synthesizer errors on its side (ISS-039).

## Latency — the daytime baseline

| | this run | the same persona, 23:15 / 23:29 / 23:39 last night |
| --- | --- | --- |
| turns measured | 13 | 7 / 9 / 7 |
| last sound → first audio | **p50 716, p95 840, max 857; 0 over 1200** | p50 1035 / 1594 / 1132; 1 / 7 / 2 over |
| chat model first token, fastest of 8 | p50 311, p90 436, max 494 | p50 656 / 775 / 729 |
| synthesizer first audio | p50 203, p90 264, max 325 | 200 / 194 / 198 |

Same build, same customer, same prompt; only the hour. 17 guesses held,
3 abandoned. Line quiet 16 times, 56 s filled.

## Barge-in

- `cut-in-5` fired at 14.6 s: golive held the floor at 09:43:15.91 and cut
  at 16.06 — 143 ms after the hold, about 460 ms after the customer's
  first sound (320 ms detection + 143). `cut-in-2` was skipped by the
  simulator: the 2nd answer was over before 700 ms.
- The customer cut 12 answers in all, every one after real audio
  (300–1901 ms played); hold → cut 143–897 ms. The slow end is the hold
  waiting for the transcript (`focus_barge_in_wait_ms` 900): the price of
  veto mode, now measured. Receiver check `interruption_overlap`: pass.

## Open

- ISS-039 is not a fixed timer: this one ended at 82 s after `tts chunk
  error` ×3 on the receiver; the earlier three at 135–145 s. The
  receiver's media loop dies on its provider errors.
- The barge-in reaction time (first sound → cut) is a figure worth
  tracking per build beside latency; 460–1200 ms here.
