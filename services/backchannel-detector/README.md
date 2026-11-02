# GoLive backchannel detector service

An optional, observation-only CPU service for the pinned
[Pipecat backchannel detector](https://huggingface.co/pipecat-ai/backchannel-detector).
It estimates caller backchannel versus floor-taking probabilities from audio.
It does not synthesize fillers, select openers, call an LLM or hang up calls.

**The original GoLive backchannel system remains the default controller.**
This service is not in the live call path. Every response states
`control_allowed: false` and `fallback: legacy`. Failure or stopping this
container has no effect on existing call handling. The upstream model is
English-only and unvalidated on real phone codecs; Chinese results are
experimental observations, not permission to bypass caller/echo checks.

## Run

```sh
python3 fetch_model.py --directory models
docker compose up -d --build detector
curl --fail http://127.0.0.1:18087/healthz
```

The Compose project publishes only loopback port 18087, uses one CPU and
384 MiB, runs without root privileges and mounts model files read-only. It is
independent of Active Call, RustPBX, Aria and GoLive. Build uses Python 3.12;
dependency versions and model revision/checksums are pinned. Deployment receipts
record the resolved image ID because the Python base tag can advance.

See [the service runbook](../../docs/services/backchannel-detector.md) for the
stream protocol, test commands, ThinkPad layout, limitations and rollback.

## Files

- `detector.py`: validate PCM/state, verify model checksums, maintain ONNX caches.
- `server.py`: bounded WebSocket sessions and CPU work, health, timeout handling.
- `model.lock.json` / `fetch_model.py`: pinned public artifact retrieval.
- `compose.yaml` / `Dockerfile` / `requirements.txt`: isolated deployment.
- `test_service.py`: state isolation, validation, overload and timeout tests.
- `probe.py`: synthetic smoke or local private stereo replay; numeric output only.

Model metadata declares BSD-2-Clause. Model authorship remains Pipecat's.
No upstream implementation or training data is vendored here. Artifact download
is separate from runtime; raw audio, transcripts and model weights are not
committed. The upstream card links `pipecat-ai/pipecat-vap`, but that repository
returned HTTP 404 during this integration; the wrapper follows the published
ONNX contract and validates actual tensor names/shapes at runtime.
