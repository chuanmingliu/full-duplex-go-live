# Tool/acknowledgment simulator trial: ab 3 candidate

2026-11-01. Simulator only; adapter `fb11e2c`, engine/CRM `toolack-candidate`.
Call `sim-toolack-e1bc76ab-c0bf-4e61-ade8-3c55a7107095`, receiver `sim-b6746e183bb58ddd494af482`.
[Call Journal](http://192.168.71.9:9906/#/call/sim-toolack-e1bc76ab-c0bf-4e61-ade8-3c55a7107095).

- End: `inactivity`, adapter exit 0.
- Archive: 5 files verified by size and SHA-256; 27.480 seconds;
  2 audio channels with signal; caller/assistant transcript entries
  0/1. Conversation capture complete: False.
- Receiver first audio: 1 valid samples, median 54 ms;
  first audio includes fillers and is not handset latency.
- Server first audio: n=0, median/p95/max
  None/None/None ms.
- Server first content: n=0, median/p95/max
  None/None/None ms.
- Deterministic readout: n=0, p95=None ms.
- Receiver warnings: warning: tts_chunk_failed (count=3).
- Strict 1200 ms/both/20-turn gate: failed. Reasons:
  first_audio: 0 samples, need 20; first_content: 0 samples, need 20.

See the [comparison report](../../docs/telephony/2026-11-01-tool-ack-comparison.md)
for controlled savings and limitations. No physical handset capture. Do not infer
causal improvement from these unmatched conversational turns or incomplete metrics.
Original configurations and Active Call remain intact; no production deployment.
