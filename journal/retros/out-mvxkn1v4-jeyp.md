# Active Call simulation out-mvxkn1v4-jeyp — 2026-11-01

Build `45b5564`; original GoLive configuration and saved prompt/voice/greeting.
Scenario `kb-coverage-nerd`. No real recipient was dialed. Permanent archive:
five files verified, stereo signal on both channels, 243.019 seconds, 33 / 33
caller / assistant transcript lines. Signal does not prove intelligibility.

31 normal response samples: content p50 / p95 / max **933 / 1200 / 1300 ms**;
first audio p95 **380 ms**, including openers. No missing, duplicate or
unverified output records. Candidate strict server gate fails because 1200 ms is not below 1200 ms. Adding the assumed 50 ms gives 1250 ms, an estimate only.
No quotation result samples; calculation settings were unchanged.

The simulator reported one TTS chunk failure(s); this is not a clean quality
pass. The generated dialogue differs from the comparison run, so p95 changes
are not causal proof. Two paired server VAD-open to playback-cut decisions:
**185 / 146 ms**, excluding pre-detection voice and audible stop latency.
No actual handset latency is established.

See `docs/telephony/2026-11-01-active-call-rollout.md` for deployment, comparison,
archive reconciliation and exact evidence. The sub-1200 ms handset goal remains open.
