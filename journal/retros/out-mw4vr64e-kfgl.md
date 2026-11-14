# out-mw4vr64e-kfgl — silence feature simulator verification

- Build: `40c20f6`; 2026-11-06; original GoLive + Active Call; simulated call.
- Scenario: `set-a-01-barge-in`; simulator run `sim-4cbd6690ecf1618013d316a8`.
- Normal first-content: n=0, p50=None ms, p95/max=None ms.
- First audio (includes openers): n=1, p50=380 ms, p95/max=380 ms.
- VAD-open to server cut: [257.0, 146.0] ms. This is not handset playout.
- Strict gate: **not passed**. first_audio: 1 samples, need 20; conversational_first_content: 0 samples, need 20.
- Silence config v1 loaded; no reminders during this conversation. Backchannel detector reported audio_gap fallback; original controller retained.
- Five archive files verified by size/hash; chat has 2 caller and 4 assistant lines; both stereo directions contain signal; duration 17.838 s.
- Quotation calculation/readout remain separate. No original config or quotation delay changed.

Previous same-scenario server cuts: 309, 234, 182 ms on b51e959. Current small sample is insufficient to claim a statistically reliable improvement.

Next measurement: real-call sustained silence followed by resumed speech/interruption; at least 20 normal responses for latency gating. Real calls require a separate owner request.
