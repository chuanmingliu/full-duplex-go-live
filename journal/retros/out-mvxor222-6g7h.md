# Active Call interruption baseline — out-mvxor222-6g7h

- Mode: simulated Active Call; engine `45b5564`; original GoLive profile,
  prompt, voice and greeting retained.
- Scenario: `set-a-01-barge-in`; run `sim-3e8e49c8dff7644ecc9020e7`.
- Two server playback cuts: VAD-open → cut **109 / 147 ms**, median 128 ms.
  The detector accumulated 320 ms of voiced input before opening. These are
  server decisions, not acoustic interruption-stop measurements.
- Receiver: no failures or warnings; one first-audio gap of 540 ms.
- Normal first-content population: **0**. This short interruption scenario
  cannot satisfy the minimum-20 conversational latency gate.
- Quotation result/readout: one at **920 ms**. First audio: one at 380 ms.
  Calculation remains separately classified and its configured delay is unchanged.
- Archive: all five files and hashes verified; 16.673 seconds of stereo audio,
  signal in both channels, two caller and four assistant transcript entries.
- Change under test: candidate ASR prefeeding during the existing playback
  detection window, without lowering echo, speaker-focus or text thresholds.
- Next: run the same scenario after full candidate race tests and CI pass.
  This baseline is not proof of a live-provider or handset improvement.
