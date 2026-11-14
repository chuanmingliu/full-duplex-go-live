# Coverage follow-up after the November 12 call

The call on build `7a3d3ac` exposed natural-language routing misses, an unsupported
claim that shared-limit medical coverage was free, an offer of an unconfigured
product, and comparison readouts that omitted individual premiums. Its eight
initial warm-up slots also went to scratch and damage-deductible variants before
any third-party liability alternatives.

## Changes

- Concepts routing recognizes “几个档”, “大家一般选哪” and 驾乘/司机责任/乘客责任
  questions. Quotation routing recognizes scoped price differences, per-tier
  prices and the observed “医保费…加个…” transcription. Quotation exclusions
  remain in force for explanation-only requests. Ambiguous medical scope still
  needs confirmation; a matcher does not establish the caller's intended scope.
- Both host catalog guidance and skill content distinguish shared compensation
  limits from premiums, require evidence for population claims, and prevent
  offers of products missing from the active catalog. The saved original prompt
  remains a component, while the active catalog governs product availability.
- The default comparison readout now includes authoritative individual premiums
  for the selected changed coverages, plus totals and differences. Failed,
  missing, duplicate and explicitly skipped lines cannot become spoken zeroes.
  Custom comparison templates remain operator-controlled.
- `prequote.priority` controls coverage order independently of the displayed
  catalog. The full-catalog draft includes `["三者险"]`; existing saved policies
  keep their behavior until this field is deliberately added. With default
  200万 and an eight-slot budget, five slots prepare the other liability tiers.

## Rollout and rollback

This change is tested in isolated box services. The original main engine, CRM,
Active Call binding, saved prompt/greeting/voice and config revisions remain in
place. Pricing retains its 2,500 ms base plus up to 2,000 ms jitter, and the
simulator retains the original preset opener and interruption policy.

For a later rollout, retain the current binaries and config/catalog revisions,
install the matching engine and CRM, publish the revised concepts and quotation
skills, then add `prequote.priority` to the intended saved plan. Preserve every
other policy field. Remove priority to restore catalog-order warming, restore
the saved skill revisions to restore routing, and restore the prior engine to
restore its default readout. Custom readout templates are not rewritten.

## Verification

The scenario and sanitized results live in
[evidence/2026-11-12-coverage-followup](evidence/2026-11-12-coverage-followup).
Private prompts, call logs and audio remain on the box. Simulator measurements
cover server/media behavior, not physical handset latency. Quote calculation
and monetary readout are reported separately from normal conversational content.

Local checks passed for the full Go suite, focused CRM/tools race tests, MCP
race tests, vet, the coverage panel tests, and the latency/retrospective parser
regressions. The race-enabled combination simulation passed 8,498 unique
policies, with 2,123 seeded cache hits, 6,375 API misses and 8,498 repeated cache
hits. Its pricing delay is deliberately zero for matrix scale; it is not the
latency benchmark. The voice tests retain the configured calculation delay.

The first model run flagged scratch limits written as `2,000元` and `5,000元`.
The option checker now normalizes currency grouping commas, with a regression
that preserves comma-separated option lists. This was a test normalization
error, not evidence that the model omitted those limits.

### Recorded comparison

The baseline call is `402cafab-d280-40bc-b427-5e5d56c2de55`; the intermediate
candidate call is `19f4ce2a-d7c4-41d7-9009-e5245a841d45`. Both are available in
the isolated Call Journal at `http://192.168.71.9:18870`. Each archive passed
five-file checksum verification, contained both transcript speakers, and had
signal in both stereo audio directions. These were synthetic calls, not calls
to the owner's handset.

| Server-side observation | Baseline | Intermediate candidate |
| --- | ---: | ---: |
| Normal substantive response p50 / p95 | 959 / 1,226 ms (12) | 920 / 1,176 ms (15) |
| Quote acknowledgment p50 / p95 | 874 / 881 ms (2) | 1,277.5 / 1,490 ms (2) |
| Loaded skill processing p50 / p95 | 4.324 / 5.685 ms (9) | 3.975 / 5.274 ms (14) |
| VAD-open to server interruption cut | 58, 191 ms | 208, 343 ms |
| Cache-hit events | 2 | 21 |
| Foreground pricing API jobs | 6 | 1 |
| Background pricing API jobs | 8 | 8 |

Counts in parentheses are samples. Quote requests and result readouts are
excluded from the normal-response row. The candidate's six observed quote
tasks completed in 2–11 ms after cache warming; the baseline's uncached
six-tier comparison task took 19,626 ms. The original 2.5–4.5 second per-job
calculation delay was retained. Cache-hit counts reflect different conversational
paths and repeated requests, not a standardized cache-hit-rate benchmark.

The candidate spoke all six requested individual liability premiums and totals
in one comparison. The synthetic amounts were 808, 1,093, 1,235, 1,378, 1,615
and 1,805 yuan for 50, 100, 150, 200, 300 and 400万 respectively, using the
existing rounded readout convention. These are mock prices, not insurance rates.
The baseline initially spoke totals and differences only. Both complete option
enumeration and refusal to offer unconfigured 驾乘险 were observed on the candidate.

This is functional evidence, not proof of a latency improvement: the generative
caller split and interrupted requests differently, candidate warm-up readiness
was not explicitly awaited, and there are only two interruption samples per
call. Candidate quote results waited 1.82–8.546 seconds for conversational gaps
despite fast computation. Its analyzer also reported one missing and three
unverified monetary readouts and two invalid tool links. A fast cache must not
be described as an equally fast audible result; the strict latency gate does
not pass on this evidence. Interruption timing remains a follow-up concern.

### Final model and recorded verification

The intermediate candidate still described shared coverage as necessarily
chargeable. The final revision explicitly prohibits both “always free” and
“always costs extra” without quotation evidence. All 20 final real-model cases
passed: 18 coverage families/options plus shared-cost uncertainty and refusal
to offer an unconfigured product. Each verified concepts-skill selection,
preserved original prompt and working memory, and no unintended quotation.
These tests used the isolated engine with the saved original prompt; they do
not establish acoustic or SIP latency.

The final recorded replay initially hit the shared simulator's 128 persisted-run
limit before dialing. With explicit owner approval, one completed run was
briefly moved aside, the idle simulator restarted, and the replay completed.
All 128 original runs were restored and their complete file manifest hashes
verified; the new test run was retained separately. No historical runs were
deleted. The utility-container preflight initially failed because the simulator
image has no shell, before any state changes; the successful operation used an
existing local Alpine image.

Final call `859201e6-a2c5-45bb-bf0f-3c7e4afab309` lasted 255.7 seconds and verified
27 assistant and 20 caller transcript entries, the archive hashes and signal in
both audio directions. It correctly listed all six liability tiers and all 11
third-party medical options, stated that shared-limit charges depend on the
quotation, declined to invent customer popularity, and refused to add the
unconfigured 驾乘险 product. It eventually read all six individual premiums and
totals in one comparison, and rejected the unsupported 500万 tier.

Final normal-content p50/p95 was **985/1,542 ms across 15 responses**; quote
acknowledgment was 1,075 ms (one sample), and loaded-skill p95 was 6.468 ms (12
samples). There were no matched VAD-open/server-cut pairs for a comparable
interruption-delay measurement. Four quotation tasks took 3–10 ms, with 15
cache-hit events and only one foreground pricing job for the initial policy;
eight background jobs retained the original calculation delay. Result delivery
still waited up to 3,625 ms for a conversational gap. The analyzer reported one
missing and one unverified monetary readout; the transcript includes a stale
result/retry and a whole-policy readout before the requested complete comparison.
These are remaining conversation/result-delivery limitations, not a claim that
all quotation interactions or latency gates pass.

The final replay verifies the requested routing, explanation and premium
improvements, but **does not prove a normal-response latency improvement** over
the baseline. Preset first audio at about 380 ms is not counted as substantive
content. No handset-latency claim is made.

The sanitized `comparison.json` records successful preservation checks for the
production binary, profile, saved call, quote plans/API/speech, openers, hangup
rules, silence reactions and MCP catalog after the final test. Concurrent
automatic-dreaming changes were fast-forwarded from the remote Codex branch,
with integrated MCP race and Journal/CRM/tools tests passing. Test services are
separate from production; the isolated Journal and its archives are retained
for review.

After verification, the isolated engine, CRM, pricing service, MCP fixture and
Aria originator were stopped. Only the isolated Journal remains for replay;
recordings, test data and rollback snapshots were preserved.
