# Ordinary replies and interruption: original GoLive 7fc4237

The owner gives calculation-related time extra tolerance. This run prioritizes
ordinary replies, opening transport and an interruption, with quote timing
kept separate. It does not modify the original GoLive provider/profile, saved
prompt/greeting/voice, quote service delay, or Active Call rollback path.

Call: `sim-interaction-ab988aa8-b778-4a5d-aab2-b51464cc2768`.
Receiver: `sim-cafbc749762c43faf105070c`.
Engine: `7fc4237` (also deployed on native SIP port 9895).
Diagnostic CRM: `3201ec4`.

| Measurement | p95 | Valid samples | Maximum |
| --- | ---: | ---: | ---: |
| Server first audio | 1087 ms | 26 | 1106 ms |
| Server conversational first content | 1087 ms | 25 | 1106 ms |
| Receiver first audio, application boundaries | 1193 ms | 20 | 1257 ms |
| Quotation readout, reported separately | 761 ms | 1 | 761 ms |

The strict separate-SLA server gate passes: p95 below 1150 ms, at least 20
valid audio/content responses, and no missing, malformed, duplicate or
unverified timing records. Seven answers were interrupted after content began;
they remain included. One verified quote-only handoff is excluded from the
conversational-content distribution. The owner's assumed 50 ms overhead gives
**1137 ms estimated handset conversational p95**, not physical handset proof.

Receiver first-audio p95 is independently below 1200 ms but has only 7 ms
margin, and one observed gap is 1257 ms. Its sample selection and speech
boundaries differ from server-content timing; the two p95 values must not be
subtracted to infer network latency, nor should 50 ms be added to the receiver
figure as if it were the same measurement. This mixed conversation differs
from the previous coverage-question run; it does not establish a causal speed
comparison with that run's 942 ms server p95.

## Opening and interruption

The first outgoing RTP packet follows SIP answer by 0.971 ms; that packet's
local queue delay is 82 microseconds. This is a transport measurement, not an
audible handset onset measurement. The unchanged greeting was warmed before
dialing and released only after SIP answer.

The scripted interruption is requested 700 ms into the eighth detected remote
speech start and reports requested, started and completed outcomes. It completes
in 4412 ms, which measures the simulator's spoken action duration, **not** the
assistant's stop latency. Total call overlap is 733 ms. Neither aggregate overlap
nor action completion establishes a per-interruption acoustic stop time; that
measurement remains outstanding. There are zero receiver failures.

The run asks for one original-plan quote, then ordinary questions. The quote
readout is already available within 761 ms, so this run does not establish
responsiveness during an uncached 2.5–4.5 second calculation. Keep that case
separate; do not infer coverage merely from the intended customer scenario.

## Remaining conversational latency budget

The slowest server reply is item_2 at 1106 ms: 380 ms recognized endpoint
silence, then 507 ms to the first model token, 52 ms more to the first segment,
and 167 ms more to TTS/output. These are measured milestones, not isolated
provider compute times. Its generation starts 103 ms before the recognized
endpoint. The adapter's opening packet queue is not the dominant wait observed
here. Keep original endpoint/provider settings and investigate sustained model
and first-segment delays only when repeated evidence supports a change.

## Recording and execution evidence

Five archive files are hash-verified; the 302.051-second stereo WAV contains
both directions. Chat contains 27 assistant and 26 caller entries. The adapter
exits 0 at the scenario duration limit; isolated processes end. Original config
hashes and Active Call identity/start are unchanged. Runner operation
`interaction-latency-7fc4237-v2-20261101` exits 0 at 15:06:12 on Nov 1.

The first operation was rejected before reservation/dialing: its 360000 ms
scenario duration and 10000 ms action limit exceed the deployed simulator's
300000 ms and 5000 ms limits. Its processes were confirmed absent and no active
simulation remained before creating the distinct v2 attempt. Rejected payloads
are retained; no service limits were changed or previous attempt replayed.
