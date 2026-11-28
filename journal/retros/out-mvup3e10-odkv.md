# Matched coverage simulator replay — c582781

Call `out-mvup3e10-odkv`, session `sess_45fe4dc0d0c906a3303260d7`, run `sim-01cf683d6387860f539a2030`.
Runtime `c582781dfb32a3535e47c632d1d49b8e7e530569`; full candidate CI [run 41022216798](https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/41022216798).
Recorded 2026-10-30, 16:13:16–16:14:43 +08:00. The same 295000 ms coverage scenario and preserved saved prompt were used. The run stopped after 87.621 seconds and did not complete the intended coverage workload. These are server and simulator observations; handset end-to-end latency remains unproven.

| Measurement | n | p50 ms | p95 ms | max ms |
| --- | ---: | ---: | ---: | ---: |
| First audio, including cached fillers | 5 | 380 | 380 | 380 |
| First non-opener content | 5 | 1060 | 1310 | 1310 |
| Explicit quotation readout | 0 | — | — | — |

The strict gate failed both the twenty-sample requirement and the content threshold. Content latencies were 1005, 1060, 1155, 976 and 1310 ms. The preceding bbdb7a3 coverage run had twenty qualifying replies and content p95 1188 ms, but this shortened, differently realized conversation is not a paired distribution-level regression test. Receiver first-audio p95 was 462 ms from five valid application-boundary observations, with five excluded; it includes openers and is not physical handset playback.

## Conversation and accounting

Nine caller transcript entries represent five completed logical requests, with four continuations. The initial exchanges establish the conversation; the next three concern compulsory insurance and its mandatory purchase, commercial coverage in relation to compulsory coverage, and whether car-damage coverage is optional. The final follow-up concerns not buying that coverage; it does not establish a request to end the call. The responses address those narrow questions: third-party injury/property coverage, commercial coverages and their roles, and optional car-damage coverage with the consequence of bearing one's own vehicle loss. This review does not certify broader product, eligibility or regional claims.

The five completed assistant responses exactly match the five retained canonical assistant history entries. Four earlier openers were converted to backchannels when the caller continued (item_3→4, item_5→6, item_7→8 and item_9→10). Three of those paths produced a short assistant fragment. Together with the interrupted greeting and five completed answers, those fragments explain the nine assistant transcript entries. Nine cached filler events are recorded; filler count is not the completed-answer sample count. Strict accounting has one non-speech greeting plus five answers and no missing, duplicate, malformed or unverified rows. No exact repeated assistant row or English text was found in the retained transcript; that is limited transcript evidence, not an acoustic intelligibility assessment.

The call never reached a quotation, total-only repeat, regional-underwriter/claims question, or farewell. ISS-079 and ISS-076 were therefore unexercised. There was no prepared quotation readout with which to recheck ISS-082. There were zero accepted end_call events, so ISS-081 received no live-path validation. Absence of receiver failures does not establish semantic success: its semantic and graceful-ending checks are indeterminate.

## Early stop and recording

The final answer, item_10, completed normally at 16:14:23.726, including response.audio.done, response.done and its latency record. The receiver then requested local hangup for an inactivity limit; the call ended at 16:14:43.924. This was neither the 295-second duration ceiling nor an observed farewell. No engine model or synthesis operation remains visibly outstanding after the final completion. The complete receiver event log contains 102 events without truncation. Its last remote-speech-end event precedes the inactivity stop by 19.800 seconds, with no subsequent customer speech or recorded scenario-completion action. These events support that the receiver observed the final engine speech, but expose no provider request/response or simulator planning state. The scenario had not reached its later coverage questions. The cause of the missing next simulator utterance remains unknown: neither a model/request stall nor an intentional internal stop nor an input/turn-handling defect can be established from these events. Investigating that receiver behavior is separate from the measured 1310 ms engine response.

All five local archive files match their manifest fingerprints. The 87.621-second stereo 16 kHz PCM16 recording contains signal on both channels and no clipped samples. Assistant signal above absolute sample magnitude 8 ends around 67.223 seconds, leaving 20.398 seconds of terminal silence; caller signal ends around 57.521 seconds, leaving 30.100 seconds of terminal silence. The final 200 ms of both channels is all zero. Thus this is not an empty recording or an observed truncation of the final engine answer. No handset, carrier or acoustic word alignment was measured.

## Slow response and preparation

The slow item_10 path decomposes as 380 ms endpoint silence minus 158 ms speculative lead plus 878 ms model-to-usable-clause, 20 ms clause assembly and 190 ms TTS-to-first-audio, totaling 1310 ms. The 878 ms term combines provider, transport, streaming and adapter work; the deployed build cannot split raw upstream first-body/first-delta timing. The source phase-telemetry change was not deployed for this replay.

Four bounded preparations completed in 500, 412, 279 and 280 ms, with no prepared-cache enqueue or observed readout. An additional admission check skipped immediately with attempted=false and capacity status during a foreground second segment. That is consistent with foreground priority; the exact admission branch is not exposed. No successful preparation overlapped any of the five completed replies' delegation-to-first-content intervals, and the last completed 41.758 seconds before the slow turn's delegation. No provider error, failed preparation or background reconnect is observed. There is no evidence in this run that background preparation caused the slow response or later simulator inactivity.

Keep the authoritative-price and configuration safeguards. Diagnose the receiver stop, deploy the reviewed transcript and phase-observation fixes, then repeat the intended workload and collect physical handset evidence. This five-response run does not meet the below-1200 ms target.
