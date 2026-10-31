# golive target-speaker verification

`codex/service-speaker-verification` maintains the lightweight speaker-focus
side channel that asks whether a window of audio belongs to the enrolled caller.
It uses CAM++ speaker embeddings and does not transcribe speech.

## Functionality and wire contract

- Accepts streaming 16 kHz, mono PCM16 audio through WebSocket `/verify`.
- Each binary message begins with an 8-byte little-endian session timestamp in milliseconds.
- Handles enrollment from an audio interval or a saved named profile.
- Returns caller/other-speaker frame probabilities for golive's focus decisions.
- Exposes `GET /health`; the usual box listener is `127.0.0.1:8032`.

Before enrollment, frames are unknown. Named embeddings can persist across
calls; they are private voice-derived data and must not be committed.

## Owned source

| Path | Responsibility |
| --- | --- |
| [edge/tsv_server.py](edge/tsv_server.py) | Embedding inference, enrollment, buffering, scoring and WebSocket server |
| [internal/focus/wire.go](internal/focus/wire.go) | Shared client wire contract; coordinate compatible changes |
| [internal/focus](internal/focus) | Shared focus consumer and tests |

This branch changes speaker verification and necessary shared-code integration
with coordinated review. The shared repository remains intact. General speech
recognition, multi-speaker diarization and phone transport are separate services.

## Runtime and checks

The native Python environment requires NumPy, ONNX Runtime and aiohttp, plus a
compatible local `campplus_zh_en.onnx` model. Use the verified environment and
model revision from the deployment; this branch does not distribute weights or
a locked Python environment.

From the repository root, syntax and shared-client checks are:

```sh
python3 -m py_compile edge/tsv_server.py
go test ./internal/focus
```

With an existing compatible environment and model, an isolated listener can run as:

```sh
TSV_DEV_PROFILES=$(mktemp -d)
/path/to/verified/python edge/tsv_server.py \
  --model /path/to/campplus_zh_en.onnx --profiles "$TSV_DEV_PROFILES" \
  --host 127.0.0.1 --port 18032 --threads 2 --threshold 0.28
```

Check `http://127.0.0.1:18032/health`, then test enrollment and scoring with
synthetic or approved audio. Syntax checks do not validate inference quality.

## State and maintenance

The observed box runtime and model live under `~/edge/tsv`; named profiles are
in its `profiles` directory. Preserve profile permissions and model identity.
The native runtime has not been migrated by the gateway Compose model.

Keep `/verify` on the established private listener. Coordinate changes to frame
rates, timestamps, enrollment and probability meaning with the focus consumer.
Evaluate false acceptance/rejection and processing lag before changing thresholds
on a live call; a healthy HTTP endpoint is insufficient evidence.
