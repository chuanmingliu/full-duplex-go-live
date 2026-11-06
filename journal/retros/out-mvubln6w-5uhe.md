# Real configured-handset call — 2026-10-30, 914fa0e

Call `out-mvubln6w-5uhe`, session `sess_4cdd63c0c3a214f96a7b669e`,
09:55:33–09:57:08 Asia/Shanghai. The owner signalled **done** before this
review. Runtime `914fa0e`; saved prompt SHA prefix `639dcecf24dd`,
13,116 characters, unchanged from the preceding real baseline.

| Server-output measurement | Samples | p50 | p95 | Maximum |
| --- | ---: | ---: | ---: | ---: |
| Last caller sound → first audio, including fillers | 5 | 380 | 380 | 380 |
| Last caller sound → first non-opener content | 5 | 1033 | 1082 | 1082 |
| Last caller sound → linked quotation result content | 1 | 3548 | 3548 | 3548 |

Timings are milliseconds, nearest-rank p95. Seven metric rows contain
five caller responses and two non-speech responses (greeting and result).
No missing, duplicate or unverified rows or links were found. First
content can be an explanatory lead-in; it does not establish delivery of
the quotation. The result is measured separately, not counted as another
caller sample. The strict content/both gate **fails**: only five caller
samples, below the required twenty, and quotation-result p95 is 3548 ms.

## Ready quotation blocked behind an unheard lead-in

The explicit chain is `item_2 → t1 → item_3`. The caller turn closed at
09:55:55.435. The quotation task completed at 09:55:56.017, 120 ms before
the original turn's first content. That content was a generic explanation,
not a quotation. Its 2553 ms clause finished at 09:55:58.449; the result
was delivered about 1 ms later. Thus ISS-066's prompt floor-release
handling worked, but protecting the entire generic clause delayed a
result that had been ready before the clause began.

The measured result delay is **380 + 3015 + 153 = 3548 ms**: the caller's
pause, the monotonic origin-to-result-start offset, and the result's own
first-content offset. The task spent approximately 2432 ms waiting for
the original clause to finish. No additional scheduler-tick delay is
evident in this call.

ISS-070 introduces a narrow handoff for a successful deterministic
quotation: after its opener has actually emitted audio, an entirely
unplayed calling body may be suppressed. The opener finishes naturally.
Any committed body audio, new caller activity, changed memory or floor,
missing readout, failed result, or started acknowledgement retains the
existing protection. Normal completion records the original turn's real
opener and its metrics before delivering the result; suppressed text is
not recorded as spoken.

ISS-071 reports linked-result latency even when the origin already has
first content. The CLI, simulator retrospective and journal show this
separately. Content/both gates also check result p95, without demanding
twenty tool calls or counting results as extra caller turns. Historical
links still require explicit verified timing; wall-clock adjacency is
not substituted.

## Transcript timing and conversation quality

Two exported caller onsets made responses appear to take 6–10 seconds.
Empty recognitions had left old utterance starts attached to later
speech. ISS-072 replaces the start on each new detector opening. It also
anchors confirmed cached-opener output using the metric's readiness and
output offsets; queueing alone is not evidence of playback. Numeric
waveform windows support the later caller activity and prompt responses.
Those energy bounds are not semantic speech labels or handset timings.

The default quotation matched all five authoritative line items using
the configured whole-unit rounding. There was no duplicate readout. The
call did not exercise comparison-memory or individual-price follow-ups,
so it cannot validate ISS-067's live effect. The first recognized closing
request led to a farewell and the assistant-goodbye fallback. No model
`end_call` invocation was logged. ISS-069 remains open because the earlier
failed closing phrases were not repeated. Navigation repetition followed
an ambiguous recognition; the evidence does not establish ignored intent.

## Recording and external systems

All five required archive files passed size and SHA-256 verification:
audio, transcript, parsed call, retained session and original log. The
94.72-second WAV is stereo PCM16 at 16 kHz; both channels contain signal.
The parsed transcript has five caller and seven assistant entries. Five
cached filler events are recorded. Private audio, prompts, transcripts,
personal details and quotation values remain outside GitHub.

The input is on the recording clock with 100 ms reported dropped; this
is not subtracted from latency. Echo analysis found no usable delayed
return: strongest significance 2.8 versus required 6, with zero qualifying
windows of six eligible, below the required four. **Physical handset
playout remains unobserved.** Neither these server timings nor the archive
prove the under-1200 ms handset target.

The deployed journal is configured for 720-hour working retention,
permanent archive, target 1200 ms, and the existing Talker/playground
endpoints. Live telephone fillers use the phrase cache; Talker is the
separate playground service. The verified release is recorded below; its
real-call effect is still pending.

## Verified release

Commit `461b79f` passed quality checks and full normal/race suites in
GitHub Actions run `40994234452`. The new handoff regressions also passed
twenty repeated local race runs; all 30 latency and nine simulator-report
tests, full journal race tests, JavaScript checks and vet passed. The
additional local full duplex/journal attempt reached the default ten-minute
Go timeout; CI used the repository twenty-five-minute limit and completed
both full suites.

Engine, CRM and journal were deployed at 11:06 Asia/Shanghai on
2026-10-30. All three running executable hashes matched the reviewed
Linux amd64 build from the clean full commit. Exact arguments/environment,
recording, 720-hour retention, archive paths, stable-process phrase/scenario
warming, and existing Desk/simulator identities were preserved.

The live journal and deployed analyzer agree on five first-content
samples with p95 1082 ms, and one linked result at 3548 ms. The strict
gate remains failed. The transcript's corrected caller onsets include
53.591 and 87.993 seconds; its text export also uses the corrected data.
All five archive files verified, and original audio/session/log hashes
matched their pre-deployment fingerprints. Talker was reachable as
`local-talker`; live fillers remain phrase-cache audio.

The new handoff's real-call effect and physical handset target remain
unverified until another recorded call is reviewed after the owner says
**done**.
