# Initial dreaming-memory verification and opening feedback — 2026-11-12

Real Active Call `out-mwcylt0w-y3eg` ran engine `dac075b`. The owner supplied done and the feedback “opening should not be interrupted.”

## Observed problem

The fixed greeting began at 12.186 s. Speech detection opened at 12.687 s. The engine yielded the floor at 12.909 s and truncated the greeting after 722 ms of server playback, with 12 characters retained as heard. This led to protected initial-opening playback in PR #8, later verified in call `out-mwd5kg3p-fixg`.

## Memory selection

Catalog revision 9 already contained the reviewed pacing skill `memory-462d1d91e7b0d08c218c5b35`, from synthetic dream `dream-single-question-20261112`. This call had 19 preparations, with 18 no-match outcomes and one cancellation. No memory resource was loaded. Maximum preparation time was 2 ms.

## Experience and retrospective process

The completed archive was imported as episode `out-mwcylt0w-y3eg`. Owner feedback was recorded separately as `owner-opening-20261112`, and the episode was released after done. Dream `dream-real-opening-20261112` pinned the episode and feedback snapshot.

The first review and an explicit retry each failed routing-case validation, leaving zero persisted lessons. PR #8 added one bounded model correction attempt within the existing 90-second deadline, followed by worker validation before draft persistence. After the owner approved DeepSeek processing, the deployed reviewer completed the job and created three candidates.

- `memory-719da9d29cf355ad4bc8443b`.
- `memory-a42a9c8670e340f4ba201e9f`.
- `memory-c9d91349c1f6fc0c9603cccb`.

These candidates remain unpublished. The successful run's per-attempt model count was not retained. Projected conversational evidence and feedback were sent to the configured DeepSeek endpoint under the owner's approval; audio and original archive files remained on the box.

## Archive and measurements

Five archive files verified. Both audio channels contained signal over 147.34 seconds, with 11 caller and 16 assistant transcript entries. Normal first content had six samples, median 1,308.5 ms and p95/maximum 1,544 ms. First audio had ten samples, median 380 ms and p95/maximum 394 ms. Quotation result/readout had four samples, median 1,184 ms and p95/maximum 1,521 ms. Interruption samples were 222 and 123 ms from VAD-open to server cut. The normal latency gate failed. Acoustic handset timing remains unmeasured.

Private evidence is under `/home/chuanming/golive-skills-mcp/backups/dreaming-06c6bb0-20261112/` and `/home/chuanming/golive-telephony/codex-latency/opening-951a6b4/`. The completed live memory verification is recorded on `out-mwd5kg3p-fixg`.
