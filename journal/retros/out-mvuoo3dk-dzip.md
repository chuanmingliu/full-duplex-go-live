# Matched simulator quotation replay — c582781

Call `out-mvuoo3dk-dzip`, session `sess_5e9f31dddc8d89ef94482aaf`, run `sim-f6a4295dede0b5532d3796be`.
Runtime `c582781dfb32a3535e47c632d1d49b8e7e530569`; full quality, normal and race CI [run 41022216798](https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/41022216798).
Evidence collected 2026-10-30T08:08:12.784718+00:00.

The same scenario and saved prompt (SHA prefix `639dcecf24dd`) were used.
Main telephony opted into a maximum of four prepared-quote audio attempts;
the separate demo profile, quote-plans v7, five companion business configs,
and the older 380c1a7 enable receipt were preserved. Six prior call archives
and retrospectives remained unchanged. These are server/simulator measurements;
physical handset/carrier end-to-end latency is still unproven.

| Measurement | n | p50 | p95 | max |
| --- | ---: | ---: | ---: | ---: |
| First audio (including fillers) | 4 | 380.0 | 380 | 380 |
| First non-opener content | 4 | 1028.0 | 1305 | 1305 |
| Linked result content | 3 | 999 | 1305 | 1305 |
| Explicit quotation readout | 3 | 999 | 1305 | 1305 |

The strict server gate **failed**: first_audio: 4 samples, need 20; first_content: 4 samples, need 20; first_content: p95 1305 ms is not below 1200 ms; tool_result: p95 1305 ms is not below 1200 ms; quote_readout: p95 1305 ms is not below 1200 ms.
The targeted quotation diagnostic **passed**. Neither replaces manual semantic review.
Receiver first-audio p95: 578 ms from 5 valid observations
(5 excluded); this includes openers at the simulator application boundary.
Receiver transcript truncated: False; events truncated: False.
All five selected archive files verified. Recording: 76.143 seconds, stereo16kHz,
6 caller entries, 9 assistant entries, 6 cached fillers.

Observed preparation events: 4; attempted: 4;
ready: 4; failed: 0; capacity: 0.
Prepared cache enqueue events: 3; strict observed readouts: 3;
observed readouts with a prepared enqueue: 3.
Diagnostics: no diagnostic schema/budget issues recorded.
An enqueue is not proof of audible output. Matched-run differences alone do not isolate causal latency gains.
Accepted-hangup diagnostic events: 0; scalar turn IDs: 0;
invalid turn values: 0. Zero events means the live path was unexercised.

## Reviewed behavior, limitations and attribution

The three quotation readouts completed, with server readout latencies of 918, 1305 and 999 ms. Their spoken totals match the logged authoritative totals under the configured whole-yuan rounding rule; all three authoritative totals also match the preceding bbdb7a3 replay. The final retained canonical quotation contains all six authoritative line premiums and its final selection, source and line prices match bbdb7a3. The earlier two complete canonical quotation histories were trimmed, so their itemized source values were not independently reconstructed in this review.

The total-repeat request was split into two caller utterances. The earlier item_8 opener was converted to a backchannel when the caller continued, and item_9 answered the combined request. Its completed 12-character response contained one monetary amount, matched the latest authoritative rounded total, and contained no coverage names or itemized list. This is a positive observed result for ISS-079 in this matched case, with broader wording and detail/comparison cases still outside this call's coverage.

Six caller entries represent five logical requests: three quotations, the split repeat request, and a final brief courtesy thanks. Nine assistant entries consist of the greeting, three short quotation openers, three quotation responses, the total-only repeat and the final response. Six cached filler events were recorded. The first three quotation origins plus item_9 account for the four measured answers. The final item_10 generated 32 characters of plan-confirmation content and started output, but no turn-complete or response-latency record was emitted before the session closed. Its absence from the four-answer statistic is therefore a completion limitation, not evidence of a fifth fast completed response.

The recognized final caller utterance was a brief thanks; it did not establish an explicit farewell request. The simulator completed its terminal courtesy action and initiated customer_hangup. No end_call tool invocation or accepted-hangup diagnostic was observed, so ISS-081 was unexercised in this call. The final response began output at 16:02:35.563, another transcript segment followed at 16:02:36.391, and the session closed at 16:02:38.568. The independent recording audit found assistant audio still active at the file end. Final playback completion is unproven; no exact missing word or completed farewell is claimed. No separate closing-policy defect is established from this evidence.

ISS-082 is a confirmed journal transcript defect: each quotation journal row is 13 characters shorter than the corresponding completed generated response. For the retained final quotation, the journal row is exactly the suffix of the canonical history entry and omits its first priced-coverage segment. This makes journal-only review understate the quote text. It does not by itself prove missing audible output. The issue remains open until a source fix is deployed and verified.

Four preparation attempts were ready and three prepared-prefix enqueue events were linked to three strict observed readouts. Enqueue evidence does not establish handset playout or isolated causal savings. The four-answer strict report fails the minimum sample count and its 1305 ms content/readout p95 exceeds the target. Physical handset latency remains unproven.

Timing decomposition locates the remaining slow quotation before result delivery: 1063 ms backend/tool-decision duration minus 141 ms speculative lead plus approximately 2 ms submission/delivery equals 924 ms after turn close. Adding 380 ms endpoint silence and 1 ms readout output yields 1305 ms. There was no active background preparation during that path; the final preparation had completed 11.965 seconds before delegation. All three pricing tasks took 0–1 ms. The eight-copy hedge selected a complete tool call rather than an early text token. First tool-fragment timing is unavailable, so the backend delay cannot yet be split into queueing, first-delta wait and argument decoding.

Compared with the preceding matched bbdb7a3 scenario, result-start to readout output changed from 189/171/184 ms to 0/1/0 ms, while close-to-result changed from 447/680/497 ms to 538/924/619 ms. On the slow second quote, the 170 ms improvement after delivery was outweighed by 244 ms more before delivery. Three observations per run and differing conversation/model draws do not prove a distribution-level improvement.

The retained prepared first clause contains substantive coverage information, an insured limit and a line premium, rather than a generic readiness phrase. Readout onset still precedes the first spoken numeric amount; no word/phoneme or first-amount alignment was measured. The local archive copies all match their five-file manifest. The 76.14325-second stereo 16 kHz PCM16 recording has signal on both channels and zero clipped samples; the last 50 ms of assistant audio has RMS 650, consistent with audio still active at disconnect. This is a signal audit, not handset or acoustic intelligibility certification.
