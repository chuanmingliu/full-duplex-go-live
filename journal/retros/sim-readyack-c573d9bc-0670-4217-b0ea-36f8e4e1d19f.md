# Ready-acknowledgment SIP simulator: 151b2e3

2026-11-01. Simulator only, original GoLive configuration, adapter `fb11e2c`.
Call `sim-readyack-c573d9bc-0670-4217-b0ea-36f8e4e1d19f`; receiver `sim-3244025f7a6adb23baa9bd12`.
[Call Journal](http://192.168.71.9:9916/#/call/sim-readyack-c573d9bc-0670-4217-b0ea-36f8e4e1d19f).

Five archived files verified by SHA-256 and size; 92.659 seconds of stereo audio, both channels with signal; caller/assistant transcript entries 13/12. Receiver failures: 0.

| Server metric | Samples | p50 ms | p95 ms |
| --- | ---: | ---: | ---: |
| first_audio | 6 | 851.5 | 995 |
| first_content | 5 | 899 | 995 |
| tool_result | 1 | 13496 | 13496 |
| quote_readout | 1 | 14387 | 14387 |

Completed-calling acknowledgment releases: 2.
Strict 1200 ms / both metrics / 20-response gate: **failed**.

- 1 missing record(s)
- 8 unverified record(s)
- first_audio: 6 samples, need 20
- first_content: 5 samples, need 20
- tool_result: p95 13496 ms is not below 1200 ms
- quote_readout: p95 14387 ms is not below 1200 ms

No physical handset capture; these are server output timings. Different turn counts and incomplete links prevent a causal A/B latency claim. Original services and configurations remain intact; no production promotion.

Future server qualification uses **strictly below 1150 ms**, based on the user’s assumed 50 ms combined telephony and handset overhead. Estimated handset p95 = server p95 + 50 ms; this is not acoustic verification.
