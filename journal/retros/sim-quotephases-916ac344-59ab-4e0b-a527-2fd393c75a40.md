# Original-GoLive quotation phase diagnostic

2026-11-01. Simulator only; engine `f8e4d83`, CRM `3201ec4`, adapter `fb11e2c`.
Call `sim-quotephases-916ac344-59ab-4e0b-a527-2fd393c75a40`; receiver `sim-214288e23f48fa676a366c34`.
[Call Journal](http://192.168.71.9:9916/#/call/sim-quotephases-916ac344-59ab-4e0b-a527-2fd393c75a40).

Five archive files verified by SHA-256 and size; 92.719 seconds stereo, two channels with signal and 16 caller/16 assistant transcript entries. No receiver warnings. Both temporary services stopped; original file hashes and Active Call identity/start time unchanged.

| Server metric | Samples | p50 ms | p95 ms |
| --- | ---: | ---: | ---: |
| first_audio | 9 | 865 | 978 |
| first_content | 8 | 931.0 | 1016 |
| tool_result | 4 | 2481.5 | 8836 |
| quote_readout | 4 | 3406.5 | 9762 |

**1150 ms / both / 20-response gate: failed.**

- 1 missing record(s)
- 2 unverified record(s)
- first_audio: 9 samples, need 20
- first_content: 8 samples, need 20
- tool_result: p95 8836 ms is not below 1150 ms
- quote_readout: p95 9762 ms is not below 1150 ms

The assumed 50 ms combined telephony/handset overhead yields an estimated content p95 of 1066 ms for these eight valid responses. This is neither a qualifying sample count nor actual handset evidence.

| Foreground memory version | API gate wait ms | API operation ms | Poll HTTP ms | Poll sleep ms | Stale replies |
| --- | ---: | ---: | ---: | ---: | ---: |
| 0 | 0 | 3006 | 2 | 3001 | 6 |
| 1 | 2852 | 4508 | 4 | 4503 | 9 |
| 4 | 0 | 4005 | 5 | 3999 | 8 |

Read-only runtime inspection confirmed the configured endpoint is the local quotation test service on port 8793. Its active process uses `-compute-ms 2500 -jitter-ms 2000`; saved polling interval is 500 ms. The code intentionally sleeps before publishing a new quote. These delays were preserved. Quotation-service time is therefore primarily deliberate asynchronous readiness delay, not network transport or ASR/LLM/TTS overhead.

After this historical shared-SLA evaluation, the owner explicitly chose to preserve the original delay and report quote completion separately from conversational response latency. The new separate-SLA evaluation still requires 20 valid conversational responses, complete timing evidence and eventual physical handset verification. The original settings remain intact.

## Re-evaluation under the accepted separate quotation SLA

The same archived log, without new calls or altered measurements, yields first
audio p95 978 ms (9 samples) and conversational content p95 1016 ms (7 samples).
One verified quote-only handoff is excluded from conversational samples.
Quotation readout remains 9762 ms (4 samples), reported separately.
The gate still fails on one missing response, two unverified links and fewer
than 20 valid samples. The 50 ms assumed overhead gives a 1066 ms content
estimate; physical handset latency is not verified.
