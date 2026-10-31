# Matched quotation simulator replay — d479ab9

Call `out-mvupyy53-pvt3`, session `sess_483af3ee9649cd2b498439a7`, run `sim-facae16a443392df864c3a02`.
Runtime `d479ab99045a67a56a89683b301f3c453178a367`; full quality, normal and race CI [run 41025553778](https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/41025553778). The exact build, preserved profile and eight previous archives were verified after deployment. Recorded 2026-10-30, 16:37:48–16:39:26 +08:00. The scenario and saved business prompt were preserved.

| Measurement | n | p50 ms | p95 ms | max ms |
| --- | ---: | ---: | ---: | ---: |
| First audio including fillers | 7 | 380 | 380 | 380 |
| First non-opener content | 7 | 896 | 972 | 972 |
| Explicit quotation readout | 3 | 878 | 958 | 958 |

The strict server gate fails because there are seven answered samples, fewer than twenty. One answered quotation was interrupted. These are first-output measurements, not proof that all seven replies completed or that the conversation was correct. Physical handset/carrier latency remains unproven.

## Journal and quotation correctness

ISS-082 is verified on this replay: the versioned stream contains one enable marker and 30 accepted segment records. All 13 assistant journal rows match its reconstructed text, millisecond timestamp and interruption flag. There are no sequence gaps, duplicate or out-of-order segments, invalid records or parse errors. Each quotation retains its 13-character prepared first segment. The second quotation's interrupted prefix is retained correctly. These checks establish consistency of stored server-output evidence; they do not certify acoustic playout.

All three spoken quotation texts contain the corresponding authoritative rounded total, and all three logged authoritative totals match the preceding c582781 replay. The final six-line quote matches the prior canonical quote exactly; retained authoritative selection, source, total and every line premium also match. All six rounded line premiums are present in the current final journal entry. Earlier complete result histories were trimmed, so their full itemized source values were not independently reconstructed from retained history.

A new defect, ISS-083, is confirmed in the second quotation. Item_6 starts a 13-character prepared prefix, a 46-character remainder, then a 59-character segment identical to those first two combined. The journal retains 116 heard-prefix characters: the full initial 59-character quote and 57 characters of the second copy before the caller begins requesting an addition. The duplicate is an additional spoken segment with a unique sequence, not a duplicate journal row or prepared-cache enqueue. The 958 ms readout onset remains valid; the duplicated, interrupted reply is not a successful full readout. The first and third quotations complete normally.

ISS-079 is reopened. The total-repeat request spans two caller entries and completes as item_12. Its 30-character answer gives the correct latest total first, then an unrequested driver/passenger coverage-limit qualifier. The second number is an insured limit in ten-thousands, not another premium. The user did not request coverage details or comparison. This is narrower than repeating the full coverage list, but it is still not strictly total-only. Detailed and comparative requests must remain supported by any fix.

## Turn accounting and ending

Nine caller entries correspond to seven logical requests: the first quotation request and the later total-repeat request are each split into two recognized entries. Item_2 and item_11 become backchannels and contribute short spoken fragments. Three other abandoned speculations emit no output. The 13 assistant rows comprise one greeting, two such fragments, three quotation-origin openers, three result readouts and four other replies. Nine cached fillers are not nine completed responses. Strict accounting contains eleven records: seven answered origins and four non-speech records (greeting plus three linked results), with one interrupted answer and no missing, duplicate or unverified metric rows.

The final caller explicitly thanks the assistant and says goodbye. Item_16 answers with a brief farewell and completes at 16:39:25.515, including response.audio.done, response.done and a latency record. The engine then logs its farewell-based automatic end decision. The receiver separately records its completed customer-farewell action and customer_hangup, with the session closing at 16:39:26.635. These overlapping observations do not establish which side's termination reached the gateway first. No accepted end_call diagnostic is present, so the scalar-log path of ISS-081 remains unexercised.

All five local raw archive files match their manifest. The 97.91775-second stereo 16 kHz PCM16 recording has signal on both channels and no clipped samples. Above absolute sample magnitude 8, assistant signal ends around 96.691 seconds, leaving 1.227 seconds of terminal silence; caller signal ends around 94.894 seconds, leaving 3.023 seconds of terminal silence. The final 50 ms of both channels is zero. This supports an uncut final output tail in the stored recording; no handset measurement or acoustic word alignment was performed.

## Preparation and phase evidence

Four bounded preparation attempts succeeded and one additional admission check was skipped for capacity without spending an attempt. Three prepared enqueue events correspond to three observed quotation onsets, at 878/958/861 ms. Result-start to first readout is 0/4/0 ms. Enqueue itself is not audible proof, and this small replay cannot attribute a distribution-level improvement to the optimization or to diagnostic logging.

The new adapter telemetry exposes 120 unique stream opens and 128 summaries: 20 DONE and 108 cancellations, including 8 canceled requests without an open/header observation. There are no missing summaries, duplicate IDs, invalid fields, phase anomalies or argument-probe-limit events. These counts include warming and abandoned work.

For the three quotation origins, headers-to-first-tool-fragment is 473/416/259 ms; fragment-to-first-valid-JSON-arguments is 9/77/81 ms; valid-arguments-to-finish is 29/21/14 ms. Finish-to-DONE and finish-to-end are zero at millisecond resolution. Request-to-turn association is reconstructed from isolated delegation windows, winning copy/lane and completion timestamps; the adapter does not emit an exact engine-turn identifier. The evidence does not show a delayed trailing DONE frame or justify dispatching merely syntactically valid arguments before model completion.

The slowest ordinary answer, item_12, reaches first content at 972 ms. Its winning request first text precedes hedge release by 110 ms, but final assembly after release is only about 3 ms and TTS takes 172 ms. The earlier first text is not automatically recoverable: speech needs a usable clause and existing slow-continuation counterexamples remain relevant. Keep endpoint, hedge and segment policies unchanged on this trace. Fix the confirmed conversation defects, then repeat the full workload and obtain physical handset evidence.

## Follow-up candidate

Source tracing confirms that the backend queues the deterministic readout and then forwards the model stream without checking for a repeated copy. The next candidate adds a one-shot exact prefix check before model text reaches speech, transcript events or generated-response state, and removes duplicate fixed-text bookkeeping. It preserves revised values, partial matches and later requested details. The total-only guidance also explicitly excludes unsolicited coverage-limit and plan-configuration confirmations; this remains a probabilistic mitigation. The duplicate-output and guidance regressions failed before their respective fixes. Relevant combined duplex race tests, repository-wide vet and all-package compilation now pass; independent source review found no blocker. CI, deployment and a new emitted-answer replay are still required. Neither change establishes a handset latency result.
