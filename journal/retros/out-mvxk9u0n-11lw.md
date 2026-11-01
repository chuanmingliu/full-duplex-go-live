# Active Call simulation out-mvxk9u0n-11lw — 2026-11-01

Build `9547d22`; original GoLive configuration and saved prompt/voice/greeting.
Scenario `kb-coverage-nerd`. No real recipient was dialed. Permanent archive:
five files verified, stereo signal on both channels, 289.356 seconds, 33 / 34
caller / assistant transcript lines. Signal does not prove intelligibility.

33 normal response samples: content p50 / p95 / max **945 / 1157 / 1279 ms**;
first audio p95 **380 ms**, including openers. No missing, duplicate or
unverified output records. Baseline strict server gate passes; adding the owner’s assumed 50 ms gives p95 1207 ms, an estimate only.
No quotation result samples; calculation settings were unchanged.

The simulator reported two TTS chunk failure(s); this is not a clean quality
pass. The generated dialogue differs from the comparison run, so p95 changes
are not causal proof. Two paired server VAD-open to playback-cut decisions:
**229 / 371 ms**, excluding pre-detection voice and audible stop latency.
No actual handset latency is established.

See `docs/telephony/2026-11-01-active-call-rollout.md` for deployment, comparison,
archive reconciliation and exact evidence. The sub-1200 ms handset goal remains open.
