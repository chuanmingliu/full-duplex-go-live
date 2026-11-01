# Ready-result handoff after an interrupted calling turn

The focused SIP simulator candidate had ready-result waits of 8,963, 14,685,
9,865 and 18,187 ms. The log released each pending acknowledgment only when the
global conversation floor became free. No scripted acknowledgment was actually
spoken in that run. These are server-side observations, not handset timings.

## Reproduction and change

A completed or interrupted calling turn can leave its scripted continuation
queued while a newer answer occupies the floor. `ackPending` then blocks result
delivery before the configured delivery timeout is checked. Its own playback
has already ended, so there is no remaining calling clause to protect.

The engine now releases that ready task's acknowledgment after its own playback
completion, retaining the function-call/output pair in history exactly once.
The existing delivery timeout still applies to an unrelated assistant response.
An unfinished calling clause and an already-playing scripted acknowledgment
remain protected. A delivery deadline cannot take the caller's floor while the
caller speaks, ASR finalization is pending, or a focus decision is pending.

This change does not alter the saved prompt, provider/voice settings, polling
interval, upstream quotation computation, or configured delivery timeout.

## Controlled scheduling simulation

Baseline: `1e38bac`. Both revisions use the same opt-in fixture: the original
calling playback has completed, the task is ready, an unrelated answer holds
the floor for 3,000 ms, and the delivery wait is 1,500 ms.

| Revision | Task-ready to delivery |
| --- | ---: |
| Baseline | 3,004 ms |
| Candidate | 1,501 ms |

The observed saving is 1,503 ms in this single controlled scheduling scenario.
It is not speech synthesis, audible playback, an upstream speedup, a p95
qualification, or a causal explanation of every delay in the live simulator.
Fresh recorded SIP comparison and handset verification remain required.

```sh
GOLIVE_TOOL_LATENCY_SIM=1 go test ./internal/duplex \
  -run '^TestCompletedCallingAckLatencySimulation$' -count=1 -v
```

The completed-calling-turn regression fails on baseline. Focused race tests
cover ready results, unfinished calling clauses, acknowledgments, quotation
readout, coalesced requests, old-plan results, and slow tools. Full branch CI and
deployment verification are separate release requirements. No production
service was changed during these local checks.

## Recorded SIP comparison

The isolated original-GoLive simulator comparison completed with baseline
`1e38bac` and candidate `151b2e3`. Each call has five verified archive files,
stereo audio with both sides, both transcript speakers, and zero receiver
failures. Neither passes the strict gate.

| Server metric | Baseline p95 (samples) | Candidate p95 (samples) |
| --- | ---: | ---: |
| First audio | 1008 ms (8) | 995 ms (6) |
| First non-opener content | 1008 ms (8) | 995 ms (5) |
| Linked tool result | 26141 ms (7) | 13496 ms (1) |
| Linked quotation readout | 13696 ms (2) | 14387 ms (1) |

Candidate has one missing response and eight invalid/unverified links; baseline
has one invalid link and five unverified readouts. Different conversations and
small sample counts prevent a causal speedup claim. The candidate released two
completed calling acknowledgments, but is not qualified for production.

[Baseline retrospective](../../journal/retros/sim-readyack-bf836a1f-184c-4afa-95d8-f4fb91f5270c.md)
and [candidate retrospective](../../journal/retros/sim-readyack-c573d9bc-0670-4217-b0ea-36f8e4e1d19f.md)
contain the individual gate failures.

### Remaining wait and follow-up regression

Candidate task t2 finished in 1 ms but waited 11485 ms. Caller VAD-open intervals
overlap 10031 ms of that wait. This is not all unnecessary scheduler delay: the
caller must retain the floor. Task t4 waited 12427 ms with no VAD-open overlap,
while the earlier result was spoken. Calling turns item_7 and item_10 have no
playback completion record, so the completion-only release cannot apply.

The follow-up regression reproduces a calling turn superseded before any audio
was emitted. Its generation is invalid, so normal queued or late audio cannot
play, but no playback completion is expected. Waiting for that receipt leaves
its acknowledgment blocking the result. The fix checks the output lock and
releases only when the generation is invalid, no audio was emitted, and there
is no sentence grace, draining tail, or inherited-opener playback permission.
It creates no completion event or latency sample. Completed audible turns still
require their real playback receipt; unfinished current turns remain protected.

This reproduction does not prove that every quiet wait in the recorded call
has the same cause. A fresh recorded comparison remains required.

## Working latency budget

The owner now assumes 50 ms for combined telephony transport and sound reaching
the ear. Under that assumption the server-output p95 target is **strictly below
1150 ms**, and estimated handset p95 is server p95 plus 50 ms. Keep first audio,
non-opener content, and linked quotation readout separate. Retain the 20 valid
response minimum and all missing/ambiguous-evidence failures. Use
`--target-ms 1150 --min-turns 20 --metric both --gate` for new checks.

This is a planning assumption, not measured physical-handset evidence. It does
not turn the failed simulations above into a pass or satisfy the acoustic
completion requirement.
