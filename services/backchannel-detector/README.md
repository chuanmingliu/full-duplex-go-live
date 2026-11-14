# GoLive backchannel detector service

An optional, observation-only CPU service for the pinned
[Pipecat backchannel detector](https://huggingface.co/pipecat-ai/backchannel-detector).
It estimates caller backchannel versus floor-taking probabilities from audio.
It does not synthesize fillers, select openers, call an LLM or hang up calls.

**The original GoLive backchannel system remains the default controller.**
An [optional engine integration](../../docs/services/backchannel-integration.md)
now consumes observations to accelerate ASR finals under the original gates.
It is disabled by default; rollout status is separate. Every response states
`control_allowed: false` and `fallback: legacy`. Failure or stopping this
container leaves the original controller available. Chinese audio is accepted
with `language: "zh"`; the upstream English-only
training label does not block requests. Chinese experiments run directly on
the ThinkPad with the original GoLive voice and recorded Active Call audio.
Caller/echo checks remain authoritative. See the
[Chinese results](../../docs/telephony/2026-11-02-backchannel-chinese.md).

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
- `chinese_cases.json` / `chinese_eval.py`: labeled Chinese fixture experiment,
  paced PCM/G.711 tests and private telephony replay. Test-only Python 3.12
  `audioop` performs the codec round trip; it is not a runtime dependency.
- `test_chinese_eval.py`: chunk-availability timing, sustained crossings, codec
  length and silence checks. Evaluation tools run outside the service image.

Model metadata declares BSD-2-Clause. Model authorship remains Pipecat's.
No upstream implementation or training data is vendored here. Artifact download
is separate from runtime; raw audio, transcripts and model weights are not
committed. The upstream card links `pipecat-ai/pipecat-vap`, but that repository
returned HTTP 404 during this integration; the wrapper follows the published
ONNX contract and validates actual tensor names/shapes at runtime.

## Expanded Chinese experiment

`chinese_matrix.json` fixes the case population, threshold and audio variants
before evaluation. It keeps the original 12 cases and adds 40 new cases: listed
and unlisted acknowledgments, short corrections, acknowledgment-to-question
transitions and ambiguous prosody probes. Ambiguous probes are never included
in accuracy denominators.

`chinese_matrix_eval.py` runs 208 case/condition combinations, eight delayed
transitions, four controls and 12 real-time paced repeats. Noise before the
caller starts cannot be credited as a successful early interruption signal.
`chinese_archive_eval.py` correlates the predictions with all selected original
GoLive archive events, including missing signals and kept acknowledgments.
`summarize_chinese_matrix.py` joins the actual Go phrase-controller baseline
with those results; final scripted text is not an ASR latency measurement.

From this directory, with private fixture WAVs in the manifest order:

```sh
python chinese_matrix_eval.py --clips /PRIVATE/clips \
  --archive /PRIVATE/completed-call --output /PRIVATE/results.json
python summarize_chinese_matrix.py /PRIVATE/results.json /PRIVATE/legacy.json \
  --output /PRIVATE/summary.json
```

Generate `legacy.json` from the repository root with
`GOLIVE_CHINESE_BC_CASES=../../services/backchannel-detector/chinese_matrix.json`
and `GOLIVE_CHINESE_BC_EVAL_OUT=/PRIVATE/legacy.json`, running
`go test ./internal/duplex -run TestChineseBackchannelFixtureBaseline -count=1`.
The explicit case-file path is relative to the Go test package directory.

See the [expanded results](../../docs/telephony/2026-11-02-backchannel-expanded.md)
for measured opportunities, negative cases and limitations.
