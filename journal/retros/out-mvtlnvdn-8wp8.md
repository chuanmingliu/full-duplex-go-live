# Simulated PBX verification — 2026-10-29, d784730

Call `out-mvtlnvdn-8wp8`, run `sim-bcd3029ade221723404055ba`, scenario
`kb-coverage-nerd`, receiver `5128a8f`. The stored prompt and scenario hashes
match the baseline call `out-mvtjljxn-28d1`.

| Measurement | Samples | p50 | p95 | Maximum |
| --- | ---: | ---: | ---: | ---: |
| Last sound → first audio leaving golive | 26 | 380 | 381 | 381 |
| Last sound → answer content leaving the player | 26 | 875.5 | 1026 | 1127 |
| Simulator customer-output boundary → first received assistant audio | 25 valid | 586 | 635 | 703 |

All timings are milliseconds. p95 uses nearest rank. The strict server-log
gate passed for both metrics with a 1200 ms target and minimum 20 answered
turns. There were 27 rows: 26 answers and one non-speech turn; no missing,
unverified, malformed, duplicate, or cancelled records. No measured value
reached 1200 ms. The receiver excluded 16 observations from its valid gap
set; no additional debounce adjustment was applied.

First audio includes cached openers. This is a simulated call through the
PBX, without a GSM carrier or handset. The receiver's first-audio p95 was
635 ms versus the baseline's 640 ms; a five-ms difference in one pair of
calls is not evidence of a sustained improvement. Current content timing
marks emitted audio; the baseline used a readiness/opener-duration estimate,
so its 1142 ms content p95 is not directly comparable. No primary TTS startup
stall was deliberately injected; the fallback deadline fix is proven by its
regression, not by a claimed live tail-latency reduction.

## Deployment and checks

Runtime commit `d784730294b1ab13895b5b43cbb35fc2f0d03468` passed full normal
and race suites, formatting, vet, 23 Python reporting tests, build and
vulnerability checks in GitHub Actions run `40883399469`. The box deployed
that exact build through the Mac runner. The previous executable and replaced
files are retained in `~/golive-releases/codex-d784730-20261029`.

Main service and browser demo were idle before restart. Active profiles,
provider settings, eight main model connections, demo limits, journal paths,
and recording/720-hour retention were preserved. Complete phrase and
scenario-opener warming was confirmed in one stable process before dialing.

The call ended at the configured 295-second duration limit. Four lifecycle
checks passed; automated semantic checks remain indeterminate and the
receiver's transcript report is truncated. Those checks do not certify
business-answer correctness or a natural conversational ending.

The permanent archive independently contains 35 caller and 30 assistant
transcript entries and 296.932 seconds of stereo audio, with signal on both
channels. All five required files passed size and SHA256 checks. Archive-wide
verification checked 642 files without mismatches. The Codex desk work log
records the deployment and archive verification without conversation content.

## Changes verified

- ISS-061: TTS startup is included in the existing fallback deadline; provider
  and race regressions pass. Ordinary unstalled voice policy is unchanged.
- ISS-062: explicit output presence and actual content-output timestamps
  prevent queued-but-interrupted answers from passing the latency gate.
- ISS-063: application tool registration captures a stable batch; the new
  regression and full race suite pass. This scenario does not independently
  exercise every application tool-call path.

The WebSocket test fixture cleanup fix in `33aa3f2` is test-only; it uses the
existing server drain API before removing temporary recordings. It does not
change the deployed runtime. Full normal and race suites and quality checks
also passed on that commit in GitHub Actions run `40884942464`.

## Conversation review limits

Review of the complete archived transcript found no confirmed skipped
question or repetition loop: several short fragments were answered with the
caller's subsequent completion. The duration limit explains the ending.
One final access-workflow answer remains unverified. The saved instructions
do not establish that workflow, and the reviewed tool metadata does not
include returned knowledge payloads or generated tool descriptions. This is
not enough evidence for a business-fact correction or a claim that every
answer was grounded. Private conversation text is retained only in the
archive and local runner output.
