# Call retrospective — out-mvuqswhl-2dw7

The matched quotation simulator replay ran on `7ee447cee1388ab54929b45ab20053309a7c0cdd` after full CI run `41028577982` and verified deployment at approximately 17:00 on 2026-10-30. It used the unchanged `sim-prequote-hit` scenario and saved business context. The call ran from 17:01:06 to 17:02:15; archived stereo audio lasts 69.68025 seconds. This is simulator/server evidence, not a real-handset measurement.

## Outcome and scope

All three authoritative quotation readouts completed, and each occurred once in the emitted output. The requested total-repeat answer contained only the correct total and a brief opener, with no extra coverage name, insured limit or configuration qualifier. These are improvements over the preceding d479 replay. The final follow-up after the caller's thanks did not have a playback-completion event, and the recording ends while assistant audio is active. Therefore the call does not establish a completed final reply or clean audible farewell.

The deployed strict server gate fails because its four recorded answered samples are below the required twenty. A subsequent audit also found an unmatched final started answer (ISS-084); the deployed analyzer did not count that missing whole record, so these statistics do not cover the full answered population. First audio p95/max was 380 ms. First-content p50 was 1005 ms and p95/max was 1117 ms; no measured sample reached 1200 ms. The three observed quotation readout onsets were 1026, 984 and 921 ms. This small, differently populated sample does not establish a distribution-level gain over d479 or prove the handset target.

## Conversation and quotation correctness

The caller asked for a quotation, changed the third-party coverage selection, added driver/passenger coverage, asked for the total again across two recognized fragments, and then thanked the assistant. There were six caller journal rows, ten assistant rows and six cached fillers. The two selection-change requests retain the same requested numerical choices as the previous matched replay.

All three logged priced selections and authoritative totals match the corresponding d479 selections and totals. The first two emitted segments of each new quotation exactly match its previous deterministic readout. Their lengths are 59, 59 and 70 characters; generated follow-ups add 10, 11 and 11 characters respectively. Each complete readout occurs exactly once in its result turn. The follow-ups contain no further quoted amount or coverage name. Result turns `item_3`, `item_5` and `item_7` all have explicit turn-complete events, with 69, 70 and 81 emitted characters.

The retained final authoritative result preserves the same status, source, currency, selection, total and all six line premiums as d479. Changes are limited to quote identifiers and computation timestamps/timing. All six rounded line premiums are present in the final quotation, and its complete journal text equals the retained canonical response. The earlier two full tool-result payloads are no longer retained in session history; their logged totals/selections and identical readouts are verified, but this is not a fresh independent external-pricing audit of every earlier item.

For ISS-083, the exact duplicate readout seen in d479's second quote is absent. The deployed fix has red-before regression coverage for removal of one exact leading readout copy and preservation of partial, changed and later text. The live telemetry does not contain raw pre-filter text or an activation counter. Thus this replay verifies non-repeating emitted output; it does not prove that the new filter removed an upstream duplicate in this particular call.

For ISS-079, the early 14-character response fragment already gives the correct total without coverage details. When the caller continues the repeat request, that fragment becomes a backchannel; the completed `item_9` reply is 16 characters including its opener. Its one currency amount, expressed in Chinese numerals, equals the latest authoritative rounded total. No coverage name, insured limit or configuration qualifier is appended. Mark this issue **watching**: an earlier single-case c582 pass regressed in d479, and this remains model guidance with alternate forms and explicit detail/comparison requests not exercised live here.

## Accounting and canonical journal

The four answered metric rows correspond to the three quote-origin turns joined to their completed result readouts, plus the completed total-repeat reply. None is marked interrupted. The greeting and three result turns account for the four non-speech rows. The split repeat's first fragment becomes a backchannel without its own completed-answer row. The final follow-up ends with the session and has no completion/latency row. Thus neither six caller rows nor ten assistant rows implies six or ten completed measured answers.

The versioned canonical output contains one enable marker and 21 accepted segment records, with no sequence gaps, duplicates, invalid records or truncations. All ten assistant journal rows exactly match the reconstructed text, millisecond timestamps and interruption flags. All three prepared 13-character quotation prefixes are preserved, providing another passing observation for ISS-082. A canonical segment record means its output started; it cannot certify that every character finished playing before disconnect. In particular, the final 33-character journal row is not proof that all of that reply was heard.

## Timing and telemetry

The observed quotation paths decompose into 380 ms end silence plus origin-to-result intervals of 646, 600 and 541 ms, followed by 0, 4 and 0 ms to first readout output. Prepared audio continues to avoid a material result-stage synthesis wait in these three samples. The raw model response before duplicate filtering is not logged, so higher stream-frame counts cannot be interpreted as proof of suppressed text.

The reconstructed quote-origin requests have header-to-first-tool-fragment intervals of 747, 531 and 460 ms, and valid-argument-to-finish intervals of 22, 15 and 0 ms. No trailing finish-to-DONE delay is observed. The build-corrected phase analyzer observes 80 stream opens and summaries, including 17 completed and 63 canceled operations, with no reported quality flags. Canceled operations include speculative/hedged work and are not, by themselves, conversation failures. This trace does not justify buffer, hedge, VAD, segment-threshold or policy tuning.

## Ending and audio evidence

The last caller row contains thanks, without an explicit goodbye. The assistant starts a policy-issuance follow-up question. The receiver records a completed `__customer_farewell` action and requests `customer_hangup`; its termination evidence names the receiver gateway, local hangup true and gateway-ended false. The server has no accepted `end_call` log or final turn-complete event. Zero accepted-hangup events do not validate the ISS-081 scalar-turn logging path.

The last response begins at 17:02:11.756; further segments begin at 17:02:12.456 and 17:02:13.318. The session ends around 17:02:15.688. The final segment was synthesized, but final playback completion is unproven. No exact missing word is inferred.

All five local raw files match their archive-manifest byte counts and SHA256 hashes. The WAV is stereo 16 kHz PCM16, with signal on both channels and no clipped samples. Caller RMS is 625.279 and assistant RMS is 1318.647. Caller terminal silence is 3.301875 seconds. Assistant terminal silence is zero: its final 50 ms has RMS 432.982 and peak 1181, confirming active audio at the file boundary. No independent word alignment or physical handset recording was performed.

## Follow-up

Keep ISS-079 under observation and retain detail/comparison request controls. ISS-083's narrow exact-prefix fix is deployed and its emitted-output replay passes, with activation observability explicitly limited. Continue the unchanged longer coverage workload for a chance to collect at least twenty valid samples; the previous matched coverage run ended early for receiver inactivity, so duration alone provides no sample-count guarantee. Preserve the incomplete ending and real-handset measurement as outstanding evidence limits.

## Shutdown measurement gap

The final follow-up exposed ISS-084. Engine shutdown can clear active playback ownership and stop pending completion callbacks before they emit a latency record. Both analyzers previously ignored whole missing records; twenty other recorded answers could therefore pass despite unmatched started output. The next repair preserves shutdown receipts and adds a conservative unverified-output check with explicit backchannel exclusions. No timing is inferred for this historical final reply, and archived recordings, transcripts and source logs remain unchanged.
