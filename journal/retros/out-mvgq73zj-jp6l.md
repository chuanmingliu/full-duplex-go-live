# Simulated call out-mvgq73zj-jp6l — 2026-10-20 21:35, build df22ed2

Run `sim-efb54af1…cfef5`, scenario `set-a-02-barge-in` (a reservation
already waiting; the kb scenario asked for ran next), receiver 5128a8f, 21 s.

**The customer hung up on its own** — the first time: script exhausted at
16 s → farewell → waited for our reply → hangup from its side. Run
`completed`, `termination_reason: customer_hangup`, no failures,
`clean_termination` and `interruption_overlap` pass, `remote_turn_count` 4
of 6 segments, one valid gap. n22 verified. Too short for figures (two
cut-ins, three answers, all cut).
