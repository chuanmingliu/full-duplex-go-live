# Covered-continuation candidate: recorded SIP simulator

- Call: `sim-coveredack-8d3132ed-a69c-4e8c-bbc4-3a10ce5e8fbe`
- Engine: `f12b6f5`; CRM diagnostics: `3201ec4`.
- Receiver: `sim-aa20f625e64dfa5f7269ecec`.
- Original GoLive provider profile, saved prompt/greeting/voice and deliberately
  slow quotation service were retained. The same 90-second scenario was used.
- Ended by scenario duration limit; adapter exit 0; all three isolated processes
  exited. Original config hashes and Active Call identity/start are unchanged.
- Archive: all five manifest files hash-verified, 92.910 seconds of stereo PCM,
  signal on both channels, 11 assistant and 12 caller transcript entries.
  Receiver warnings: zero.

## Latency and qualification

| Server metric | p95 | Valid samples |
| --- | ---: | ---: |
| First audio | 1105 ms | 4 |
| Conversational first content | 1105 ms | 4 |
| Verified linked quotation readout | unavailable | 0 |

The separate quotation policy gate **fails**: six unverified result links and
only four conversational samples (minimum 20). The assumed 50 ms telephony and
handset overhead gives an estimated 1155 ms content p95, not a measured handset
result. The previous diagnostic had 1016 ms with seven conversational samples;
these free-running conversations differ and do not establish a causal speedup.

All six unverified links reference calling turns with no playback metric.
Their result turns do have playback records. Do not invent original playback or
remove these failures to obtain a pass. Future instrumentation must retain the
actual caller-input timing independently of whether the calling turn played.
Two result turns also lack an observed quotation readout; one was truncated and
one had changed memory. Quotation completion is not verified for them.

The run exercised one existing superseded-without-audio acknowledgment release.
It did **not** exercise the new delivered-coalesced-continuation retirement, so
that fix's benefit remains the controlled baseline/candidate result: 3010 to
1504 ms ready-to-delivery with the configured 1500 ms wait unchanged. Ready task
gap waits in this SIP trial were 0, 1514, 3171, 1542 and 0 ms. The 3171 ms wait
requires separate analysis; it is not proof the covered-continuation fix failed.

The revised quote task t3 took 7665 ms. Its API gate wait was 3641 ms and its
API operation 4008 ms, consistent with preserved serialized quotation work.
Receiver application audio gaps were 552, 613, 900, 929 and 1750 ms; they are a
different measurement boundary and are not handset or server-gate samples.

Chat/audio remain in Call Journal on the focused experiment source (port 9916).
No real call was placed and the production call engine was not promoted.
