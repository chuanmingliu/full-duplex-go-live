# Ready-acknowledgment SIP simulator: 1e38bac

2026-11-01. Simulator only, original GoLive configuration, adapter `fb11e2c`.
Call `sim-readyack-bf836a1f-184c-4afa-95d8-f4fb91f5270c`; receiver `sim-a260daa85a2a294bee371c24`.
[Call Journal](http://192.168.71.9:9916/#/call/sim-readyack-bf836a1f-184c-4afa-95d8-f4fb91f5270c).

Five archived files verified by SHA-256 and size; 92.69 seconds of stereo audio, both channels with signal; caller/assistant transcript entries 9/13. Receiver failures: 0.

| Server metric | Samples | p50 ms | p95 ms |
| --- | ---: | ---: | ---: |
| first_audio | 8 | 903.0 | 1008 |
| first_content | 8 | 938.5 | 1008 |
| tool_result | 7 | 21959 | 26141 |
| quote_readout | 2 | 12489.0 | 13696 |

Completed-calling acknowledgment releases: 0.
Strict 1200 ms / both metrics / 20-response gate: **failed**.

- 1 unverified record(s)
- first_audio: 8 samples, need 20
- first_content: 8 samples, need 20
- 5 quote_readouts_unverified record(s)
- tool_result: p95 26141 ms is not below 1200 ms
- quote_readout: p95 13696 ms is not below 1200 ms

No physical handset capture; these are server output timings. Different turn counts and incomplete links prevent a causal A/B latency claim. Original services and configurations remain intact; no production promotion.

Future server qualification uses **strictly below 1150 ms**, based on the user’s assumed 50 ms combined telephony and handset overhead. Estimated handset p95 = server p95 + 50 ms; this is not acoustic verification.
