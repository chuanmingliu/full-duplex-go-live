# Simulated coverage replay — bbdb7a3

Call `out-mvumqi79-ghis`, session `sess_9f777687c427f399ef920d97`, run `sim-a9c59117763ca8f0bf6080a9`.
Evidence collected 2026-10-30T07:12:53.242807+00:00. Runtime `bbdb7a364c18422eff3bfaf98259a2689b57725b` passed full
quality, normal and race CI ([run 41016283585](https://github.com/chuanmingliu/full-duplex-go-live/actions/runs/41016283585)).
The existing `kb-coverage-nerd` scenario, saved prompt (SHA prefix `639dcecf24dd`),
quote-plans v7, its five companion configuration snapshots, and the earlier
380c1a7 enablement receipt were preserved. These are simulator/server measurements,
not a physical handset measurement. Milliseconds, nearest-rank p95:

| Measurement | n | p50 | p95 | max |
| --- | ---: | ---: | ---: | ---: |
| First audio, including fillers | 20 | 380.0 | 380 | 380 |
| First non-opener content | 20 | 1000.0 | 1188 | 1238 |
| Linked result content | 0 | — | — | — |
| Explicit quotation readout | 0 | — | — | — |

The strict server gate **passed**: no gate failures.
Both first audio and content require at least 20 valid samples at the 1200 ms target.
Receiver first-audio p95 was 624 ms over
20 valid observations; 17 were excluded.
Receiver observations use the simulator application audio boundary, including openers;
they do not measure handset or carrier playout.

All five archive files passed size and SHA-256 checks. The 297.424-second
stereo recording had signal on both channels, with 28 caller and
25 assistant entries and 28 cached filler events.
Private recordings and conversation text remain outside the repository.
The physical handset target remains unproven.

## Measured outcome

The unchanged coverage scenario produced 20 completed caller responses. First-audio p50/p95/max was 380 ms; content p50 was 1000 ms, p95 1188 ms, and maximum 1238 ms. The recorded-turn strict gate passed, though one response exceeded 1200 ms. No quotation tool ran, so there are no linked-result or quotation-readout samples and this replay does not retest ISS-079.

The slowest response, item_12, consisted of 380 ms end silence, 604 ms to the first token, approximately 58 ms segmentation, and 196 ms first-segment synthesis. The second slowest, item_23, was 380 + 279 + approximately 26 + 503 = 1188 ms. It encountered an account-limit switch and a secondary synthesis request after 320 ms; that 320 ms is part of the 503 ms synthesis interval, not an additional term.

## Conversation and completeness

Twenty-eight finalized caller entries became twenty completed requests after eight prefixes were converted to backchannels. Twenty-five assistant transcript entries comprise the greeting, twenty responses, and four short body fragments from converted prefixes. Those fragments mean interrupted_answers=0 must not be read as no partial speech. Thirty-two turn IDs comprise those twenty answers, one greeting, eight converted prefixes and three abandoned speculations. No orphan, duplicate or incorrectly owned metric row was found. All twenty-eight fillers were cached openers; this is not evidence of live Talker generation.

The conversation covered policy coverage and combinations, tiers, deductibles, renewal and regional underwriting. No new confirmed semantic or tool defect was established; this is not a comprehensive certification of policy accuracy. The regional response followed the saved underwriting branch. The separate ISS-076 claims-provider contradiction remains unresolved and was not directly retested.

The receiver stopped at its configured duration limit. A twenty-ninth utterance began near the end and reached an unfinished partial before disconnection; it was never finalized or answered. This is a cutoff in-progress utterance, not an additional completed latency sample. The run therefore does not establish whole-conversation or closing success. Receiver semantic checks are indeterminate and the receiver transcript is truncated, despite complete originator archive verification.

## Evidence limits

All five archive files were verified, including the 297.424-second stereo 16 kHz recording with signal on both channels. Receiver application first-audio p95 was 624 ms over twenty valid observations, with seventeen exclusions; it includes openers and does not measure handset or carrier playback. The 9000 ms input clock correction is recorder accounting, not a deduction from latency. The saved prompt, scenario and v7 quote policy were unchanged. Physical handset latency remains unmeasured.
