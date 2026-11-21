# Quotation-call retrospective — out-mvurvsmk-u9yk

The unchanged `sim-prequote-hit` simulator ran on `d85232a` after full CI run `41032112560` and verified deployment. The call ran on 2026-10-30 from 17:31:20 to 17:32:33 and retained 73.168125 seconds of stereo 16 kHz PCM16 audio. The source review was completed on 2026-10-31. All five immutable archive files match their manifest. This is simulator and server-output evidence, not a physical handset measurement.

## Result

The strict gate fails. First audio has six observations with p95/max 381 ms; first content has five complete observations with p95/max 1845 ms. The three quotation readouts are 862, 2881 and 922 ms. One mid-call speech response has no content timing; it is retained as missing. There are no unmatched whole output records, unverified records, duplicate latency rows or malformed rows. Five completed content answers do not meet the minimum of twenty.

The missing content belongs to `item_6`, a follow-up that reused the pending second quotation. That turn completed its 540 ms cached opener. The result retained only its original `item_5` linkage. This is not a missing final shutdown row: the ending completed normally. The original request and the additional follow-up must both remain visible; assigning the result only to the later request would produce a faster but misleading headline.

## Why the second quotation was slow

The second task completed in 2 ms, at 17:31:49.448. It was ready shortly before the caller opened a follow-up at 17:31:49.508. Delivery waited until the follow-up opener completed at 17:31:50.521: 1073 ms after the task became ready. This interval overlaps real caller-floor activity and subsequent acknowledgment playback, so it is not demonstrated to be entirely avoidable.

At delivery, the result was current and noninterrupting, but the posthook was not skipped. The original task owner was `item_5`, while the current turn was `item_6`; the completed-opener guard therefore did not establish the required current-origin proof. The result started with a nine-character generic price-readiness phrase, carrying no amount, coverage qualification or constraint. The phrase has 1336 ms of cached audio. The actual quote synthesis produced its first audio after 170 ms, but first quote output was delayed until 1036 ms after result start. The strict server result is therefore **380 + 1465 + 1036 = 2881 ms**.

The canonical readout segment's predicted playback time is a further 300 ms beyond its output event, or 1336 ms after the readiness phrase begins. This explains why output timing and audible playback must remain distinct. Neither is a physical-handset recording. Removing only the redundant posthook would still not establish a sub-1200 ms result for the original request.

Four optional audio preparations completed; one additional candidate was declined by the existing capacity limit. Prepared audio was used for the first and third quotations, not the second. No receiver warning or failure was recorded, and no provider failure is established as the cause of this slow response.

## Conversation and recorded output

All three deterministic quotation readouts occur exactly once and complete. Their authoritative totals, line counts and sources match the prior 7ee replay; the final result's selected coverage, all six line premiums and other retained authoritative fields also match. The total-only repeat contains one correct amount and no unsolicited coverage qualifier. These are additional passing observations for ISS-079 and ISS-083, not proof of future behavior or direct evidence that the prefix filter activated.

After the second quotation's fit question, a separate eleven-character fit question plays without a new caller row. Its wording is not identical to the preceding question. Source review traces this to the ordinary continuation created by the coalesced request: its logged 13832 ms wait starts at the exact fold event, and it resumes after the authoritative result. The repair must preserve the conversation history and unrelated work while preventing this already-covered continuation from starting another response.

All twelve assistant journal rows exactly match the twenty-two canonical segments in text, millisecond timestamp and interruption flag. There are no sequence gaps, invalid records or truncation events. A converted early fragment remains visible; the absence of truncated measured answers does not mean every speculative fragment completed as an answer.

## Ending and follow-up

The caller and assistant explicitly say farewell. The final six-character response completes at 17:32:33.178; the server requests hangup at 17:32:33.982. The accepted end-call diagnostic retains scalar `item_14`, supporting ISS-081 again. Audio has signal on both channels, no clipped samples, and ends with 0.913062 seconds of assistant silence and 1.883687 seconds of caller silence. No cut-off final reply is observed.

The deployed shutdown-accounting correction remains covered by its regression tests and historical missing-output audit, but this naturally completed call does not exercise disconnect during playback. Continue investigating coalesced-request attribution, the restored generic readiness phrase and the extra fit question. Do not discard the missing content record, shorten the scenario, or change VAD/hedge settings to obtain a pass. Actual handset latency remains unverified.

The local repair records bounded additional origins without replacing the primary interval, omits only the guarded redundant posthook, and preserves function history while suppressing fully covered stale continuations. Red-before engine and parser regressions pass, including race checks; historical reports remain unchanged. Full candidate CI, deployment and a fresh replay are still required to verify the behavior and latency effect.
