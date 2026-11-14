# Active Call quotation and addV review

Real call `out-mw92i4vn-q487`, engine `0ac7966`, 2026-11-09. Original GoLive Active Call configuration. The owner confirmed completion before review.

## Stored evidence

Five permanent archive file hashes verified. The 145.64-second stereo WAV contains signal in both channels. The archive contains 15 caller and 17 assistant transcript entries. These checks establish recorded signal and text. Acoustic handset timing and full audio intelligibility were not assessed.

## Timing

| Population and boundary | Samples | Median | p95 / maximum |
| --- | ---: | ---: | ---: |
| Normal response, caller end to server first content | 8 | 1,032 ms | 2,205 ms |
| First audio, including preset openers | 11 | 380 ms | 380 ms |
| Quotation request to first authoritative readout | 3 | 1,049 ms | 1,279 ms |
| VAD utterance opening to server playback cut | 1 | 208 ms | 208 ms |

The normal-response gate fails with a 2,205 ms tail and fewer than 20 normal samples. One kept acknowledgment and one detector assist were recorded, with zero detector fallbacks. There were two interrupted answers, including a quotation readout. One cut was a non-barge cancellation, so it is excluded from the VAD-paired interruption sample. Downstream queued audio and pre-VAD detection time remain outside this measurement.

The slow normal turn recorded 282 ms to its first model token and 1,825 ms to first content after the recognizer's turn boundary. Adding 380 ms end silence gives 2,205 ms. Its preset opener was 240 ms long. The model completed at 17:38:57.462, while the first content segment's audio arrived at 17:38:58.703, a further 1,241 ms later. This locates the tail after model completion in the speech pipeline. These timestamps do not separate synthesis-provider delay from local scheduling.

## Quotation calculation

Three foreground quote tasks returned in 0, 1 and 1 ms. The initial result was prefetched, and the later two used prepared cache entries. Across 17 pricing jobs, the CRM phase totals ranged from 3,004 to 4,508 ms with a 4,006 ms median. These include backend polling and are distinct from foreground result readout. The configured calculation delay was preserved. No separate slow-task acknowledgment was emitted because the foreground results arrived inside the acknowledgment grace period.

## WeChat completion

The marked simulated callback was accepted at 17:39:00 China time, 90.04 seconds after answer. Its receipt verified `wechat.status=added`. The tracking producer is still external future work.

All observed skill-preparation records returned `no_match`. This call therefore does not verify successful detailed MCP skill selection. On-box transcript pattern checks found WeChat navigation/invitation wording and no explicit addition-success confirmation. These pattern checks have limited semantic coverage. The deployed handler could update state without proactively announcing success, motivating the configurable callback response implementation.

## Follow-up

The Skills & tools panel now supports a maintained `addV` response pool, read through MCP once per trusted callback and played at a safe conversational boundary. Verify its audible behavior in a separately authorized real call after deployment. Keep original preset openers and quotation computation settings.

Numeric evidence is in [the analysis receipt](../../docs/telephony/evidence/2026-11-09-event-responses/call-analysis.json). Raw call files remain on the box. Automatic approval review rejected their proposed export to the Mac, so this review used on-box numeric and content-category checks.
