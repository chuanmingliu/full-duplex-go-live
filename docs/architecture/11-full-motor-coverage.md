# Full motor coverage catalog and quotation contract

The `motor-full` plan implements the owner's 2026-11-12 catalog as 18 separate
selection keys: 16 commercial coverages, compulsory insurance and tax collection.
The shared source is `internal/motorcatalog/catalog.json`. GoLive, CRM and our
quotation service keep amount, percentage, boolean and shared-limit selections
separate. These product options are configuration, not universal insurer terms.

## Supported selections

All entries accept `false` for exclusion. Amounts below are yuan unless they
explicitly contain 万. The owner's bare third-party tiers mean 万元.

| Selection key | Display label | Allowed choices | Required parent | Our backend code |
| --- | --- | --- | --- | --- |
| `车损险` | 机动车损失保险 | 投保 / 不投保 | — | `951` |
| `划痕险` | 车身划痕险 | 2000 / 5000 / 1万 / 2万 | 车损险 | `955` |
| `绝对免赔率-车损` | 绝对免赔率-车损 | 5% / 10% / 15% / 20% | 车损险 | `XC-DEDUCT-DAMAGE` |
| `三者险` | 第三者责任险 | 50万 / 100万 / 150万 / 200万 / 300万 / 400万 | — | `952` |
| `精神损害险-三者` | 精神损害险-三者 | 1万 / 2万 / 5万 / 10万 | 三者险 | `XC-MENTAL-TP` |
| `假日三者翻倍险` | 假日三者翻倍险 | 投保 / 不投保 | 三者险 | `XC-HOLIDAY-TP` |
| `绝对免赔率-三者` | 绝对免赔率-三者 | 5% / 10% / 15% / 20% | 三者险 | `XC-DEDUCT-TP` |
| `医保外用药责任险-三者` | 医保外用药责任险-三者 | 共享保额 / 10万 / 15万 / 20万 / 30万 / 50万 / 100万 / 150万 / 200万 / 300万 / 400万 | 三者险 | `XC-MEDICAL-TP` |
| `司机责任险` | 司机责任险 | 2万 / 3万 / 4万 / 5万 / 10万 / 20万 | — | `953` |
| `精神损害险-司机` | 精神损害险-司机 | 1万 / 2万 / 5万 / 10万 | 司机责任险 | `XC-MENTAL-DRIVER` |
| `绝对免赔率-司机` | 绝对免赔率-司机 | 5% / 10% / 15% / 20% | 司机责任险 | `XC-DEDUCT-DRIVER` |
| `医保外用药责任险-司机` | 医保外用药责任险-司机 | 共享保额 / 1万 / 2万 / 5万 / 10万 / 20万 | 司机责任险 | `XC-MEDICAL-DRIVER` |
| `乘客责任险` | 乘客责任险 | 2万 / 3万 / 4万 / 5万 / 10万 / 20万 | — | `954` |
| `精神损害险-乘客` | 精神损害险-乘客 | 1万 / 2万 / 5万 / 10万 | 乘客责任险 | `XC-MENTAL-PASSENGER` |
| `绝对免赔率-乘客` | 绝对免赔率-乘客 | 5% / 10% / 15% / 20% | 乘客责任险 | `XC-DEDUCT-PASSENGER` |
| `医保外用药责任险-乘客` | 医保外用药责任险-乘客 | 共享保额 / 1万 / 2万 / 5万 / 10万 / 20万 | 乘客责任险 | `XC-MEDICAL-PASSENGER` |
| `交强险` | 交强险 | 投保 / 不投保 | — | `` |
| `车船税` | 车船税 | 代缴 | — | `` |

## Selection and memory flow

1. The active `quote-plans` document supplies coverages, `options`,
   `coverage_aliases` and `coverage_details` to GoLive at call setup. The snapshot
   retains every named plan. Model requests render the plan selected by
   `memory.quote_plan`, or the saved default if that field is absent.
   `quote_plan_version` travels in the trusted tool-request envelope and pins CRM
   intake, defaults, normalization and comparisons to that same saved document.
   Unknown names/versions fail explicitly. New calls see new plan revisions;
   existing calls retain their snapshot without an extra network request per turn.
2. The model sends confirmed differences in `config`; hypothetical options use
   `configs`. Customer facts remain separate in working memory.
3. GoLive canonicalizes aliases/units before recording a confirmed revision and
   submitting its asynchronous task. CRM also normalizes base and patches
   **separately before merging**, covering direct HTTP/MCP clients.
4. CRM checks tiers, unknown selections and rider dependencies. A missing parent
   returns `status: need`, with `missing[].kind: dependency`. It asks whether to
   include the main coverage or remove the rider; neither action is automatic.
5. Valid selections are sent to the configured quotation backend. Missing
   mappings fail the strict plan instead of returning a smaller successful quote.
6. The result must contain exactly the requested coverage/parent pairs, each once, and their premiums.
   Explicit sums, percentages, shared limits and requested tax collection are
   checked. Extra/duplicate lines and invalid compulsory/tax monetary values are rejected. Canonical selection labels are retained for speech and memory.

Examples of equivalent amounts are `300w`, `300万`, and `3000000` for third-party
liability. The full plan also understands bare `300` for that coverage because
its `input_unit` is 万. `5%` never becomes five yuan. `共享保额` remains an explicit
choice; the pricing service resolves it using the matching parent's amount.
Driver and passenger coverages are separate. The ambiguous old `座位险` key stays
in the legacy plan; it is not silently expanded in the full plan.

```json
{
  "name": "quote_total",
  "arguments": {
    "config": {
      "司机责任险": "5万",
      "医保外用药责任险-司机": "共享保额",
      "绝对免赔率-司机": "10%",
      "车船税": "代缴"
    }
  }
}
```

To remove a parent and its riders in a single confirmed change, explicitly send
`false` for each. Unrelated selections are preserved. Comparisons apply each
alternative to the same base, retain each error separately and never adopt an
alternative as the selected plan. Tax `false` is retained in memory/cache keys
so it cannot be confused with implicit legacy tax behavior.

## Our backend protocol

`quote-api.coverages` retains the existing code/name/parent mapping and adds:

- `choice_values`: explicit wire values for special options, such as
  `{"共享保额":"shared"}` or `{"5%":"5","10%":"10"}`.
- `value_field`: the coverage field receiving that value. The default is
  `sumInsured`; deductible selections use `deductibleRate`.
- `tax_field`: a top-level boolean field. Our backend uses `collectVehicleTax`.

Example commercial rider on the wire:

```json
{
  "coverageCode": "XC-DEDUCT-DRIVER",
  "coverageName": "绝对免赔率-司机",
  "baseRiderType": "1",
  "coverageType": "0",
  "parentCoverageCode": "953",
  "isNonDeductible": "0",
  "deductibleRate": "10"
}
```

`sumInsured: "shared"` is allowed for medical riders only. Their response amount
is the parent's amount and their tag is `共享保额`. Holiday coverage is a separate
boolean rider; the backend returns doubled coverage for that rider without
rewriting the base third-party limit. Parent identity is included in every rider
response. The existing async submit / poll endpoints and serialized CRM pricing
gate remain unchanged.

`collectVehicleTax: true` requests tax collection; `false` excludes it. An absent
field retains the legacy backend behavior of collecting tax with compulsory
insurance. Tax can also be requested independently. A returned tax amount is a
separate line. These fields/codes define **our service contract**; migrate mappings
before pointing this configuration at a different insurer's backend.

## Pricing and failure behavior

The local quotation service is a simulator with configurable `rating.json`.
New `XC-*` test rules include separate rider prices, a test holiday supplement,
and synthetic deductible adjustments. For testing, deductible adjustments use
minus the parent's base premium times the selected percentage times 0.5; holiday
supplements use 0.1 times the parent's base premium. These are deliberately
synthetic prices, not insurer rates or claims-payment calculations.

Existing `rating.json` files are not replaced on startup. The draft utility adds
missing test rates/tiers while preserving existing rates. Operator-provided
rating tables can replace them. Unknown codes, absent parents, and incomplete
responses fail visibly. The strict full plan also fails if the legacy CRM local
rules lack a requested coverage; it never invents or silently drops its price.
Use the upgraded quotation service for the full catalog.

## CRM panel and configuration drafts

`GET /api/quote/catalog` is authenticated and returns the plan template, entries
and our backend mappings. On the **Configs → quote-plans** editor,
**Add full motor plan to draft** adds `motor-full` while preserving the current
`default`. Review the JSON and save a new version. Set `default: "motor-full"`
when ready for new calls, or select it via the call's `quote_plan` variable.

On **Configs → quote-api**, **Load Xiaocui backend mappings into draft** merges
our mappings while retaining the existing URL/channel settings. This is only a
draft until saved. `GET /api/quote/selection-catalog?plan=motor-full` returns
only the saved plan's current entries and version; omit `plan` for its default.
The **Quote** page uses this saved-catalog endpoint for its selection form,
showing all tiers and parent requirements. Applying a selection updates the
request locally and pins its `quote_plan_version`; it does not send a quotation
until **Quote** is pressed. Reload the form to pick up later saved edits.

For offline drafts, from the repository root:

```sh
go run ./cmd/golive-quote-config -kind plans -input saved-plans.json -output plans-draft.json
go run ./cmd/golive-quote-config -kind api -input saved-api.json -output api-draft.json
go run ./cmd/golive-quote-config -kind rating -input saved-rating.json -output rating-draft.json
```

Add `-make-default` only to the plans command to activate the new plan in that
draft. With no input, the command emits a standalone sample; a standalone plans
sample necessarily selects `motor-full`. Output is a new mode-0600 file; an
existing output path is refused. No credentials or settings are printed.

## Rollout and rollback

1. Keep the current binaries, versioned CRM documents, `rating.json`, service
   arguments and saved GoLive profile. Use the Mac runner and verify idle state
   before any service replacement. No real call is part of these tests.
2. Review the generated rating draft. Deploy the quotation-service binary and
   rating together, preserving its configured calculation delay/workers/data.
3. Deploy CRM and GoLive (including the pending immediate-memory revision fix).
   Save the full backend mappings, then the full plan. Existing default plans and
   preset openers stay available. Existing Active Call routing stays unchanged.
4. Publish the updated quotation skill and concept-routing configuration through
   the MCP panel. Existing published catalogs are not overwritten by source seed
   changes. A published catalog may retain operator-specific instructions.
5. Start new simulator sessions using the full plan, then verify real calls only
   when requested. GoLive snapshots plan guidance at session setup; backend
   configuration changes must not be treated as updating that prompt mid-call.
6. Roll back by restoring the previous default and mapping versions and retained
   service/rating pair after confirming idle state. Preserve later operator edits
   when rolling back individual fields.

All changes in this task are repository changes until explicitly deployed. The
interactive demonstration remains local and is not part of this feature.

## Validation

Tests cover all allowed catalog choices, every rider's missing-parent case,
boolean declines, amount aliases, explicit percentages/shared limits, a complete
18-line async backend result, per-option backend pricing, independent comparisons,
tax exclusion/cache identity, incomplete mappings/results, Active Call's immediate
canonical memory revision, MCP transport and skill routing, and safe draft output.
The pricing tests use synthetic vehicle data and local HTTP servers. They do not
measure real handset latency or insurer pricing; configured calculation delays
and normal conversational/interruption behavior are unchanged.

### Local validation result (2026-11-12)

- All 77 selectable catalog choices pass CRM intake and API serialization.
- All 12 rider-parent absence checks require clarification.
- All 75 commercial choices pass the local backend's pricing matrix.
- The asynchronous full-selection test returns all 18 lines; comparison tests
  isolate invalid alternatives and distinguish explicit tax exclusion.
- Race-enabled CRM, quotation service, catalog, tools and draft-generator suites
  pass. Focused Active Call revision/memory tests and the full MCP module pass
  with the race detector.
- Three panel tests pass: preserve defaults in drafts, preserve connection
  settings, and require parent selection without sending an automatic quotation.
- `go vet ./...`, JavaScript syntax checks and `git diff --check` pass.

Selection normalization adds local map/validation work on quotation requests.
It adds no extra LLM request and no provider call to normal conversation turns.
The full catalog does enlarge the tool description when that plan is selected;
these tests make no end-to-end latency claim. Business calculation delay remains
independent of conversational latency.


## Complete spoken options and fallback

`coverage_details.<name>.description` carries the product-specific explanation,
including the owner's holiday/weekend and deductible-scope terms. Descriptions
are versioned with the plan and supplied alongside its exact tiers. They are
not copied into a separate hard-coded skill price/option table.

The model must enumerate the full requested option list, clarify ambiguous
third-party/driver/passenger rider scope, and avoid inventing unavailable tiers.
Declined default items are omitted from its included-coverage list. Even if CRM
cannot be read at startup, the tool description keeps the grounding rule and
an explicit catalog-unavailable notice. Inquiry alone neither revises memory
nor authorizes a price calculation.

The MCP concepts routes cover catalog names/aliases and option questions;
`怎么选` alone is not a quotation trigger. Short follow-ups can inherit a nearby
coverage label from the preceding conversational exchange, without replaying
previous pricing instructions or crossing an unrelated user turn. The host
still controls original prompt, echo rejection, interruption and preset openers.

## Prioritize common cached combinations

The `quote-plans` policy accepts an ordered `prequote.priority` array of canonical
coverage names. For example, `"priority": ["三者险"]` prices the other configured
third-party tiers before the remaining coverages. With a 200万 default and the
current six-tier catalog, the first five variants are 50万, 100万, 150万, 300万
and 400万. The default itself is prepared separately. Remaining slots follow the
original catalog order. This is an explicit business priority, not a claim that
usage frequency has been learned automatically.

An omitted or empty priority retains the old ordering. Unknown or duplicate
names fail config validation. `max`, `follow_selection_max`, call/fact/version
cache isolation, foreground scheduling and the upstream calculation delay are
unchanged. The full-catalog draft includes the priority but still defaults to
`max: 0`. Existing saved plans are never migrated automatically. To enable it
on an existing plan, preserve its current config revision and add only the
priority field to that plan's existing policy. Remove the field to restore
legacy ordering. Already-running calculations still drain.

## Comparison premium readout

The built-in comparison speech now reads the individual premium for each
selected coverage in a variant's `change`, followed by that variant's total and
total difference. It uses the exact matching `variants[].lines[].premium`,
including valid zero amounts. Missing or duplicate lines are announced as
unavailable; the total difference never substitutes for a coverage premium.
Unchanged coverages are not repeated for every variant. A same-as-current
variant also states its individual premium and total. Failed variants have no
price readout. Explicitly configured comparison templates retain their behavior
and can use `variants[].lines` to implement a different presentation.

Coverage explanations use the active catalog over legacy product examples.
Shared limits describe the compensation limit, not a free premium. Population
preferences require actual statistics. An unconfigured product such as 驾乘险
can be discussed as a concept, but cannot be offered as available in the active
plan. These guards are included in the host catalog guidance as well as the
concepts skill so they also apply when skill retrieval falls back.
