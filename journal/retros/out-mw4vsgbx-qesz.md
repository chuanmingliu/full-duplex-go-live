# out-mw4vsgbx-qesz — silence feature simulator verification

- Build: `40c20f6`; 2026-11-06; original GoLive + Active Call; simulated call.
- Scenario: `sim-quote-direct`; simulator run `sim-f41e7f4d4b77416255093493`.
- Normal first-content: n=4, p50=989.0 ms, p95/max=1268 ms.
- First audio (includes openers): n=6, p50=380.0 ms, p95/max=380 ms.
- VAD-open to server cut: [271.0] ms. This is not handset playout.
- Strict gate: **not passed**. 1 unverified record(s); first_audio: 6 samples, need 20; conversational_first_content: 4 samples, need 20; conversational_first_content: p95 1268 ms is not below 1200 ms.
- Silence config v1 loaded; no reminders during this conversation. Backchannel detector reported audio_gap fallback; original controller retained.
- Five archive files verified by size/hash; chat has 6 caller and 10 assistant lines; both stereo directions contain signal; duration 66.356 s.
- Quotation calculation/readout remain separate. No original config or quotation delay changed.

Uncached calculation: 3,022 ms. Two verified readouts: 5,255 and 6,216 ms. One additional tool-result link is unverified.

Next measurement: real-call sustained silence followed by resumed speech/interruption; at least 20 normal responses for latency gating. Real calls require a separate owner request.
