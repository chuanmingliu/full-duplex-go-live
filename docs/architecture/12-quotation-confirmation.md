# Confirming a revised quotation

A caller can change several coverages while discussing an existing quote. A
calculation must wait until the caller has finished adjusting that selection.
The first quotation keeps its current flow. Follow-up policy changes and
hypothetical comparisons both require confirmation of the pricing scope. Quotation calculation and result delivery remain separate from normal
response latency.

## Conversation and state

1. After an existing quote, `quote_total` with a selection change or comparison creates a
   call-scoped pending revision. It returns `awaiting_confirmation` and a
   `revision_id`, without submitting a pricing request or changing confirmed
   quotation memory.
2. The model briefly acknowledges the actual requested changes and asks whether
   the caller wants any other adjustment. It chooses wording from the current
   conversation and avoids repeating the previous confirmation sentence.
3. Additional selected-policy changes merge into the pending revision. Updated
   comparisons replace the pending alternatives and issue a new identifier.
   An explanation or interruption leaves those pending changes available.
4. Only a later caller turn clearly approving that selection permits
   `quote_total` with `revision_action: confirm` and its current `revision_id`.
   A yes to “anything else to change?” means more changes are coming. A no to
   that question can mean the selection is complete. The model must interpret
   the answer in context and clarify ambiguous responses.
5. Confirmation commits a selected-policy change before normal async dispatch.
   A comparison dispatches the confirmed alternatives with the original selected
   policy intact.
   A short contextual acknowledgment is model-generated when calculation is
   actually running. Prices continue to come exclusively from the quote result.
6. `revision_action: cancel` discards pending changes. Expired identifiers,
   same-turn confirmation, or changed quotation inputs cannot execute them.

## Configuration and compatibility

The existing CRM `quote-speech` JSON panel exposes `confirm_revisions`. It is
opt-in, defaults to false for existing configurations, and is read at call
start. Enabled calls expose the additional host-local arguments on
`quote_total`. The host removes them before HTTP or MCP dispatch. Existing
presets, first-quote speech and calculation delays retain their
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

## Follow-up comparison gap found in the real call

Call `out-mwgjkzc6-hpnz` loaded quote-speech version 4 but made no pending
revision. Its follow-up request compared three third-party tiers. The initial
implementation exempted `configs`, so it immediately submitted that comparison.
The owner expected a natural confirmation question before this follow-up quote.

The correction extends the same pending-request gate to follow-up `configs`
and expanded `compare_coverage`. First quotations retain their existing flow.
A comparison confirmation authorizes pricing its alternatives and leaves the
selected policy unchanged. Further comparison edits send the complete updated
`configs`; a `config` explicitly switches to selecting one revised policy.
Pending selected-policy changes carry into a subsequently requested comparison.
Unspecified adjustments require a clarification about supported choices before
staging, such as whether to include or remove vehicle-damage cover.

Regression coverage must first reproduce the comparison bypass, then verify
no early pricing, exact alternative preservation, no selection mutation,
cancellation, stale IDs and confirmation after interruption. A real-model replay
uses synthetic three-tier comparisons, additional edits, an ambiguous reply
and explicit approval. Keep calculation delay, voice and telephony configuration
as configured; measure the new conversational question separately from pricing.

## Maintaining the behavior in the quotation skill

`services/skills-mcp/quotation/SKILL.md` contains the conversation procedure and
paraphrasable examples. Its corresponding `motor-quotation` entry can be edited
in the MCP panel. Publish with the current catalog revision, retaining other
skills, routing rules, tools and prompt settings. The next retrieval observes
the published content without restarting the MCP service.

The engine owns the pending request, revision identity, same-turn guard and
pricing dispatch. Its fallback instructions preserve the confirmation contract
when skill retrieval is unavailable. A style change can live in the skill;
changing the execution contract requires compatible host support.

For a numeric comparison follow-up that omits the coverage name, the host adds
only a nearby coverage label to the routing query. It stops at an intervening
unrelated caller turn. The model receives the original caller words. Catalog
rules also recognize an explicit request to adjust a named coverage.

## Comparison and skill replay verification

The final follow-up replay passed 17 real-model turns with DeepSeek
`deepseek-chat`, the actual quotation skill body, v1 prompt rendering, synthetic
caller prompts and mock audio/pricing. Four baseline turns submitted four
immediate requests. The candidate staged all four initial requests, made no
pricing requests before approval, preserved additional edits through
clarification, cancelled correctly and submitted one three-alternative comparison
without changing the selected policy. The comparison fixture returned distinct
totals and individual premiums for all three alternatives.

Baseline first audio ranged from 539 to 1,166 ms. The four initial confirmation
questions ranged from 762 to 1,868 ms. Their conversational purposes differ,
so this is a behavior comparison rather than evidence of a speedup. Additional
change questions took 1,905 and 1,841 ms. Approved-calculation acknowledgments
took 1,590, 1,906 and 1,408 ms. The mock pricing delay stayed at three seconds.
Question-form variation, contextual consent, pending-state retention and exact
alternative preservation passed. Live configuration and binary hashes were
unchanged by the replay.

The replay fixes skill selection to isolate verified content retrieval and
rendering. Independent catalog/host tests cover routing and bounded coverage
context. Deterministic tests cover interrupted confirmation playback; these
results do not measure acoustic interruption or handset latency.
