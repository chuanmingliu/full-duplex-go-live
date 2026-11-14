# Chinese backchannel tests on ThinkPad, 2026-11-02

Chinese is an authorized test language. The detector accepts `language: "zh"`;
the English-only upstream label does not block inference. We tested the deployed
model directly with the original GoLive Chinese voice, G.711 audio and existing
Active Call recordings. The original backchannel controller remains the default.
No live model control hook was enabled and no real recipient was dialed.

## What ran

- Model revision `4896f4774cdf1a9c5b581b0cd4a0c4000e8bb854`, existing CPU service
  at ThinkPad loopback `127.0.0.1:18087`.
- Twelve public synthetic sentences: five intended acknowledgments, seven
  intended interruptions including acknowledgment followed by a question/change.
- Each sentence tested as 16 kHz PCM and as 8 kHz G.711 A-law decoded back to
  16 kHz, with Python `audioop.ratecv` for resampling. This is codec stress,
  not reproduction of the complete carrier DSP/resampling path.
- Three controls: silence, reduced-volume same-voice speech representing an
  echo-like residual, and quiet bystander-like speech. These are constructed
  negative scenarios, not real AEC or spatial speaker-separation validation.
- 27 cases streamed in real time, 40 ms per chunk. Bot state was controlled as
  continuous speaking, with caller speech beginning after two seconds.
- Two complete private stereo recordings replayed unpaced, with model state
  reset per call and bot state approximated from recorded output energy.

Synthetic speech came from an isolated copy of the actual `f1fd9a6` engine,
using the original profile and effective saved voice. Each sentence was
synthesized separately and checked before copying into the indexed fixture set;
a failed synthesis cannot silently shift labels. The original production process
was not restarted. Both original runtime/context hashes and Aria binding matched
afterward. The isolated process was stopped. The test ran 6,946 model chunks
without service errors and ended with zero active detector sessions.

## Synthetic Chinese results

The diagnostic threshold was fixed at `p_floor >= 0.7` before running. A second
diagnostic requires three consecutive 20 ms frames. Both frames become available
only after their 40 ms input chunk; the measurement never credits the first
frame with a prediction before that chunk arrives. Speech onset is the first
20 ms fixture frame with RMS at least 300, not independently annotated human
onset. Inference/round-trip time is reported separately from audio-clock delay.

| Observation | PCM | G.711 A-law round trip |
| --- | ---: | ---: |
| Intended interruptions crossing floor threshold within 700 ms | 7 / 7 | 7 / 7 |
| First threshold crossing: min / median / max | 20 / 100 / 700 ms | 60 / 100 / 700 ms |
| Three-frame crossing: min / median / max | 60 / 140 / 740 ms | 100 / 140 / 740 ms |
| Intended acknowledgments crossing floor threshold | 3 / 5 | 3 / 5 |
| Intended acknowledgments reaching backchannel probability 0.7 during speech | 0 / 5 | 0 / 5 |

The model avoided a floor crossing for “嗯嗯” and “对对”. It crossed for
“好的”, “明白了” and “你继续说” despite the intended listening context. These
scripted labels are not an independently human-labeled prosody dataset. The
original configured `PhraseSet`, given the final scripted text directly, also
matches only the first two and treats the other three as non-acknowledgments.
That is a phrase-only comparison, not a whole-engine/ASR latency comparison.
Neither path establishes improved handling of those three cases in this test.

The silence control produced no floor crossing. Both reduced-volume speech
controls did cross the floor threshold (800 ms after the inserted clip starts).
The model cannot replace speaker identity, echo or background-voice rejection.
Three-frame persistence did not eliminate these control crossings or the three
intended-acknowledgment crossings. No threshold was tuned on this small dataset.

Per-case local service round-trip p95 ranged from **2.251 to 2.554 ms** for
paced synthetic tests. This is service overhead, not conversational response
latency. The run reproduces neither the upstream VAD/backstop implementation
nor its published evaluation protocol; it evaluates raw audio-model signals.

## Recorded Active Call comparison

The completed call `out-mvykfqfn-wibn` supplied 120.62 seconds of Chinese audio.
Its two existing cuts followed VAD-open by 112 ms and 358 ms. The replay had a
floor-threshold crossing in each corresponding recording-clock window:

| Existing event | Original VAD-open → cut | Replay crossing after VAD-open | Difference to existing cut |
| --- | ---: | ---: | ---: |
| Real-call interruption 1 | 112 ms | 11 ms | 101 ms earlier |
| Real-call interruption 2 | 358 ms | 7 ms | 351 ms earlier |
| Simulator interruption 1 | 150 ms | 39 ms | 111 ms earlier |
| Simulator interruption 2 | 175 ms | No crossing before cut | Next crossing 162 ms after cut |

These are **potentially useful earlier signals, not demonstrated latency savings**.
The model had already heard the preceding audio when the VAD opened. Its 7/11 ms
numbers are therefore not detection times from speech onset. No live model
result reached the engine; speaker/echo checks, transport and playback were not
changed. Recording-clock alignment is approximate: input is jitter-adjusted,
and the simulator recording reports 1,140 ms of dropped repeated input. The
real recording reports zero dropped input. Bot state comes from audio energy,
not exact live playback/filler events. The small sample is not a p95 benchmark.

The real call also contained one acknowledgment that the existing controller
kept. Between its last VAD-open and the keep event, replay peak backchannel
probability was **0.735**, peak floor probability **0.429**: agreement with
keeping the floor on that example. This is one controller-labeled event,
not independently assessed population accuracy.

Recorded replay round-trip p95 was 1.811 ms for the real call and 1.643 ms for
the simulator call. No new normal-response or quotation latency was measured;
quotation calculation remains separate and its configured delay is unchanged.
There is no handset acoustic claim and no added 50 ms assumption in these results.

## Decision and next integration boundary

Chinese is worth testing further based on the actual recorded examples; it is
not excluded by the upstream language label. Keep the current controller as
default/fallback. A future separately enabled assist trial should consume only
fresh per-call predictions, preserve all current caller/echo/background checks,
and fall back without waiting when the model is unavailable or ambiguous.
The mixed synthetic/control results do not justify a raw probability threshold
that directly cuts assistant playback or suppresses caller turns.

The detector remains deployed and ready; the evaluation code and private clips
remain on ThinkPad for repeatable tests. This change adds a test harness and
evidence, not a live engine integration or new call route.

## Reproduction and evidence

- [Synthetic fixtures](../../services/backchannel-detector/chinese_cases.json)
  and [evaluator](../../services/backchannel-detector/chinese_eval.py).
- [Test and deployment receipt](evidence/2026-11-02-backchannel-chinese/operation.json).
- [Per-case results](evidence/2026-11-02-backchannel-chinese/results.json).
- [Recorded event correlation](evidence/2026-11-02-backchannel-chinese/correlation.json).
- [Original phrase baseline](evidence/2026-11-02-backchannel-chinese/legacy-phrases.json).
- [Exact runner operations](../../scripts/local-ops/recorded/2026-11-02-backchannel-chinese/README.md).

Eight service tests and four evaluator tests passed with the actual ONNX model
locally; the four evaluator tests also passed inside the disposable Linux
container. The original phrase-profile regression and opt-in Chinese fixture
baseline passed in Go. CI now includes the evaluator timing/codec checks.
Raw audio, customer transcripts, credentials and per-frame private traces are
not committed.
