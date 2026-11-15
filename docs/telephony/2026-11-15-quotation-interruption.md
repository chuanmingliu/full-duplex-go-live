# Interrupted quotation changes and confirmation accounting

## Implementation plan

The production baseline is `26e0bd7-quote-flow` on Active Call. The real call
`out-mwhad4ty-l7z9` lost an uncommitted medical-rider change after interruption.
The model subsequently described that change as selected while confirming an
older revision. Rule routing missed natural comparison and adjustment phrases.
An input-only confirmation continuation was visible in the detailed trace but
absent from the conversational latency population.

- Retain interrupted, unprocessed coverage-change requests outside trimmed
  conversation history. Require explicit reconciliation through the quotation
  tool before an older revision can be confirmed. Preserve confirmed memory
  and keep pricing behind the existing confirmation boundary.
- Extend contextual routing for natural Chinese comparisons, relative limits,
  coverage questions and confirmation replies. Selection supplies guidance.
  The existing quotation guard continues to authorize pricing.
- Include validated input-only continuation responses once in both Journal and
  Python latency populations. Preserve separate confirmation milestones when
  speech has already started, and retain cancellation and duplicate safeguards.
- Add direct-address guidance to the actual rendered prompt and quotation
  skill. Customer-facing speech uses 您 and omits internal planning narration.
- Run deterministic regressions, real-model simulations and full-media call
  simulations for interrupted additions, accumulated changes, comparisons,
  repeated confirmations and slow-result waiting speech. Report calculation,
  first content and interruption boundaries separately.

Production settings, the saved original prompt, voice, presets, quotation delay
and the deployed rollback bundle remain the baseline. Simulation services use
isolated ports and storage. A repository push is separate from deployment.

## Runtime contract

A committed coverage-change utterance is retained as soon as the final caller
transcript is accepted. Hard interruption and finish-sentence interruption also
retain any outstanding request before cancelling its answer. The request
has the original turn ID and caller text. This session-local list survives
history trimming and leaves confirmed policy memory untouched.

The next backend prompt includes the unresolved requests alongside the pending
revision. `quote_total` accepts `resolved_request_ids` with a `config` or `configs`
update. A successful stage clears the explicitly handled requests, preserves
other pending changes and requires a fresh confirmation. An unresolved request
blocks confirming an older revision and blocks empty or unchanged pricing calls.
Explicit cancellation can clear pending adjustments, including a request that
was interrupted before any pending revision existed.

Capture currently recognizes explicit Chinese coverage names and change intent.
The model still interprets the request, resolves relative limits against the
active catalog and chooses the correct configuration fields. The guard verifies
request IDs, explicit coverage keys in every submitted variant and revision
sequencing. Rider names are matched before their parent coverage names. Semantic correctness remains part of the
scenario tests.

## Routing and speech

The quotation exclusion previously matched the character 别 inside 分别对比.
The revised rule recognizes refusal contexts while allowing 分别对比. Nearby
coverage context also supports natural questions about other tiers, highest
limits and pending confirmation responses.

The host backend prompt and quotation skill instruct direct address with 您 and
suppress internal planning narration. The saved original prompt remains a host
component. The preset opener and coordinated answer flow remain enabled.

For an existing MCP catalog, updating the binary alone does not replace published
operator content. A future deployment must merge the new quotation match rules,
replace the known old refusal exclusion and publish the updated quotation skill
while preserving custom rules and the previous catalog snapshot.

The full-media simulator also exposed a conflict between the retained quotation
readout and the newly staged comparison. The readout prompt now ends with the
current pending revision and its calculation status. It directs the model to
interpret consecutive caller fragments together and submit an explicit
confirmation before describing calculation as running. Short replies such as
“你算吧”, “就这三档”, “其他不动” and “我等着” route to quotation guidance when
there is a pending revision. Routing alone does not authorize a calculation.

Generated waiting acknowledgments omit tool definitions and skip the redundant
MCP selection request. They retain the original prompt and add a speech-only
instruction for the already-running task. Caller turns continue through normal
skill selection and tool execution. This prevents the acknowledgment itself
from repeatedly confirming the same quotation. The confirmation note also
distinguishes a new caller turn from the continuation of a staging tool result.

## Latency evidence

Journal evidence version 7 adds validated input-only synchronous continuations
to conversational first-content and first-audio distributions exactly once per
caller origin. First audio and first content retain their independent earliest
boundaries across child responses. Duplicate links, conflicting caller clocks
and origin output without a terminal metric remain unverified.

A continuation that follows an already audible answer remains a separate
milestone. Its later confirmation question does not replace the first-content
measurement. Asynchronous quotation calculation and result readout retain their
separate measurements.

In the prior real call, turn 12 contained the caller's “嗯，好的。” and 1,269 ms
to first content, comprising 380 ms end silence plus 889 ms output delay. The next
caller utterance, “可以可以，就这样吧。”, interrupted the assistant after 420 ms
of playback. The stored “这个方案您觉” is the interrupted prefix. The trace shows
135 ms from the floor hold to yielding. This evidence does not establish missing
transcription of a completed sentence.

## Validation

The diagnostic full-media simulator call `95bdabce-0e54-42c5-add1-574ae2eba7e7`
ran for 297 seconds. Its archive verified all five file hashes, 37 caller entries,
45 assistant entries and signal in both recorded audio channels. The deployed
profile and saved original call settings matched their pre-test hashes.

| Server-output measurement | Samples | Median | p95 |
| --- | ---: | ---: | ---: |
| Normal first content | 31 | 987 ms | 1,602 ms |
| All first audio, including openers | 34 | 380 ms | 381 ms |
| Quotation readout | 4 | 1,185 ms | 7,084 ms |

These figures do not establish a latency improvement or the 1,200 ms target.
The baseline simulator run ended early before covering the same
business sequence. Its archive was recovered and verified after a conflicting
older Journal listener was identified. The comparison is therefore limited to
functional regressions and separately labelled timing observations.

The diagnostic run used candidate `quote-reconcile-r4`. It exposed a second
retention boundary. A completed reply could promise the rider without staging it,
so interruption-only capture did not protect a later confirmation. The final
implementation records the request when the final transcript is committed.
The final candidate also
normalizes 座位 into 座位险 for skill selection and moves static direct-address
guidance into the stable system prompt, preserving tool-result ordering. Those
final changes are covered by the subsequent real-model and regression runs.

Candidate `quote-reconcile-r10` with the complete synthetic motor catalog passed 30 conversational
steps and 208 assertions. It verified accumulated changes, explicit confirmation,
three-way comparison, repeated confirmation, cancellation, interruption recovery,
medical-rider reconciliation and direct address for an unsupported 500万 tier.
The model evidence labels its confirmation-disabled fixture as `baseline`.
That comparison isolates confirmation-flow behavior. A before/after performance
claim for the deployed binary requires matched completed media runs.
The model executor retains a separate three-second synthetic quotation delay.
Its TTS is synthetic, so its audio timing is an engine test measurement.
The harness waits for paced playback to drain before the next ordinary turn.
The explicit interruption case still cuts the waiting response during playback.

One diagnostic candidate repeated five staging calls in a rider-reconciliation
step and took 6,190 ms to first audio. The staging result now describes the
completed action and the waiting acknowledgment has no tools. In the final
model run, the rider-reconciliation response began at 1,274 ms, confirmation of
accumulated changes at 2,359 ms, and confirmation of a comparison at 2,092 ms.
These include model/tool round trips and synthetic TTS. The fixtures evolved to
include old quotation readouts and complete playback, so these observations do
not establish a controlled before/after speedup.

The full local Go suite passed, including the 693-second duplex package. Final
quotation regressions, targeted race checks, the Journal race suite, MCP race
tests, 67 Python latency tests and Go vet also passed.

Paced playback regressions confirm that an actual interruption stops waiting
speech without late audio, while result readiness allows the current waiting
segment to finish. The acknowledgement fixture played 1,540 ms naturally. With
a result ready at 610 ms, its remaining wait was 930 ms. These are fixed-fixture
regression values, separate from the full-media and handset measurements.

A subsequent diagnostic call `48e67870-73f6-4ee1-9188-333a0ec058d3`
verified both audio directions and archive hashes but stalled at comparison
confirmation. It produced one initial quotation and repeatedly claimed to await
new prices. Its normal-content median was 1,213 ms and p95 was 1,619 ms. This
failed business-flow test led to the readout/confirmation prompt correction.
The real-model fixture now includes an existing quotation delivery, matching
that production interaction, and tests fragmented confirmation wording.

The final full-media call `7900ae5a-28c7-4cab-bf6e-509632c8e7f8` used
`quote-reconcile-r10` for 297.1 seconds. It retained the medical rider through the
seat-coverage interruption, then confirmed and priced it with the already
selected 400万 third-party limit. The spoken rider premium was 760元. The
200万/300万/400万 comparison completed, and the final question about the two
400万 limits recalled both coverages without another calculation. Archive
verification covered all five file hashes, 36 caller entries, 40 assistant
entries and signal in both audio channels.

| Final server-output measurement | Samples | Median | p95 |
| --- | ---: | ---: | ---: |
| First audio, including preset openers | 21 | 380 ms | 381 ms |
| Normal first content | 18 | 1,276 ms | 1,627 ms |
| Quotation readout first content | 4 | 4,138 ms | 7,697 ms |
| Floor hold to interruption yield | 7 | 295 ms | 699 ms |

The diagnostic and final calls used the same scripted goals but produced
different conversations. Normal-content p95 changed from 1,602 ms to 1,627 ms.
This evidence supports the business-flow corrections, while normal-response
latency remains above the 1,200 ms target. The interruption interval begins at
the logged floor hold, so it excludes earlier acoustic speech detection.
These are server measurements and exclude handset playout.

The uncached medical-rider calculation took 3,506 ms inside the CRM and 3,525 ms
as an engine task. Its ready result waited another 1,667 ms for delivery. Cached
tasks took 2–4 ms. These phases remain separate from normal conversational
responses and from the 7,697 ms quotation-readout interval.

The committed `scenario.json` contains synthetic caller behavior. Its
`retain_audio=false` controls the simulator's own retention. The isolated
GoLive/Call Journal recorder retains both audio directions independently using
the copied original recording settings.

## Rollback and operation

Production remains on `26e0bd7-quote-flow`. The existing rollback directory is
`/home/chuanming/golive-telephony/codex-latency/releases/quote-flow-26e0bd7/rollback`.
The simulator used separate ports, storage and copies of the original CRM,
opening, speech, hangup, silence and MCP settings. No production deployment or
real call is part of this change.
