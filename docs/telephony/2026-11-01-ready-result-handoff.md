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
