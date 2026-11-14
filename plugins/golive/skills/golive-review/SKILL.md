---
name: golive-review
description: Analyze a completed GoLive call after the owner says done, verify chat and stereo audio in Call Journal, and compare normal response and interruption latency while keeping quotation timing separate.
---

# Review a GoLive call

Bind the exact call ID from this conversation. If it is missing, use
`golive_list_calls` with bounded pages and identify the requested call from its
time/build/type. Do not silently replace it with the latest call.

Queue `golive_get_call` and poll the returned request ID. For real calls the
owner must have said `done` for this call. Set `owner_done=true` in
`golive_analyze_call` only when that signal exists. Simulations may be reviewed
when ended. If the archive is still pending, poll metadata before analyzing.

Poll the analysis request and check `result.ok`. Analysis verifies five archive
hashes, session closure, both transcript speakers and non-silent stereo channels.
This proves stored evidence, not intelligible audio or acoustic handset latency.
Raw transcripts and audio stay on the box; use Journal playback for an authorized
qualitative review. Never copy private content to GitHub or public demos.

Report the following evidence together

- Build, call ID, real/simulated route, analyzer hash and verified recording status.
- Normal first-content sample count, p50, p95 and maximum. First audio includes
  openers and fillers and can precede meaningful response content.
- VAD-utterance-open to server playback-cut samples, p50/p95/max, unpaired cuts,
  kept acknowledgments and detector assists/fallbacks. This boundary excludes
  pre-VAD delay and downstream queued audio; it is not total acoustic interruption delay.
- Quotation acknowledgments, result completion and readout separately. Preserve
  configured quotation calculation delay. Result timing includes more than calculation;
  a calculation-only breakdown requires the existing private CRM phase analysis.
- Strict gate status and missing evidence. The existing canonical analyzer can
  include quotation acknowledgments in its conversational population. The plugin's
  normal-only population additionally excludes quotation-origin turns. Compare
  like populations and require at least 20 normal responses for the normal target.

Default to server-output measurements. Only request `handset_estimate=true` if
the owner wants their optional +50 ms assumption. Label it an estimate.
Actual handset verification needs a separate both-sided acoustic capture.

For comparisons, review both exact call IDs and show their sample counts and
builds. Different conversations or two small populations do not establish a
causal improvement. Preserve unfavorable tails and failures. `done` never
authorizes redialing, owner-feedback edits or messages to other operators.
