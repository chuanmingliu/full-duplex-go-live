# golive edge speech gateway

`codex/service-edge-gateway` maintains the authenticated API in front of local
speech-model runtimes. It normalizes requests and audio formats while keeping
model hosting separate from callers.

## Functionality and API

- Lists configured models through `GET /v1/models` and `/v1/models/{id}`.
- Accepts audio through `POST /v1/audio/transcriptions`.
- Streams generated audio through `POST /v1/audio/speech`.
- Proxies speaker activity through WebSocket `GET /v1/audio/diarize`.
- Applies per-model concurrency control, bounded waiting and cancellation.
- Reports unauthenticated gateway health at `GET /healthz`.

The HTTP shapes are OpenAI-compatible where applicable; that does not make
OpenAI the provider of these self-hosted models. Normal API requests require
the gateway token.

## Owned source

| Path | Responsibility |
| --- | --- |
| [cmd/edge](cmd/edge) | CLI, token setup, listeners and lifecycle |
| [internal/edge](internal/edge) | API, model adapters, audio normalization and request limits |
| [EDGE.md](EDGE.md) | Detailed gateway and runtime integration guide |

The gateway does not own model weights or the model servers' source. This
branch changes gateway functionality and necessary shared-code integration
with coordinated review; shared repository files remain present.

## Build and run

Use the Go version in [go.mod](go.mod), from the repository root:

```sh
go test ./internal/edge
go build -o /tmp/golive-edge ./cmd/edge
EDGE_DEV_DIR=$(mktemp -d)
/tmp/golive-edge -addr 127.0.0.1:18030 -dir "$EDGE_DEV_DIR" \
  -audio8-asr http://127.0.0.1:7860 \
  -audio8-tts http://127.0.0.1:8024 -diarize http://127.0.0.1:8031
```

The model URLs must point to separately running runtimes. Gateway startup or
`curl --fail http://127.0.0.1:18030/healthz` does not prove inference readiness.
Exercise an appropriate synthetic transcription/synthesis request separately.

## State and deployment

The observed box gateway listens on loopback and LAN port 8030. Its token and
operational log are under `~/golive-edge`. Keep token values out of Git and
preserve token continuity when moving the service.

The [staged `edge` Compose service](deploy/services/box/core.compose.yml) covers
the gateway only. It does not start the ASR, TTS or diarization runtimes.
See [migration requirements](deploy/services/README.md) before replacing the
native gateway, and retain the existing model URLs and request behavior.
