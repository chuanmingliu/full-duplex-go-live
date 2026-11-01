# Silent-calling acknowledgment SIP simulator: f8e4d83

2026-11-01. Simulator only, original GoLive configuration, adapter `fb11e2c`.
Call `sim-silentack-8395ce4c-0e58-4939-afa1-ded9247c784b`; receiver `sim-ce605ea8c491bc93ce8d9847`.
[Call Journal](http://192.168.71.9:9916/#/call/sim-silentack-8395ce4c-0e58-4939-afa1-ded9247c784b).

Five archive files verified by SHA-256 and size; 92.512 seconds of stereo audio, both channels with signal; caller/assistant transcript entries 13/15.

| Server metric | Samples | p50 ms | p95 ms |
| --- | ---: | ---: | ---: |
| first_audio | 9 | 381 | 1181 |
| first_content | 9 | 877 | 5180 |
| tool_result | 3 | 5180 | 6162 |
| quote_readout | 3 | 6131 | 7113 |

Acknowledgment release reasons: {"playback_completed": 1, "superseded_without_audio": 2}.
Receiver warnings: one tts_chunk_failed.

Strict **1150 ms / both metrics / 20-response gate: failed**.

- 2 unverified record(s)
- first_audio: 9 samples, need 20
- first_audio: p95 1181 ms is not below 1150 ms
- first_content: 9 samples, need 20
- first_content: p95 5180 ms is not below 1150 ms
- tool_result: p95 6162 ms is not below 1150 ms
- quote_readout: p95 7113 ms is not below 1150 ms

The 1150 ms server target uses the owner’s assumed 50 ms combined telephony and handset overhead. Estimates are server timing plus 50 ms; no physical handset capture establishes that overhead.

Different conversations and small samples prevent a causal A/B improvement claim. Original configurations and Active Call are unchanged; no real service promotion. See the [comparison](../../docs/telephony/2026-11-01-ready-result-handoff.md).
