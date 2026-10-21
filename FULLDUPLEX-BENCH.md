# The full-duplex benchmark

`golivebench` (BENCHMARK.md) asks one question: how long after the caller
stops does the first audio come. That is the project's headline and it stays
the headline. But a full-duplex service is also judged on everything else a
caller does with their voice: murmuring agreement over an answer, hesitating,
cutting in with a question, telling someone else to wait, pausing to think
mid-sentence. `golivefdb` is a caller who does those things, scored the way
the two public benchmarks score them:

- **τ-voice** (Sierra, arXiv 2603.13686; `sierra-research/tau2-bench`,
  `docs/interaction-metrics.md`): task completion (pass^k) plus a
  voice-interaction panel — responsiveness, latency, interrupt rate,
  selectivity — computed offline from who was speaking when.
- **Full-Duplex-Bench** (arXiv 2503.04721): pause handling, backchannel,
  smooth turn-taking and user interruption, scored by takeover rate (TOR) and
  latency.

The panel below uses τ-voice's names and detection windows, with FDB's names
given beside them where the two agree (τ-voice's own doc maps them: FDB
takeover rate ↔ yield rate, FDB stop/response latency ↔ L_Y / L_R, FDB
backchannel handling ↔ S_BC). FDB's backchannel-timing JSD against a human
corpus is not portable to a scripted caller and is not reported. FDB's
pause-handling TOR, which τ-voice leaves out, is reported, because golive has
a setting for exactly that (`duplex.patient_silence_ms`).

```bash
make run-fdb-sim      # golive on configs/fdb-sim.json + configs/fdb-sim.env
make bench-fdb        # the suite, 2 sessions × 6 episodes per scenario
bin/golivefdb -list   # the scenarios
```

## The panel

| metric | direction | what it is |
| --- | --- | --- |
| R_R response rate | ↑ | user turns that got an answer before the caller had to speak again (FDB turn-taking TOR) |
| L_R response latency | ↓ | caller's last voiced frame → first output audio (FDB turn-taking latency) |
| R_Y yield rate | ↑ | real interruptions where the agent stopped within 2.0 s (FDB interruption TOR) |
| L_Y yield latency | ↓ | interruption's first voiced frame → the answer cut (FDB interruption latency) |
| I_A agent interruption rate | ↓ | agent segments begun while the caller was speaking, per user turn |
| S_BC | ↑ | murmurs (嗯 / 对对 / 好) over the answer the agent talked through (stopping within 1.0 s is the error) |
| S_VT | ↑ | hesitations (呃) ignored: not yielded to within 1.0 s over the answer, not answered within 2.0 s into silence |
| S_ND | ↑ | speech to someone else (等一下我接个电话 / 先别说) ignored, same windows |
| pause held | ↑ | an unfinished opening (我想问一下), a pause, the rest: the agent did not take the turn in the pause (1 − FDB pause TOR) |
| last sound → first audio | ↓ | the project's headline, p50 / p95, on answered plain turns |
| tool task pass^k | ↑ | a lookup that needs a tool call: the call came with the request and its result was read out once; τ-bench's pass^k over the attempts |

τ-voice hides any rate backed by fewer than 10 events; the report prints the
count beside every rate so the reader can apply the same rule.

## The scenarios

Each scenario is one caller behaviour, played for `-episodes` rounds in one
session, on `-sessions` fresh connections. Between rounds the caller waits
1.0 s after the answer ends (τ-voice's wait-to-respond). Over-the-agent events
are said 1.5 s into the answer, which lands inside the mock backend's first
sentence.

| scenario | the caller | scores |
| --- | --- | --- |
| turns | asks, waits for the answer, asks again | R_R, L_R, headline latency |
| interrupt | asks, then 1.5 s into the answer asks a follow-up | R_Y, L_Y, the answer to the interruption |
| backchannel | asks, murmurs 嗯 / 对对 / 好 over the answer | S_BC |
| vocal-tic | asks, says 呃 over the answer; later says 呃 alone into silence | S_VT (over and silent) |
| non-directed | asks, says 等一下我接个电话 over the answer; later alone into silence | S_ND (over and silent) |
| pause @ N ms | says 我想问一下, pauses N ms, says 车险一年多少钱 | pause held, I_A, L_R on the joined turn |
| rapid | asks twice 300 ms apart | the second supersedes the first (R_R counts the first as unanswered, as τ-voice would) |
| tool | asks for a lookup; the application answers the tool call after 600 ms | pass^k |
| tool-interrupt | the same, with a follow-up said during the preface | pass^k under interruption |

## How the caller speaks to the mocks

The mock recognizer used to hear one fixed phrase whatever the audio was,
which is fine for a latency bench and useless here: a murmur and a question
have to arrive as different words, through the audio, with nothing passed on
the side. So the caller speaks in a key. Each phrase in its vocabulary
(`cmd/golivefdb/phrases.go`) is a pitch, 220 Hz + 40 Hz × its index, and the
mock recognizer (`GOLIVE_MOCK_PHRASES`, `configs/fdb-sim.env`) reads the
pitch of the first 100 ms of voiced audio and hears that phrase, one
character per 180 ms of voice, with the same handshake, lag and final delay
as before. The engine sees only audio and words; which path a turn takes —
barge-in margin, the floor hold, the phrase list, hesitation handling,
semantic endpointing — is decided exactly as it is on a call.

The tones clear the detector's bar with the telephony profile's margins
(-14 dBFS RMS against a -34 dBFS bar over the assistant) and sit at a
zero-crossing rate far under `vad.max_zcr`. `configs/fdb-sim.json` is the
telephony profile's turn-taking settings (early final, patient silence,
murmur list, hesitation handling, barge-in margins, echo tail) with the
mock providers and no speaker-focus channel.

## What the harness measures, and from where

- Both parties' speech is on the harness's own clock: the caller's from the
  frames it sends, the agent's from a model of a client's player (each chunk
  queued behind the one before; `golive.output_audio.truncated` clears it).
  That is how τ-voice's adapter sees an agent too. A real client plays at
  most `playback_lead_ms` (300) behind the arrival of a chunk.
- The harness's last sound → first audio agrees with the engine's
  `first_audio_out_ms + end_silence_ms` to within a frame (checked on
  `fdb-sim.json` with `early_final_ms` on and off), the same cross-check
  BENCHMARK.md makes for `golivebench`.
- τ-voice works on 200 ms ticks; this works on instants, so no window is
  rounded to a tick.

## Against the real providers

The same harness runs against the telephony build on the box with
`-url ws://127.0.0.1:8765/v1/live`, but the mock-keyed tones mean nothing to
Tencent's recognizer. For that, `-speak` (not built yet) would have the
service say each phrase in its own voice and play those clips as the caller,
as `golivebench -speak` does. Until then the panel on real providers comes
from the simulated customer (Codex's receiver, TELEPHONY.md), whose reports
carry the same events, and from real calls in the journal, where every
interruption, murmur and truncation is in the log.

Running τ-voice itself against golive is possible in principle: its OpenAI
Realtime adapter takes a custom endpoint for models named `pine-*`
(`PINE_REALTIME_BASE_URL`, `PINE_API_KEY`), the repository has `zh` language
packs, and golive speaks that protocol on `/v1/realtime`. Its `openai_live`
adapter (the gpt-live-1 protocol) is WebRTC, which golive answers with 501.
What it would take is a user-simulator LLM key, ElevenLabs for the caller's
voice, and a Chinese task domain whose end state the lead service can
verify; see the project doc on the benchmark for the plan.

## Reading a result from the mocks

A rate is the engine's: whether a murmur is talked through, whether a
hesitation cuts the answer, whether a 900 ms pause is answered into. Those
hold on real providers, where the recognizer's words arrive later and less
reliably (so the hold's 600 ms cap matters more). A latency is the injected
stage latencies in `configs/sim.env` plus the engine's own waiting, and means
nothing outside this harness except as a comparison between two builds.

## The standing evaluation: `make eval` and the Benchmarks tab

Each benchmark run can leave a record behind, so a build's figures sit beside
the last build's instead of in a terminal that scrolled away:

```bash
make eval              # golive on configs/fdb-sim.json, then golivefdb (L2) and golivebench (L3) on it
bin/golivefdb  … -record journal/benchmarks -source box    # any run, recorded
bin/golivebench … -record journal/benchmarks -source box
```

A record is `journal/benchmarks/<date>-<build>-<layer>.json` (package
`internal/benchrec`): the layer, the build (`golive`'s `/healthz` version;
`-build` overrides it), where it ran (`mocks`, `box`), the metrics, their
counts, and the bars each is held to. The layers follow the project doc:

| | What | Bars |
| --- | --- | --- |
| L1 | the turn decision (TurnBench) | EOT/INT FPR ≤ 0.15 |
| L2 | duplex behaviour (`golivefdb`) | S_BC, S_VT, R_R, R_Y ≥ 0.9; p95 ≤ 1200 ms |
| L3 | latency (`golivebench`) | p95 ≤ 1200 ms |
| L4 | task and conversation (τ-voice, the simulated customer) | — |

The journal's **Benchmarks** tab (`/api/benchmarks`) lists the records per
layer, newest first, a red figure where a bar is missed, and a trend line per
metric across builds. Records are committed with the code, so a PR carries
its own scorecard; the mocks' latencies compare builds and say nothing about
the phone. L1 waits on the TurnBench adapter (`cmd/goliveturn`), L4 on
τ-voice; neither has a record yet.
