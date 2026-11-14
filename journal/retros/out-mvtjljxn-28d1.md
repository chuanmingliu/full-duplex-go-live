# Simulated PBX baseline — 2026-10-29, b1c4f5f

Scenario `kb-coverage-nerd`, receiver `5128a8f`, run
`sim-d7e8b8de710b6fb862e89148`. The existing stored prompt was preserved.
The running service was `b1c4f5f`; the bundle VERSION file said `3a85def`,
so the running health version is the identity used for this comparison.

| Measurement | Samples | p50 | p95 | Maximum |
| --- | ---: | ---: | ---: | ---: |
| Last sound → first audio leaving golive, legacy log estimate | 29 | 380 | 381 | 381 |
| Last sound → first content leaving golive, legacy log estimate | 29 | 920 | 1142 | 1191 |
| Simulator customer-output boundary → first received assistant audio | 27 valid | 586 | 640 | 648 |

All timings are milliseconds. p95 uses nearest rank. The receiver excluded
22 observations from its valid gap set; no additional debounce adjustment was
applied. First audio includes cached openers. The simulator crosses the PBX
but does not include a real GSM carrier or handset. This single run does not
establish a sustained population p95.

The old service emits no explicit audio-presence or speech-origin flags:
the new strict server-log gate therefore correctly refuses to certify its
legacy rows, even though their reported values are below 1200 ms.

The call ended through remote hangup. Four lifecycle checks passed; semantic
checks were indeterminate. The receiver's transcript report was truncated,
so it is not evidence of conversation correctness or a full transcript.
The permanent archive independently contains 37 caller and 34 assistant
transcript entries and 288.839 seconds of stereo audio, with signal on both
channels. All five required files have verified sizes and SHA256 checksums;
the archive-wide verification checked 637 files without mismatches.

## Changes to test next

- ISS-061: a primary TTS startup stall must count toward the fallback deadline.
  Its deterministic regression fails before the fix and passes after it;
  no such stall was deliberately injected into this baseline call.
- ISS-062: retain zero-ms cached openers, report content separately, and
  distinguish absent timestamps from actual instant output. The retrospective
  script now shares the same numeric analyzer as the acceptance command.

Repeat the same stored prompt and scenario on the candidate, verify its
running version and archive, and compare both server and receiver metrics.
Do not attribute ordinary provider variation to the startup-stall fix.
