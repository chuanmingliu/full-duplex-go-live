# Simulated call out-mvc8lypr-bnxc — 2026-10-17 18:11, build 1e35e09

The simulated customer (run `sim-cc945ec5d678aefae625de13`, scenario
`kb-busy-bargainer`, receiver ae89b93) answered; **the assistant hung up**
after the customer said goodbye, 155 s in — the first call ended by
`end_call`. The receiver recorded the end as `failed: Active Call control
disconnected` (its view of our BYE; ISS-042).

## What golive measured

Headline, **caller's last sound → first audio** (380 ms pause + the
console's figure):

- 25 turns measured: p50 **842**, p95 1093, max 1154 ms; **0 over 1200**.
  (Console's definition: p50 462, max 774.) Fewer than 40 turns: the p95 is
  the worst turn or the one before it. Early evening, the chat model quick.
- Chat model, fastest of eight copies, 29 answers: p50 403, p90 551, max 643.
  Synthesizer first audio, 42 segments: p50 182, p90 236, max 317.
- Guesses: 25 held, 2 abandoned. Answers cut by the customer speaking: 11,
  all after audio had played (the scenario's barge-ins).
- The line went quiet 18 times; 121 s of 155 filled with silence — the
  simulated customer's gateway sends no RTP while it listens.
- The one turn begun from nothing (item_28, `speculative=false`): 1154 —
  the slowest of the call; everything else had a head start of 150–310 ms.

## Focus

26 sounds over the answer judged, 0 vetoed, 0 too late; 11 barge-ins judged,
0 kept (the customer is one voice, so every one is the caller's).

## The hangup

Customer's goodbye → the model's answer: 6 characters and `end_call`
(reason: the customer said goodbye), first token 618 ms, complete at 753.
The farewell played in full (1126 ms); `end_call: hanging up` 770 ms after
the player went quiet; BYE sent by Active Call; session closed
`assistant_hangup`.

Two faults (ISS-041, fixed in the commit after this): the engine then resumed
the backend with the tool's output as with any tool, the model answered with
a second farewell (from the phrase cache) that began 391 ms before the
hangup and was cut after ~400 ms; and the grace did not look at the player
again. Now an accepted `end_call` ends the response and the grace re-checks.

## What the customer's report says

- source `ae89b93`, provider `deepseek-chat`; assistant utterances 33,
  customer utterances 83, overlap 734 ms.
- Checks: answered_once pass, greeting_once pass, call_limits pass; the
  rest indeterminate (no completed report). 0 valid response gaps.
- Failures: Active Call control disconnected (20 s after our BYE, after an
  "inactivity limit" stop).

## Open

- Codex (desk n23, ISS-042): a remote BYE ends the run `completed`,
  `termination_reason: remote_hangup`, with the full report and valid gaps.
- The reservation made on the desk at midday had expired to `failed` by
  18:10 and the first dial (`out-mvc8kobd-to9m`) went to it; `call sim` now
  checks the run's phase before dialling.
- Same scenario in the 23:00 hour.
