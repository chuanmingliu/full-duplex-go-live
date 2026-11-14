# Active Call interruption candidate A — out-mvxpmcug-bpqi

- Simulated call, engine `f1fd9a6`, original GoLive configuration and Active Call;
  run `sim-fefb525a5a26bacab96d3709`, same `set-a-01-barge-in` scenario as baseline.
- VAD-open → server playback cuts: 353 / 157 / 126 ms.
  Median 157 ms; maximum 353 ms. The baseline measured 109/147 ms.
  Detection still required 320 ms accumulated voiced input. These are server
  cut decisions, not handset playback-stop measurements.
- Normal content: one sample at 881 ms. Quotation readout: one at
  925 ms, separately classified. Two first-audio samples at 380 ms.
- Receiver: no warnings/failures. The normal latency gate fails only its
  minimum sample counts; this is not a 20-turn normal-response validation.
- Archive: five files/hashes verified, 13.282s stereo audio, both channels
  with signal, 4 caller and 5 assistant transcript entries.
- Candidate prefeeding is active on later utterances; the first two were not
  prefed. Echo/background/backchannel gates and original settings are unchanged.
- Controlled simulation median: 486 → 310 ms with all negative controls retained.
  Live runs have mixed results and differing turn counts; no consistent live
  improvement or physical handset target is claimed. Keep ISS-095 open.
