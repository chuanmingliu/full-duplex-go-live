# Benchmarking response latency

`golivebench` drives **both** golive and an OpenAI Realtime service — such as the
Python cascade this project was modelled on — from one client, one clock, over
the same recorded audio, alternating between them run by run.

```bash
make build
./bin/golivebench \
    -a golive=ws://127.0.0.1:8080/v1/live \
    -b cascade=ws://127.0.0.1:8765/v1/realtime \
    -clips clips/*.wav -runs 12 -md bench.md -json bench.json
```

## What it measures, and why that origin

```
response latency = (first output audio byte received)
                 − (last frame of speech sent)
```

Both numbers come from the harness's own clock, so neither service can flatter
itself by choosing a friendlier origin. Deliberately included in it:

* **Each engine's end-of-turn silence threshold.** golive waits
  `vad.min_silence_ms` (380 ms by default) before deciding you have stopped. A
  caller waits through that, so it belongs in the number. If one stack wins only
  because it is more trigger-happy about ending your turn, that is a real
  trade — it also interrupts you more — and the `VAD close p50` column is there
  to show it separately.
* **Connection reuse.** The first synthesis of a session pays socket setup;
  later turns do not. That is why `-warmup` exists and why warmup runs are
  excluded from every figure.

Deliberately excluded: client-side playback and speaker latency, which are the
same for both.

## Running it honestly

**Use real recordings, not the synthetic fallback.** `-clips` takes 16-bit PCM
WAVs of you actually speaking. A recognizer's behaviour on a tone says nothing
about its behaviour on a sentence, and the built-in synthetic audio exists only
to smoke-test the harness itself.

Record a few, 2–4 seconds each, varying length:

```bash
# macOS, 24 kHz mono
sox -d -r 24000 -c 1 -b 16 clips/01.wav trim 0 4
```

**Give both stacks the same providers.** Same Tencent account, same DeepSeek
model, same MiniMax voice, same machine, same network. Anything else and you are
benchmarking the providers.

**Alternate, don't batch.** The harness already does: each run drives A then B
before moving on, so network drift and a warming provider cache hit both
equally. Running twenty of one and then twenty of the other measures the passage
of time as much as the software.

**Twelve runs minimum.** Fewer and the spread swamps the difference.

## Reading the report

```
| stack   | runs | failed | min | p50 | p95 | max | IQR | transcript p50 | VAD close p50 |
| golive  |   12 |      0 | 612 | 690 | 934 | 951 |  61 |            402 |           384 |
| cascade |   12 |      0 | 735 | 848 | 1194| 1220| 130 |            505 |           520 |
```

Medians, never means — one cold-start turn moves a mean enough to hide what
every other turn did. `IQR` is the middle-50% spread and is how you judge
whether a gap is real.

The **paired difference** section is the part to trust: it compares the same
clip at the same moment on both stacks, so drift cancels instead of adding
noise. When the median gap is smaller than the run-to-run spread, the report
says **“no measurable difference”** and declines to name a winner. That is the
correct answer in that situation, and it is the one most benchmark scripts get
wrong.

There is also a transcript check: if the two stacks heard *different words* on
the same clip, they answered different questions, and the report says so. A
latency comparison across different answers is much weaker than it looks.

## Reference points

Numbers worth knowing before you read your own, so you can tell "slow" from
"normal for this architecture":

| System | Turn-taking latency | Architecture |
| --- | --- | --- |
| Moshi | ~0.25 s | multi-stream, one model |
| PersonaPlex | <0.2 s | multi-stream, one model |
| gpt-live-1 | ~0.8 s | interleaved single sequence, delegating backend |
| a cascade | its own VAD hangover, plus ASR + LLM + TTS in series | what golive is |

The gpt-live-1 and Moshi figures come from Full-Duplex-Bench via [a third-party
analysis][dissect]; OpenAI has not published them, and the measurement method is
not the one used here, so treat them as the right order of magnitude rather than
a directly comparable number.

The useful implication is that ~0.8 s is what a purpose-built full-duplex model
costs, and that a cascade's floor is its silence threshold plus three network
round trips. If your measured p50 is far above the sum of the stage timings in
`golive.turn.metrics`, the time is going somewhere other than the providers —
look at pacing, connection reuse, and the VAD before blaming the model.

[dissect]: https://desh2608.github.io/2026-09-11-dissecting-gpt-live/

## Cross-checking the harness

The harness and the service measure from different origins on purpose, and the
gap between them should equal the VAD hangover. Against the mock providers:

```
golivebench   first audio 518 ms   (from last speech frame)
golive log    first_audio_out_ms=138  (from VAD close)
difference    380 ms = vad.min_silence_ms
```

Two independent instruments agreeing to the millisecond is the reason to trust
either. If that subtraction stops working, fix the measurement before believing
any result it produces.

## Where the time actually goes

Stage figures from a real 110-second call against Tencent + DeepSeek + MiniMax,
twelve turns, measured from VAD close:

| stage | p50 | share |
| --- | ---: | ---: |
| backend time-to-first-token | 724 ms | 47% |
| `vad.min_silence_ms` | 380 ms | 25% |
| segmentation + synthesis | 335 ms | 22% |
| ASR final | 102 ms | 7% |

Two things follow. The backend dominates, so the lever that matters is not
making the cascade faster but **starting the backend earlier** — which is what
speculation is for. And the silence threshold is the second largest term, which
is worth remembering before tuning anything downstream of it.

Those two rows are in fact one lever. Speculation fires `speculative_stable_ms`
into the end-of-turn pause, so it spends the tail of the silence threshold
running the backend instead of waiting — with the defaults, 200 ms of the 380,
plus the ~100 ms the final transcript costs. The trigger is the pause the
microphone hears, not a transcript that stopped changing: a recognizer running
behind the speaker also stops changing, and a watch keyed on that fires
mid-sentence on a prefix. When it did, nearly every speculative turn was
immediately revised — `think: speculation missed` on almost every line — which
is slower than not speculating at all, because the wasted generation still had
to be cancelled. If you see that line often, raise `speculative_stable_ms`; if
you see it almost never and TTFA is still high, lower it toward 120.

`first_segment_ms` splits the third row: the gap from `llm_first_token_ms` to it
is time spent waiting for a sentence boundary, and the gap from it to
`tts_first_audio_ms` is the synthesis provider. A slow reply and a late comma
look identical without that split, and they have opposite fixes.

## Is the synthesis provider holding your first syllable?

`ttsprobe` measures time-to-first-audio from the synthesizer alone, with the
rest of the cascade removed:

```bash
go run ./cmd/ttsprobe -compare-endpoints -runs 12
```

It exists because of a specific, checkable suspicion. MiniMax buffers text
server-side and decides for itself when to synthesize: a segment ending in
`。！？.!?` goes immediately, one ending in a comma accumulates, and one ending
in nothing waits for a length limit or a silence window. golive deliberately
cuts a six-character first segment to get a syllable out early — and six
characters usually end in nothing. So the segment that exists purely to be fast
is exactly the shape the server sits on, and `stream_first_chunk_chars` could be
buying nothing, or costing.

The probe sends the shapes a real turn produces — a bare `好的`, a holding
filler, a complete sentence, a comma-ended fragment — and reports first-audio
latency for each, alternating between configurations so warm-up and drift land
on both. Read the per-text table first: if the unpunctuated rows are much slower
than the complete sentence, the buffer is the problem and `task_flush` is the
fix. If they are the same, server-side buffering is not where your time goes,
and lowering `stream_first_chunk_chars` further is the wrong lever.

**The two endpoints are not interchangeable.** `/ws/v1/t2a_v2` and
`/ws/v1/t2a_v2_bidi` share auth, `task_start` and audio frames, but only the
bidirectional one accepts `task_flush` and `task_cancel`. On the standard
endpoint those come back as `2202 illegal event`. golive detects the mismatch
and declines to send them, so the settings are inert rather than destructive —
but inert is not the same as working, and `-compare-endpoints` is how you find
out whether switching is worth anything on your account.

## The prompt is probably your biggest number

Two logs from one machine, one model (`deepseek-chat`), one endpoint, a day
apart:

| system prompt | `history_turns` | time to first token |
| --- | ---: | ---: |
| 79 characters, generic assistant | 16 | **259–546 ms** |
| a full call-centre persona | 16 | **2441–3385 ms** |

Nothing else differed. That is five to seven times the latency, and at the top
of that range the prompt alone costs more than the silence threshold, the
recognizer and synthesis put together. Every lever elsewhere in this document is
worth tens to low hundreds of milliseconds; this one is worth seconds.

It also has a second-order cost that does not show up in any single turn's
metrics. When time-to-first-token exceeds the caller's patience, they repeat
themselves — which supersedes the answer that was about to arrive, and the next
turn starts from scratch. A real call went three questions deep with no answer
at any point. No stage was slow enough to blame on its own.

`llm: stream open` logs `prompt_chars` next to the latency it bought, because
the message count hides this: the count barely moves while the prompt behind it
grows by an order of magnitude. `history_max_chars` bounds history by size as
well as by `history_turns`, since sixteen exchanges can be four hundred
characters or four thousand.

If your `llm_first_token_ms` is in the seconds, stop tuning the cascade and
shorten the prompt.

## The service's own numbers

Every turn emits `golive.turn.metrics`, all stages measured from VAD close:

```json
{"type":"golive.turn.metrics","turn_id":"item_1","speech_end_ms":2180,
 "asr_final_ms":41,"llm_first_token_ms":516,"first_segment_ms":690,
 "tts_first_audio_ms":1180,"first_audio_out_ms":1204,
 "turn_complete_ms":4106,"output_audio_ms":2902}
```

`llm_first_token_ms` goes **negative** on a speculative turn, because the
backend started before you finished talking. That is the point of speculating,
so the sign is information and is not clamped.

At INFO level each turn logs one line, and the session logs a summary at close:

```
turn: response latency  turn=item_1 first_audio_out_ms=1204 asr_final_ms=41 …
session: response latency summary  turns=9 min_ms=780 p50_ms=910 p95_ms=1204 max_ms=1290
```
