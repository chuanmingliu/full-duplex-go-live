# The edge gateway: self-hosted speech models behind an OpenAI-compatible API

`cmd/edge` serves speech models that run on a machine of ours — the test
group beside the vendors golive is built on — through the API an OpenAI
client already speaks. A model is added to the gateway, not to every client:
golive, the probes, a `curl`, the OpenAI SDK in any language all ask for it
the same way.

What is behind it today, on the ThinkPad:

| Model id | Does | Runtime | Rate | Licence |
| --- | --- | --- | --- | --- |
| `audio8-asr` | speech → text, whole clip | Audio8-ASR-0.1B, ONNX int8 (`~/edge/asr`, port 7860) | takes 16 kHz | **CC BY-NC 4.0 — non-commercial** |
| `audio8-tts` | text → speech, streamed | Audio8 TTS 0.1B DualAR, ONNX int8 (`~/edge/Audio8_TTS/onnx_runtime_0_1b_int8`, port 8024) | makes 44.1 kHz | Apache 2.0 |
| `nemotron-diar` | who is speaking, frame by frame, streamed | Nemotron-3-Diarization (streaming Sortformer, ~100 M params) on CPU PyTorch via Transformers (`~/edge/diar`, `edge/diar_server.py`, port 8031) | takes 16 kHz | OpenMDW 1.1 |

VibeVoice-ASR-BitNet (`microsoft/VibeASR.cpp`, MIT) is cloned at
`~/edge/vibeasr` and not yet built: the box has no `cmake`. It is a
whole-clip recognizer too (RTF 0.3–0.5 on eight threads in its own figures),
and would be served here as another transcription model.

The gateway listens on `127.0.0.1:8030` and the LAN address; the model
services themselves stay on loopback. `scripts/phone-brain.sh edge start`
runs it.

## Design

**OpenAI's shapes wherever OpenAI has one.** `POST /v1/audio/transcriptions`
takes the multipart form the OpenAI SDK sends (`file`, `model`, `language`,
`prompt`, `response_format`, `stream`) and answers with the same objects;
`POST /v1/audio/speech` takes `model`, `input`, `voice`, `response_format`,
`speed` and answers with audio bytes; `GET /v1/models` lists models as
OpenAI does. Errors are OpenAI's error object with its `type`, `param` and
`code`. So `openai.OpenAI(base_url="http://192.168.71.9:8030/v1", api_key=...)`
works unchanged.

**Where the API says nothing, the answer is in a field or header that starts
with `edge` / `X-Edge-`,** never in a field that means something else to
OpenAI. The sample rate a stream is in, how long the model took, how long
the request queued, which runtime a model is — all `X-Edge-*` headers or an
`edge` object inside a response. A client that ignores them sees plain
OpenAI.

**One request at a time per model.** These models are a CPU each; two
syntheses at once would both be late. The gateway holds one slot per model;
a second request waits up to `-queue-wait` (10 s) and is then refused with
`429 rate_limit_exceeded` and `Retry-After: 2`, so that a burst turns into a
refusal the caller sees rather than a queue nobody can see the end of.

**Audio is normalized at the gateway.** A clip arrives as WAV (any rate,
8/16/24/32-bit PCM or float, mono or stereo) or raw PCM16 with its rate
stated; the gateway decodes it, mixes to mono, resamples to the model's rate
and hands the model exactly what it takes. Synthesized audio is resampled to
the rate the caller asked for (`sample_rate` in the request) before it leaves,
so golive can ask for 16 kHz and the phone path for 8 kHz without either
knowing what the model makes.

**Speech is streamed as it is made.** `/v1/audio/speech` writes the first
bytes when the model produces its first chunk (a WAV header with unknown
length, then PCM; or bare PCM). The time to that first chunk is in
`X-Edge-First-Audio-Ms`. A client that closes the connection cancels the
synthesis, and the runtime is told to stop, since it would otherwise keep the
CPU for the rest of the sentence.

**A token, kept out of the repository.** Every request but `/healthz` carries
`Authorization: Bearer <token>`; the token is made on first start and kept in
`~/golive-edge/api.token` (mode 600). `-no-key` serves without one, for a
listener only the LAN can reach.

**Honest about what a model is.** `/v1/models` says for each model whether
its service is up right now, whether it streams, what rate it takes or makes,
its licence, and a note on what to expect (whole-clip recognition, no
partials). The licence is there because a model served here may be licensed
for less than the vendors are: Audio8-ASR is **non-commercial**.

## Endpoints

### `GET /healthz`

No token. `200 {"ok":true,"models":{"audio8-asr":"ok","audio8-tts":"ok"},"uptime_s":…}`,
or `503` with the failing model's error in place of `"ok"`.

### `GET /v1/models`, `GET /v1/models/{id}`

```json
{"object":"list","data":[
  {"id":"audio8-asr","object":"model","created":1760436000,"owned_by":"edge",
   "edge":{"kind":"transcription","family":"audio8-asr","status":"ok","streaming":false,
           "sample_rate":16000,"licence":"CC BY-NC 4.0 (non-commercial)",
           "note":"whole-clip recognition: …","upstream":"http://127.0.0.1:7860"}},
  {"id":"audio8-tts","object":"model","created":1760436000,"owned_by":"edge",
   "edge":{"kind":"speech","family":"audio8-tts","status":"ok","streaming":true,
           "sample_rate":44100,"licence":"Apache 2.0","note":"…","upstream":"http://127.0.0.1:8024"}}]}
```

`status` is `ok` or `unavailable` (with `error`), asked of the model's
service on each call.

### `POST /v1/audio/transcriptions`

The audio, one of:

- `multipart/form-data` with a `file` part (what the OpenAI SDK sends) and
  the fields below as form fields;
- a raw body of `audio/wav`, or `audio/pcm;rate=16000` (16-bit mono
  little-endian; the rate may also come as `X-Edge-Sample-Rate` or
  `?sample_rate=`), with the fields as query parameters.

| Field | | |
| --- | --- | --- |
| `model` | required | a transcription model from `/v1/models` |
| `language` | optional | ISO code, `zh`; passed to the model |
| `prompt` | optional | OpenAI's "words to expect"; a model that takes hot words gets them from it, one per whitespace-separated token |
| `response_format` | `json` (default), `text`, `verbose_json` | |
| `stream` | `true` for OpenAI's `transcript.text.delta` / `transcript.text.done` events | the models here answer whole, so the delta is the whole text |

A clip is at most a minute; the model's own limit (about 30 s for
Audio8-ASR) is reported by the model if exceeded.

Responses:

- `json`: `{"text":"…"}`
- `text`: the text, one line
- `verbose_json`: `{"task":"transcribe","language":"zh","duration":3.2,"text":"…","segments":[{"id":0,"start":0,"end":3.2,"text":"…"}],"edge":{"model":"audio8-asr","elapsed_ms":1190,"model_ms":1120,"queued_ms":0,"tokens":14,"audio_ms":3200}}`

Headers on every answer: `X-Edge-Elapsed-Ms` (gateway, request in to answer
out), `X-Edge-Model-Ms` (what the model reports for itself), `X-Edge-Audio-Ms`
(the clip's length), `X-Request-Id` (yours if you sent one, else made).

```bash
TOKEN=$(cat ~/golive-edge/api.token)
curl -H "Authorization: Bearer $TOKEN" -F model=audio8-asr -F language=zh \
     -F file=@clip.wav http://127.0.0.1:8030/v1/audio/transcriptions
```

```python
from openai import OpenAI
c = OpenAI(base_url="http://192.168.71.9:8030/v1", api_key=open("api.token").read().strip())
r = c.audio.transcriptions.create(model="audio8-asr", file=open("clip.wav", "rb"), language="zh")
print(r.text)
```

### `POST /v1/audio/speech`

JSON body:

| Field | | |
| --- | --- | --- |
| `model` | required | a speech model |
| `input` | required | up to 1000 characters |
| `voice` | optional | a voice the runtime has registered; `default` when absent |
| `response_format` | `wav` (default) or `pcm` | 16-bit mono little-endian either way |
| `speed` | optional | passed to models that take it |
| `sample_rate` | ours | the rate the audio is wanted at, 8000–48000; the model's own when absent |
| `stream_format` | `audio` (the only value) | the bytes as they are made, which is what happens regardless |

The response is `audio/wav` or `audio/pcm;rate=N`, streamed: the first bytes
leave when the model has made its first chunk. A WAV stream carries a header
with the length fields at their maximum, which every player reads to the end.
Headers: `X-Edge-Sample-Rate` (what you get), `X-Edge-Model-Sample-Rate`
(what the model made), `X-Edge-First-Audio-Ms` (request in to the first
chunk out).

```bash
curl -H "Authorization: Bearer $TOKEN" -o out.wav \
     -d '{"model":"audio8-tts","input":"您好，我是车险管家小翠儿。","sample_rate":16000}' \
     http://127.0.0.1:8030/v1/audio/speech
```

```python
with c.audio.speech.with_streaming_response.create(model="audio8-tts", voice="default",
        input="您好，我是车险管家小翠儿。", response_format="pcm") as r:
    for chunk in r.iter_bytes(3200):   # 100 ms of 16-bit mono at 16 kHz … at the model's rate
        play(chunk)
```

### `GET /v1/audio/diarize?model=nemotron-diar` (WebSocket)

Not an OpenAI shape — OpenAI has no streaming diarization — so it is a
WebSocket of our own, documented in `internal/focus/wire.go`. The client
sends binary messages of 16 kHz PCM16 mono, each prefixed by an 8-byte
little-endian session time in ms of its first sample. The server sends JSON:

```json
{"type":"ready","lag_ms":345,"frame_ms":80,"model":"nemotron-3-diarization","mode":"ultra_low_latency"}
{"type":"frames","start_ms":1200,"frame_ms":80,"p":[[0.93,0.02],[0.91,0.01]],"compute_ms":41}
```

`p` is one row per 80 ms frame from `start_ms`, one column per speaker in
the order the model first heard them; the values are activity probabilities.
`lag_ms` is the model's input buffer: a frame's label arrives that long after
its audio, plus compute. One session at a time per model (the second gets
429), the token as everywhere else. golive's `internal/focus` is the client;
`duplex.focus` in a profile turns it on.

### Errors

OpenAI's object, with the status it implies:

```json
{"error":{"message":"there is no model \"whisper-1\" here; see /v1/models","type":"invalid_request_error","param":"model","code":"model_not_found"}}
```

| Status | `type` | `code` | When |
| --- | --- | --- | --- |
| 400 | `invalid_request_error` | | a field is missing or wrong (`param` names it); the model's service refused the request as malformed |
| 401 | `authentication_error` | `invalid_api_key` | no or wrong token |
| 404 | `invalid_request_error` | `model_not_found` | no such model |
| 429 | `rate_limit_error` | `rate_limit_exceeded` | the model has a request already and this one waited its turn out; `Retry-After: 2` |
| 499 | `server_error` | | the client went away |
| 502 | `server_error` | `model_unavailable` | the model's service is not answering |
| 500 | `server_error` | | anything else; the message says what |

## Running it

```bash
scripts/phone-brain.sh edge start      # the two runtimes, then the gateway on 8030 (loopback + LAN)
scripts/phone-brain.sh edge status     # /healthz, ports, pids
scripts/phone-brain.sh edge stop
scripts/phone-brain.sh edge token      # the bearer token, to your terminal only
scripts/phone-brain.sh edge probe      # one transcription and one synthesis, timed
```

By hand: `bin/edge-linux-amd64 -addr 127.0.0.1:8030,192.168.71.9:8030
-audio8-asr http://127.0.0.1:7860 -audio8-tts http://127.0.0.1:8024`. The
runtimes are started by their own scripts (`run_local.sh`, `start_server.sh`)
inside their virtual environments under `~/edge`; see "Setting up the
runtimes" below. The gateway log is `~/golive-edge/edge.log`: one line per
request with the request id, model, audio length, queue time, model time and
first-audio time, never the text.

## What was measured

First probes on the ThinkPad (Xeon E3-1535M v6, 4 cores / 8 threads, AVX2,
no VNNI or AMX), 2026-10-14 evening, the box otherwise idle. Through the
gateway unless said; the gateway adds 1–5 ms to what the model reports.

**audio8-asr works, and is a second and a half behind the end of speech.**

| Clip | Model time | Text |
| --- | ---: | --- |
| 7.3 s, the MiniMax voice sample at 16 kHz | 1283–1290 ms (three runs, 27 tokens) | the sentence, every character right |
| 3.0 s, cut from a synthesis | 1560–1640 ms | — |

So a request costs about 1.3 s however short the clip (the prefill of the
audio encoder and the model's own warm-up), and the words only come when the
clip is over. On the call path the recognizer's final arrives ~95 ms after
the audio ends; this one would arrive ~1,300 ms after it, which is the whole
latency budget. It is a test-group recognizer for offline comparison (what
it heard against what Tencent heard, on the archive's recordings), not a
candidate for the turn. Its licence is non-commercial besides.

**audio8-tts does not work on this machine as deployed.**

| Text | First audio | Rate of making | What came out |
| --- | ---: | ---: | --- |
| 3 characters, 64 tokens allowed | 6.5 s | ~3.5 s per 0.5 s chunk (~0.15× real time) | 3.0 s of sound; the model never emitted its stop token |
| 10 characters, 120 tokens | 6.4 s | | 0.74 s; audio8-asr hears nothing in it |
| 26 characters, 400 tokens | 6.9 s | 241 s in all | 18.6 s; audio8-asr hears "《Oh!》!" |

The first 6.4 s is the same whatever the text, 5 or 8 threads alike, and the
sound is not speech. Two things differ from the runtime's own setup, either
of which may be the cause and neither verified: it pins `onnxruntime<1.24`
and runs here on 1.31 (int8 kernels and their fallbacks changed between);
and the codec decoder is fp16, which onnxruntime's CPU provider executes by
casting, slowly. Its README figures (~19 ms a slow-AR token on eight
threads) are 25–30× what this box does. The ThinkPad's CPU has no VNNI, so
int8 matrix kernels fall back to AVX2 and are not faster than fp32 here.
Next things to try, in order: a Python the pinned `onnxruntime<1.24` has a
wheel for (the TTS venv's site-packages path says `python3.12`, so one is on
the box; check which interpreter each venv runs), the fp32 codec decoder if
the model card offers one, and the runtime's own CLI with its tests
(`.venv/bin/python -m pytest -q tests`) to separate "slow" from "broken".
Until then the model is listed, honest about itself, and not used.

**nemotron-diar runs in real time on the CPU, just.** Measured with
`edge/probe_diar.py` on archived caller tracks, `ultra_low_latency` (chunk
345 ms, 4 threads): compute per chunk 127 ms at the median on a 22 s track,
but **329 ms at the median, 480 at p90, 637 at worst on a 120 s track** —
the speaker cache and FIFO fill over the first ~20 s and attention over them
is the steady-state cost. So RTF ≈ 0.95 in steady state and the labels run
345 + ~330 ≈ 700 ms behind the audio, with no headroom; 8 threads were no
faster than 4. `very_low_latency` (665 ms chunks) costs about the same per
chunk, RTF ≈ 0.5, labels ~1 s behind. The model separated a second voice
mixed into a real caller track at −8 dB (7.2 of 9.1 s of it labelled as a
second speaker) and at −3 dB (5.7 of 6.7 s); the assistant's own echo on
the caller track did not become a speaker of its own. Things to try for
headroom: a shorter speaker cache / FIFO (the config's 264 frames), the
`q8_0.gguf` the model card ships through NeMo-Speech.cpp, int8 dynamic
quantization of the encoder in PyTorch.

## Setting up the runtimes

Python on the box is 3.14 without `ensurepip`, so `python3 -m venv` makes an
environment with no `pip` and every `setup.sh` fails at that point. The
pattern that works:

```bash
python3 -m venv --without-pip <dir>/.venv
python3 -m pip --python <dir>/.venv/bin/python install -r requirements.txt
```

- Audio8 TTS pins `onnxruntime<1.24`, which has no wheel for this Python;
  `>=1.22` installs 1.31 and runs. The model files (819 MB) go in
  `onnx_runtime_0_1b_int8/model` (`hf download Edge0/audio8-TTS-0.1B-ONNX-INT8
  --local-dir model`); the default voice is registered once with
  `scripts/register_default_voice.py`; `PORT=8024 bash start_server.sh`.
- Audio8-ASR pins every package to versions without wheels for this Python;
  with the pins removed it installs (onnxruntime 1.31, numpy 2.5, transformers
  5.19) and runs. The repository is the model (2.9 GB, `hf download
  Edge0/Audio8-ASR-0.1B-onnx-runtime --local-dir asr`);
  `PYTHON=.venv/bin/python PORT=7860 bash run_local.sh`.
- Nemotron-3-Diarization: NeMo 2.5 does not import on this Python (protobuf
  and ml_dtypes conflicts), so the runtime is Transformers from source
  (`pip install git+https://github.com/huggingface/transformers`) with CPU
  PyTorch (`--index-url https://download.pytorch.org/whl/cpu torch`), in
  `~/edge/diar/.venv`; the model (678 MB) is `hf download
  nvidia/Nemotron-3-Diarization --local-dir ~/edge/diar/model`. The server is
  `edge/diar_server.py` from this repository, copied to `~/edge/diar/`;
  `phone-brain.sh edge start` runs it (`EDGE_DIAR_MODE`, `EDGE_DIAR_THREADS`).
- The `hf` CLI lives in its own environment, `~/edge/hfcli`.
