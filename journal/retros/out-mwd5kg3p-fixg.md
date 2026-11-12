# Dreaming memory and protected opening verified — 2026-11-12

Real Active Call `out-mwd5kg3p-fixg` ran engine `951a6b4`. The owner supplied done before review. This record connects the published memory skill, its originating dream, the live MCP trace, and the observed answer.

## Memory skill

- Skill ID `memory-462d1d91e7b0d08c218c5b35`.
- Title “Sequential single-question pacing when caller asks one question at a time”.
- Resource `skill://memory-462d1d91e7b0d08c218c5b35/SKILL.md`.
- SHA-256 `141085d0c1f026ad066bcbe12f80a5ea80966fa0b13039f20304705ef7e9e37e`.
- Published in catalog revision 9 with event `memory.recalled`, enabled, and no required working-memory state.

The reviewed procedure says to acknowledge an explicit request for one question at a time, ask exactly one question, and wait for the answer. Timing, coverage, contact-channel and scheduling requests must not be bundled into that acknowledgment turn. The next question depends on the previous answer. A later withdrawal of the constraint permits normal flow. Current user input, authoritative tool results and host instructions govern application of the lesson.

## Originating dream and publication

Two explicitly synthetic examples were stored as `synthetic-single-question-a-20261112` and `synthetic-single-question-b-20261112`. Their fictional feedback was labeled synthetic. The real DeepSeek reviewer processed `dream-single-question-20261112`, producing this procedural lesson and its routing rules. Evidence references, bounded text, regex syntax, and positive/negative routing cases passed validation. The lesson was reviewed and published with explicit allowance for simulation evidence. The published resource has the same digest in the controlled probe and this real call.

The private experience database is `/home/chuanming/golive-skills-mcp/data/memory/experiences.sqlite`. The published catalog is `/home/chuanming/golive-skills-mcp/data/catalog.json`. Raw evidence and credentials remain in their private stores.

## Selection, loading and injection

The pacing request appears at call time 22.738 s. The catalog's matching expression was `(?i)(先问|只问).{0,8}(一|一个|一件事)`. No exclusion matched. The quotation, WeChat and insurance-concepts skills had no matching text rules for this request.

At 22.879 s, engine turn `item_4`, response `resp_sess_8ba8854b55996324ef343b34_3`, completed preparation with status `loaded` in 3.380 ms. Working-memory version remained 0 through preparation.

- Snapshot current turn took 3 microseconds.
- `prompts/get` on `outbound-turn` with event `auto` took 1,031 microseconds and selected the resource from catalog revision 9.
- Selection validation took 12 microseconds.
- `skills/get` took 468 microseconds. Manifest verification took 16 microseconds.
- `resources/read` took 994 microseconds. Size, digest and frontmatter verification took 21 microseconds.
- A second `prompts/get` took 764 microseconds and confirmed catalog revision 9.
- Waiting for the engine loop took 6 microseconds. Working-memory recheck and rendering took 60 microseconds.

Renderer v1 appended a system message named `load_business_skill` containing loaded status, turn scope, working-memory version, resource URI, digest and the verified lesson body. It then appended the instruction to apply the guidance to the current request within host policy and existing tool permissions. The model call followed preparation. The message structure is established by deployed code and the trace; a complete provider request body was not retained.

The call contains 16 preparation attempts, with one loaded and 15 no-match outcomes. The Skills / MCP section above preserves every attempt and its per-stage timings. The following assistant response contained 29 characters and one question mark before the next caller turn. This observation and the controlled test support compatible single-question behavior. The skill body applies to the current request; continued pacing in later turns relies on conversation history because no dedicated persistent pacing field exists.

## Related opening process

The fixed greeting began at 5.143 s. Speech overlapped at 5.445 and 8.277 s. The engine kept the floor and retained two utterances. At 11.223 s it completed the 6.080-second greeting with `truncated=false`, then answered the deferred input. Later responses remained interruptible. Opening protection is implemented in engine playback and turn handling.

## Archive and measurements

All five archive files verified, with two audio channels containing signal, 142.16 seconds of audio, 15 caller entries and 17 assistant entries. The permanent archive is `/home/chuanming/golive-archive/calls/2026/11/2026-11-12_141417_out-mwd5kg3p-fixg`.

- Normal first content, 11 samples, had median 1,084 ms and p95/maximum 3,230 ms.
- First audio including openers, 13 samples, had median 380 ms and p95/maximum 2,485 ms.
- Quotation result/readout, two samples, had median 1,103 ms and p95/maximum 1,284 ms.
- VAD-open to server playback cut measured 57, 1 and 126 ms, with zero unpaired cuts and one retained backchannel acknowledgment.

The 3,230 ms content outlier includes the protected opening wait. Its retained input waited 2,105 ms for the greeting to finish before turn startup. The pacing-memory turn's content latency was 1,229 ms including end silence. The normal 1,200 ms gate remains failed, with two samples at or above target and 11 of the required 20 samples. Acoustic handset timing remains unmeasured.

Analyzer SHA-256 `c5df3bf2a120d4bcc241f75a1e2c28b3205a903dd94b33516fa21e11ea549a50`.

## Related records and current state

The initial call `out-mwcylt0w-y3eg` exposed the cut opening and generated the owner feedback. Call `out-mwd4vx8b-ak45` verified complete greeting playback but did not exercise overlap or the pacing trigger. A controlled synthetic engine probe then verified this same resource in an actual DeepSeek request and produced a single-question response.

The earlier real-call dream `dream-real-opening-20261112` completed after deployment of bounded routing repair. Its three candidates, `memory-719da9d29cf355ad4bc8443b`, `memory-a42a9c8670e340f4ba201e9f` and `memory-c9d91349c1f6fc0c9603cccb`, remain unpublished. The per-attempt model count was not recorded, so successful completion alone does not establish that the repair request was needed.

This completed call was imported and released in the local experience database after done. No new dream was queued for it. PR #7 introduced dreaming memory and PR #8 added protected opening playback and bounded validation repair. Private deployment and verification receipts are under `/home/chuanming/golive-telephony/codex-latency/opening-951a6b4/`. The demo-only MCP environment override is backed up on the box and remains outside the repository.
