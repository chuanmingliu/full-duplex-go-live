# Original GoLive Active Call repeat — 2026-11-01

Call `out-mvxlfkke-kr5y`, run `sim-437b48b131514b62dd78a571`, engine `45b5564`.
The same `kb-coverage-nerd` scenario and saved prompt, voice and greeting were
used; the intervening update changed only Journal to `0c2ba18`.
No real recipient was dialed.

30 normal responses: content p50 / p95 / max **787 / 1049 / 1147 ms**.
First audio including opener: **380 / 380 / 404 ms**. The strict server-output
gate passes. Adding the owner's assumed 50 ms gives content p95 **1099 ms**;
this remains an estimate, not acoustic handset verification. No quotation
result/readout samples; original calculation delay is unchanged.

All five permanent files match their manifest. Stereo PCM16 has signal on both
channels over 296.954 seconds, with 31 caller and 32 assistant transcript lines.
No missing, duplicate, malformed or unverified output records. The simulator
reported no warnings or failures; semantic quality was not independently scored.

Seven answered records were interrupted. Only one paired server VAD-open to
cut decision occurred while playback was active: **336 ms**, after a 320 ms
active-input detection window. No unpaired playback cuts. This is not audible
handset-stop latency and one event is not a representative interruption p95.

The previous candidate run on the same engine had content p95 1200 ms. The
new 1049 ms result shows run-to-run variation; it is not a Journal-induced
speedup or a reason to hide the previous miss. Results remain separate.
Handset qualification still needs a clean acoustic capture and twenty valid
normal responses on the configured real phone.
