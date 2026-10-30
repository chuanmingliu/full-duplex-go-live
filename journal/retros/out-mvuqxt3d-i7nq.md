# Coverage-call retrospective — out-mvuqxt3d-i7nq

The unchanged `kb-coverage-nerd` simulator scenario ran on `7ee447cee1388ab54929b45ab20053309a7c0cdd` after full CI run `41028577982` and verified deployment. The call ran from 17:04:55 to 17:09:52 on 2026-10-30, with 297.842375 seconds of archived stereo audio. The configured scenario limit remained 295000 ms. The saved instructions, voice and greeting, profile, v7 policy and its five companion configurations were preserved. The ten prior call archives and their fifty raw files were verified before and after this run.

## Result and measurement scope

The strict server gate passes on **25 completed, noninterrupted answered samples**. First-audio p50/p95/max is 380/381/381 ms; first-content p50/p95/max is 806/994/1017 ms. Every measured first-content value is below 1200 ms. This is a valid passing observation for this workload, not proof that the physical-handset goal is met or that the next call will have the same distribution.

The subsequent offline missing-output guard reports zero missing output records, zero unverified records and zero missing metrics on this trace; the same twenty-five-sample gate remains passing. This is an audit of recorded evidence, not evidence that the pending shutdown-metric source fix was deployed during this call.

There were no quotation tasks or quotation readout samples. This run therefore does not revalidate ISS-079's total-repeat behavior or ISS-083's duplicate-quotation suppression. No benefit in this coverage result is attributed to prepared quotation audio.

## Conversation and turn accounting

The full originator archive contains 34 caller rows, 35 assistant rows and 34 cached fillers. Twenty-five caller requests complete with valid latency records, including the initial invitation to ask questions and the final farewell; these are not twenty-five independent pricing paths. Nine additional caller fragments become continued utterances after their early output is converted to a backchannel. The converted turn IDs are `item_2`, `item_5`, `item_7`, `item_10`, `item_12`, `item_14`, `item_24`, `item_31` and `item_33`. Their short started fragments remain in the journal. The assistant rows therefore comprise one greeting, twenty-five completed replies and nine such fragments. Across 36 turn IDs, `item_9` is an abandoned speculation with no completed reply. Zero interrupted measured answers does not mean that no partial backchannel speech occurred.

The conversation exercises compulsory and commercial coverage explanations, how they relate, optional car-damage cover, third-party limits, seat versus driver/passenger cover, deductibles, holiday-limit features, available combinations, policy duration and advance renewal, regional underwriting, contact preferences and whether documents must be supplied immediately. The assistant answers the continued questions, accepts the later refusal to proceed with contact/document collection, and ends after an explicit farewell. No exact duplicate long assistant reply or Latin-letter output was found. No new confirmed semantic or tool defect was established from this review; this is not independent certification of every insurance-policy claim.

The regional questions concern underwriting, rather than the regional claims-provider contradiction recorded in ISS-076. That existing ambiguity remains open and is not directly resolved by this run. The saved business prompt was not changed.

The receiver transcript is truncated, although its event list is not marked truncated. Its semantic expectation checks are indeterminate. The semantic and accounting review above uses the complete originator call, session and log archive; receiver check labels alone are not treated as proof of correctness.

## Canonical journal and accepted hangup

There is one canonical-output enable marker and 101 accepted segment records, with no sequence gaps, duplicates, invalid segments or truncation records. All 35 assistant journal rows exactly match the canonical reconstruction in text, millisecond timestamp and interruption flag. This is another passing observation for the existing journal fix. A started-segment record by itself is not proof of completed acoustic playback; here the twenty-five measured replies also have explicit turn-complete events.

ISS-081 now has an exercised live case. At log line 2337, time 17:09:51.663, the accepted `end_call` diagnostic contains the scalar ID `item_36`, with only the expected level, message, session, time and turn fields. It does not serialize the private Turn object. Earlier unit coverage exercised both text and JSON handlers; this live text-log observation confirms the accepted-hangup path in this call.

The final caller and assistant entries are explicit farewells. The final six-character assistant response completes at 17:09:51.968, with audio-done, response-done and latency events. The server requests hangup at 17:09:52.965 after that completion. Receiver evidence reports remote hangup, a native caller-side gateway termination, and no local hangup request. Its `__customer_farewell` action completed. These are mutually consistent with the server closing after its farewell; no additional closing question or unfinished final reply is observed.

## Worst measured response

The slowest first-content sample is `item_8`, the commercial-coverage explanation: 380 ms end silence plus 637 ms after turn close gives 1017 ms. Wall-event decomposition of that 637 ms is approximately 418 ms to released model text, 18 ms to synthesis start, and 201 ms to first synthesized/output audio, with no visible extra output-queue delay. The model started about 153 ms before turn close. These intervals describe this observation; they are not isolated provider compute times or a proof that speculative lead can always be recovered.

## Audio and limitations

All five raw files match their archive-manifest byte counts and SHA256 digests. Audio is stereo 16 kHz PCM16, with signal on both channels and zero clipped samples. Caller/assistant RMS is 674.154/1341.128. Terminal silence is 1.802875 seconds on the caller channel and 1.107687 seconds on the assistant channel; both final 200 ms intervals are silent. Together with the completed final-response event, this supports an uncut stored farewell tail. No independent phoneme/word alignment or physical-handset recording was performed.

The receiver's first-audio application-boundary statistic is p95 668 ms over 22 valid observations, with twenty excluded. It includes openers and is not a carrier/handset measurement. The session's `input_dropped_ms=11400` is retained as clock-accounting telemetry; it is not subtracted from latency or treated here as quantified loss of intelligible speech.

The receiver reports one `tts_chunk_failed` warning/failure. A subsequent bounded event read locates it at sequence 172, 143.664 seconds after answer and 152.847 seconds before normal termination. All 322 events are contiguous and include the terminal event; the next cursor is the last sequence, not evidence of omitted pages. Twenty-two customer speech-end events follow the warning, the first after 3.867 seconds. The call therefore continued after this warning. The retained event has no provider status, error detail, cancellation reason or chunk identity, so neither its cause nor recovery of the affected chunk is established. This is not a warning-free simulation, and the event does not by itself confirm a new server defect.

Optional quotation preparation has one ready attempt (426 ms) and one failed attempt (502 ms), with no prepared enqueues or readouts. The failed attempt finishes at 17:05:04.127, one millisecond after a foreground utterance/prewarm event and roughly 38 seconds before the slowest reply starts output. That ordering is consistent with intentional foreground cancellation, but its cause is not logged and remains an inference. No foreground answer-path TTS failure was identified in the server log. Neither the optional failure nor the receiver warning is demonstrated to cause the worst measured latency; they belong to separate systems.

## Follow-up

Record the 25-sample coverage pass and the now-exercised scalar hangup diagnostic. Preserve the earlier quote-call shutdown evidence and the pending ISS-084 correction separately. Keep ISS-079 under observation despite its passing short quote replay. Do not change VAD, hedge, buffer, scenario duration or timeout thresholds on the strength of this one run. The real-handset latency measurement remains outstanding.
