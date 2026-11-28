# out-mw4vnujw-0qwl — silence feature simulator verification

- Build: `40c20f6`; 2026-11-06; original GoLive + Active Call; simulated call.
- Scenario: `silence-quiet-40c20f6`; simulator run `sim-1a3255943e83a650b77f21f2`.
- Normal first-content: n=12, p50=1032.5 ms, p95/max=1292 ms.
- First audio (includes openers): n=13, p50=380 ms, p95/max=503 ms.
- VAD-open to server cut: none observed ms. This is not handset playout.
- Strict gate: **not passed**. first_audio: 13 samples, need 20; conversational_first_content: 12 samples, need 20; conversational_first_content: p95 1292 ms is not below 1200 ms.
- Silence config v1 loaded; no reminders during this conversation. Backchannel detector reported audio_gap fallback; original controller retained.
- Five archive files verified by size/hash; chat has 13 caller and 15 assistant lines; both stereo directions contain signal; duration 121.689 s.
- Quotation calculation/readout remain separate. No original config or quotation delay changed.

The model did not obey the silent persona. This is **not** sustained-silence coverage. A separate deployed-engine zero-audio probe observed two bounded reminders and silence-rule closure.

Next measurement: real-call sustained silence followed by resumed speech/interruption; at least 20 normal responses for latency gating. Real calls require a separate owner request.
