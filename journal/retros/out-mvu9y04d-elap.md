# Real configured-handset call — 2026-10-30, 1869e54

Call `out-mvu9y04d-elap`, session `sess_79a1a9a9b94693ad6a616b74`,
09:09:10–09:12:38 Asia/Shanghai. The owner signalled **done** before this
review. Saved prompt SHA prefix `639dcecf24dd`, 13,116 characters, matching
the preceding real baseline. Runtime `1869e54`.

| Server-output measurement | Samples | p50 | p95 | Maximum |
| --- | ---: | ---: | ---: | ---: |
| Last caller sound → first audio, including fillers | 19 | 380 | 600 | 600 |
| Last caller sound → directly observed answer content | 16 | 983 | 1119 | 1119 |

Timings are milliseconds, nearest-rank p95. There are 23 metric rows:
19 speech origins and four non-speech responses. Sixteen origins have
content, including five interrupted answers; three lack content in their
own row because their quotations were delivered on subsequent result
turns. There are no malformed or duplicate metric rows.

The strict gate **fails**: only 16 content samples and three missing
caller-to-result measurements. Full-lifecycle analysis also marks the
three historical delivery links unverified because this runtime did not
emit a monotonic origin-to-result offset. No missing value is counted as
zero, and no result turn is counted as another caller question.

## Quotation handoffs

Lifecycle IDs establish `item_2 → t1 → item_3`, `item_5 → t2 → item_6` and
`item_13 → t3 → item_14`. Wall-clock reconstruction estimates their
caller-to-content waits at approximately **1293, 1009 and 1017 ms**.
Adding these diagnostic estimates once to the 16 direct samples gives
19 responses with p95/max approximately 1293 ms, including one at or above
1200 ms. This is not a strict acceptance result: wall-clock logs lack the
new monotonic relationship and there are still fewer than 20 samples.

The earlier acknowledgement-grace fix is active. Two results followed
task completion in 14 and 47 ms. The first still waited 295 ms: task done
09:09:28.049, original opener done28.161, acknowledgement released28.225,
result delivered28.345. The extra 120 ms follows the loop's delivery-before-
deferred-drain ordering. ISS-066 adds delivery checks on task completion,
skipped acknowledgement and player-floor release. The calling clause,
scripted acknowledgement, fresh caller speech and shutdown guards remain.
Three event-order regressions reproduced the defect before the fix and
pass afterward, including repeated race checks.

ISS-068 adds explicit caller origin and a monotonic result-start offset.
The strict analyzer joins only unique session/task/origin/result links
with explicit playback evidence. It preserves the original filler timing,
adds the original pause once, and leaves results classified as non-speech.
Missing, duplicate, conflicting or unplayed evidence cannot establish
acceptance. The journal now separates first answer content from fillers
and corrects modern turn grouping using the explicit speech flags.

## Answer quality

An individual coverage premium contradicted a later authoritative
quotation. Repeated requests for individual premiums were answered with
plan totals. Comparison variants omitted coverage line items, and the
recent-result memory retained only the configured spoken readout after
history trimming. Its interruption guidance also explicitly encouraged
repeating totals.

ISS-067 retains authoritative coverage lines in both rules/API comparison
results, including unchanged alternatives. Compact retained price facts
preserve known zero, missing, failed and excluded values distinctly;
answers are guided toward the caller's current scope. The configured
readout is unchanged. Extra retained facts are capped at 4096 characters
with whole-option omission warnings; a representative two-option fixture
adds 210 characters. The six-option wire fixtures add 1038–1450 characters.
This is a correctness/latency tradeoff to measure in the next real call.
Rules/API, slice-isolation and retained-memory regressions pass.

The caller also had difficulty identifying the intended business account
and received categorical QR guidance without a verified source. The saved
prompt does not establish the exact account's full name, so none was
invented or written into configuration. Two closing requests received
continued guidance; only the final explicit farewell ended the call.
Active hangup-rules v3 already includes goodbye and completed-business
conditions. No end_call invocation was logged. This model-adherence issue
is tracked as open ISS-069; no hangup rule was changed speculatively.

The exclusions-cache fix, ISS-065, was not exercised: all three quotations
had empty skipped lists and no excluded rider was requested.

## Remaining latency and measurement limits

The slowest direct content response was 1119 ms: 420 ms of turn-ending
pause, 86 ms to the corrected final, 431 ms from model request to the first
eligible clause and 182 ms to output. A preceding speculative answer was
correctly abandoned when the transcript changed. Model requests used all
eight lanes and approximately 97.7% cached input. Provider timings were
better than the evening baseline; this is not a controlled measurement
of a general speedup from the deployed code. No turn-ending VAD, lane
count or saved business prompt tuning is justified by this evidence.

The permanent archive contains 51 transcript entries (27 caller, 24
assistant) and 207.54 seconds of 16 kHz stereo PCM audio, with signal on both
channels. All five required files passed size and SHA256 checks. The
recording is on the input clock; 220 ms of dropped input was reported and
was not subtracted from latency. Private conversation, audio and prompt
remain outside GitHub.

The exact-session echo probe found no reliable handset return: strongest
significance 2.5 against required 6, zero significant stretches of 31
eligible against required 4. Physical handset playout and the phone leg
remain unmeasured. The below 1200 ms handset target is not established.

This call records 26 cached filler events. Talker is separate from live
telephony generation; these are configured cached phrases, not Talker
results. The journal retains that distinction.

## Release verification

Targeted regressions pass. Full CI, identifiable engine/CRM/journal build,
idle-gated box deployment and live verification are pending for this
revision. Repeat the real call with the same saved prompt after deployment
and wait for **done** before reviewing it. New telemetry must be collected
on that runtime; it cannot retroactively supply missing historical offsets.
