# Quotation interruption recovery

The optional engine setting `duplex.quote_interruption_recovery` retains the
progress of an interrupted quotation across clarification questions, skill
changes and conversation-history trimming. Enable it in a test profile with

```json
{
  "duplex": {
    "quote_interruption_recovery": true
  }
}
```

The default is `false`. The saved host prompt, quotation speech templates and
quotation calculation settings continue to supply their existing content.

## State and ownership

`internal/duplex/quote_recovery.go` retains the latest delivered quotation task,
its pricing inputs, ordered readout sections and selected explanation scope.
Working memory still owns the confirmed coverage selection. The task result
owns the prices. Playback receipts own completion progress.

A readout is divided at sentence or semicolon boundaries. Commas stay inside a
section so a coverage, its premium and its total remain together. There are at
most 64 sections, with any remaining text retained as the final section.

Each section has an ID, text, completion flag and selection flag. The delivery
has one of these statuses.

| Status | Meaning |
| --- | --- |
| `queued` | The initial quotation is scheduled for speech |
| `interrupted` | Playback stopped with unfinished sections |
| `pending` | Sections remain available for continuation |
| `paused` | The caller requested a pause |
| `cancelled` | The caller cancelled this explanation |
| `complete` | All selected sections have played |
| `superseded` | A newer quotation or changed pricing input invalidated continuation |

The engine accepts progress only from a playback receipt bound to the same
quotation and control revision. Generation and turn validation also run through
the existing playback-completion path. A delayed receipt from an earlier
selection cannot complete a newly selected explanation. Duplicate receipts are
handled by the turn's existing completion guard.

An interruption drains pending playback receipts before the next request is
constructed. A late truncation can revoke completion attributed to that turn,
while retaining sections completed by earlier turns. A control arriving after
its spoken response has finished reconciles the saved scope with that response's
played text.

The complete normalized section text must appear in the played prefix to mark
it complete. Whitespace and selected punctuation are normalized. Decimal points
are retained. A partial amount leaves the section unfinished. Paraphrases can
therefore cause a short section to be repeated. Server playback provides the
progress estimate available to this mechanism.

## Prompt and skill cooperation

A dynamic `golive_quote_delivery` system message carries the current delivery
status and sections. It follows the selected supplementary skill content. The
original prompt and authoritative quotation facts remain in their existing
positions.

For a clarification such as “医保外用药是什么意思？”, the response model first
answers the question using the appropriate skill and then returns to the next
unfinished section. An explicit request to finish the comparison asks for the
remaining selected sections. An unrelated question retains the pending quote
for a later return. A request for one individual premium receives that premium.

For a recognized concept clarification with
`skill://vehicle_insurance_concepts/SKILL.md` loaded, the model generates the
explanation and the engine appends the next unfinished section to the same
speech stream. “继续” or “接着” requests append all remaining selected sections.
The model receives a coordination instruction to leave that transition and its
amounts to the engine. A final state check suppresses this suffix when the quote
was paused, cancelled, completed, superseded or assigned a newer control scope.
Tool calls and failed model responses also suppress it.

This coordinated path recognizes explicit concept-question wording such as
“是什么意思” and “保障什么”, and excludes pause/cancel wording. Other questions
retain prompt-guided recovery. The concept skill must load successfully for the
coordinated path. This uses the existing skill selection and the same model
response, with no additional inference request for the quotation suffix.

The model is instructed to use the section text when continuing a quotation so
coverage limits and prices stay associated. Ordinary clarification and
continuation use the normal response generation. There is no separate decision
model or external decision request.

The existing task-result note now describes queued speech accurately. Its
instruction to avoid duplicating the deterministic prefix applies to that
result response. It does not declare queued text fully played or prohibit
prices in later answers.

Quotations without an opening are retained too. When a quotation has no fixed
readout template, its authoritative result and interrupted prefix are available
for free-form recovery. Precise section progress is unavailable for that case.

## Explicit controls

When the feature is enabled and `quote_total` exists, the engine adds the local
`quote_delivery` tool. The tool takes the task ID from the current delivery.

```json
{"task_id":"t1","action":"pause"}
```

```json
{"task_id":"t1","action":"cancel"}
```

```json
{"task_id":"t1","action":"resume","section_ids":[2,3]}
```

`resume` without IDs retains the current scope and continues unfinished
sections. Specified IDs select those sections and permit repeating them. Invalid
IDs, duplicate IDs, obsolete tasks and calls from dead turns fail before state
changes. These controls change explanation state. Confirmed insurance choices
continue to use `quote_total.config` or `update_memory` under their existing
contracts.

The response model must issue a control call when the caller explicitly pauses,
cancels or narrows the explanation. These actions use the existing local tool
continuation path and can require an additional completion. Simulation reports
include their response timing separately from ordinary clarification.

An explicit Chinese request beginning with “只说”, “只讲” or “只介绍”
(optionally preceded by “请”) and referring to a “档” requires
`quote_delivery` on its initial model request. This closes the observed case
where the model spoke the requested tier but omitted its scope update. The
model selects the section IDs from the current readout. Tool-result
continuations return to normal tool selection. An explicitly configured tool
policy takes precedence. Other phrasing relies on the model's semantic control
selection, so live wording should remain part of acceptance testing.

When the same response already contains a pause/cancel acknowledgement, or all
the requested continuation sections, the engine saves the control and finishes
without another model completion. Errors and incomplete answers continue
through the normal tool-result path. A delivered asynchronous quotation also
retires its obsolete `running` continuation, preventing a delayed waiting phrase
from restarting after the result.

## Freshness and observability

Recovery uses the existing quotation-input comparison before exposing or
updating progress. A changed coverage selection supersedes the old readout. An
`addV` update alone retains valid prices through the existing WeChat-state
exception. A newer delivered quotation replaces the prior delivery.

Call Journal displays `quote: delivery progress` records in the Quotation
section. Each includes the task, turn, pricing memory version, status, reason,
completed section IDs, remaining section IDs and interruption count. The trace
omits spoken content and customer values. Use the transcript and both recorded
audio directions to review what was actually said.

Disable the profile flag to return to the existing recent-result/prefix recovery
policy. Deployments should keep their original profile and executable snapshot.

## Verification

The deterministic suite exercises repeated interruption halfway through a
premium, immediate follow-up prompt construction, retained progress after
history trimming, stale receipts, scope changes, late truncation, decimal
preservation and quotation freshness. The paced engine simulation checks that
the quotation executes once and that cancelled audio stops while subsequent
responses finish the remaining sections.

Run the local checks with

```sh
go test -race -timeout 20m ./internal/duplex ./internal/journal ./internal/config
go vet ./internal/duplex ./internal/journal ./internal/config
node --check internal/journal/ui/app.js
```

`TestQuoteRecoveryRealModel` is an opt-in replay using `QUOTE_RECOVERY_REPORT`,
`SKILLS_REAL_PROVIDER`, `COVERAGE_PROMPT_FILE`, `SKILLS_SIM_URL` and
`SKILLS_SIM_TOKEN_FILE`. Supply provider credentials through the private process
environment. The saved prompt file contains the `instructions` field. Use an
isolated Skills MCP fixture with the vehicle-insurance concept skill. The report
contains full model traces and must remain private.

The replay compares the feature disabled and enabled on the same candidate
build, with synthetic quotation facts, real model inference and mock TTS. It
checks explicit and implicit clarification, pause/resume, cancellation followed
by clarification, selection of one tier and a revised quotation. The revised
quotation fixture retains its three-second calculation delay. Response-start
timing and calculation/result completion are separate measurements.

These simulations measure server behavior. Acoustic handset latency and live
caller interpretation require a subsequent real-call test.

### Results from 2026-11-14

The final real-model replay used DeepSeek Chat and the saved original host
prompt. All eight enabled recovery scenarios passed. The disabled comparison
missed the next unfinished premium on both clarification scenarios.

| Scenario | Disabled, first output ms | Enabled, first output ms |
| --- | ---: | ---: |
| Clarification with explicit continuation | 991 | 781 |
| Clarification without asking again to continue | 1,000 | 647 |
| Pause | 550 | 1,057 |
| Cancel | 693 | 732 |
| Narrow to one tier | 492 | 1,323 |
| Revised quotation acknowledgement | 1,480 | 1,379 |

Resume after pause took 840 ms and clarification after cancellation took 674 ms.
The latter respected the cancelled quotation. Existing prices were reused for
clarification and resumption. Revision issued a new quotation request and kept
the confirmed coverage change. Its acknowledgement timing excludes quotation
calculation completion.

These are individual input-to-first-mock-audio observations. Earlier paired
runs varied substantially, including multi-second provider responses. They do
not establish a latency percentile or a stable speedup. Saving a narrowed scope
can require an additional model completion and exceeded 1,200 ms in this run.

Repeated testing exposed and corrected a text-only answer that omitted scope
persistence, plus an omitted return to the quote after implicit clarification.
The mandatory scope control and coordinated quotation suffix address those
cases. Final content checks inspect the engine's response output, including
its appended quotation, alongside the model trace.

The package race suite passed in 701.7 seconds for duplex, with journal and
configuration tests also passing. Subsequent scope and coordinated-suffix
changes passed three repetitions of the focused recovery, skills, playback
ownership and shutdown tests. Those repetitions include interrupting the
engine-appended quotation and completing its remaining section. Go vet,
JavaScript syntax validation and whitespace checks passed.

The feature defaults off. On 2026-11-14 the owner authorized a practical test.
Test build `87b9b92-quote-recovery`, including the box's RealtimeVAD integration,
was deployed with the flag enabled and the preceding executable and profile
saved for rollback. The original host prompt, greeting and voice were retained.
The first call encountered speech-provider timeouts and an audio-quality
complaint before a quotation conversation occurred. Practical recovery and
acoustic handset validation remain pending. See the
[call review](../../journal/retros/out-mwg5x8a3-pkz0.md).
