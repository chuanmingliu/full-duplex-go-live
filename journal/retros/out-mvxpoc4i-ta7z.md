# Active Call interruption candidate B — out-mvxpoc4i-ta7z

- Simulated call, engine `f1fd9a6`, original GoLive configuration and Active Call;
  run `sim-fcaef5f52c949db3b634cdc2`, same `set-a-01-barge-in` scenario as baseline.
- VAD-open → server playback cuts: 100 / 178 / 24 ms.
  Median 100 ms; maximum 178 ms. The baseline measured 109/147 ms.
  Detection still required 320 ms accumulated voiced input. These are server
  cut decisions, not handset playback-stop measurements.
- Normal content: one sample at 942 ms. Quotation readout: one at
  792 ms, separately classified. Two first-audio samples at 380 ms.
- Receiver: no warnings/failures. The normal latency gate fails only its
  minimum sample counts; this is not a 20-turn normal-response validation.
- Archive: five files/hashes verified, 13.518s stereo audio, both channels
  with signal, 3 caller and 5 assistant transcript entries.
- Candidate prefeeding is active on later utterances; the first two were not
  prefed. Echo/background/backchannel gates and original settings are unchanged.
- Controlled simulation median: 486 → 310 ms with all negative controls retained.
  Live runs have mixed results and differing turn counts; no consistent live
  improvement or physical handset target is claimed. Keep ISS-095 open.
