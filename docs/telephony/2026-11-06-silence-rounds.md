# Ordered silence reminders after the real-call review

Call `out-mw557y82-p3ft` used original GoLive + Active Call build `40c20f6`.
The owner said done before review. The installed GoLive MCP tools verified five
permanent archive files, both transcript speakers (1 caller / 6 assistant lines),
and nonzero audio in both stereo channels over 75.04 seconds. This proves stored
evidence, not intelligibility or physical handset latency.

The call loaded the owner's **silence config v2**: 3,000–6,000 ms, two attempts,
and the original two-template pool. At 23:43:01.502 and 23:43:09.182 +08:00, it
selected template index 0 both times, after sampled waits of **5,767 and 5,843 ms**.
Both reminder lines appear in the assistant transcript. Random independent pool
selection allowed the repeated wording. The silence hangup rule fired at
23:43:35.821; Active Call returned HTTP 200 to the hangup command at 23:43:39.936,
and the call ended at 23:43:39.940.

There were **zero VAD-open-to-server-cut observations** and **zero normal-response
samples**. No interruption-delay comparison or normal-response latency conclusion
can be drawn from this silence-focused call. The earlier simulator's cut times
are a separate sample and must not be substituted.

Quotation remains separate: one quotation-classified result/readout was observed
at **901 ms**, with first audio at **380 ms**. The associated tool completed in
**0 ms**; this is not an uncached premium calculation benchmark. There was no
observed multi-second calculation to assess. No timing evidence was missing or
unverified, but the strict and normal latency gates fail the minimum sample count.
No acoustic handset capture was verified and no 50 ms estimate is applied.

## Configuration change

`silence-reaction` now accepts an ordered `rounds` array. Each round has its own
random wait range and template pool. After playback finishes, the next round
waits its complete interval; exhausting the sequence does not repeat it. Accepted
caller speech restarts round one. All templates must be distinct within/across
rounds. Mixing round and legacy schemas is rejected. Existing legacy pools remain
readable and avoid immediate repetition when another distinct line is available.

The requested live migration should retain the owner's **3–6 second** timing,
with the first original template in round one and the second in round two. Keep
legacy v2 and both prior binaries for rollback. Existing conversations pin their
startup settings; migrate only while idle, after exact-commit CI and warmup.
The old engine does not understand rounds, so restore the saved legacy config
before an engine rollback. All other Leads settings and the original GoLive
profile, prompt, voice, greeting, preset openers and quotation delay remain intact.

Focused race tests cover order, distinct wording, limits, per-round deadlines,
caller reset, task/floor suppression, missing/disabled config, legacy compatibility,
authentication/versioning and invalid save rejection. Engine/player simulations
verify two distinct recorded reminder lines and interruption of reminder speech.
The ASR interruption simulation retains caller-cut and echo/background/backchannel
rejection controls. Deployment and subsequent live-probe evidence are separate
from these tests and must be recorded before calling this change deployed.

[Sanitized call and preflight evidence](evidence/2026-11-06-silence-rounds/).
[Configuration guide](../services/silence-reaction.md).
