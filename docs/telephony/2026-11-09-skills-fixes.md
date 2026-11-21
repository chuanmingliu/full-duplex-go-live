# Skill routing and session recovery, 9 November 2026

## Implemented changes

- The MCP service selects guidance from configurable text patterns, exclusions and confirmed state before the first model completion. The panel edits and previews those rules. Ordinary no-match turns use the original prompt components directly.
- The engine retains unanswered fragments for caller clarifications and renders the latest request in its own user message after an interruption boundary. The boundary preserves prior context for clarifications while pausing independent old tasks. Selection uses that newest input. A topic-changing interruption abandons the old answer, and canceled retrieval cannot restart output.
- `golive.business.event` accepts `addV` on the matching initialized session or sideband. Completion is idempotent and authoritative. Model memory edits cannot create or reverse it. Active WeChat guidance is canceled and replanned on completion.
- The latest memory snapshot is applied before model generation. Completion, refusal and unknown-consent constraints also apply during fallback. Unrelated questions after completion avoid repeated success announcements.

The [implementation guide](../architecture/09-skills-prompt-rendering.md) describes the prompt components, event envelope, routing fields, rollback requirements and remaining integration stages.

## Final comparison

The benchmark runner job was `skills-final-context-8`. The final-code confirmation job was `skills-boundary-confirm-10`. It used real `deepseek-chat` completions, synthetic Chinese prompts and state, and mock TTS. Both modes ran the same candidate engine. Original mode disabled MCP skills. Enabled mode used an isolated instance of the real MCP service on ThinkPad loopback, seeded from versioned skill Markdown.

There were 20 observations per scenario and mode, producing 120 core benchmark trials, plus nine state/fallback cases and ten edge cases. All core benchmark checks passed. The intermediate interruption implementation still appended old WeChat instructions in one edge case. The final separated-message rendering then passed all 26 confirmation cases, including a newly added contextual-clarification case. The final rendering change applies after interruption, and the core benchmark scenarios do not interrupt. The table gives milliseconds from text submission to first mock-audio output, using the median and nearest-rank p95.

| Scenario | Original flow p50 / p95 | Skills p50 / p95 |
| --- | --- | --- |
| Normal response | 661 / 1009 ms | 757 / 1025 ms |
| WeChat guidance | 832 / 1084 ms | 914 / 1128 ms |
| Quotation acknowledgment | 1381 / 2651 ms | 1760 / 2552 ms |

Normal and WeChat turns each used one model completion. The previous sequential implementation added a model completion for skill selection. Its historical WeChat median was 1671 ms and quotation acknowledgment median was 2778 ms. The earlier runs used the deployed catalog, while the current tests use the versioned seed catalog. The historical comparison also spans different provider request times. The paired columns above compare both flows within the same run.

Normal and WeChat p95 were below 1200 ms in this run. Their median skills overhead versus the paired original flow was 96 ms and 82 ms. An earlier run measured WeChat p95 at 1360 ms, so this is insufficient evidence of a stable production latency bound. Provider variability remains visible across runs. Reported distribution differences include model/network variation, response wording and prompt processing. These tests measure the engine's text-to-mock-audio boundary. Actual ASR, synthesis, transport, echo handling and acoustic handset delay require media-path validation.

## Quotation timing

Quotation submission reached the executor at p50/p95 866/1395 ms in original mode and 1008/1419 ms with skills. The separate completion checks measured 3000.514 ms and 3000.180 ms of calculation. Quotation acknowledgments still carried an additional median delay of 379 ms in this run.

The native synthetic executor remained at three seconds. Both modes delivered the result, and same-session revision requests preserved compulsory and damage coverage while changing third-party liability to 300万. Quotation acknowledgment and result completion remain separate from the normal-response target.

## Interruptions and backend events

| Interrupted phase | Mode | New answer first audio |
| --- | --- | --- |
| retrieval | skills | 874 ms |
| playback | original | 938 ms |
| playback | skills | 498 ms |
| retrieval, contextual clarification | skills | 1033 ms |

All four interruption cases stopped old output within the 100 ms assertion window. No output frames were observed after the command timestamp. Topic-changing recovery answered the new insurance-start-time question and omitted the abandoned WeChat or claims-process task. The clarification case continued the WeChat instructions when the caller asked for the next step. This verifies command-to-engine-output cancellation with mock audio.

`addV` passed between turns, during retrieval, during playback and during native quote calculation. The response acknowledged successful addition and ended add-account instructions. Both modes then answered a normal question without repeating the completion notice. Unit tests cover duplicate events, attempted model forgery, whole-object deletion/replacement, and both session protocol adapters. A follow-up comparison ignores only `wechat.status` when validating a pending native quote, so account completion cannot falsely mark the insurance plan as changed. Coverage and other input changes still invalidate the old quote.

Discovery and retrieval outages produced fallback responses. Declined and unknown-consent cases selected no WeChat guidance. Their final concept answers avoided unsolicited invitations and invented navigation paths. These language checks combine focused text assertions with manual review of the stored synthetic output.

## Verification history and deployment state

The initial fix pilot still revived interrupted requests. A first fix separated their history, but broader regression tests showed that callers must also be able to add details before an answer starts. System-only and combined-JSON alternatives still resumed old WeChat instructions in real-model tests. The final rendering keeps prior context, then inserts an interruption boundary and a separate current request. Continuation tests verify that the earlier words remain available across the message list. A real-model clarification case checks that “下一步呢？” continues the prior flow. A subsequent assertion falsely rejected “no need to search again” and was corrected to distinguish a negated step from an instruction. Manual review later found an unknown-consent response that invented a Contacts location and invited an add-account action. A later wording variant still asked whether the caller wanted help adding WeChat. The final fallback instruction ends a concept answer at its definition, and the stronger regression rejects any follow-up question for these definition fixtures. Both final concept responses passed manual review. Earlier artifacts remain available for inspection.

Local focused race tests cover the changed engine and protocol paths. Service race tests, Python renderer regressions, JavaScript syntax, formatting and Go vet passed. The final GitHub CI result belongs to the pushed commit.

The original live engine remains `9e29eed`, using Active Call and the original profile hash `c98dea3a562a9544d6e7242fda0e86621e0a514c941ccd3a66000c085dd483ae`. The runner verified its executable, launch arguments and profile throughout the simulations. Preset openers and Aria binding revision 13 were preserved. The isolated test service exits after each job.

Live activation requires the paired catalog and engine update. The external backend still needs to forward actual `addV` traces to their matching sessions. The original configuration and deployed catalog remain available as the current service state.

## Evidence

- [Final summary](evidence/2026-11-09-skills-fixes/summary.json)
- [Final benchmark and state cases](evidence/2026-11-09-skills-fixes/benchmark-report.json)
- [Benchmark edge cases, including intermediate recovery failure](evidence/2026-11-09-skills-fixes/benchmark-edges.json)
- [Final-code confirmation](evidence/2026-11-09-skills-fixes/recovery-report.json)
- [Final-code edge confirmation](evidence/2026-11-09-skills-fixes/recovery-edges.json)
- [Benchmark fingerprints](evidence/2026-11-09-skills-fixes/benchmark-provenance.json)
- [Source and binary fingerprints](evidence/2026-11-09-skills-fixes/provenance.json)
- [Original runtime status](evidence/2026-11-09-skills-fixes/runtime-status.json)
- [Historical sequential implementation](2026-11-09-skills-real-model.md)
- [Reproduction through the Mac runner](../../scripts/local-ops/README.md#synthetic-real-model-skills-comparison)
