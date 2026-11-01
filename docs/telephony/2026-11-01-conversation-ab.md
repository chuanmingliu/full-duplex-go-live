# Original GoLive: conversational SIP comparison

The owner retains the quotation service's deliberate **2500–4500 ms** delay.
Conversational response timing and quotation completion are reported separately.
The server target is strictly below **1150 ms**, using the owner's assumed
50 ms combined telephony/ear allowance for the 1200 ms estimate. These are
simulator results, not physical handset measurements.

Two five-minute, sequential SIP calls used the same original provider profile,
saved prompt/greeting/voice, private CRM snapshot and diagnostic CRM `3201ec4`.
Only the engine build changed: retained original `9547d22` versus `302267b`.
The synthetic customer's 28 coverage/procedure questions replaced the prior
quote-heavy customer scenario; original agent instructions were unchanged.

| Metric | Original `9547d22` | Candidate `302267b` |
| --- | ---: | ---: |
| Conversational responses | 19 | 28 |
| First-audio server p95 | 1017 ms | 934 ms |
| Conversational-content server p95 | 1017 ms | 942 ms |
| Content p95 plus assumed 50 ms | 1067 ms | 992 ms |
| Content maximum | 1017 ms | 1078 ms |
| Simulator receiver first-audio p95 | 1075 ms (18 samples) | 1087 ms (20 samples) |
| Strict separate-SLA gate | Fail | Pass |
| Verified quote-readout samples | 0 | 0 |

The baseline fails for one unverified tool-result link and fewer than 20 valid
responses. The candidate has no missing, unverified, malformed or duplicate
timing records. Both recordings have zero receiver warnings. Four candidate
answers and three baseline answers were interrupted after output began; those
remain counted and labelled in the analyzer rather than discarded.

The candidate's 75 ms lower server-content p95 is an observed difference, not
an isolated causal speedup: generated conversations differed, the baseline
invoked a task and the candidate did not, and the samples are small. Receiver
first-audio p95 did not improve. Server content and receiver first audio are
different measurement boundaries and must not be substituted for each other.

This candidate run meets the conversational simulator gate but does not test
quote-result delivery. The retained quote-heavy `302267b` run still fails with
1178 ms conversational p95 (6 samples), two unverified chained task origins and
two missing quote readouts. That evidence remains visible. A separate trial of
`7fc4237` tests protection of a newer caller answer against an older ready task.

## Retained evidence

- Baseline call: `sim-conversation-e70c323e-a0eb-4242-b573-420c8c18a66a`.
- Candidate call: `sim-conversation-31ed840c-46c6-4282-b339-5c44b55bfc39`.
- Five archive files per call verified against SHA-256 manifests.
- Baseline: 301.596 seconds, 22 assistant / 20 caller transcript entries.
- Candidate: 301.824 seconds, 28 assistant / 28 caller transcript entries.
- Stereo audio contains signal in both channels for both calls.
- Both adapters exit 0; all isolated processes ended after the duration limit.
- Runner operation `conversation-ab-20261101` exits 0 at 14:50:12 on Nov 1.
- Original configuration hashes and Active Call container identity/start match.
- No real call or engine promotion occurred during this operation.

Exact reviewed operations and numeric verification are retained in
[the operation directory](../../scripts/local-ops/recorded/2026-11-01-conversation-ab/).
The native SIP test engine remains `f12b6f5` until a later verified deployment;
the original GoLive engine and Active Call remain available for rollback.
