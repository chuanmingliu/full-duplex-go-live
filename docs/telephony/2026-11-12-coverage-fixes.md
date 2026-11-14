# Coverage gaps: fixes and verification

The expanded motor catalog supports 18 selections, 77 positive options, and
12 dependent riders. It preserves the owner's exact configured limits rather
than inferring which limits are common in the insurance market. Prices in the
bundled rating service are synthetic test values, not insurer quotations.

## What changed

- The model receives the effective `memory.quote_plan` catalog, including all
  configured tiers, descriptions, parent dependencies, and explicit boolean
  choices. Counted lists prevent older example menus from appearing authoritative.
- Calls pin the fetched `quote-plans` revision. Pricing and comparisons use that
  revision; a saved panel change applies to new calls. An unknown plan or failed
  catalog fetch produces an explicit unavailable guard, not another plan's menu.
- Declined defaults are not described as included. An all-declined default does
  not claim there is a prepared quotation. The pinned default keeps the fast
  default-result readout marker.
- Insurance-concepts routing covers every catalog name/alias and option inquiry.
  “怎么选” alone no longer requests quotation. Short follow-up questions, including split “请完整列出来” turns, carry a
  nearby coverage topic, stopping at an unrelated user turn.
- The selector reads the saved plan and revision through
  `/api/quote/selection-catalog`; `/api/quote/catalog` remains the template for a
  new draft. Editing a template never silently replaces the saved default.
- Strict pricing checks reject missing, extra or duplicate result coverages,
  changed limits, incorrect shared limits/deductibles, invalid monetary values,
  and unrequested compulsory insurance/tax. Signed rider credits remain valid.
- Confirmed `config` changes update quotation memory before the asynchronous
  snapshot; `configs` alternatives remain non-mutating comparisons. The
  quotation/memory architecture document describes the full contract.

## Combination and routing tests

The race-enabled matrix passed 8,498 unique policies: all 5,733 within-family
states plus compatible cross-family combinations. The full structural product
is 413,677,492,904 states; this report does not claim exhaustive enumeration of
that product. The matrix had 2,123 seeded cache hits, 6,375 API misses, 8,498 repeat
cache hits, and two call/fact isolation checks. At matrix scale, mock calculation
delay is zero; the HTTP request, polling, validation and cache paths are real.

MCP tests exercise `prompts/get` and `resources/read` for all 77 configured
positive options, plus every canonical/alias name and the seven review queries.
Panel tests check saved selection, parent requirements and preservation of
existing fields. These checks now run in CI, including a separate race-enabled
combination job.

## Real-model and recorded call simulations

The first real-model pass found two remaining omissions: the 400万 third-party
tier and an explicit holiday-rider opt-out. Those failures led to counted,
line-separated option lists and explicit boolean choices. The same 18-question
suite then passed every option and skill check, with unchanged memory and the
saved original prompt retained. It uses the actual `deepseek-chat` provider but
mock ASR/TTS, so its timing is not phone latency. The successful pass had
first-text p50 914 ms and p95 1,238 ms; one pass is insufficient for a stable
latency comparison. The broader phone test also caught an unsupported
rescheduled-holiday exception. The final guidance distinguishes explicitly
configured weekend coverage from missing rescheduling details, and tests
require both correct inclusion and an uncertainty response for missing rules.

The cache/revision voice simulation used Active Call and the real simulator
media path. Call `e4b54f29-4927-4f84-a4a1-10273f6252e4` completed with 24 assistant
and 15 caller transcript entries. Archive hashes and signal in both audio
channels were verified. It quoted the default, driver/shared-medical change,
300万 revision, 400万 plus holiday rider, restored 300万 policy and 200/300万
comparison. The restored total matched its earlier quote. Its first candidate
predates the final counted-list clarification.

Normal-content timing was 957 ms median and 1,132 ms p95, but there were only two
normal-response samples. Quote-result p95 was 6,645 ms and verified monetary
readout p95 5,860 ms. One interrupted quote result had unverified readout evidence;
there were no missing readouts in that report. These are different measures and
must not be pooled. Fresh calculations retained the configured 2,500 ms base
plus up to 2,000 ms jitter. No acoustic handset timing was measured.


The first menu call (`e07982ab-67da-4a5a-b385-6d162e5ce5d4`) completed with both
audio directions archived and no quotation calls. It verified driver/passenger
medical lists, scratch coverage, mental-damage limits and deductible percentages.
Normal-content p95 was 1,037 ms across nine samples. Its unsupported holiday
exception and split-follow-up routing were inputs to the final refinement;
this intermediate call is not evidence that those refinements were already active.


### Final verification and limits

The final engine candidate was `coveragefix-20261112-e`. Its 18-question
real-model suite passed every configured option, concepts-skill selection,
original-prompt preservation and unchanged-memory check. It also correctly
separated included weekends from unconfigured rescheduling rules. First-text
p50 was 746 ms and p95 1,282 ms in this single model-only run.

Recorded call `5c0f68b9-83a5-4a05-aed7-3d73e6bba05e` completed in 171.1 seconds,
with 19 caller and 19 assistant transcript entries and verified stereo archive
hashes. It read all 11 third-party medical options, all six driver and six
passenger medical options, and all six third-party liability tiers. No quotation
was executed. Both split “请完整列出” / “请完整列出来” turns loaded the concepts
skill; seven skill-loaded preparations had a median 3.22 ms and p95 3.79 ms.

One ASR rendering changed “司机” to “四级”; the initial answer used the previous
scope, and a caller clarification produced the correct driver list. That
recognition ambiguity remains a limitation, not a successful first-attempt
scope classification. A generic “回答一下…的问题” holiday request also had no
skill match in this recording despite a correct catalog-grounded answer. Its
exact utterance was added to the MCP regression, and reverse-order explanation
routing was extended and retested locally after the call.

The final phone simulation's normal-content median was 1,000 ms and p95
3,179 ms across 15 responses. This is above the 1,200 ms target. These scripts
had different questions and interruptions, so the earlier 1,037 ms p95 is not
a controlled baseline for attributing a latency change to the new catalog.
Coverage correctness improved; this work does not establish a latency win or
handset compliance. Quotation timings remain in their separate call report.

All temporary engine, CRM, rating, MCP-fixture and originator processes were
stopped after archive verification. The test Journal remains available. Original
profile, saved-call and running-binary hashes were unchanged. Full Go tests,
focused race tests, MCP race tests, panel tests, Python regressions, formatting
and vet checks passed; CI runs the final committed tree again.

## Deployment and rollback boundary

All new simulations ran on isolated ThinkPad candidate services. The existing
Active Call service, saved prompt/greeting/voice, original telephony profile and
production CRM settings were preserved. The candidate holds private rollback
copies under `~/golive-telephony/codex-latency/coverage-fixed-20261112/rollback`.
They are not committed. The original runtime does not acquire this catalog until
an explicit coordinated engine/CRM/catalog rollout. Older adapters should not
be presumed to honor the new revision field.

Before production use, retain the current profile, saved call, CRM configuration
versions, service binaries and MCP catalog revision. Add the full plan as an
opt-in draft; choose the full plan for a call or activate it deliberately after
review. Deploy the compatible engine and CRM together, publish the updated
concepts skill, verify health, then perform an authorized call. Revert those
versions together to roll back. Do not overwrite the original default silently.

## Evidence

- [Matrix](evidence/2026-11-12-coverage-fixes/matrix-race.json)
- [Initial model failures](evidence/2026-11-12-coverage-fixes/model-initial.json)
- [Successful model rerun](evidence/2026-11-12-coverage-fixes/model-fixed.json)
- [Cache/revision voice simulation](evidence/2026-11-12-coverage-fixes/cache-voice.json)
- [Final model suite](evidence/2026-11-12-coverage-fixes/model-final.json)
- [Final voice simulation](evidence/2026-11-12-coverage-fixes/menu-final.json)
- [Spoken content and skill checks](evidence/2026-11-12-coverage-fixes/menu-content-checks.json)
- [Preservation verification](evidence/2026-11-12-coverage-fixes/preservation.json)
- [Pre-fix audit](2026-11-12-coverage-audit.md)

Private prompts, raw model traces, raw call logs and recordings stay outside Git.

## Reproduce

```sh
GOLIVE_MOTOR_CACHE_SIM=1 \
GOLIVE_MOTOR_CACHE_REPORT=/tmp/motor-combinations.json \
  go test -race -timeout 10m ./internal/crm \
  -run '^TestMotorCombinationCacheSimulation$' -count=1
node --test scripts/tests/test_motor_catalog.cjs
(cd services/skills-mcp && go test -race ./...)
```

The optional `TestCoverageRealModelOptions` test additionally needs the configured
provider credentials, `SKILLS_REAL_PROVIDER`, `SKILLS_REAL_MODEL`, a private
`COVERAGE_PROMPT_FILE` containing the saved `instructions`, and an isolated MCP
fixture with the concepts skill published (`SKILLS_SIM_URL` and
`SKILLS_SIM_TOKEN_FILE`). Set `COVERAGE_REAL_REPORT` to a private output path to
enable it. That report includes response text and must not be committed. The
normal test suite skips it when that variable is absent. A model-only pass is
supplemented by the recorded Active Call simulator runs above.
