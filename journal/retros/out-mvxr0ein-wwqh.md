# Active Call interruption review — out-mvxr0ein-wwqh

Real call on f1fd9a6, 2026-11-01 19:30–19:34 Asia/Shanghai. Compared with the
previous real Active Call out-mvxo348d-w5pb on 45b5564; original prompt, voice
and greeting match. Different conversations, not a matched causal experiment.

- Comparable barge-in cuts: 15 now versus 11 previously; VAD-open → server-cut
  median **207 vs 265 ms** (58 ms / 21.9% lower); p95/max **694 vs 680 ms**.
  The slow tail did not improve. Four current non-barge cancellations are
  retained separately; two are quotation deliveries, not caller interruptions.
- The previous broad 14-cancellation figure included three non-barge events.
  Use the 11-to-15 comparable population above. Detection still requires 320 ms
  accumulated voiced input; audible handset stop is unmeasured.
- Eight interruptions used prefeeding. The 694/669 ms tails had early partials
  at 148/175 ms that remained acknowledgment-compatible; clearer words then
  released the holds. Another 554 ms case waited for caller identity evidence.
- Ordinary responses excluding every quotation-origin request: n=28,
  content p50/p95/max **989/1135/1260 ms**; previous n=32,
  **963.5/1078/1081 ms**. One current ordinary reply exceeded 1200 ms.
- The established conversational metric also includes a 1504 ms spoken quote
  acknowledgment before calculation: n=29, p50/p95/max **991/1260/1504 ms**.
  Its strict gate fails at p95 1260 and one unverified quotation readout.
  This acknowledgment delay is not calculation time and is not hidden.
- First audio including openers/quote requests: n=33, p50/p95/max
  **380/458/688 ms**. No missing ordinary output records.
- Quotation: 18 API/poll cycles **3003–4507 ms**, 16 ahead, zero failures.
  Foreground tools **0/1/1/1/3523 ms**. Verified deterministic readouts:
  **990/869/6148/4424 ms**, plus one unverified result. The 6148 ms case
  includes **4418 ms ready-to-delivery wait**, not slow calculation. Another
  ready result waited 1587 ms and has no deterministic-readout timestamp.
- Archive verified: five files/hashes; **273.620 s** stereo, both channels with
  signal; **34 caller / 40 assistant** transcript entries. Call ended, engine idle.
- Measurements are server output/cut boundaries. Optional assumed +50 ms gives
  ordinary-only p95 1185 ms and canonical conversational p95 1310 ms; neither
  is actual acoustic handset verification.

ISS-095 stays open. Preserve echo/background/acknowledgment checks and original
quotation delay. Next work: useful ASR partials and identity-evidence timing,
model/answer production on slow ordinary replies, and ready-result scheduling.
No runtime change or redial was made during this review.
