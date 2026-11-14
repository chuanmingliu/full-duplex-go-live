# Contextual Talker openers on the Active Call engine

The Codex branch now has an opt-in Talker path in GoLive's normal duplex
engine, including the realtime endpoint used by Active Call. The saved
telephony profile, caller prompt, greeting, voice, interruption thresholds,
quotation delay and Aria route are not changed. The live phone engine remains
`f1fd9a6`; a Git push alone does not enable this feature there.

## Behavior

1. After the existing caller/backchannel gates, an ASR hypothesis starts
   optional preparation concurrently with listening and the normal answer.
2. The Mac `local-talker` model receives only the current hypothesis, at most
   256 characters. It generates a short contextual topic opener through
   llama-server's `/completion` endpoint. A constrained grammar permits a
   short Chinese topic plus `呢，` or `嘛，`. The topic must occur literally
   in the caller's words; numeric and selected non-neutral terms are rejected.
   This is conservative phrase validation, not general semantic verification.
3. Existing cached audio may be reused. Otherwise an optional synthesis uses
   the original voice, language, speed and provider's background allowance.
   Foreground synthesis has priority. Unsupported/busy TTS falls back.
4. At the **existing** opener playback boundary, take the generated phrase
   only if its complete audio is ready, still matches the utterance/revision,
   differs from the immediately preceding opener, and fits the 540 ms audio limit after silence
   trimming. A longer generated word is rejected, not cut mid-word.
5. Otherwise play the original scenario/generic preset immediately. There
   is no waiting for Talker, no late replacement of an answer already playing,
   and no additional opening on a guarded continuation fragment.

Transcript corrections cancel outdated work. Attempts are limited to one
current candidate, at least 250 ms between starts, eight per utterance and
64 per session. Preparation expires after 800 ms by default. Call shutdown
cancels and joins the workers. Optional synthesis also requires a valid call
ID and shares the existing bounded per-call admission with quote warming;
a reconnect does not obtain a fresh background-audio budget. Talker-only profiles
can claim admission even when quotation warming is disabled. Generated PCM stays in bounded call-owned
memory and is not inserted into the shared phrase cache.

The main answer is asked to stand on its own when an optional opener is
pending, so it does not depend on a specific preset which might be replaced.
Playback still uses the original generation, truncation, transcript and
first-content accounting. Openers do not become meaningful answer content
for latency scoring. Greeting and quotation-result delivery are separate.

The existing model was trained for short first clauses after an opener,
not solely for topic openers. Direct probes produced unrelated clauses;
rejected/late output is therefore an expected fallback case. Service health
alone does not establish that generated openers were used.

## Enablement and rollback

Make a private copy of the original profile and add only these duplex fields:

```json
{
  "talker_opener_url": "http://192.168.71.101:18081",
  "talker_opener_timeout_ms": 800
}
```

Do not replace the entire `duplex` object with this fragment. The URL is an
operator configuration, not a client-controlled endpoint. Credentials in URLs,
query parameters, redirects and environment proxies are not supported. Use
the trusted LAN service. No phone-provider credentials are sent to Talker.
An empty/omitted URL preserves the original preset-only behavior. Accepted
timeout range is 0–1500 ms; zero means 800 ms. This is a background expiry,
**not** additional time allowed on the caller's response path.

A production enablement requires the ordinary idle-only deployment and a
verified warm preset cache. Preserve the original profile and Aria restore
anchor. Disabling the URL restores preset-only operation on the next service
launch; no changes to Active Call routing are necessary.

Private service logs include `opener: selection` with turn, revision,
`source=talker|preset`, reason, model time and total preparation time. The
existing opener enqueue log additionally records the selected source. Match
that selection to the canonical first-output transcript before counting a
server-emitted opener; preparation or enqueueing alone does not prove output. Existing private transcript logging records the spoken phrase.

## Live-provider result and recommendation

**Keep this feature opt-in; do not enable it in production on this evidence.**
The integration can use a ready generated opener, as the controlled tests
verify, but this actual-model/provider trial emitted **zero generated openers**.
It demonstrated safe preset fallback rather than successful production-style
opener generation. No latency saving has been established.

The final isolated box comparison alternated ten sessions per arm after two
warmups each. It used five synthetic ordinary questions, the original **16 kHz**
rate, distinct call IDs, the same saved prompt/voice and configured ASR,
answer-model and TTS providers. Both endpoints used the realtime protocol
consumed by Active Call, with `hold=0` solely for already-answered lab sessions.
The Active Call SIP/PSTN transport and handset were not part of this trial.

| Metric | Preset baseline | Talker enabled |
| --- | ---: | ---: |
| Measured normal responses | 10 | 10 |
| Session errors / missing content evidence | 0 / 0 | 0 / 0 |
| Client first-audio p95 | 381 ms | 381 ms |
| Server first-content p50 | 1,016 ms | 1,033 ms |
| Server first-content p95 / maximum | 1,224 ms | 1,344 ms |
| Content at or above 1,200 ms | 1 | 2 |
| Canonically verified preset openers emitted | 10 | 10 |
| Canonically verified generated openers emitted | 0 | 0 |

With ten samples, nearest-rank p95 is the maximum. The +120 ms tail difference
is not proof of causation, but it is certainly not evidence of improvement;
both arms miss the 1,200 ms content gate in this sample. First audio includes
the opener and must not be substituted for answer-content latency.

The Talker arm made 20 preparation attempts: 18 were rejected by grounded
phrase validation and two failed optional TTS admission/preparation. Model
p95 was 145 ms; total preparation p95 was 157 ms. These are attempt counts,
not 20 successfully generated responses. A selection log exists for only
eight of the ten turns; all ten preset outputs were independently verified
against the canonical output transcript. The current small model was trained
for a different first-clause task and often proposes an unrelated topic;
the validator correctly refuses those phrases. Model suitability and optional
synthesis availability need improvement before live enablement is justified.

There were no quotation tool/result samples in this ordinary-response set.
Their configured delay is unchanged, and these measurements say nothing about
quote completion. This set also contains no intentional interruption trials;
its interrupted-answer counters must not be used as interruption latency.
Use the dedicated controls below for that comparison.

The runner verified unchanged production executable, configuration, saved
call context and Aria binding, then stopped only its two temporary processes.
The original configuration SHA-256 remains
`072ab8dbda962eff682fa56fcd049f70cd3717530e64a06bec97e66618878e7d`.
Sanitized evidence is in [live-provider-comparison.json](evidence/2026-11-01-talker-openers/live-provider-comparison.json).
Raw text and audio remain private on the box.

An excluded diagnostic trial used 8 kHz and omitted call IDs. Its startup
cache did not match that rate, and anonymous sessions could not claim optional
background synthesis. Its timings are retained as explicitly invalid for the
original-profile comparison. An earlier trial also omitted the opening-hold
release. Neither is used in the table above. The corrected harness adds
explicit protocol selection and `-call-ids`; the copied profile stays intact.

## Controlled simulation

Twenty alternating samples per mode used the actual engine/VAD/playback with
fixed ASR, main-LLM and speech fixtures. The Talker HTTP fixture takes 60 ms;
both opener audio choices last 300 ms. These conditions isolate scheduling
and fallback overhead; they do not predict real speech-provider performance.

| Server-output metric | Presets | Talker |
| --- | ---: | ---: |
| Samples | 20 | 20 |
| First audio p95 | 400 ms | 400 ms |
| First content p50 | 701 ms | 701 ms |
| First content p95 | 707 ms | 705 ms |
| First content maximum | 710 ms | 706 ms |
| Generated openers used | 0 | 20 |

The 2 ms p95 difference is effectively unchanged, not evidence of a material
latency reduction. The endpoint silence is included. These are controlled
server-output measurements, not SIP or acoustic handset measurements.

Reproduce with:

```sh
GOLIVE_TALKER_SIM=1 TALKER_SIM_SAMPLES=20 \
  TALKER_SIM_OUT=/tmp/talker-opener-sim.json \
  go test ./internal/duplex -run '^TestTalkerOpenerSimulation$' -count=1 -timeout=240s
```

`TALKER_SIM_URL` optionally replaces only the opener model fixture with the
live model. ASR, main LLM and speech remain fixtures in that test. The separate
box experiment uses isolated realtime servers and real configured providers.
Quotation calculation is not included in the controlled normal-response
comparison and its configured delay is unchanged.

Repeating the same twenty alternating samples with the **actual Mac Talker**
(and still controlled ASR/main-LLM/300 ms speech fixtures) used generated
openers on 20/20 eligible responses. First-audio p95 remained 400 ms in both
modes; first-content p95 was 703 ms for presets and 704 ms for Talker (maxima
704 and 706 ms). This verifies live model integration under that fixture;
it does not include a fresh vendor synthesis or diverse conversational turns.
The input is the same synthetic coverage question repeated in new sessions.

## Interruption controls

Eight repetitions per mode used the original phone profile and the existing
paced recognition/speaker fixture. Caller onset-to-server-cut was **311 ms
median, 312 ms maximum** with both presets and Talker enabled. All 24 negative
controls per mode (eight each for background voice, echo, and acknowledgment)
kept the floor. This tests the decision gates with simulated speaker evidence;
it does not measure audible Active Call buffer clearing or handset silence.

```sh
GOLIVE_TALKER_SIM=1 go test ./internal/duplex \
  -run '^TestTalkerOpenerInterruptionSimulation$' -count=8 -timeout=120s -v
```

Focused race tests additionally cover slow/unreachable/model-error fallback,
changed hypotheses, generation/utterance isolation, final-only turns,
background synthesis, overlong audio, and existing backchannel behavior.

## Validation

The full normal suite and full race suite passed with the repository's
25-minute timeout, as did final focused Talker race tests, `go vet ./...`,
format/diff checks, 62 latency-parser tests, 10 simulator-report tests and
handset-evidence regressions. The first normal run hit Go's default ten-minute
suite deadline; it was rerun with the same 25-minute timeout used by CI.

The production baseline `f1fd9a6` and the feature's parent `e89438b` have no
intervening `internal/` or `cmd/` code differences. The isolated comparison
therefore does not bundle another engine optimization into the Talker arm.

The final call-admission edge-case fix and benchmark call-ID option were
subsequently checked with focused race tests and vet; the earlier full-suite
results are not presented as a rerun of these final small changes.
