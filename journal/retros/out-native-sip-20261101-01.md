# Original GoLive through the native SIP adapter: real call

2026-11-01, 11:19:36–11:22:10 Asia/Shanghai. Call `out-native-sip-20261101-01`,
session `sess_8ef68022d5e1a84feb49beb5`. Real configured handset, adapter
`fb11e2c`, original engine `9547d22`. The original Active Call service remains
running for rollback. [Call Journal](http://192.168.71.9:9896/#/call/out-native-sip-20261101-01).

## Outcome and recordings

The assistant initiated hangup; the SIP BYE transaction succeeded and the adapter
exited 0. This verifies assistant-initiated teardown for this call, not a
handset-originated BYE. Both original and isolated engines were idle afterward.

All seven permanent archive files passed size and SHA-256 verification:
`audio.wav`, `call.json`, `golive.log`, `ringback.json`, `ringback.wav`,
`session.json`, and `transcript.txt`. The 153.66-second 16 kHz stereo recording
has signal in both channels; transcript counts are 14 caller and 16 assistant
entries. The separate carrier-ringback WAV is 9.2105 seconds with signal.
These checks establish capture completeness, not intelligibility or acoustic
handset timing. No separate handset acoustic capture was verified.

## Latency

Canonical analyzer: `--gate --target-ms 1200 --metric both --min-turns 20`.
Scope is caller last sound to GoLive player output, before carrier/handset playout.

| Measurement | Samples | Median | p95 / maximum |
| --- | ---: | ---: | ---: |
| First audio, including fillers | 13 | 380 ms | 707 ms |
| First content | 13 | 1051 ms | 1198 ms |
| Linked deterministic quotation readout | 2 | 1135.5 ms | 1198 ms |

The two readouts were 1198 and 1073 ms. Both include the original caller's pause
and result handoff, even though the prepared result's own readout stamp was zero.
No missing, unverified, malformed, duplicate, fallback-content or invalid-link
records were reported. Two answered turns were interrupted and remain counted.

**Strict gate fails:** both first-audio and first-content populations have 13
samples, below the required 20. The largest server measurement has only 2 ms
of margin below 1200 ms. This is not proof of the end-to-end handset target.

SIP answer to first outbound RTP was 2.722 ms. Across 16 response starts,
adapter enqueue-to-first-send was median 0.082 ms, nearest-rank p95/max 19.387 ms.
Those are packet timings, not the time audible speech reached the phone.

The earlier [simulator comparison](../../docs/telephony/2026-11-01-native-sip-comparison.md)
did not establish a large first-audio gain over Active Call. Its slow quotation
examples used different requests/cache histories; this real call cannot establish
a causal quotation improvement relative to them.

## Diagnostics and follow-up

- Accepted RTP stream counters show no detected loss or late/duplicate packets;
  153 packets were rejected without a recorded reason. Their cause is unknown.
- Peak output queue was 359 ms; this is not a uniform per-response delay.
  Fourteen playback-clear events removed 30 queued frames, with no stale audio
  events. These counters alone do not imply 14 failed interruptions.
- One echo-like barge-in warning occurred. It is a diagnostic signal, not proof
  that real caller speech was ignored. Do not change interruption thresholds
  based on that warning alone.
- The separate latency console rejected 88 events with HTTP 400, reporting
  `Invalid canonical field: callId`. Its phone-like-identifier filter rejects
  the date-based ID. A prefixed UUID passes the deployed validator. Use that
  format for future calls; keep this archived identity unchanged (ISS-090).
  Call Journal and the canonical log analysis remain available.

No new adapter runtime defect was established by this call. Keep the deployed
candidate available and Active Call intact. The next explicitly authorized test
should use a fresh UUID ID, matched requests, at least 20 caller replies, a
handset acoustic capture, deliberate interruptions, and handset-initiated hangup.
Verify actual console ingestion then; a validator check alone does not prove it.
