# Preset-preserving interruption scheduling

The owner chose the original preset openers and coordinated main-LLM answer.
Talker remains disabled. This change wakes the engine when speaker evidence
arrives, while preserving all existing identity, echo, background-speech,
transcript and backchannel gates. It does not shorten endpointing holds,
change prompts/voices or alter quotation calculation and farewell timing.

## Implementation and scope

Previously, a new speaker label updated the locked focus tracker but did not
notify the engine loop. Some held/ignored utterances therefore waited for the
next 120 ms tick to recognize that the caller had joined background speech.
`focus.Run` now sends a nonblocking notification to a capacity-one channel
after updating the tracker. The engine loop reruns the existing focus/hold
checks against that evidence. Bursts coalesce; decisions stay on the engine
loop rather than running concurrently on the focus reader.

This removes avoidable scheduling delay **after sufficient evidence exists**.
It does not remove the time needed to detect speech, acquire ASR words or
establish caller identity. The previous real-call 694 ms interruption tail
included waiting for decisive ASR after an acknowledgment-compatible partial;
this change does not claim to eliminate that tail. Handset stop time also
includes transport and buffered playback outside the engine.

## Controlled before/after simulation

Twenty repetitions per arm, same `411b6d0` source baseline and original phone
timing profile; the candidate adds only the focus wakeup. The delayed-label
fixture uses the real engine loop with a fake scorer, injects no new audio
after labels arrive, and varies arrival phase relative to the 120 ms tick.
These are scheduling measurements, not a real recognizer or handset test.

| Measurement | Before | Candidate |
| --- | ---: | ---: |
| Decisive speaker-label delivery → server cut, median | 79 ms | 1 ms |
| Decisive speaker-label delivery → server cut, p95 | 109 ms | 1 ms |
| Controlled short-response first content, median | 555 ms | 554 ms |
| Controlled short-response first content, p95 | 557 ms | 557 ms |
| False cuts in 80 rejection controls per arm | 0 | 0 |

Controls include 20 each of background speech, masked echo, 399 ms of caller
evidence below the required 400 ms, and acknowledgment-compatible input.
Cut detection is sampled every millisecond, so the candidate's 1 ms result
is at the fixture's measurement resolution, not a submillisecond guarantee.
The short-response fixture uses mock ASR/LLM/TTS and disables focus only for
that isolated ASR scheduling case. Its generic mock output is not a meaningful
insurance answer. There is no measured normal-response improvement in this
fixture. An attempted extension of audio prefeeding to ordinary quiet-line
turns did not improve this fixture and was not retained.

Evidence: [summary](evidence/2026-11-01-preset-focus-wakeup/scheduling-summary.json),
[baseline rows](evidence/2026-11-01-preset-focus-wakeup/scheduling-baseline.json),
[candidate rows](evidence/2026-11-01-preset-focus-wakeup/scheduling-candidate.json).

To repeat the opt-in fixture, run `TestPresetSchedulingSimulation` in
`internal/duplex` with `GOLIVE_PRESET_SCHED_SIM=1`, `SCHED_SIM_SAMPLES=20`
and `SCHED_SIM_OUT` pointing to a private JSON destination. Run it once on
the source baseline with the fixture copied in, and once with the change.
The regular suite always includes
`TestFocusEvidenceWakesCallerAndPreservesRejection`; the repeated benchmark
is opt-in to keep normal CI bounded.

## Original-provider normal-response comparison

The isolated box comparison used the original Tencent ASR, DeepSeek LLM,
MiniMax TTS configuration, saved prompt/voice and warmed preset cache. Five
synthetic normal questions were reused, alternating the two arms after two
warmups each. Each arm contributed 20 verified first-content responses, with
no benchmark errors, missing output evidence or quotation-result turns.

| Measurement | Live-build baseline `f1fd9a6` | Candidate |
| --- | ---: | ---: |
| Client-observed first audio, p95 (includes preset) | 381 ms | 381 ms |
| Server first substantive content, median | 991 ms | 981 ms |
| Server first substantive content, p95 | 1,197 ms | 1,166 ms |
| Server first substantive content, maximum | 1,230 ms | 1,266 ms |
| Responses at/above 1,200 ms | 1 / 20 | 1 / 20 |
| Canonically verified preset opener outputs | 20 / 20 | 20 / 20 |

The observed p95 difference is 31 ms, but it is **not an established causal
normal-response saving**: providers vary, the population is small, and the
live baseline predates other already-committed Codex changes. The same-source
controlled comparison above isolates the wakeup improvement and shows no
normal-response p95 change. One candidate response still took 1,266 ms.

Both arms pass the server first-content p95 gate for this dataset. Adding the
owner's optional 50 ms telephony/handset assumption gives an **estimated**
candidate p95 of 1,216 ms, so even that estimate is not below the 1,200 ms
end-to-end target. No acoustic handset claim is made.

The benchmark closes each single-turn session after 1.5 seconds without
output or its 12-second observation deadline. The latency parser recorded
8 baseline and 9 candidate replies as truncated after first content had
already been emitted. Their first-content observations remain valid, but
these counts are **not caller-interruption tests** or proof of complete,
smooth multi-turn delivery. The numeric component appendix includes only
naturally completed responses (12 and 11); zero stage fields must not be
interpreted as zero-cost ASR/TTS or summed as independent durations.

All 40 verified openers were presets; no Talker preparations or generated
outputs occurred. Original runtime/profile/context hashes and the Aria
binding matched afterward; both owned experiment processes were stopped.
Evidence: [provider comparison](evidence/2026-11-01-preset-focus-wakeup/provider-comparison.json).

## Full Active Call baseline

The original `call sim` workflow completed on the existing `f1fd9a6` runtime:
call `out-mvxuom0e-mp6l`, scenario `set-a-01-barge-in`. Its permanent archive
verified all five required files and both audio channels. The simulator had
no receiver failures or warnings. This short scenario contains one quote
readout and **zero qualifying normal responses**; it cannot establish the
20-response normal-latency gate. The quote-only first-content figure of
721 ms is reported separately, not counted as an ordinary response or as
the duration of quote calculation.

Two matched VAD-open → server-cut events were 150 ms and 175 ms. That small
baseline sample excludes initial speech-detection time and handset playback;
there is no candidate transport sample to compare against it. See the
[cut receipt](evidence/2026-11-01-preset-focus-wakeup/activecall-baseline-cuts.json).

This is a baseline transport check, not a paired candidate Active Call call.
The candidate provider comparison uses isolated realtime loopback processes;
neither constitutes acoustic handset verification or proves the 1,200 ms
handset target. No real recipient was dialed.

Evidence: [baseline archive/latency receipt](evidence/2026-11-01-preset-focus-wakeup/activecall-baseline.json).

## Conversation behavior and operations

Preset selection, cached audio, opener-to-answer instructions and continuation
grace remain unchanged. See the [detailed opener/hangup guide](../architecture/08-openers-and-hangup.md).
The hangup rules still combine main-LLM semantic decisions, literal regex
rules and timers; this scheduling change does not modify those rules.

The production Active Call executable, original profile, saved call context
and Aria binding are preserved. The owner's `45b5564` restore anchor remains
available. The experiment does not redirect to SIP/RTP or Speko.
Historical [runner jobs and collection bodies](../../scripts/local-ops/recorded/2026-11-01-preset-focus-wakeup/README.md)
retain executable hashes and state guards. Private audio, customer text and
credentials remain on the box.

## Validation

`go test -timeout=25m ./...`, `go test -race -timeout=25m ./...` and
`go vet ./...` passed. The focused focus/background/backchannel/opener/hangup
race tests passed, as did five race-enabled repetitions of the new wakeup
regression. The full duplex package took approximately 644 seconds normally
and 647 seconds under the race detector. See the
[validation receipt](evidence/2026-11-01-preset-focus-wakeup/validation.json).
