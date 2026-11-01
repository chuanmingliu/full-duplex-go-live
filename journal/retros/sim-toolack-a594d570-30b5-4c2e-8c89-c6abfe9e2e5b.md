# Tool/acknowledgment simulator trial: focused 1 baseline

2026-11-01. Simulator only; adapter `fb11e2c`, engine/CRM `toolack-baseline`.
Call `sim-toolack-a594d570-30b5-4c2e-8c89-c6abfe9e2e5b`, receiver `sim-09b328baf5bd3a25aeaa65d9`.
[Call Journal](http://192.168.71.9:9916/#/call/sim-toolack-a594d570-30b5-4c2e-8c89-c6abfe9e2e5b).

- End: `duration_limit`, adapter exit 0.
- Archive: 5 files verified by size and SHA-256; 92.755 seconds;
  2 audio channels with signal; caller/assistant transcript entries
  13/16. Conversation capture complete: True.
- Receiver first audio: 2 valid samples, median 321.5 ms;
  first audio includes fillers and is not handset latency.
- Server first audio: n=10, median/p95/max
  816.5/992/992 ms.
- Server first content: n=9, median/p95/max
  892/992/992 ms.
- Deterministic readout: n=1, p95=13177 ms.
- Receiver warnings: none.
- Strict 1200 ms/both/20-turn gate: failed. Reasons:
  1 missing record(s); 4 unverified record(s); first_audio: 10 samples, need 20; first_content: 9 samples, need 20; 1 quote_readouts_unverified record(s); tool_result: p95 25054 ms is not below 1200 ms; quote_readout: p95 13177 ms is not below 1200 ms.

See the [comparison report](../../docs/telephony/2026-11-01-tool-ack-comparison.md)
for controlled savings and limitations. No physical handset capture. Do not infer
causal improvement from these unmatched conversational turns or incomplete metrics.
Original configurations and Active Call remain intact; no production deployment.
