# Simulated quotation changes — 2026-10-30, 1fa2846

Call `out-mvukudco-7zrb`, session `sess_0b88908207ae87c1ead0c9e6`,
receiver run `sim-4f18657fec2eb9a237de70e2`, 14:14–14:15 Asia/Shanghai.
The existing `sim-prequote-hit` scenario and saved business prompt
(SHA prefix `639dcecf24dd`) were unchanged. This is a simulated PBX test.

| Server-output measurement | Samples | p50 | p95 / maximum |
| --- | ---: | ---: | ---: |
| Caller last sound → first audio, including fillers | 5 | 380 | 380 |
| Caller last sound → first non-opener content | 5 | 1094 | 3952 |
| Caller last sound → linked result content | 3 | 1135 | 3952 |
| Caller last sound → actual quotation readout output | 3 | 1135 | 4848 |

Milliseconds, nearest-rank p95. All three quotation readouts were explicitly
observed, with unique verified caller/result links. There were no missing,
unverified or interrupted response rows. The strict gate fails: fewer than
twenty caller samples and a slow third quotation. Receiver application
first-audio p95 was 652 ms over six valid observations, including fillers;
four other observations were excluded. This is not handset evidence.

## What the quotation test established

| Task | Origin → result | Backend task | Result content | Readout output |
| --- | --- | ---: | ---: | ---: |
| t1 | item_3 → item_4 | 0 | 1094 | 1094 |
| t2 | item_5 → item_6 | 0 | 1135 | 1135 |
| t3 | item_7 → item_9 | 3040 | 3952 | 4848 |

The first two skipped the built-in posthook. The second was the caller's
requested change to the initial plan, with completed opener, unchanged
memory and a free floor. Its original result JSON had been trimmed from
retained history, so the automated diagnostic could not independently
verify its `default` flag. The diagnostic therefore failed conservatively;
this missing flag is not evidence that omission failed. A boolean flag at
delivery will make future checks independent of retained private history.

The third calculation began at 14:15:01.999 and completed at 05.039. The
waiting acknowledgement ran from 02.400 to 04.664 and did not delay the
ready result: delivery was immediate at 05.039. However, the current turn
was then the acknowledgement, rather than the original caller turn, so the
new posthook guard did not match. The cached posthook played before the
figures. Its PCM duration was 1195 ms; the readout was emitted 896 ms after
result start under the existing player lead. Neither duration is an
independent measurement of physical phone playback.

ISS-077 tracks safely recognizing the same task's completed acknowledgement
for redundant-posthook omission. ISS-078 tracks preparation of likely
follow-up selections: the current cache warms changes around the startup
default, but not around a newly quoted plan. All eight initial preparation requests were used. The quote API polls at
500 ms and fronts the project's local `quotesvc`, whose purpose is to retain
a seconds-long pricing workload. That workload is preserved. Any additional
preparation will use a separate explicit cumulative cap; the existing
request budget must not silently expand.

## Conversation and archive

All requested plan steps were delivered. The third quote preserved the
previous selection change and added the requested coverage. Spoken values
matched the available authoritative totals, line sums and configured
rounding. No lost selection or wrong price was established.

A later total-only repeat request received every coverage detail before the
total. ISS-079 records that scope mismatch. The final closing turn invoked
`end_call` and ended after the farewell without further selling.

All five required archive files passed size and SHA-256 checks. The
85.517-second stereo PCM16 WAV at 16 kHz has signal on both channels;
five caller and ten assistant transcript entries and five cached filler
events were retained. The receiver report was not truncated. The recorder
reported 1940 ms of input clock correction, which is not subtracted from
latency. Private recordings, prompts and business values remain off GitHub.

Next: fix the same-task acknowledgement path, evaluate explicitly bounded
follow-up preparation, then rerun this same scenario. The physical handset
goal remains unverified.

## Targeted follow-up implementation

The same-task acknowledgement fix passed failing-before regressions,
twenty repeated race runs and a broader focused race suite. Independent
review caught a graceful-interruption case where a completion timestamp did
not prove natural playback completion. The final fix now requires natural
completion for both the original caller opener and its linked acknowledgement;
truncation invalidates that proof. Review of the corrected code found no
remaining issue. Interrupted, unrelated or inaudible acknowledgements,
changed callers/memory and custom wording retain their protection. Successful
quote deliveries also log a boolean default classification, avoiding
dependence on trimmed private history.

A legacy greeting-metrics timeout reproduced on both the candidate and
unchanged HEAD in isolated tests; its runtime behavior was not altered.
Full candidate CI, deployment and a new quotation-path measurement remain
pending. The posthook change alone cannot remove the slow backend request.

Follow-up preparation is implemented behind the default-disabled
`follow_selection_max` policy. It bounds additional new calculations per
call, preserves initial preparation limits, prioritizes foreground work at
the next API slot and discards stale queued selections. Existing in-flight
pricing cannot be preempted. A 512-call counter bound limits process memory;
new calls skip extra preparation after that capacity is reached.

The full CRM race suite passed in 8.016 seconds, including pricing-source
changes, expiry ownership, cumulative limits, cancellation and shutdown.
Failing-before regressions reproduced the missing neighbor preparation and
an API result entering a rules cache claim during a configuration change.
The final dispatch pins the pricing source/version through computation.
Local latency-report tests (37) and simulator-report tests (10) passed.
The box policy remains unchanged until the reviewed runtime is deployed.
