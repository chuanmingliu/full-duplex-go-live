# Confirming a revised quotation

A caller can change several coverages while discussing an existing quote. A
calculation must wait until the caller has finished adjusting that selection.
The first quotation and explicit hypothetical comparisons keep their current
flow. Quotation calculation and result delivery remain separate from normal
response latency.

## Conversation and state

1. After an existing quote, `quote_total` with a selection change creates a
   call-scoped pending revision. It returns `awaiting_confirmation` and a
   `revision_id`, without submitting a pricing request or changing confirmed
   quotation memory.
2. The model briefly acknowledges the actual requested changes and asks whether
   the caller wants any other adjustment. It chooses wording from the current
   conversation and avoids repeating the previous confirmation sentence.
3. Additional changes merge into the pending revision and issue a new identifier.
   An explanation or interruption leaves those pending changes available.
4. Only a later caller turn clearly approving that selection permits
   `quote_total` with `revision_action: confirm` and its current `revision_id`.
   A yes to “anything else to change?” means more changes are coming. A no to
   that question can mean the selection is complete. The model must interpret
   the answer in context and clarify ambiguous responses.
5. Confirmation commits the accumulated selection before normal async dispatch.
   A short contextual acknowledgment is model-generated when calculation is
   actually running. Prices continue to come exclusively from the quote result.
6. `revision_action: cancel` discards pending changes. Expired identifiers,
   same-turn confirmation, or changed quotation inputs cannot execute them.

## Configuration and compatibility

The existing CRM `quote-speech` JSON panel exposes `confirm_revisions`. It is
opt-in, defaults to false for existing configurations, and is read at call
start. Enabled calls expose the additional host-local arguments on
`quote_total`. The host removes them before HTTP or MCP dispatch. Existing
presets, first-quote speech, comparisons and calculation delays retain their
configuration. Disabling the option restores the earlier revision flow.

The host stores pending changes outside confirmed `update_memory` state so they
survive history trimming without contaminating a price request. It rejects
attempts to bypass revision confirmation by changing `memory.quote` directly
once a quote exists. Other working-memory fields retain their existing rules.

## Validation plan

Test no pricing request before approval, several accumulating changes,
confirmation after interruption, cancellation, stale/same-turn identifiers,
input changes, duplicate confirmation, first quotations and comparisons.
Run synthetic real-model conversations to check contextual consent and varied
confirmation phrasing. Compare request counts and first-content timing against
the earlier flow. No handset latency claim follows from a mock-audio replay.

## Tool examples

The proposed change uses the existing tool name.

```json
{"name":"quote_total","arguments":{"config":{"三者险":"300万"}}}
```

With `confirm_revisions` enabled and an existing quote, the result contains
`status: awaiting_confirmation` and a `pending_revision` object. Its identifier
belongs to this call and this accumulated selection.

```json
{"name":"quote_total","arguments":{"revision_action":"confirm","revision_id":"revision-1"}}
```

The host accepts that confirmation on a later caller turn with unchanged
pricing inputs. It strips both host-only fields, commits the accumulated
`config` to quotation memory, and submits the existing pricing request. A second
use of the same consumed identifier returns `already_confirmed` without another
submission. `cancel` removes the pending revision and keeps the prior plan.

When pricing inputs change during confirmation, the pending selection remains
available, but its old identifier cannot authorize calculation. Restaging issues
a new identifier against the updated inputs, followed by another confirmation.
The `wechat.status` callback alone does not invalidate pricing input identity.

The conversation model interprets the caller's meaning. The engine enforces
selection identity, turn separation and freshness. Keyword matching alone does
not authorize calculation. A generic affirmative to an unrelated question must
not be treated as consent.

## Speaking without an extra generation round

The model can briefly recap the changes and ask its confirmation question in the
same response that stages `config`. The engine records the tool exchange and
skips a duplicate continuation only when that question completed natural
playback and the tool returned the current pending revision. An unfinished
question, interrupted playback or failed staging takes the ordinary continuation
path. Once approved, the model generates a short contextual acknowledgment;
the fixed first-quote prehook remains available under the existing configuration.

## Simulation evidence on 2026-11-14

Three isolated real-model runs used DeepSeek `deepseek-chat`, synthetic caller
turns, mock audio and a three-second quotation backend. The final run passed
all checks across three baseline turns and nine confirmation-flow turns.
The original profile, saved call settings and live binary hashes stayed equal.

The three initial confirmation questions started model text at 468, 247 and
1,272 ms, and mock audio at 619, 405 and 1,447 ms. The baseline submitted
three immediate calculations; the candidate submitted none until approval.
Additional changes, a contextual “有”, explanation, cancellation and later
approval all preserved the expected request count and selection. Two approved
calculations started at 929 and 505 ms; their generated acknowledgments began
mock audio at 1,626 and 1,276 ms. These acknowledgment tails still need work.
The three-second calculation delay was retained.

These small samples measure different conversational actions and synthetic
audio. They do not establish a handset latency improvement. Deterministic
regressions separately cover interrupted question playback, stale identifiers,
input changes, idempotence, unrelated tool results and dispatch ordering.
