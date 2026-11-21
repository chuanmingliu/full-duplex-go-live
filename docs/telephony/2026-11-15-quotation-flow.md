# Quotation confirmation, routing and waiting speech

The target branch is `codex/telephony-latency`. Testing uses Active Call and the
original saved GoLive prompt, greeting, voice and conversational settings. The
live baseline is `b1aed5a-quote-skill`.

## Changes

- Short adjustments such as “帮我调整到三百万” and follow-up option questions
  inherit a nearby coverage label for skill selection. An intervening unrelated
  caller turn stops that inheritance. Explicit coverage names take precedence.
- Quotation routing also accepts increases, decreases, replacements and removals.
  Short replies inherit a pending quotation context only while a revision is
  staged. This selects guidance; the model and revision guard still decide consent. The
  skill asks one concise confirmation question, preserving earlier changes.
- Tool continuations retain the caller's original timing through multiple model
  rounds. Journal evidence version 6, the numeric analyzer and plugin tests cover
  confirmation questions, generated acknowledgments, silent cancellations,
  duplicate links and out-of-order observations.
- A confirmed quote's generated waiting response uses the existing 400 ms
  ready-result grace. An already available result suppresses the wait phrase.
  Once waiting speech owns the floor, its result waits for it to finish. Caller
  interruption continues to cancel the speech normally. A ready quotation also
  finishes a newer caller response’s speaking segment before taking the floor,
  covering repeated confirmations while pricing is pending.

## Verification scope

The first real-model probe used synthetic Chinese prompts, DeepSeek and mock speech
output. All 127 assertions across 24 observed turns passed, including accumulated
changes, repeated approval, a second revision, three-way comparison, cancellation,
clarification and interruption during the generated wait. The mock quotation
calculation retained its 3,000 ms delay. These timings cover synthetic text input
to mock audio output. [Structured results](evidence/2026-11-15-quotation-flow/real-model.json)

A second pass used the actual isolated MCP router and passed all 129 assertions
across 24 observed turns, including skill loading for removal and confirmation.
[Actual MCP routing results](evidence/2026-11-15-quotation-flow/real-model-mcp.json)

The final default-prompt probe repeats those scenarios with the saved host prompt
and synthetic inputs. [Default-prompt results](evidence/2026-11-15-quotation-flow/real-model-default-prompt.json)

The full-media comparison uses isolated engine, CRM, quotation, MCP, Aria and
Journal instances. The baseline engine is a copy of the running executable.
Both phases use the same saved prompt and scenario. Quotation pricing retains
2,500 ms computation plus up to 2,000 ms jitter. The candidate also retains the
deployed optional VAD code, with energy VAD selected.

## Final full-media results

The final quotation call was `13bf1c97-dd6e-467d-918a-962cc9413767`, lasting
193.3 seconds. It saved 19 caller and 26 assistant transcript entries. The
separate interruption call was `171cc103-cc6f-4147-9dc4-6554ff578a82`.
Both archives passed manifest size/hash checks and contained signal in both
audio channels. [Numeric evidence](evidence/2026-11-15-quotation-flow/call-sim.json)

The final trace staged 300万 and removal of 车损险 together before approval.
It submitted one calculation for that confirmed revision, preserved the selected
400万 policy through a 50万/300万 comparison, and avoided recalculating on a
repeated confirmation. The repeat-confirmation waiting response finished before
the quotation result began. Caller interruptions remained effective.

| Server measurement | Baseline | Final candidate |
| --- | --- | --- |
| Conversational content median | 956 ms, n=7 | 1,058 ms, n=13 |
| Conversational content p95 | 1,137 ms | 3,606 ms |
| Missing caller response observations | 7 | 0 |
| Invalid timing links | 0 | 0 |
| Dedicated interruption cut decision | 457 ms, n=1 | 176 ms, n=1 |
| Quotation result first-content p95 | 7,957 ms, n=4 | 6,125 ms, n=4 |
| Quoted-figure first-output p95 | 9,118 ms, n=4 | 7,126 ms, n=4 |

The candidate's initial response took 3,606 ms and dominates its p95. Its newly
joined confirmation responses took 1,545 and 1,600 ms. A repeated-confirmation
waiting response took 1,556 ms. All include the 380 ms endpoint pause. Three
continuations now contribute to caller response accounting. The candidate's
loaded-skill preparation p95 was 3.246 ms over 12 loads.

These single runs have different generated dialogue and baseline observation
coverage. They do not establish a response-latency improvement or attainment of
the 1,200 ms target. The final quotation call also had one 267 ms interruption
cut decision. These cut timings end at the server decision, with handset output
outside the measurement.

Pricing remained configured at 2,500 ms plus up to 2,000 ms jitter. Foreground
worker durations were 4,158/2,534 ms in the baseline and 2,770/3,863 ms in the
candidate. Each run had five CRM cache hits. Pricing, result handoff and quoted
figure output are reported separately from conversational replies.

Intermediate simulations exposed a routed removal that the model acknowledged
without staging, and a waiting reply cut by a ready quote. Both drove further
fixes before the final replay. One dedicated interruption attempt failed while
preparing caller speech due to provider RPM limiting and is excluded from the
completed-call measurements.

The checked-in [scenario](evidence/2026-11-15-quotation-flow/scenario.json) uses
synthetic lead facts. The deployed receiver requires `retain_audio: false` in
its scenario; the isolated GoLive Journal records both directions independently.
Final default-prompt real-model testing passed all 129 assertions across 24
observed turns. Local analyzer, plugin, MCP, and targeted race regressions also
passed. The broader duplex/Journal suite and GitHub CI are tracked for this
commit separately.

## Configuration and rollback

The original live profile, saved call settings, running executable and business
configuration remain the rollback baseline. The simulation's API endpoints,
synthetic lead identity, storage and local service ports belong to isolated
copies. Their archives are retained separately. After testing, the isolated engine, CRM,
quotation worker, MCP fixture and Aria were stopped. The isolated Journal on port
19970 remains available for archive review. The live service remains
`b1aed5a-quote-skill`.

Existing MCP catalogs are operator-owned. Updating the binary does not replace
their nonempty routing rules. At deployment, save the current admin catalog,
append the missing `motor-quotation` match rules from `defaults()` and publish the
updated staging-before-acknowledgment and concise-confirmation guidance while
preserving custom instructions, exclusions,
state guards and other skills. Use the current catalog revision for the update.
The isolated candidate tests this same append-only routing publication.

Quotation execution and result/readout completion remain separate from normal
response latency. Acoustic handset latency is outside these simulations.
