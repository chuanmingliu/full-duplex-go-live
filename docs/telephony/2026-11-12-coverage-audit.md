# Coverage completeness review — 2026-11-12

This is the pre-fix snapshot. See [fixes and verification](2026-11-12-coverage-fixes.md) for the subsequent implementation and remaining deployment boundary.

The expanded catalog matches the owner's requested selections, but the current
candidate still has integration and validation defects. Passing the generated
combination matrix does not establish complete spoken coverage support.
This review makes no production configuration changes and performs no calls.

## Scope and catalog result

Reviewed the local, uncommitted expanded-coverage implementation on
`codex/telephony-latency`, the prior real-call findings for
`out-mwdh1b7g-p66i`, CRM plan/API/panel handling, prompt construction, MCP routing,
and synthetic quotation response validation.

A new independent contract test transcribes the owner's list rather than deriving
its expectations from `catalog.json`. It verifies exact option arrays, parents,
kinds, absence of duplicate names, and the ability to decline each selection.
All 18 selections match. There are 77 positive choices and 12 parent-dependent
riders. Decline states are additional to the 77 positive choices.

| Family | Selections | Positive choices | Checked details |
|---|---:|---:|---|
| Vehicle damage | 3 | 9 | Damage on/off, scratch 2k/5k/10k/20k, deductible 5/10/15/20% |
| Third-party | 5 | 26 | 50/100/150/200/300/400万, mental damage, holiday doubling, deductible, medical/shared |
| Driver | 4 | 20 | 2/3/4/5/10/20万, mental damage, deductible, medical/shared |
| Passenger | 4 | 20 | Separate from driver, same required tiers and rider families |
| Compulsory | 1 | 1 | On/off |
| Vehicle tax | 1 | 1 | Collection/decline |

The independent test is `internal/motorcatalog/coverage_contract_test.go`.
Existing tests also exercise all configured tiers through request mapping,
parent validation, strict unsupported-tier rejection, a full HTTP quotation
round trip, shared/percentage readout labels, comparison isolation, tax identity,
and missing backend mappings.

## Findings

### P1 — Expanded coverage is absent from the last verified live plan

The real call ran build `951a6b4` and CRM plan `standard`. Its available
coverages were compulsory, damage, third-party, seats, scratch, glass and personal
accident. Its only explicit tier tables were third-party and personal accident.
The new plan has not replaced that deployment. In particular, medical riders,
mental-damage riders, deductibles, holiday doubling, separate driver/passenger
selections and tax collection are not exposed by that live plan.

The live third-party tiers were 100/200/300/500万, which differ from the requested
new 50/100/150/200/300/400万. Retaining the old configuration for rollback is
intentional, but it must not be presented as verification of the expanded product.
The real call's unsupported medical-tier claims are documented in its retrospective.

Action: deploy a compatible quotation service/rating table, CRM mappings and
plan, engine, and published MCP catalog together through a staged rollout.
Merely deploying a binary or editing skill seed files is insufficient.

### P1 — A call-specific plan can be priced using different options from its voice prompt

`ParseQuoteBrief` chooses `cfg.Default` for the prompt's coverages, fields,
default selection and tiers. `setupTools` renders that brief into the tool
once. `toolDefinitions` subsequently emits the stored definitions unchanged.
Meanwhile CRM resolves `memory.quote_plan`, and the new engine normalization
code also uses it.

Reproduction by code path: retain `standard` as the system default and assign
`motor-full` to a call. The model still sees standard's options while CRM prices
motor-full. Adding motor-full without changing the global default is therefore
not enough to make per-call spoken explanations correct.

Sources: `internal/tools/quoteplan.go:65`, `internal/duplex/tools.go:170`,
`internal/duplex/tools.go:258`, `internal/duplex/quote_revision.go:104`,
`internal/crm/motor_catalog.go:112`.

Action: select the same effective plan for rendering and pricing, retain each
plan's full brief, and handle unknown plans explicitly. Define whether a call
pins a configuration revision or refreshes it; the current brief is a startup
snapshot, not an automatically refreshed panel view.

### P1 — Strict result validation accepts extras and malformed compulsory prices

Four negative probes with a legitimate third-party request and a matching result
line were accepted by `validateComplete` when rejection was expected:

1. Another unrequested coverage was added to the result.
2. The requested coverage appeared twice.
3. Compulsory insurance was returned although not requested.
4. Requested compulsory insurance returned a non-numeric premium.

The function verifies presence of requested business lines but does not check the
reverse relationship or duplicates. Compulsory and requested-tax checks only
require nonempty strings. The normalizer later iterates returned business lines;
its numeric helper ignores parse errors. Missing requested business lines are
already rejected, which does not address these separate cases.

Sources: `internal/crm/coverage_contract.go:58`,
`internal/crm/coverage_contract.go:97`, `internal/crm/quoteapi.go:515`.

Action: validate an exact requested coverage set, unique code/parent identities,
and finite valid monetary values for compulsory insurance and tax, with explicit
handling for declined coverages. Preserve legitimate synthetic deductible
credits when validating business-line premiums.

### P2 — Declined defaults are described as included

A prompt probe with default selection
`{"交强险":true,"车损险":false,"车船税":false}` produced the default-plan
list `交强险、车损险、车船税`. The renderer enumerates all keys and does not
filter declined values. This can directly contradict CRM pricing and customer
instructions even with a complete catalog.

Source: `internal/tools/quoteplan.go:89`.

Action: distinguish selected, declined and missing values when describing the
default plan. Include tests for boolean false and supported textual decline forms.

### P2 — Coverage inquiry routing remains incomplete

Actual `prompts/get` probes produced no concepts skill for:

- 划痕险有哪些档位？
- 三者险有哪几档？
- 司机责任险可选档位？
- 乘客责任险有几档？
- 车船税能代缴吗？
- 假日三者翻倍险周末也算吗？

`医保外用药怎么选？` selected only `motor-quotation`. The phrase `怎么选`
is currently a quotation trigger. That is an inquiry, not necessarily approval
to price or modify a policy. Skill selection alone does not prove that a tool
was actually executed; these probes inspect routing, not model behavior.

Sources: `services/skills-mcp/vehicle_insurance_concepts/routing.json`,
`services/skills-mcp/catalog.go:79`.

Action: cover all catalog names/aliases and option-inquiry formulations, keep
inquiry separate from confirmed modification or premium requests, and test
follow-up questions such as “还有其他的吗” with conversational context.

### P2 — Panel selection ignores saved catalog edits

The full-motor selector reads `/api/quote/catalog`, whose handler always returns
`FullMotorPlan()` and embedded `motorcatalog.Entries()`. It does not read the saved
motor-full plan. If an operator removes a tier or coverage in the panel, the
selector can still offer it, although CRM will reject the request later.

Sources: `internal/crm/ui/motor-catalog.js:29`,
`internal/crm/motor_catalog.go:105`.

Action: keep the built-in catalog endpoint for creating drafts, but drive actual
selection from the saved effective plan and its revision.

### P2 — Catalog-fetch failure removes the new grounding guard

The instruction that prohibits invented choices lives in `QuoteBrief.Text`.
If the catalog fetch fails at startup, `setupTools` only logs a warning and keeps
the saved tool description. The new guard and an explicit “catalog unavailable”
state are not appended on this path.

Sources: `internal/tools/quoteplan.go:88`, `internal/duplex/tools.go:175`.

Action: retain the no-invention guard regardless of fetch outcome, explicitly
identify unavailable catalog information, and avoid treating historical profile
examples as a verified current product list.

### P2 — Some supplied product descriptions are not carried into the knowledge content

Option completeness does not imply complete explanations. The catalog carries
kind, label, parent and unit, but not product descriptions. The concepts skill
has general scope/limit guidance; it does not encode the owner's specified
weekend inclusion for the holiday rider or the stated injury/death scope of
third-party deductible treatment. These are details of the supplied product
specification, not claims that every insurer uses those terms.

Sources: `internal/motorcatalog/catalog.go:17`,
`services/skills-mcp/vehicle_insurance_concepts/SKILL.md:38`.

Action: add versioned, product-specific explanatory text and exclusions alongside
the effective catalog, inject only relevant content, and verify it against the
configured product rather than infer it from synthetic prices.

## Verification and limits

- Independent 18-selection contract test passed.
- Focused Go tests across motorcatalog, tools, CRM, quotesvc and the draft-config
  CLI passed.
- Existing MCP publication/routing tests passed.
- All three existing JavaScript panel tests passed.
- New negative probes exposed 12 failing expectations: four strict-result cases,
  one declined-default case, and seven inquiry-routing cases.
- `probes.json` retains synthetic failure evidence. Temporary failing test files
  were removed after collection; production code was not changed by this review.
- The default Go cache encountered sandbox write restrictions. Repeating with
  `GOCACHE=/tmp/golive-coverage-review-cache` completed the test runs.
- No new real-model or voice simulation, deployment, commit or push was done.
  The prior 8,498-policy simulation remains evidence for valid configured
  combinations, not these missing negative cases or complete natural-language
  explanation behavior.

Recommended order: correct prompt/pricing plan alignment and strict response
validation, fix inquiry/decline/fallback behavior and saved-panel selection,
complete product descriptions, then replay real-model option queries before
rolling out the expanded catalog to the original Active Call service.
