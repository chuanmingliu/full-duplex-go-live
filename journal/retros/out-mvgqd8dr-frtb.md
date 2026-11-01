# Simulated call out-mvgqd8dr-frtb — 2026-10-20 21:40, build df22ed2

Run `sim-7be5b52ce53326e187aa531e`, scenario `kb-process-struggler`,
receiver **5128a8f**, 192 s, evening. The first run in which both sides
ended the call on purpose and the receiver's report came out whole.

## The ending

- 21:43:18 the customer's model reported its goal reached (190 s in) and
  the receiver said its farewell.
- 21:43:19.7 our model answered with a six-character goodbye and **no tool
  call** — and the goodbye net caught it: `hangup: the assistant said
  goodbye; ending the call rule=said_goodbye` (ISS-047 verified) →
  `end_call: hanging up` 1.3 s later → `the gateway was told to hang up
  status=200`.
- The receiver: `ended: Active Call ended`, **`completed`,
  `termination_reason: remote_hangup`**, no failures, `clean_termination
  pass`, 15 valid response gaps (its measure: p50 1084, p95 1353),
  `remote_turn_count` 23 against 71 segments (ISS-042 / n21 / n23
  verified). Ours came 1.3 s after its farewell, inside its own 3 s wait,
  so the hangup is ours; a slower goodbye and it would have been the
  customer's (`customer_hangup`, as on `out-mvgq73zj-jp6l` at 21:35).

## What golive measured

31 turns, last sound → first audio: p50 **934**, p95 1210, max 1345; **3
over 1200** (chat model p50 452, p90 574 at 21:40; synthesizer p50 177).
Console's definition p50 554, max 965. 43 guesses held, 15 abandoned;
26 answers cut by the customer (the scenario's follow-ups), 12 quiet
closes; 48 judged, 0 vetoed.

## Open

- The receiver's own gap figure (p50 1084) sits ~150 above ours (934):
  its boundary is the first non-silent audio it *receives* after its
  own speech ends — the gateway and its 200 ms debounce are inside it.
  Worth a line in TELEPHONY once a few runs agree on the offset.
- Three runs on 5128a8f now; a daytime set of three kb-* runs for the
  pooled p95.
