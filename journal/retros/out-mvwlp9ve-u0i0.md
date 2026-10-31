# Native transport control — out-mvwlp9ve-u0i0

Simulated call, 2026-11-01, original native GoLive build `9547d22` through
Active Call, repeated after adapter codec experiments. The original saved
prompt, greeting, voice, providers and endpointing configuration were used.

- Receiver first audio: 10 valid samples, median 462 ms, p95/max 567 ms;
  eight invalid observations excluded. Includes fillers.
- Server first audio: 16 samples, p50/p95/max 380 ms.
- Server content: p50 899.5 ms, p95/max 1075 ms. No missing, unverified or
  duplicate output records. Per-call strict gate remains below its minimum
  of 20 samples; these are not physical handset measurements.
- Receiver: 19 remote turns, duration limit, no reported failures.
- Permanent archive: five required files checksum/size verified; stereo
  audio has signal on both channels; 16 caller and 17 assistant transcript
  entries retained.

All three Active Call controls total 26 valid receiver samples, median
466 ms, p95 569 ms, max 601 ms. The initial two PCM16/PCMU adapter trials total 19,
median 457 ms, p95/max 612 ms, with receiver TTS warnings and remote-hangup
failures. A nine-ms median difference does not demonstrate a useful gain.

See the [full comparison](https://github.com/chuanmingliu/full-duplex-go-live/blob/codex/sip-rtp-adapter/docs/telephony/2026-11-01-native-sip-comparison.md).

The final fixed-adapter cohort totals 20 valid samples, median 450 ms, p95
574 ms. It ended cleanly, but 6186/7172 ms quotation readouts still fail
the strict content target.
