# Audio8 TTS integration

`codex/service-audio8-tts-integration` maintains the connection between golive's
edge gateway and the ThinkPad's Audio8 text-to-speech runtime. It is a
**deployment-integration branch**, not an upstream Audio8 source fork. Model
weights, voice artifacts and the installed Python environment remain external.

## Scope of this branch

| Path | Responsibility |
| --- | --- |
| `internal/edge/audio8.go` | golive's adapter for the Audio8 speech services |
| `deploy/services/box/core.compose.yml` | Optional edge gateway, with an external TTS dependency |
| `deploy/services/box/core.env.example` | Gateway paths and deployment settings |
| `EDGE.md` | Audio interface, installation history and timing limitations |
| `docs/services/box.md` | Runtime provenance and lifecycle boundaries |

The runtime is maintained upstream in
[Audio8-AI/Audio8_TTS](https://github.com/Audio8-AI/Audio8_TTS). Its source is not
imported into this branch, and the gateway Compose model does not build it.

## Observed deployment

On 2026-10-31, `/home/chuanming/edge/Audio8_TTS` was checked out at
`07e40f5d0b03fc473635ef378654bfb581027ac3`. The active native process ran from
`onnx_runtime_0_1b_int8` under that checkout, using Python 3.12.

The runtime listens on `127.0.0.1:8024`; its availability endpoint is
`/api/health`. The edge gateway on port 8030 calls this service. Existing
runtime scripts and its `.venv` own model startup; Compose currently owns only
the proposed gateway container.

The runtime exposes local synthesis for experiments. Its installation does
not change the production phone profile's TTS provider automatically.

## Maintenance

- Record the upstream commit, local modifications, package versions, voice
  configuration and model hashes before rebuilding the native environment.
- Probe runtime `/api/health` separately from gateway `/healthz`.
- Use an isolated synthesis fixture to check first-audio time, complete output,
  sample rate and decoding through the gateway after a change.
- Preserve the runtime model/voice directories and service logs. They are not
  disposable source checkout files.
- Do not mount the native `.venv` into an arbitrary container: interpreter paths
  and native dependencies must match a deliberately built runtime image.

Retain the prior checkout, environment and model/voice configuration for
rollback. Restore those together with the gateway URL when reverting a runtime
change. `phone-brain.sh edge stop` affects the other speech runtimes as well;
it is not a narrow TTS restart.

See [native speech runtimes](docs/services/box.md#native-speech-model-runtimes),
[the edge contract](EDGE.md), and
[the service branch registry](docs/services/branches.json).
