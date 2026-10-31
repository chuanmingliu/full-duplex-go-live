# golive streaming diarization

`codex/service-diarization` maintains the Nemotron-based speaker-activity
runtime. It labels which speakers are active over time and supplies those
frames to the edge gateway or a compatible focus client.

## Functionality and wire contract

- Receives 16 kHz, mono PCM16 over WebSocket `/diarize`.
- Binary audio is prefixed with an 8-byte little-endian session timestamp in milliseconds.
- Returns streaming speaker probabilities and reports frame granularity/model lag.
- Bounds queued work and leaves skipped spans unlabelled rather than inventing silence.
- Exposes `GET /health`; the usual box listener is `127.0.0.1:8031`.

Diarization supplies speaker activity, not a speech transcript. It is separate
from the target-speaker verifier used for enrolled-caller matching.

## Owned source

| Path | Responsibility |
| --- | --- |
| [edge/diar_server.py](edge/diar_server.py) | Streaming model execution, backlog handling and server |
| [edge/probe_diar.py](edge/probe_diar.py) | Offline timing/activity probe |
| [internal/focus/wire.go](internal/focus/wire.go) | Shared client wire contract; coordinate compatible changes |

This branch changes diarization and necessary shared-code integration with
coordinated review. Other services remain in the shared checkout; model weights
and the deployment's Python environment are maintained outside source control.

## Runtime and checks

The verified Python environment needs NumPy, PyTorch, aiohttp and a Transformers
version supporting this model's streaming processor. A generic latest package
install is not a substitute for the deployment's compatible model/environment.

From the repository root:

```sh
python3 -m py_compile edge/diar_server.py edge/probe_diar.py
go test ./internal/focus
```

With the compatible native environment and local model directory:

```sh
/path/to/verified/python edge/diar_server.py --model /path/to/diarization-model \
  --mode ultra_low_latency --host 127.0.0.1 --port 18031 \
  --threads 4 --frame-ms 80 --max-backlog 1
```

Check `http://127.0.0.1:18031/health`, then validate streaming probabilities and
processing lag with approved input. Syntax/shared-client tests do not run the model.

## State, dependencies and maintenance

The observed box runtime and model are under `~/edge/diar`. Session audio and
speaker cache live in memory; the server does not archive input audio. Runtime
logs and model files remain outside this branch.

The edge gateway can forward its diarization endpoint to this runtime. The
[gateway Compose model](deploy/services/box/core.compose.yml) does not migrate
or start this native Python process. Preserve model identity and compatible
package versions when updating it.

Compare frame coverage, skipped chunks and sustained compute lag after changes.
Keep the shared timestamp/probability contract compatible with consumers;
changing modes or backlog limits can affect call behavior even when health is green.
