# Call out-mvl1b850-7bny — 2026-10-23 21:57, build 45c67de

A real call from a metro station platform, six minutes, the kept prompt;
the first on the three fixes of the 09:43 call (ISS-052/053/054) and the
first real call to run a quotation end to end.

## What held

- **The announcements.** 16 utterances judged another voice's and let go
  by, 33 judgements, 0 vetoes of the caller, 3 sounds over the answer kept
  as not the caller's; 8 interruptions yielded to, every one "the caller's
  voice". No answer was cut by an announcement; no turn was made of one.
  The enrolment confirmed itself by the third utterance (6 candidates, 6
  agreeing — the 09:43 call never got past one).
- **Which fixes fired:** none of the three paths was reached — no hold
  waited out (ISS-052), the one quiet close was the caller's own turn
  (others 100 ms; ISS-053's branch not taken), no refusal (ISS-054). They
  are verified by their tests and by this call not reproducing the 09:43
  failure under the same noise; a direct hit awaits a louder call.
- **The hangup:** the goodbye net (`said_goodbye`, 12 characters) after
  the caller wound the call up; the farewell played whole.
- **The quotation, on a real call for the first time:** 9 quote_total
  calls, 9 delivered. One `need` (plan) → the caller's coverages → quotes
  3135 → 2897.5 → 3135 → a `need` for seats when 座位险 was added → 3348.75
  → 3690.75 → a last recompute; 13 memory versions. The journal's
  Quotation section has the steps.

## Latency — the evening

19 caller turns, last sound → first audio: **p50 1146, p90 1319, p95 1346,
max 1356; 8 over 1200, 14 over 1000.** Console's definition (the summary's
14 measured turns): p50 876, p95 966.

One cause: the chat model's first token, 609 ms at the median from the
request (397–730), against ~360 in a good hour — 22:00 on a Thursday, the
evening slow spell (ISS-018), on every turn. The synthesizer was ordinary
(first audio 150–250 after the clause), five turns waited 700 ms of
patience on a mid-thought (就是 / 这个 / 呃), six segments played from the
phrase cache. Nothing in the build; compare within the half hour.

The quotation read-outs (tool-result turns, no pause) ran 608–1615 ms
from their gap; the 1615 was a 1371 ms first clause — the long-first-clause
case of RETRO-1000 §3.2, now seen on a delivery turn too.

## Open

- The 1000 ms target is not reachable in this hour by anything but
  decision 15 / decision 1 (RETRO-1000.md).
- An announcement the caller talks into still opens a turn whose transcript
  mixes both voices (13 take-ups at 200 ms of the caller); most were the
  caller interrupting and were cut at once, correctly. The clean fix —
  restarting the recognizer at the join — is not built.
