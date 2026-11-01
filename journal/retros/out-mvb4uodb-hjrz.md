# Simulated call out-mvb4uodb-hjrz — 2026-10-16 23:39, build d0bb53d

The third conversation with the simulated customer, on the owner's
synthesized `set-a-01-backchannel` (run `sim-484c01e89664b95b7f2fe10e`):
the persona of a real call with two scripted listening sounds over the
3rd and 6th answers. 135 s, ended by the customer's gateway (ISS-039).

## The effect

- `listen-3`, 0.75 s of a listening sound over the 3rd answer, reached
  golive at 23:39:15.6; golive held the floor ("this barge-in looks like
  the assistant's own voice returning") and the answer went on.
  `listen-6` over the 6th answer: no cut logged. The receiver's
  `interruption_overlap` check: pass. So a backchannel did not cut — the
  first for the echo reason rather than for being a short sound; a
  `barge_in` run on the same persona is the other half of the pair.

## What golive measured

- 7 turns: p50 752, worst 1076 (+pause 1132 / 1456). Chat model fastest of
  eight copies p50 729, p90 872, max 1503 (the hour); synthesizer 198 / 431.
- 25 guesses held, 15 abandoned; **21 answers cut** by the customer's own
  sentences (2 after audio played) — 74 customer utterances against 44.
  This persona interrupts constantly; it is a turn-taking stress test
  more than a latency sample.
- Line quiet 12 times, 84 s filled.

## Open

- ISS-039 (three for three: 144, 145, 135 s).
- The same persona with `barge_in`, in daytime.
