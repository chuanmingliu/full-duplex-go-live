# Real configured-handset call — 2026-10-29, d784730

Call `out-mvtm6iu4-47uf`, session `sess_0db2033303cf7da2303cbd4e`,
22:03:57–22:08:05 Asia/Shanghai. The owner ended the review wait with
**done**. Runtime `d784730`; saved prompt SHA prefix `639dcecf24dd`,
13,116 characters. The earlier simulator used a different prompt, so its
results are not a controlled latency comparison with this call.

| Measurement | Samples | p50 | p95 | Maximum |
| --- | ---: | ---: | ---: | ---: |
| Last caller sound received by golive → first player audio | 16 | 380 | 382 | 382 |
| Last caller sound received by golive → answer content output | 14 | 1196.5 | 1400 | 1400 |

All timings are milliseconds, using nearest-rank p95. First audio includes
cached fillers. Seven content observations reached or exceeded 1200 ms.
The strict gate fails: there are fewer than 20 valid samples, two
speech-origin rows lack content, and content p95 exceeds the target.
There are 20 metric rows: 14 answered, four non-speech and two without
content. Four answered turns were interrupted. No duplicate, malformed or
unverified rows were found.

The existing journal headline uses its older turn grouping (17 turns,
719 ms maximum first audio), whereas this table uses the strict reporting
script and explicit speech/output flags. The filler-panel change does not
replace that headline or make it a content-latency acceptance gate.

## Measurement boundary and recordings

These are server-output timings during a real call, not physical handset
end-to-end measurements. An echo probe of this exact session found no
reliable echo: strongest significance 2.4 against the required 6, and zero
significant stretches against the required four. The phone leg remains
unmeasured; the below-1200 ms handset target is not established.

The permanent archive contains 26 caller and 21 assistant transcript
entries and 247.6 seconds of stereo audio with signal on both channels.
All five required files passed size and SHA256 checks. Archive-wide
verification checked 652 files without mismatches. Input is on the
recording clock; 500 ms of dropped input was reported and was not
subtracted from latency. Private audio, transcript and saved instructions
remain outside GitHub.

## Findings and fixes

- **ISS-064 — ready quotation held by the acknowledgement timer.** Two
  quotation tasks completed in 0–2 ms but waited roughly 400 ms before
  the acknowledgement was skipped. The result then started a separate
  turn. A ready result now releases that wait once the original calling
  clause has yielded the floor. A result that arrives while that clause
  is playing still waits. Three deterministic regressions failed before
  the change and pass afterward, including race checks. Delivery logs
  also identify the result turn, completing the origin-turn/task/result
  linkage. No live latency improvement is claimed before a new call.
- **ISS-065 — cached quotation lost request-specific exclusions.** The
  cache key describes the effective selected coverage. Intake exclusions
  from a later request could disappear on a cache hit; exclusions from
  the first request could also leak into later answers. Cache entries now
  contain pricing exclusions only, with each request's intake exclusions
  added to a copied result. Rules/API and slice-isolation regressions
  pass, as does the CRM race suite. An additional-rider promise followed
  by an unchanged quote was observed in this call, but the original
  tool arguments were trimmed from history, so exact attribution to this
  defect is unproven.
- **Remaining content delay.** The slowest answer comprises 380 ms of
  turn-ending pause, 776 ms to the first model text delivered by the hedge
  after closure, and 244 ms from that text to content output. This text
  timestamp is after the four-character hedge gate, not raw provider TTFT.
  The slowest model requests
  already had 97–98% cached tokens and all eight lanes running in
  parallel. No evidence supports increasing lane count or enabling
  per-utterance warming. The saved prompt is replayed verbatim and
  contains historical template syntax; no safe prompt reduction or CRM
  renderer defect was established.

The two content-missing rows were tool handoffs, not proof of lost
answers: their result turns have no independent speech origin and are
excluded from the speech-response gate. Future origin/result linkage
must be analyzed without counting one caller question twice or treating
missing content as zero latency. The gate has not been relaxed.

## Talker and fillers

This call used cached scenario fillers. No Talker-generated live-call
clause was recorded. A separate synthetic probe from the box after the
call confirmed the CPU Talker service was healthy: first token 121 ms,
first clause 148 ms, total 156 ms. This is a playground-generation
measurement, not phone audio latency or evidence that Talker ran in the
call. Per-call filler evidence and this distinction belong in the call
journal.

## Deployment and next verification

Runtime `1869e549150c503bcb6ab685b50875a8bcbc453f` passed quality checks
and the full normal/race suites in GitHub Actions run `40894903434`.
The first CI attempt found a scheduling-dependent MiniMax test fixture:
its fake could finish synthesis before the test cancelled it. The test
now holds the first response until cancellation and checks connection
reuse; 100 focused race repetitions and the MiniMax race package passed
before the successful full rerun. Production code is unchanged from
`6307ad6` by that test-only correction.

The box successfully deployed the engine, CRM, journal and launcher at
23:08:36 Asia/Shanghai. All three running executable hashes were verified,
along with their arguments/environment, recording, 720-hour retention,
journal archive configuration and completed phrase/scenario warming.
The desk and simulator processes were preserved. Rollback executables and
the deployment manifest are retained in
`~/golive-releases/codex-1869e54-20261029-230704`.

The first deployment attempt rolled back after a listener lacked a visible
process owner during CRM startup. The old services were independently
verified healthy, with their original executable hashes and warmed audio
caches. The retry waits up to three seconds for ownership to become
visible, still rejects unresolved ownership, and records failure phases.
Recovery does not depend on successful diagnostic writes. This changes
deployment checks only; it does not relax idle or process-identity guards.

Live journal verification found 21 cached fillers, two converted to
backchannels and eight matched continuations. Recording seek/play/pause
worked; the Talker status probe succeeded; all five required archive files
still matched their manifests. These checks establish deployment and
recording integrity, not a latency improvement on a new call.

Repeat the real call with the same stored prompt and wait for **done**
before its deep review. Measure content and first audio separately;
physical handset latency remains unverified until the phone leg is
observed. The reported measurements above still describe runtime d784730.
