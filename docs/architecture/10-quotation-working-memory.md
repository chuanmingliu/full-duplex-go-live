# Quotation and working memory

This describes the GoLive Active Call engine on `codex/telephony-latency`.
The engine records a confirmed coverage revision before submitting its quotation.
It carries that requested plan into subsequent prompts and quotations while
calculation runs. The quotation service supplies the prices and the normalized
selection in its result.

## Components and ownership

| Component | Responsibility | Source |
| --- | --- | --- |
| `tools_memory` | Enable per-call structured state and the `update_memory` builtin | `internal/duplex/tools.go`, `setupTools` |
| `tools.Memory` | Merge JSON patches, serialize snapshots, count content changes | `internal/tools/tools.go` |
| `update_memory` | Record confirmed facts, apply business-state protection, emit changes | `internal/duplex/tools.go`, `applyMemoryCall` |
| `quote_total` | Request a default quote, confirmed revision, or preview | Saved tool config plus `tools.QuoteRevisionInstructions` |
| Revision handler | Apply a confirmed `config` before the task snapshot | `internal/duplex/quote_revision.go` |
| Async coordinator | Submit, coalesce, supersede and deliver work | `internal/duplex/tools.go` |
| CRM quote endpoint | Resolve the plan, collect missing inputs, calculate or use prepared prices | `internal/crm/quote.go`, `service.go` |
| Result reconciliation | Check input freshness and record normalized selection | `recordSelection`, `quoteInputsUnchanged` |

`memory.quote` contains the requested coverage selection. A delivered quotation
stores the prices for the selection it actually priced. During calculation, the
requested plan can be newer than the latest price result. Prompt rendering marks
older prices when their selection or other inputs differ from current memory.

## Tool contract

The model receives `update_memory` when the profile has `"tools_memory": true`.
The following is the function definition, including the quotation-specific
instruction appended to its general memory description.

```json
{
  "type": "function",
  "function": {
    "name": "update_memory",
    "description": "记录或修改本次通话已经确定的信息（工作记忆）。每当用户确认、新增或更改了投保方案、车辆信息、联系方式等事实时调用；传入要合并的字段，值为 null 表示删除该字段。当前工作记忆会随时出现在你的上下文中，不必复述。若可调用 quote_total，对已有方案的明确局部改动直接用其 config；假设或比较用其 configs。仅新建方案或补齐事实时需要先调用本工具。",
    "parameters": {
      "type": "object",
      "properties": {
        "patch": {
          "type": "object",
          "description": "要合并进工作记忆的字段"
        }
      },
      "required": ["patch"]
    }
  }
}
```

The schema permits arbitrary fields inside `patch`. The engine implements it as
`Builtin: "update_memory"`. It runs synchronously inside GoLive. The canonical
arguments have a `patch` wrapper. For existing callers, the handler also accepts
a raw patch object when the wrapper is absent.

A successful response is `{"ok":true,"version":8}`. Invalid patches return
`{"ok":false,"error":"the patch must be a JSON object"}`. Model attempts to
establish WeChat completion before backend `addV` return an error. After `addV`,
model patches preserve the completed state.

## Patch semantics

| Input | Effect |
| --- | --- |
| Omitted key | Retain its current value |
| String, boolean, number or array | Replace that key's value |
| Object | Recursively merge its children |
| `null` value | Delete that key |
| Empty patch, equal values, deleting an absent key | Retain state, version and update timestamp |
| Malformed JSON or a non-object patch | Reject before mutation |

A mutex covers the merge and snapshot operations. A snapshot sees the complete
state before or after a patch. Versions count changes in contents. Concurrent
writes are serialized, and a later write to the same field takes effect. The
API has no expected-version argument.

Keys are literal. Use `{"quote":{"三者险":"300万"}}`, because a key named
`"quote.三者险"` would create a different top-level field. Empty nested objects
retain existing children. To rebuild an entire plan, explicitly remove obsolete
keys, or clear `quote` with `null` before filling the new plan.

For example, this patch changes one limit and removes one stored field.

```json
{"patch":{"quote":{"三者险":"300万","车损险":null}}}
```

For quotation arguments, use `false` to request exclusion of a coverage.
`quote_total.config` accepts booleans and tier strings. Nulls, nested objects,
arrays and numeric values in that change object are rejected before the plan is
modified or a task is submitted.

## Choosing a quotation operation

| Caller intent | Model action | State effect |
| --- | --- | --- |
| Ask for the prepared default price | `quote_total({})` | Record the full default selection when its current result is delivered |
| Supply missing vehicle information | `update_memory`, then retry `quote_total` | Store the fact immediately |
| Specify a complete new plan | Record its fields with `update_memory`, then quote | Store the selected plan immediately |
| Confirm a local revision | `quote_total({"config":{...}})` | Commit the change before taking the calculation snapshot |
| Explore one hypothetical option | `quote_total({"configs":[{...}]})` | Retain selected plan |
| Compare multiple alternatives | `quote_total({"configs":[{...},{...}]})` | Retain selected plan |

The LLM interprets intent from the user's meaning. The engine deterministically
uses the chosen argument shape. A single hypothetical option therefore belongs
in a one-element `configs` array. If one request contains both `config` and
`configs`, the service treats `config` as another alternative. When the model
emits a separate single-change call alongside a comparison in the same response,
the engine folds the single-change call into the comparison path before mutation.
This applies to responses containing application tools as well as server tools.

The engine appends these operation rules to `quote_total` descriptions loaded
from saved profiles. The dynamic quotation brief describes missing-fact intake
and directs confirmed local revisions through `config`.

## Two-option comparison from request to speech

For `quote_total({"configs":[{"三者险":"200万"},{"三者险":"300万"}]})`,
the engine submits one asynchronous task with the current memory snapshot. The
CRM converts the nonempty entries into `QuoteRequest.Variants` and dispatches to
`Service.Compare`. It resolves the current plan from memory and, where needed,
the call's default selection. Customer variables are supplemented from the lead
record when absent from the request.

`Compare` first prices the base plan. It then processes variants sequentially,
copying the same base for each and overlaying that variant's changes. Options do
not build on one another. If the base already has `三者险=200万`, the first
variant reuses the base quote and has `same=true`. The second prices the entire
base plan with only that limit changed to `300万`. Pricing can use existing
cached or prepared results. The comparison returns after processing all options.

| Result field | Meaning |
| --- | --- |
| Top-level `selection`, `total`, `lines` | Current base plan and its prices |
| `compare` | `true` for the combined comparison result |
| `variants[].change` | Original changes for this option |
| `variants[].selection`, `total`, `lines` | Full alternative selection, total premium and line premiums |
| `variants[].diff`, `diff_abs`, `more` | Signed difference from the base total, its magnitude and direction |
| `variants[].same` | Reuses the current base quote |
| `variants[].prequoted` | A differing option used a prepared or cached price |
| `variants[].error`, `skipped` | Unavailable option or excluded coverages |
| `compare_ms` | Elapsed comparison work, including base pricing and variant pricing |

A failure to price the base fails the comparison. An individual pricing failure
is attached to that variant while later variants can still be processed. Each
successful option retains authoritative line prices for later questions.

The top-level selection remains the base. Result reconciliation can materialize
that base in working memory, but does not select any alternative. The configured
`quote-speech.compare_template` reads the combined result. If that template is
omitted, the built-in template reads the base total, identifies matching options,
and reads each differing option's total and difference. The task coordinator
controls acknowledgement and result delivery around the caller's speech.

If the caller subsequently chooses the 300万 option, the model submits
`quote_total({"config":{"三者险":"300万"}})`. That confirmed revision takes
effect in working memory before its quotation snapshot is taken.

## Confirmed changes while calculation runs

Assume the selected plan includes `三者险=200万` and `车损险=true`.

1. The caller says “三者改成三百万，其他不变。”
2. The model calls `quote_total({"config":{"三者险":"300万"}})`.
3. The engine patches `memory.quote`, increments its version, and emits
   `golive.memory` before submitting the task.
4. The task receives a serialized snapshot with `三者险=300万`.
5. While it is pending, “现在三者是多少？” has `300万` available in the prompt.
6. The caller says “再把车损去掉。” The next `config` contains `车损险=false`.
7. Its snapshot includes both `三者险=300万` and `车损险=false`. The older running
   quotation is superseded.
8. A current successful result records its normalized selection and becomes the
   latest price result.

Calculation failure retains the caller's requested plan. A retry therefore uses
the same requested coverage. A failed calculation supplies no new valid price.
An identical patch retains the version, allowing the existing repeated-request
coalescing rule to work without a version increment caused only by repetition.

## Changes before the default selection has been materialized

A call can request a revision before any default quotation result has populated
`memory.quote`. In that case the engine records the requested changes immediately
and marks the quote request with `quote_base_default=true`.

```json
{
  "tool": "quote_total",
  "call_id": "synthetic-tool-call",
  "session": "synthetic-session",
  "call": "synthetic-phone-call",
  "arguments": {"config":{"三者险":"300万"}},
  "memory": {"quote":{"三者险":"300万"}},
  "memory_version": 1,
  "quote_base_default": true
}
```

This flag is generated by the engine. It is outside the model's tool arguments.
The CRM overlays all recorded revisions on the call's default selection, so
unchanged default coverages still participate. Further revisions and missing-fact
patches retain this provenance. The prompt explains that unlisted coverages still
follow the default plan. A valid result with a full selection clears the flag.
Explicitly deleting `quote` also clears it.

The ordinary request calculation uses `memory.quote` overlaid with
`arguments.config`. Variables use the remaining memory fields overlaid with
`arguments.vars`. With `quote_base_default=true`, the CRM first supplies the
missing default coverages. Preview operations use the same resolved base for
each alternative.

## Missing facts and result freshness

The service returns `status=need` with ordered `missing` fields when required
inputs are absent. Xiaocui asks one question, stores its answer, and retries.
Only a field configured with `if_unknown=skip` can use `"unknown"` to skip the
corresponding coverage. Returned exclusions must remain visible in the answer.

Tasks carry their submission memory version and snapshot. Before delivery, the
engine accepts equal versions or, for `quote_total`, equal input snapshots after
excluding `wechat.status`. A backend `addV` can therefore update WeChat status
without invalidating a quote. A newer coverage or other input change makes the
older result stale. Stale results cannot reconcile `memory.quote` and receive an
explicit old-plan explanation if delivered. Superseded running tasks are dropped.

Speech interruption does not undo confirmed state. A result can wait for a speech
gap. Its freshness is checked at delivery, so a change during that wait is also
considered. The latest readout and its heard prefix remain available for follow-up
questions. Prices whose recorded selection differs from current state are marked
as belonging to the earlier plan.

## Prompt, cache and latency behavior

Current prompt assembly places stable instructions first, then working memory,
then conversation history and request-specific guidance. Memory survives history
trimming. Changes to the memory block can shorten the reusable prompt prefix.
Suppressing no-op patches removes version-only changes and duplicate
`golive.memory` events from the model update path.

A confirmed local revision still uses one `quote_total` call. Its in-process patch
runs before the async calculation and introduces no separate model continuation.
Missing-fact collection through `update_memory` can require a continuation.
Calculation time and waiting for result delivery must be measured independently
from normal response and interruption latency. Timing tests with synthetic
providers establish control-flow behavior, while production latency needs an
actual provider and call trace.

## Lifecycle and compatibility

Working memory is per-call process state. The existing persistent session snapshot
does not automatically restore it into a new call. `SeedMemory` supports explicit
initialization. The generic patch schema does not validate every business field.

Update the CRM quote endpoint before using the new engine with early revisions
against an unmaterialized default plan. An older CRM ignores `quote_base_default`
and can price only the partial selection. The new CRM also accepts legacy
requests with that field omitted. Roll back the engine before rolling back CRM.
Existing MCP catalogs retain their published skill bodies. During rollout, apply
the confirmed-revision and hypothetical-preview wording to the deployed
`motor-quotation` skill through the panel, preserving operator-specific guidance.

## Verification

The focused regression suite covers accumulation of revisions before a blocked
calculation finishes, prompt visibility, stale-input detection, default-plan
provenance, one-option and mixed previews, invalid changes, unchanged patches,
and selection-aware price context. Existing tests cover speech delivery,
comparisons, missing-field intake, backend events, skills and interruptions.

```sh
go test ./internal/tools ./internal/duplex ./internal/crm \
  -run 'Test(Memory|QuoteRevisions|QuotePreview|RepeatedMemory|DeliveredQuote|AChangeWithNoPlan|ASingleChangeBeside|APlanChange|SeveralOptions)' -count=1
```

```sh
go test -timeout 25m ./internal/tools ./internal/duplex ./internal/crm ./internal/skillsmcp ./internal/server
```

The optional [full motor catalog](11-full-motor-coverage.md) adds canonical amount, percentage and shared-limit choices, parent/rider validation, and explicit tax collection.


### Comparing every configured tier

Active Call can request `quote_total({"compare_coverage":"三者险"})` to expand all tiers from the effective activity catalog before backend dispatch. This produces the existing `configs` comparison shape and does not commit a new selection. Explicit subsets still use `configs`; never combine the shorthand with `config` or `configs`. The standalone MCP tool continues to accept its existing arguments. See [the comparison and recovery note](../telephony/2026-11-12-coverage-repair.md).
