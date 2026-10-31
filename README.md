# Audio8 ASR integration

`codex/service-audio8-asr-integration` maintains the golive deployment contract
for the ThinkPad's local Audio8 speech-recognition runtime. It is a
**deployment-integration branch**, not a fork or import of the upstream model
runtime. The maintained interface turns recorded speech into text through the
local edge gateway.

## Scope of this branch

| Path | Responsibility |
| --- | --- |
| `internal/edge/audio8.go` | golive's adapter for the external Audio8 runtime |
| `deploy/services/box/core.compose.yml` | Optional edge gateway service; does not start the ASR runtime |
| `deploy/services/box/core.env.example` | Gateway paths and deployment settings |
| `EDGE.md` | Model interface, setup history and measured limitations |
| `docs/services/box.md` | Observed runtime, ownership and persistence |

The local runtime under `~/edge/asr` and its model files are outside this source
branch. This branch does not contain a complete container image for them.

## Observed deployment

The 2026-10-31 inventory found a native Python process in
`/home/chuanming/edge/asr`, listening on `0.0.0.0:7860`. The gateway contacts it
at `http://127.0.0.1:7860`; availability is checked at `/health`.

The installation was documented as the Hugging Face runtime bundle
`Edge0/Audio8-ASR-0.1B-onnx-runtime`. No Git metadata was detected in the runtime
directory. Its exact downloaded revision and package environment must be
recorded before rebuilding it.

Audio8 ASR provides whole-clip transcription. Its presence does not mean that
the production phone profile uses it as the streaming ASR provider. The
observed wildcard bind also differs from the earlier loopback-only intent.

## Maintenance

- Check the runtime's `/health` endpoint and the edge gateway's `/healthz`
  independently; a gateway process alone does not establish model readiness.
- Preserve the current model bundle, configuration, virtual environment
  manifest and `service.log` under `~/edge/asr` when preparing a replacement.
- Validate changes to the adapter with the edge package tests; validate any
  runtime change with an isolated transcription fixture and timing check.
- Review current clients before changing the wildcard listener to loopback.
- Keep native lifecycle ownership until a tested runtime image exists.
  `phone-brain.sh edge stop` also stops other model runtimes and is not an
  ASR-only operation.

For rollback, restore the retained runtime/environment and gateway endpoint
configuration. Do not replace the model directory with an empty container
volume or assume the optional gateway Compose profile packages the model.

See [native speech runtimes](docs/services/box.md#native-speech-model-runtimes),
[the edge contract](EDGE.md), and
[the service branch registry](docs/services/branches.json).
