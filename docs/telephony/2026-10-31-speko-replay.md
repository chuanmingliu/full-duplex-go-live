# Speko latency replay after the interruption fix

Two 90-second, no-phone replays exercised bridge `3a58396` on 2026-10-31.
They used the first 90 seconds of the recorded caller track after SIP answer
in `out-speko-20261031-05` (source offset 6.186362 seconds), `marin`, eight
seconds of simulated ringing, and the existing private call prompt/greeting.
Neither run dialed a number or changed the saved configuration.

The baseline is `latency-74163ecfd24f`; the experimental conversation-policy
run is `latency-policy-2547d8520865`. Private captures remain on the Mac under
`/tmp/<replay-id>/`. These are temporary diagnostic captures, not Call Journal
handset archives.

## Observations

The table uses 20 ms PCM windows with RMS >= 300, merging signal gaps up to
240 ms and retaining spans of at least 120 ms. Only caller endings with at
least a second before the next caller span and no assistant span already in
progress were considered. The next assistant signal must arrive before the
next caller span. Missing signal is not a zero-ms response.

| Boundary, seconds after replay release | Saved prompt | Added conversation policy |
| --- | ---: | ---: |
| Opening, first qualifying signal | 1920 ms | 1320 ms |
| Caller ending at 17.10 s | 4380 ms | No signal before next caller span |
| Caller ending at 31.22 s | 1220 ms | 4900 ms |
| Caller ending at 54.48 s | 2420 ms | 3400 ms |
| Caller ending at 76.14 s | 3540 ms | 4240 ms |
| Bridge maximum output queue | 1200 ms | 560 ms |
| Local playback clears | 1 | 3 |
| Client backend delegations | 0 | 1 |
| Bridge connection errors | 0 | 0 |

First non-digital-zero output arrived at 1763 ms and 1103 ms respectively.
Those earlier times use a weaker signal criterion than the table and must not
be substituted for its acoustic-energy threshold.

These pairs are heuristic signal boundaries, not verified semantic answers.
The caller recording cannot adapt to the new assistant responses. Arrival-time
rendering also includes scheduling jitter. There is one run per condition,
the backend behavior differed, and the sample is far below the required 20
valid handset responses. No p95 success or end-to-end latency claim follows.

## Experiment and decision

The experiment appended this application-authored text without removing any
saved business instructions:

```text
Conversation timing: Respond as soon as the caller finishes a complete thought. Start with a direct useful answer in one short sentence. Do not insert a deliberate pause, preamble, or thinking aloud. If the thought is incomplete, keep listening.
Backchannel policy: Do not generate fillers or listening interjections.
Interruption policy: When the caller interrupts, stop immediately, listen until they finish, then answer their latest request. Never resume an answer they replaced.
```

**Do not deploy this prompt addition.** Its faster opening does not compensate
for worse observed reply gaps and a missing reply. Smaller maximum queue depth
also did not imply faster responses. This result does not isolate model
generation, network, endpointing, or backend delegation as a single cause.

The saved prompt is 13,116 characters and lacks GPT-Live's recommended policy
headings and explicit backend-spawn instruction. [GPT-Live prompting guidance](https://developers.openai.com/api/docs/guides/live-prompting)
recommends keeping voice instructions short and detailed workflows in the
backend. Separating those instructions is a subsequent experiment, not a
validated improvement; business constraints and capabilities must survive it.

The current deployed preconnection and local playback-clearing fixes remain
enabled. Clears were observed in these replays, but semantic interruption
recovery and actual handset cutoff still need a real-call check.

## Reuse

Use [the private replay tool](../../scripts/local-ops/probe-speko-latency.py)
for future runs. It records input send timestamps and raw output arrival events,
including both audio and transcript fragments, to a new private directory.
See [local operation instructions](../../scripts/local-ops/README.md#speko-replay-without-dialing).
It never invokes SIP or changes the call-mode mapping.

Before the next explicitly requested `call gpt-live-1`, retain the current
bridge and saved prompt. Measure the opening, complete replies, and short and
long interruptions separately. Use the handset acoustic recording for the
1200 ms goal and the Call Journal for the corresponding SIP audio/transcript.
