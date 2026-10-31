# Native adapter simulation — ab-sim-015ab58a08ac39a2e46abd3a

Adapter `f758708`; original GoLive engine `9547d22`; PCM16 / PCMU; shared Contact.
Simulated customer only, 2026-10-31 / 2026-11-01. Original saved prompt,
providers and endpointing settings. Conversation occurred, but remote BYE was misrouted to Active Call; explicit stale-session cleanup was required.

- Receiver first audio: 7 samples, median 429 ms, p95/max 493/493 ms; 10 excluded observations.
  Includes fillers; not acoustic handset or semantic-content latency.
- Receiver end: `inactivity`; 16 remote turns;
  1 reported failure/warning entries.
- Server first audio: 12 samples; p50/p95/max
  380.0/380/380 ms.
- Server first content: 12 samples; p50/p95/max
  893.5/1736/1736 ms.
- Strict per-call server gate: first_audio: 12 samples, need 20; first_content: 12 samples, need 20; first_content: p95 1736 ms is not below 1200 ms.
- Permanent archive: 5 files size/hash verified; both audio
  channels contain signal; 12 caller and 14 assistant transcript entries.

See ISS-088/ISS-089 and the [full comparison](https://github.com/chuanmingliu/full-duplex-go-live/blob/codex/sip-rtp-adapter/docs/telephony/2026-11-01-native-sip-comparison.md).
