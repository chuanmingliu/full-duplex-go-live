# Active Call scripted interruptions — 2026-11-01

Call `out-mvxksnvy-4i3q`, build `45b5564`, original GoLive configuration,
scenario `set-a-01-barge-in`. Both scripted cut-ins completed. No simulator
warnings or failures. No real recipient was dialed.

All five permanent files verified; 16.631 seconds, stereo signal on both
channels, 3 caller / 4 assistant transcript lines. Two paired server VAD-open
to active-playback cut decisions were **154 / 169 ms**. These exclude the
320 ms active-input detection window and transport / audible playout stop.

No qualifying normal content samples: the minimum-20 gate fails. One linked
quotation readout measured 844 ms from its caller endpoint; this is not a
separate quotation calculation-duration measurement. Quotation delay remains
unchanged. First audio including opener was 380 ms. No missing, duplicate or
unverified output records. No handset target claim is made.

Initial verification stopped on the Journal greeting field. Read-only collection
proved the first 41-character opening exactly matched the saved greeting;
a later 12-character system opening overwrote the derived field. ISS-100 fixes
the Journal parser to retain the initial greeting. Call audio was not altered,
the failed receipt is retained, and no redial was made. Journal-only fix `0c2ba18` is now deployed after full CI. The phone engine
remains `45b5564`; this metadata correction does not change its latency.
