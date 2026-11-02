# Pipecat backchannel observation service

## Scope and default

GoLive's original transcript phrase matching, acknowledgment hold, caller
identity, echo and background-voice rejection remain the default call policy.
The new service runs independently on the ThinkPad and is **not connected to
live call control**. No GoLive profile, prompt, voice, opener or quotation-delay
setting changes. Aria and Active Call routing remain unchanged.

The model answers an audio question: is the caller listening/responding briefly,
or trying to take the floor? It is not a small decision LLM, an opener generator,
or a replacement ASR. No outbound model API receives the call audio.

The owner explicitly requested Chinese telephony testing. Chinese is accepted
with `language: "zh"`; `language_supported: false` describes upstream validation,
not a request rejection. The service has now been tested with Chinese synthetic
speech and real Active Call recordings; see the
[results and comparison](../telephony/2026-11-02-backchannel-chinese.md).

The current integration stage exposes raw observations only. Upstream training
and evaluation are English-only; that is a measurement caveat, not a test gate.
The model expects echo-cancelled input; existing caller identity and echo gates
remain authoritative. `control_allowed` is always false, including for English.
Its `fallback: legacy` describes the required integration policy; it does not
claim that an unimplemented live engine hook is already running.

Source: [pinned model card](https://huggingface.co/pipecat-ai/backchannel-detector/blob/4896f4774cdf1a9c5b581b0cd4a0c4000e8bb854/README.md).

## Model and transport

Pinned revision: `4896f4774cdf1a9c5b581b0cd4a0c4000e8bb854`.
Use FP32 ONNX on CPU, one inference thread. The model and `model.json` hashes
are verified before readiness. Each WebSocket owns seven recurrent/cache
tensors, initially zero. No state is shared between calls or reconnects.

- Health: `GET http://127.0.0.1:18087/healthz`.
- Stream: `WS ws://127.0.0.1:18087/v1/stream`.
- Connection limit: four; excess connections receive HTTP 503 with legacy fallback.
- Idle timeout: 20 seconds; lifetime/audio limit: 30 minutes.
- Inference admission wait: at most 25 ms; execution/queued-result deadline:
  another 75 ms. Timeout closes the observation stream and cannot stall GoLive.
  A timed-out computation still holds its bounded worker slot until it exits.
- Only internal clients without a browser `Origin` header may open streams.
  Do not expose this unauthenticated loopback service on the LAN or a public tunnel.

First message:

```json
{"type":"start","sample_rate":16000,"language":"zh","aec":false}
```

Each subsequent message carries exactly 640 mono PCM16 little-endian samples
(40 ms at 16 kHz), base64-encoded, and two 20 ms bot-state frames:

```json
{
  "type": "audio",
  "seq": 0,
  "pcm_s16le": "BASE64_OF_1280_BYTES",
  "bot_state": [[1,0,0,1,0,0,0,0],[1,0.004,0.004,1,0,0,0,0]]
}
```

`seq` starts at zero and must be contiguous. On a dropped frame or timeout,
reconnect to reset state; do not reuse stale predictions. The eight bot-state
fields are: speaking flag; elapsed seconds since onset, since last pause of at
least 200 ms, and since stop (each clipped to five seconds and divided by five);
filler-playing flag; three reserved zeros. Supply actual output-side activity,
not queued TTS activity. `aec` is a caller declaration, not verification.

Each prediction has two frames of five intent probabilities (`continuer`,
`assessment`, `negative`, `floor_take`, `non_speech`), plus VAD/VAP auxiliary
outputs, queue time and inference time. Backchannel probability is the sum of
the first two classes; floor probability is negative plus floor-taking. These
are raw per-frame observations, not calibrated utterance decisions. Do not
treat any single high backchannel frame as permission to ignore an interruption.
The upstream evaluation's utterance-end/backstop policy is not reproduced here.

## ThinkPad operations and rollback

Compose project: `golive-backchannel-detector`; service: `detector`.
Release root: `/home/chuanming/golive-backchannel/releases/20261102-4896f47`.
The health endpoint is host-loopback only, reachable from host-networked box
processes. A container on a separate bridge needs an explicit network design;
its own localhost is not the ThinkPad host.

Use the existing Mac runner and inspect current state before mutations:

```sh
cd /home/chuanming/golive-backchannel/releases/20261102-4896f47
docker compose ps
curl --fail http://127.0.0.1:18087/healthz
docker compose logs --tail 50 detector
```

Rollback is simply `docker compose stop detector` in that release directory.
It leaves all existing phone services and original backchannel behavior intact.
Restart only this optional service with `docker compose up -d detector`.
Do not restart the production GoLive engine merely to run this observer.

## Verification and Chinese testing

```sh
python3 -m venv /tmp/bcd-test
/tmp/bcd-test/bin/pip install -r requirements.txt
BCD_MODEL_DIR=models /tmp/bcd-test/bin/python -m unittest -v test_service test_chinese_eval
/tmp/bcd-test/bin/python probe.py --seconds 4
# Private box-local replay; never upload the WAV or output raw transcripts:
/tmp/bcd-test/bin/python probe.py --wav /PRIVATE/archive/audio.wav --seconds 180
```

Run those commands from `services/backchannel-detector` or the deployed release.
Replay takes caller audio from the left channel and approximates bot activity
from right-channel energy; filler identity and verified AEC are unavailable.
It measures service cost and exercises state continuity. It cannot establish
Chinese classification accuracy, echo rejection, end-to-end latency savings
or equivalence to live playback timing. Raw threshold-frame counts are not
interruptions and must not be compared to the number of server cut events.

The Chinese evaluator consumes 12 WAV clips in the exact order of
`chinese_cases.json`, generated independently to prevent failed synthesis from
shifting labels. It runs 24 speech cases (PCM and G.711 A-law round trip), plus
three controls. With `--paced`, input arrives in 40 ms chunks in real time.
Its raw 0.7 floor threshold and three-frame diagnostic are fixed before the
run. It does not reproduce the upstream unavailable VAD/backstop policy.

```sh
python chinese_eval.py --clips /PRIVATE/chinese-clips --paced \
  --archive /PRIVATE/completed-call --output /PRIVATE/results.json
```

All speech and per-frame traces stay private on the box. The public evidence
contains numeric summaries and synthetic case IDs only. Test tools are mounted
into a disposable container using the existing detector image; they do not
restart the detector or change GoLive's configuration.

A future live integration should be an asynchronous, bounded shadow tap first:
16 kHz input plus actual playback state, no wait on the foreground audio loop,
per-call sequence IDs, fresh decisions only, and legacy fallback on every
deadline/error/disconnect. Existing identity and echo checks remain authoritative.
Only labeled Chinese/codec/echo/background/backchannel/interruption replay and
real-call evidence can justify a separately enabled assist policy. No such
control-policy switch is enabled by this deployment.


## Expanded Chinese test suite

The [expanded matrix](../../services/backchannel-detector/chinese_matrix.json)
keeps the original 12 regression cases and adds 40 new utterances. Four fixed
conditions cover clean PCM, G.711 A-law, deterministic noise and delayed residual
echo. Separate sequence cases vary the gap between an acknowledgment and a
real interruption. Prosody-only probes remain ambiguous until their audio is
independently labeled; punctuation is not ground truth for vocal intent.

The suite preserves misses and original-controller disagreements, including
pre-onset false floor signals. It replays the most recent eligible completed
`f1fd9a6` archives selected before inspecting model predictions, including calls
with no interruption events. No result authorizes a live playback cut. See the
[service README](../../services/backchannel-detector/README.md#expanded-chinese-experiment)
for commands and baseline generation.

The [expanded results](../telephony/2026-11-02-backchannel-expanded.md) report
232 synthetic trials and all six eligible recorded-call replays, retaining
false triggers and missing signals alongside earlier interruption evidence.
