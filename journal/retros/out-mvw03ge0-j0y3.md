# Real call out-mvw03ge0-j0y3 — 2026-10-31

Build `f842928`; session `sess_9d108fbb4e053eccc2c721d9`. The owner said **done** before this review. All five permanent archive files match their manifest; the stereo 16 kHz PCM16 recording lasts 105.14 seconds. Manifest SHA-256: `7a3f5c9917ca44cf11fd6ea90b9dcddef00254c41db6e55512642ac4582c7dbc`.

## Measured response

Nine caller samples, including two interrupted quotation replies. Server first audio: p50/p95/max **380/380/380 ms**. Server first content: **883/1189/1189 ms**. Three deterministic quotation readouts have p50/p95/max **883/1189/1189 ms**. No missing, duplicate, malformed or unverified rows are reported for this call. The strict gate fails the minimum of twenty samples. These are server-output figures, not acoustic handset observations; the physical handset target remains unproven.

All 29 canonical output segments are timestamped at or after their actual log emission within timestamp precision; none is backdated by more than 5 ms. This supports the deployed ISS-086 correction on this call. There are no opener-to-backchannel conversions here, so this call does not exercise ISS-087.

## Conversation and audio review

The assistant handles a default quote, a question about one coverage, and two tier changes. The first two readouts are interrupted by the caller and their truncated evidence is retained. The final quotation completes; affirmative replies lead into repeated onboarding guidance. An additional acknowledgement is recorded without a separate answer. The farewell completes and the call ends. This transcript review does not establish the intended words behind ambiguous recognition.

The caller channel has 1,353 full-scale samples (peak 32,768); the assistant channel peaks at 13,074 with no full-scale samples. Non-silent channels establish signal, not intelligibility or where input clipping originated. The log includes the existing unsupported-bidirectional-feature warning and a suspected assistant-echo interruption warning. No provider or VAD setting is changed solely on these diagnostics.

## Related correction

The preceding quotation replay `out-mvvxxkse-wiqh` exposed ISS-087: already-started answer content could be discarded as an opener backchannel and lose its latency row. The correction atomically limits this conversion to an exact playback generation with no emitted body, rejects late retired segments, and preserves ordinary interruption handling once body output starts. Both barge-in and speculative-abandonment regressions reproduced the problem before the change. Full CI, deployment and subsequent behavioural verification must be recorded separately; a unit fix does not prove handset latency.
