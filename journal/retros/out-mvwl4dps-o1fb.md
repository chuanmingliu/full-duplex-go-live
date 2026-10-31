# Native transport baseline — out-mvwl4dps-o1fb

Simulated call, 2026-10-31, original GoLive build `9547d22` through Active
Call. Original saved prompt, greeting, voice and provider/endpointing profile.

- Receiver first audio: 10 valid samples, median 465.5 ms, p95/max 561 ms;
  10 invalid observations excluded. This includes fillers.
- Server first audio: 13 samples, p50 380 ms, p95/max 381 ms.
- Server first content: p50 872 ms, p95/max 1130 ms; no missing,
  unverified or duplicate output records. Per-call strict gate is not accepted:
  fewer than 20 samples. Simulator audio arrivals are not handset latency.
- Permanent archive: five required files verified by size and SHA-256;
  stereo audio has signal on both channels and transcript has both speakers.

The PCM16/PCMU adapter comparison is broadly similar in receiver latency,
but receiver TTS warnings and remote hangup routing require qualification.
PCMA transcoding trials did not sustain conversation. See ISS-088/ISS-089
and the [full comparison](https://github.com/chuanmingliu/full-duplex-go-live/blob/codex/sip-rtp-adapter/docs/telephony/2026-11-01-native-sip-comparison.md).
Production Active Call remains in use; no real call was placed for this test.
