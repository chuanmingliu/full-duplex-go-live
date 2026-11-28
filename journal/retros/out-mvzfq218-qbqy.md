# Real Active Call backchannel review — out-mvzfq218-qbqy

Build **b51e959**, 2026-11-02 23:49:48–23:52:30 Asia/Shanghai. Reviewed after
owner **done**. Original GoLive settings; preset openers; `asr_assist` enabled.

- Detector: **3,883 chunks, zero fallback, 14 fresh observations, 3 assists**.
  All three asked ASR for a final at 40 ms silence versus the original 120 ms.
  Two led to cuts 167/129 ms after VAD-open; one remained an acknowledgment.
  This proves an 80 ms request head start, not an 80 ms handset saving.
- Three acknowledgments retained by the original controller, including one
  with a strong model floor signal. Keep transcript/speaker/echo safeguards.
- Normal content, quotation origins excluded: **14 samples**, median **919.5 ms**,
  p95/max **1,302 ms**; **12/14 below 1,200 ms**. Previous call: n=8,
  median 1,641.5 ms, p95 2,149 ms. Different conversations; no causal claim.
- Interruptions: **11 cuts**, VAD-open → server decision median **168 ms**,
  p95/max **407 ms**. Previous call: n=2, median 235 ms, max 358 ms.
  Longest cut waited for 400 ms primary-caller evidence after an initial
  non-caller classification. One non-barge tool-result replacement excluded.
- Main LLM request/start → first token: **392–789 ms**, median 502.5 ms.
  The two slow normal responses spent 789/730 ms here. First token → content
  median 258.5 ms. Preserve preset/main-answer coordination.
- Quotation, separate: 17 calculation/poll cycles **3,005–4,507 ms**;
  prepared foreground tasks 0–4 ms; verified readouts **989/1,111/2,573 ms**.
  Result scheduling waits up to 2,322 ms are not calculation time.
  A chained t4→t5 result lacks a verified readout/caller-origin link; retain
  the unverified flags and investigate provenance separately.
- Strict gate **FAILS**: insufficient 20-turn population, normal p95 above
  target and unverified quotation chain. No missing output records.
  Owner's optional +50 ms estimate gives **1,352 ms** normal p95; actual
  acoustic handset latency is unverified.
- Five archive files verified; **161.42 s**, both audio directions, **23 caller /
  24 assistant** transcript entries. Session closed; healthy engine, zero
  active sessions. Original prompt/voice/greeting and trial profile preserved.

Keep the guarded integration with legacy fallback. Prioritize the normal LLM
first-token tail, without weakening background-voice or acknowledgment checks.
No runtime change or redial during this review.
