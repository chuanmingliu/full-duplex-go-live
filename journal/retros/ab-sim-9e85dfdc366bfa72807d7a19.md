# Native adapter simulation — ab-sim-9e85dfdc366bfa72807d7a19

Adapter `fb11e2c`; original GoLive engine `9547d22`; PCM16 / PCMU; isolated Contact; 60-second probe.
Simulated customer only, 2026-10-31 / 2026-11-01. Original saved prompt,
providers and endpointing settings. Remote hangup reached the adapter and it exited normally without external cleanup.

- Receiver first audio: 6 samples, median 451.0 ms, p95/max 556/556 ms; 5 excluded observations.
  Includes fillers; not acoustic handset or semantic-content latency.
- Receiver end: `duration_limit`; 11 remote turns;
  0 reported failure/warning entries.
- Server first audio: 7 samples; p50/p95/max
  380/381/381 ms.
- Server first content: 7 samples; p50/p95/max
  869/1001/1001 ms.
- Strict per-call server gate: first_audio: 7 samples, need 20; first_content: 7 samples, need 20.
- Permanent archive: 5 files size/hash verified; both audio
  channels contain signal; 7 caller and 8 assistant transcript entries.

See ISS-088/ISS-089 and the [full comparison](https://github.com/chuanmingliu/full-duplex-go-live/blob/codex/sip-rtp-adapter/docs/telephony/2026-11-01-native-sip-comparison.md).
