# ThinkPad backchannel observer deployment

Deployed on 2 November 2026 as a separate Compose project:
`golive-backchannel-detector`, container `golive-backchannel-detector-detector-1`.
The host binding is **127.0.0.1:18087 on the ThinkPad**, not on the Mac.
Release: `/home/chuanming/golive-backchannel/releases/20261102-4896f47`.

The original GoLive backchannel rules remain the live controller and default.
**No live engine hook or model-controlled interruption policy is enabled.**
The new service is ready for explicit local observation/replay clients. It
always returns `control_allowed: false`, with legacy fallback. No Active Call,
RustPBX, Aria, preset opener, voice or quotation configuration was replaced.

## Artifact and deployment verification

- Model: Pipecat FP32 ONNX, revision
  `4896f4774cdf1a9c5b581b0cd4a0c4000e8bb854`.
- Model SHA-256:
  `3e5d47740e5f75211cc78c5df701be9afe7b76c4115144212a18598766909240`.
- Docker image ID:
  `sha256:da33c33ac70a17f98c06f4278fa024a8c055540249e7c6f5c79c68f01285c0d2`.
- Docker health: healthy; model readiness: ready.
- CPU-only, one CPU limit, 384 MiB memory limit, non-root user, read-only root
  filesystem and model mount. Four bounded streaming sessions.
- Eight tests passed locally and inside the Linux image, including the actual
  model reset, independent stream state, invalid input, sequence gaps, overload,
  inference deadlines and browser-origin rejection.
- The new observer was stopped and restarted; original GoLive remained healthy.
- Original engine executable/process arguments/environment/profile/saved-context
  hashes matched afterward. Aria binding and all pre-existing containers matched.
- The live phone engine remains `f1fd9a6`, healthy with zero active sessions at
  verification. No real call was placed.

## Measured processing cost

The full private 16 kHz stereo recording from `out-mvykfqfn-wibn` was replayed
locally on the ThinkPad. Caller input used the left channel. Bot activity was
approximated from the right-channel energy; filler identity and verified AEC
were unavailable. Audio and transcript contents were not exported.

| Measurement | Synthetic silence | Private call replay |
| --- | ---: | ---: |
| 40 ms chunks | 100 | 3,015 |
| Audio duration | 4.0 s | 120.6 s |
| Inference median | 0.720 ms | 0.720 ms |
| Inference p95 | 0.894 ms | 0.940 ms |
| Inference maximum | 1.080 ms | 31.206 ms |
| Local round-trip p95 | 1.761 ms | 1.821 ms |
| Local round-trip maximum | 2.202 ms | 46.566 ms |
| Service errors | 0 | 0 |

Replay was unpaced and completed in 4.853 seconds. These measure observer
processing cost, not interruption latency, acoustic handset latency or savings.
The model emitted raw probabilities over every 20 ms frame. Threshold-frame
counts in the receipt are not utterances, correct classifications or cut events.

## Why the existing controller stays authoritative

The [upstream model card](https://huggingface.co/pipecat-ai/backchannel-detector/blob/4896f4774cdf1a9c5b581b0cd4a0c4000e8bb854/README.md)
limits validation to English and simulated telephone band-limiting, with no
real phone-codec validation. Our conversation is Chinese, and the recorder is
not proof of echo-cancelled microphone input. This replay therefore establishes
operability and bounded cost, **not improved backchannel accuracy**.

The next step, before any separately enabled assist mode, is labeled Chinese
telephony replay with known acknowledgments, true interruptions, echo and
background speech, followed by a bounded asynchronous live shadow tap. Neither
model scores nor service failure should bypass existing caller/echo safeguards.
Stopping this standalone observer leaves today's call path intact.

[Service/API runbook](../services/backchannel-detector.md),
[source and Compose](../../services/backchannel-detector/README.md),
[deployment receipt](evidence/2026-11-02-backchannel-detector/deployment.json),
[final verification](evidence/2026-11-02-backchannel-detector/verification.json),
[historical runner operations](../../scripts/local-ops/recorded/2026-11-02-backchannel-detector/README.md).
