# Tool/acknowledgment simulator trial: ab 4 baseline

2026-11-01. Simulator only; adapter `fb11e2c`, engine/CRM `toolack-baseline`.
Call `sim-toolack-1909dea7-b3dc-48de-8f9e-a2bce8a8503e`, receiver `sim-42fd56629d2d28a40df3703e`.
[Call Journal](http://192.168.71.9:9906/#/call/sim-toolack-1909dea7-b3dc-48de-8f9e-a2bce8a8503e).

- End: `duration_limit`, adapter exit 0.
- Archive: 5 files verified by size and SHA-256; 121.908 seconds;
  2 audio channels with signal; caller/assistant transcript entries
  17/14. Conversation capture complete: True.
- Receiver first audio: 4 valid samples, median 345.5 ms;
  first audio includes fillers and is not handset latency.
- Server first audio: n=14, median/p95/max
  869.0/1148/1148 ms.
- Server first content: n=14, median/p95/max
  912.5/1148/1148 ms.
- Deterministic readout: n=0, p95=None ms.
- Receiver warnings: warning: tts_chunk_failed (count=2).
- Strict 1200 ms/both/20-turn gate: failed. Reasons:
  1 unverified record(s); first_audio: 14 samples, need 20; first_content: 14 samples, need 20.

See the [comparison report](../../docs/telephony/2026-11-01-tool-ack-comparison.md)
for controlled savings and limitations. No physical handset capture. Do not infer
causal improvement from these unmatched conversational turns or incomplete metrics.
Original configurations and Active Call remain intact; no production deployment.
