# Metro call — coverage catalog grounding

- Call `out-mwdh1b7g-p66i`, real Active Call, original GoLive configuration.
- Build `951a6b4`; call duration 169.3 seconds on 2026-11-12.
- Owner said done before analysis. No redial or service restart performed.
- Owner reported that metro background noise did not disrupt the conversation.
  This is qualitative feedback, not a controlled noise-rejection benchmark.
  The stored recognition transcript contains some wording errors; their cause
  cannot be isolated to noise from this review.

## Recorded evidence

The GoLive review MCP operation verified all five archive file hashes, session
closure, 15 caller and 17 assistant transcript entries, and signal in both
channels of the 170.68-second stereo recording. Signal presence alone does not
establish intelligibility or acoustic handset timing.

| Measurement | Samples | Median | p95 / maximum |
|---|---:|---:|---:|
| Normal first content, server output | 12 | 1,032.5 ms | 1,469 ms |
| First audio including openers | 14 | 380.5 ms | 727 ms |
| VAD utterance open to server playback cut | 3 | 137 ms | 554 ms |
| Quotation acknowledgment | 1 | 1,156 ms | 1,156 ms |
| Quotation result/readout | 2 | 2,365 ms | 3,582 ms |

Interruption samples were 554, 128 and 137 ms. There were no unpaired cuts,
one kept acknowledgment, zero detector assists and zero detector fallbacks.
The cut boundary excludes pre-VAD detection and downstream buffered playback.

The normal-response gate failed, with fewer than 20 normal turns and p95 above
1,200 ms. No acoustic handset measurement is included. Quotation result timing
includes more than calculation; no calculation-only timing is established by
this report. There were no missing or unverified quotation readouts.

Analyzer SHA-256
`c5df3bf2a120d4bcc241f75a1e2c28b3205a903dd94b33516fa21e11ea549a50`.
Numeric evidence is in
`docs/telephony/evidence/2026-11-12-metro-call/analysis.json`.

## Coverage failure

The selected live CRM default was the legacy `standard` plan. Its coverage
list contained no medical-outside-social-insurance rider, and its configured
tier tables covered only third-party liability and personal accident coverage.
The expanded `motor-full` catalog used by isolated simulations was not active
on this real call.

The assistant nevertheless supplied a short medical-rider tier list and
presented it as exhaustive. It also deferred to a quotation page instead of
answering the caller. This is an unsupported product-option claim, not merely
a slow or incomplete quotation calculation. Raw conversation stays private
in the existing archive.

## Local follow-up changes

- Render instructions to ground options in the active CRM catalog, enumerate
  the complete requested list, and explicitly state when configuration lacks
  the information. Inquiry alone must not change the selected policy.
- Distinguish third-party, driver and passenger medical riders and shared
  versus independent limits. Do not duplicate a hard-coded tier table in the
  knowledge skill.
- Extend concepts-skill routing for shortened medical-rider names and requests
  for all available options.
- Regression tests cover full medical option rendering, changed configuration,
  legacy missing options and seven additional knowledge-query formulations.

Focused tool-rendering tests and MCP publication/routing tests pass, as does
`git diff --check`. These checks establish prompt data and routing, not model
obedience. The new instructions and expanded catalog remain local and have
not changed the running service. A real-model replay and explicit catalog
rollout are still required before claiming the spoken behavior is corrected.
