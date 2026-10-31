# Simulated quotation replay — 2026-10-30, 380c1a7

Evidence collected at 2026-10-30T06:45:18.605787+00:00. Commit `380c1a7350ec73094f01cc19006acff08b587719` passed the complete
quality, normal and race CI jobs ([run 41014100115](https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/41014100115)).
The engine, CRM and journal executable hashes matched the reviewed build;
the deployed release completed its cache, recording and journal checks.
Historical call audio, session and log fingerprints remained unchanged.

The separate guarded configuration change enabled only standard-plan
`prequote.follow_selection_max=8` in quote-plans v7
(SHA `f2142bd9cddd`). The original initial limit remains
implicit eight. All other configuration snapshots were unchanged.

The subsequent simulator replay was `out-mvuluygh-p1vy`, session `sess_fe205c981a9694ecd25c6acc`,
run `sim-ed7527b2b5e18a97e6184bcb`, using the same `sim-prequote-hit` scenario and saved
business prompt (SHA prefix `639dcecf24dd`). All five archive files passed
size and SHA-256 checks; both recorded audio channels contained signal.

| Replay measurement | Observed result |
| --- | --- |
| Server first audio, including fillers | n=5, p50=380, p95=380, max=380 ms |
| Server first non-opener content | n=5, p50=994, p95=1192, max=1192 ms |
| Linked result content | n=3, p50=1103, p95=1192, max=1192 ms |
| Explicit quotation readout output | n=3, p50=1103, p95=1192, max=1192 ms |
| Receiver first-audio boundary | n=7, p95=667 ms |

The targeted quotation exercise **passed** with
3 observed readouts and 2
confirmed guarded nondefault posthook omissions. The separate strict
twenty-sample server gate **failed**. Its recorded reasons were:
first_audio: 5 samples, need 20; first_content: 5 samples, need 20.
Delivery and CRM logs provide the attribution below. This replay is
simulator evidence; the physical handset target remains unproven.

## Delivery and conversation review

All three quotation tasks completed within the acknowledgement grace period, so no waiting acknowledgement was emitted. The new same-task acknowledgement omission path was therefore not exercised. The ordinary guarded posthook omission ran for all three deliveries: t1 was explicitly classified as a default quote, and t2/t3 as nondefault quotes. Their measured readout output times were 1103, 1192 and 972 ms respectively.

The recorded CRM work comprised one foreground/default calculation, eight initial preparations and eight follow-selection preparations, with zero errors. The final requested combination used quote 401, prepared by the follow-selection generation for memory 1. Its 4509 ms backend computation ran from 14:43:17.269 to 14:43:21.778 Asia/Shanghai; the caller requested it at 14:43:30.758, 8.98 seconds after preparation completed. Pricing workload was preserved and completed ahead of demand.

Foreground task durations for t1/t2/t3 were 0/1/1 ms, compared with 0/0/3040 ms in the earlier quotation simulator call. Readout times changed from 1094/1135/4848 ms to 1103/1192/972 ms. The third readout improved by 3876 ms: 3160 ms less origin-side waiting plus 716 ms less child readout time. This is not all attributable to preparation alone, and the two calls are diagnostic runs rather than a controlled latency study.

The 93.648-second stereo recording retained seven caller and nine assistant transcript entries, plus six cached filler events. No response row was missing, unverified or interrupted. Receiver first-audio timing had seven valid observations and eight exclusions, with p95 667 ms. The strict server gate failed only because five answered caller samples are below its twenty-sample minimum. These are simulator and server measurements; physical handset latency is not established.

All three spoken totals matched the available authoritative results and the earlier baseline. The first quote's changed individual-line source was trimmed, so its full line-item correctness cannot be certified from retained evidence. The second quote's five items matched the third quote's source; all six third-quote items and selection continuity were correct. The two extra caller entries were hesitation or a split query, not a dropped substantive request.

ISS-079 persists: the total-only repeat at item_12 still included all six coverage details. Its separate fix was not deployed for this call. The final recognized utterance was a bare acknowledgement; no end_call tool was invoked, and the receiver ended after its farewell action. This establishes neither a new closing failure nor successful server-side closing behavior. Receiver semantic checks were indeterminate. No additional confirmed conversation defect was found.


## Follow-up on runtime bbdb7a3

New simulator call `out-mvumm8e2-t8in` used the same scenario and saved prompt. The targeted quotation diagnostic passed; the separate strict server gate failed. Readout samples=3, p95=1231 ms. The physical handset target remains unproven.

Runtime bbdb7a3 passed full CI and was deployed with the same v7 quote policy. Replay out-mvumm8e2-t8in measured quotation readouts of 1016/1231/1061 ms: the second exceeded 1200 ms and only five caller samples were available. Pricing remained cached at 0/1/1 ms; timing variation before result delivery explains the observed increase, without establishing a code-caused regression. Total-repeat output improved but still named unwanted coverages. Exact playback ownership produced complete, nonduplicate timing rows on this replay; physical handset latency remains unverified.
