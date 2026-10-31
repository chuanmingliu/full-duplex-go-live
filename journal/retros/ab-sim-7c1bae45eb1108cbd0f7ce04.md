# Native adapter simulation — ab-sim-7c1bae45eb1108cbd0f7ce04

Adapter `c3e049b`; original GoLive engine `9547d22`; direct G.711 / PCMA.
Simulated customer only, 2026-10-31 / 2026-11-01. Original saved prompt,
providers and endpointing settings. Failed conversation: native silence hangup; no recognized caller turn. Short receiver gaps do not establish success.

- Receiver first audio: 1 samples, median 79 ms, p95/max 79/79 ms; 0 excluded observations.
  Includes fillers; not acoustic handset or semantic-content latency.
- Receiver end: `remote_hangup`; 2 remote turns;
  0 reported failure/warning entries.
- Server first audio: 0 samples; p50/p95/max
  None/None/None ms.
- Server first content: 0 samples; p50/p95/max
  None/None/None ms.
- Strict per-call server gate: first_audio: 0 samples, need 20; first_content: 0 samples, need 20.
- Permanent archive: 5 files size/hash verified; both audio
  channels contain signal; 0 caller and 2 assistant transcript entries.

See ISS-088/ISS-089 and the [full comparison](https://github.com/chuanmingliu/full-duplex-go-live/blob/codex/sip-rtp-adapter/docs/telephony/2026-11-01-native-sip-comparison.md).
