# Silence reminder deployment and simulator results — 2026-11-06

GoLive and Leads CRM build `40c20f638a8adf60b71850b60acde1e41c5c38ed` are deployed
on the ThinkPad. Exact-commit quality, normal Go, and race tests passed in
[CI run 42206853218](https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/42206853218).
The engine completed preset/phrase warming in one stable process. The six
previous Leads configs, original prompt/greeting/voice/profile, Active Call,
RustPBX, Journal, Desk, browser demo, and Aria binding/restore anchor were retained.
Prior binaries and CRM data are under the private box release directory
`~/golive-releases/silence-40c20f6`. Journal retention remains 365 days.

The newly seeded **Leads → Configs → silence-reaction** is enabled, with an
8–12 second uniform random wait and at most two literal reminders. See the
[configuration and behavior](../services/silence-reaction.md). Existing calls
keep their startup snapshot; new config versions affect subsequent calls.

## Original Active Call `call sim` runs

All three used the existing Mac runner and `phone-brain.sh call sim`, with a
fresh reservation and the original saved GoLive settings. No real recipient was
dialed. All three loaded silence config v1 and completed without receiver
failures. Each permanent Call Journal archive passed five file checksums,
contained both transcript speakers, and had nonzero audio in both directions.

| Run | Call ID | Normal content samples / p95 | First audio p95 | Server VAD-open → cut | Reminders |
| --- | --- | --- | --- | --- | --- |
| Quiet-persona attempt | `out-mw4vnujw-0qwl` | 12 / 1,292 ms | 503 ms | none | 0 |
| Existing barge-in scenario | `out-mw4vr64e-kfgl` | 0 / unavailable | 380 ms | 257, 146 ms | 0 |
| Existing quotation scenario | `out-mw4vsgbx-qesz` | 4 / 1,268 ms | 380 ms | 271 ms | 0 |

The quiet persona did **not** remain silent: it produced 13 caller transcript
lines over 121.7 seconds. It is ordinary conversation coverage, not proof of
sustained-silence behavior. The deployed receiver rejects scripted silence
control, so no unsupported action was silently treated as executed.

The same barge-in scenario on `b51e959` previously yielded cuts of 309, 234,
and 182 ms (`out-mvzfoloq-k0yg`). This run's two cuts are consistent with retained
fast interruption behavior, but different turn counts and stochastic dialogue
prevent a statistically supported improvement claim. One run contains no normal
content samples after quotation-only responses are separated.

**The strict latency gate did not pass.** Every run has fewer than 20 qualifying
normal responses; two contain a normal-content response above 1,200 ms. The quote
run also has one invalid/unverified tool-result relationship. Each run recorded
an `audio_gap` fallback from the backchannel detector to the existing controller;
these tests do not establish uninterrupted detector coverage. No result here
proves physical handset latency or applies an assumed 50 ms phone adjustment.

## Quotation stays separate

In the quotation scenario, the uncached calculation took **3,022 ms**. Its
configured artificial delay was preserved. Two observed caller-to-result-content
intervals were **4,035 and 4,996 ms**, and the corresponding deterministic spoken
readout intervals were **5,255 and 6,216 ms**. These include time outside the
calculation and are not normal-response samples. A zero-duration tool completion
also occurred and must not be described as an uncached calculation. One additional
tool-result relationship is unverified and remains excluded from verified result
claims. No silence reminder fired during this run.

## Controlled silence against the deployed engine

A separate WebSocket probe used the deployed `:8765/v1/realtime` endpoint, the
saved prompt/greeting/voice, and a scoped opening release. It sent paced PCM16
zeros, bypassing the simulated customer's inability to remain silent. This is a
**deployed engine transport test, not an Active Call/SIP/handset test**.

Session `sess_69990a1f2a359d94172b0dac` produced:

1. The original greeting.
2. Reminder attempt 1, sampled wait **8,300 ms**.
3. Reminder attempt 2, sampled wait **9,553 ms**.
4. The existing silence-rule farewell and session closure, with no third reminder.

Received `response.done` → next `response.created` gaps were **8,796 and
9,705 ms**. They include scheduling/synthesis beyond the sampled wait and are
protocol boundaries, not acoustic onset measurements. After the second reminder,
farewell started **25,282 ms** after its `response.done` event. The entire
probe lasted 60.6 seconds and received nonzero audio; the private session store
contains output PCM and a closed session record. With no real SIP dialog, its
closure is not evidence of a carrier BYE.

The original probe receipt reports `passed:false`: its final preservation check
compared the entire saved-call file. `keepLastCall` legitimately updates `call`
and `saved_at` at opening. A separate read-only postcheck confirmed identical
prompt, greeting, voice and profile, and an idle healthy engine. The initial
receipt is retained unchanged alongside the postcheck rather than hiding the
harness failure. Future probes should compare semantic settings and independently
track expected metadata updates.

Focused engine/player and race tests additionally cover caller interruption of a
reminder, attempt reset, busy/task/recognition suppression, hangup precedence,
random bounds, disabled/unavailable config and versioning. Those are controlled
software tests; real-call silence/resumption remains unverified.

## Evidence

[Sanitized receipts and summaries](evidence/2026-11-06-silence-reaction/) contain
CI, deployment, the three calls, the initial probe, and its settings postcheck.
Raw prompts, transcripts, credentials and audio remain private on the box.
