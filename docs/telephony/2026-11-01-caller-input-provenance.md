# Result timing when the calling turn never plays

The recorded `f12b6f5` simulator call had six result links whose originating
turns had no playback metric. Result turns did have output evidence. A task can
be submitted from speculative conversation and survive cancellation of its
calling turn; playback records alone cannot preserve that caller's clock.

The engine now snapshots the originating turn's input timing when submitting
or coalescing a task. Result-delivery records retain the monotonic
`origin_to_result_start_ms` and add:

- `origin_timing_version=1`;
- `origin_speech_end_ms`: the captured session-relative caller boundary;
- `origin_end_silence_ms`: the captured endpoint silence, added exactly once.

The snapshot requires the turn's own nonzero speech-end timestamp and
nonnegative input values. Delivery never borrows the engine's newer caller
clock. The new numeric fields contain no credentials or caller text. They are
input evidence, not synthesized output or a completion receipt.

Both the analyzer and Journal accept complete version-1 input evidence to time
a result whose originating turn has no playback record. They still require a
unique explicit task/turn relationship and actual result content output.
Malformed/overflowing/partial fields, conflicting supplied playback evidence,
duplicate links, missing child metrics, missing readout and output without a
metric remain invalid. Old logs without these fields retain their old strict
requirements; historical archives are never backfilled or rewritten.

Rows using this evidence are labelled `origin_evidence=caller_input_v1`.
Absent or cancelled calling playback does not acquire a new conversational
sample, first-audio receipt or joined answer. Quote readout still requires its
own deterministic output timestamp. Non-quote tool results remain subject to
the conversational content gate. Journal evidence version 5 also exposes
`conversational_first_content`, excluding only verified quote-only handoffs;
the original inclusive `first_content` distribution remains available.

Validation: the public-command regression fails on the old analyzer and passes
on the candidate. All 62 analyzer tests pass (8.887 s), the full Journal race
suite passes (3.649 s), and focused engine/Journal tests pass. Tests cover silent
and cancelled origins, exact once-only pause addition, coalesced origins,
legacy behavior, conflicts, duplicate/overflow/partial metadata, missing
readout, non-quote slow results, and no inflated conversational sample count.

This is a measurement repair, not a latency saving. Original provider settings,
quotation delay, scheduling and production services are unchanged. Fresh
recorded SIP validation is required; the six old links remain unverified.
