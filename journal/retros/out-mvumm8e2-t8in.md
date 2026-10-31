# Simulated quotation replay — bbdb7a3

Call `out-mvumm8e2-t8in`, session `sess_1d19e327b0fb094ff4e31177`, run `sim-90b6a0e4fb927cdc9ed98f60`.
Evidence collected 2026-10-30T07:12:53.242807+00:00. Runtime `bbdb7a364c18422eff3bfaf98259a2689b57725b` passed full
quality, normal and race CI ([run 41016283585](https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/41016283585)).
The engine, CRM and journal matched their reviewed executable hashes.
The existing quote-plans v7 and its five companion configuration snapshots
were preserved; the earlier 380c1a7 enablement receipt remains unchanged.

The existing `sim-prequote-hit` scenario and saved prompt (SHA prefix
`639dcecf24dd`) were unchanged. These are simulator/server measurements,
not a physical handset measurement. Milliseconds, nearest-rank p95:

| Measurement | n | p50 | p95 | max |
| --- | ---: | ---: | ---: | ---: |
| First audio, including fillers | 5 | 380 | 380 | 380 |
| First non-opener content | 5 | 1016 | 1231 | 1231 |
| Linked result content | 3 | 1061 | 1231 | 1231 |
| Explicit quotation readout | 3 | 1061 | 1231 | 1231 |

The targeted quotation diagnostic **passed**. The strict server gate
**failed**: first_audio: 5 samples, need 20; first_content: 5 samples, need 20; first_content: p95 1231 ms is not below 1200 ms; tool_result: p95 1231 ms is not below 1200 ms; quote_readout: p95 1231 ms is not below 1200 ms.
Receiver first-audio p95 was 537 ms over
4 valid observations; 9 were excluded.

All five archive files passed size and SHA-256 checks. The 79.126-second
stereo recording had signal on both channels. The archive retained
7 caller and 9 assistant entries and
7 cached filler events. Private recordings and conversation
text remain outside the repository. The physical handset target is unproven.

## Measured outcome

The three quotation readouts were 1016, 1231 and 1061 ms. All were explicitly observed, with unique caller/result links and guarded posthook omission. Pricing tasks took 0/1/1 ms. The strict server gate fails both the five-versus-twenty sample requirement and the second readout's 1231 ms latency. First-audio p95 remained 380 ms, including fillers.

The second result had 380 ms of end silence, 680 ms from pause to result delivery, then 171 ms to readout output. Compared with the preceding replay, its delivery interval grew by 68 ms while synthesis improved by 29 ms. Model duration increased by 39 ms and speculative overlap fell by 29 ms. One replay cannot establish a code-caused regression. The third result remained prequoted; its 4509 ms backend computation finished approximately 3.96 seconds before demand. The quote, prompt and scenario configuration were unchanged.

## Conversation and accounting

The total-repeat change helped but did not resolve ISS-079: item_10 gave the correct total first, then named all six coverages. It stopped repeating the individual premiums, but the caller had not requested a coverage list. ISS-079 remains open. All three spoken totals matched authoritative task results; the second selection change and third added coverage were preserved, and all six final line amounts matched the source. No new pricing or selection-continuity defect was found.

Seven caller entries form five answered requests because two questions were split across recognition events. item_2 and item_9 became backchannels; item_3 and item_10 answered the joined requests. Answered IDs were item_3, item_5, item_7, item_10 and item_12. The abandoned closing speculation item_11 produced no extra metric. Nine rows comprise five caller responses, three result deliveries and one greeting, with no missing, duplicate, interrupted or unverified response rows. This supports the deployed accounting but does not exhaustively exercise every ownership race.

Closing worked for this explicit farewell: end_call was accepted at 15:05:13.721, farewell playback completed at 15:05:14.943, and hangup followed at 15:05:15.724 Asia/Shanghai. The receiver confirms native remote hangup. Its semantic checks remain indeterminate, so zero listed failures are not semantic validation.

The complete 79.126-second stereo recording and all five archive files were verified. Receiver first-audio p95 was 537 ms over four valid observations, with nine exclusions; it includes fillers and is not a handset content measurement. Physical handset latency remains unverified. The subsequent coverage replay out-mvumqi79-ghis passed the 20-response server p95 gate at 1188 ms, but did not exercise quotation tools or physical handset playback. Further latency work remains necessary.
