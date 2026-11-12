# Protected greeting and controlled memory probe — 2026-11-12

Real Active Call `out-mwd4vx8b-ak45` ran engine `951a6b4` after PR #8 deployment. The owner supplied done before review.

## Opening and memory observations

The opening began at 8.215 s and completed at 14.295 s with `truncated=false`, a duration of 6.080 seconds. The first detected caller utterance began at 15.196 s, so this call did not exercise overlapping speech during the greeting.

All 12 skill preparations returned no-match against catalog revision 9. Maximum preparation time was 4 ms. The recognized input did not contain the pacing request needed to select `memory-462d1d91e7b0d08c218c5b35`.

## Related controlled probe

A subsequent text-only probe ran the same engine source against the deployed MCP catalog and real DeepSeek model using synthetic instructions, input and mock audio. It observed the pacing skill in the actual model request, with resource digest `sha256:141085d0c1f026ad066bcbe12f80a5ea80966fa0b13039f20304705ef7e9e37e`. The response acknowledged the pacing request and asked one question. All six checks passed, including completed response, loaded memory, a single question and zero business-tool calls. This probe used one model request.

The later authorized call `out-mwd5kg3p-fixg` exercised both overlapping speech and live memory selection. Its retrospective carries the skill's complete lifecycle and live trace.

## Archive and measurements

All five files verified with both audio channels containing signal, 90.16 seconds of audio, 12 caller entries and 12 assistant entries. Normal first content had seven samples, median 1,008 ms and p95/maximum 1,260 ms. First audio had nine samples, median 380 ms and p95/maximum 454 ms. Quotation result/readout had two samples, median 1,044 ms and p95/maximum 1,165 ms. VAD-open to server-cut samples were 107 and 108 ms. The normal 1,200 ms gate failed because one sample exceeded the target and the population was below 20. Acoustic handset timing remains unmeasured.

The completed call was imported and released in the local experience database after done. No retrospective model job was queued for this episode. Private probe and call receipts are under `/home/chuanming/golive-telephony/codex-latency/opening-951a6b4/`.
