# Speko call 05: opening delay and interruption control

Call `out-speko-20261031-05` used bridge `37d1908`. The owner reported slow
opening and replies, weak interruption response, and no unexpected hangup.
The original GoLive engine and SIP configuration were not changed.

## Evidence

The first audible opening in the adapter WAV begins at about 12.90 s; SIP
answer was at 6.186 s: approximately **6714 ms after answer**.

Nine selected reply gaps, using the same 20 ms PCM16 RMS-threshold-300 method
as [call 04](2026-10-31-speko-latency.md):

| Caller end (s) | Reply start (s) | Approximate gap (ms) |
| ---: | ---: | ---: |
| 23.30 | 26.64 | 3340 |
| 60.68 | 63.12 | 2440 |
| 82.34 | 84.76 | 2420 |
| 143.48 | 145.90 | 2420 |
| 151.66 | 154.10 | 2440 |
| 154.98 | 157.16 | 2180 |
| 215.24 | 218.66 | 3420 |
| 245.62 | 248.04 | 2420 |
| 276.36 | 278.70 | 2340 |

Median **2420 ms**, range **2180–3420 ms**. These are approximate adapter audio
boundaries, not acoustic handset latency. Reply onset can include acknowledgments.
This is not a controlled comparison with call 04: the conversations differ.

The bridge reported maximum queued output of 1200 ms and **zero client
backend delegations**. The SIP send queue peaked at 140 ms, and no playback
frames were cleared. Caller speech alone had no local mechanism to clear the
bridge and adapter queues: the old path relied on GPT-Live deciding to yield.
The transcript is a stream and its arrival time is not a speech boundary.
We cannot split the remaining upstream delay precisely into recognition and
voice generation from these historical records.

## Changes

Two opt-in settings apply to the Speko Realtime-to-SIP bridge:

- `SPEKO_PRECONNECT=1`: establish and configure the provider session during
  ringing. Send no greeting context or caller audio before answer. Release the
  input clock and greeting only after the named `/v1/opening` request. This
  overlaps connection/model setup with ringing; it does not promise a fixed
  generation time. An unanswered call closes its prepared provider connection.
  The provider may charge for the prepared session during ringing.
- `SPEKO_LOCAL_BARGE_IN=1`: detect 120 ms of sustained receive-leg signal above
  RMS 400, clear locally queued assistant audio and the SIP adapter queue, and
  send a stop/listen instruction. Keep forwarding caller audio. Suppress stale
  model output until the caller has been quiet for 240 ms and the provider
  output has been quiet for 200 ms, then permit the next response.

The gate is based on audio energy, not semantic speech recognition or echo
cancellation. It can yield to a sustained acknowledgment, loud noise or echo.
The separate telephone receive leg reduces, but does not eliminate, that risk.
Interruption recovery uses an audio-quiet boundary because GPT-Live does not
supply this bridge with an authoritative per-utterance completion event.

This follows the distinction between
[voice-model interruption behavior](https://developers.openai.com/api/docs/guides/live-prompting#interruptions)
and [application playback control](https://developers.openai.com/api/docs/guides/voice-server-controls?api=live#control-playback-when-needed).
The application clears and suppresses audio; a steering instruction alone does
not establish that previously generated audio stopped reaching the caller.

## Validation and remaining work

Before the change, targeted tests failed because no connection was prepared
while ringing and sustained caller audio never cleared output. The corrected
tests verify answer gating, cancellation before answer, local clearing, rejection
of stale audio, and recovery to new audio. Noise/click and detector rearm checks,
Speko/SIP race tests, Speko vet and standalone build also pass.

A live no-phone replay with eight seconds of simulated ringing produced first
non-silent output **1178 ms after opening release**, and triggered local playback
clears during recorded caller speech. It completed 65 seconds without a bridge
error. This is a bridge-client observation, not
a real handset result, and a single opening measurement is not a percentile.

Response gaps on the completed handset call remain above 1200 ms. The local
interruption fix and prepared opening need a subsequent explicitly requested
real call; neither establishes the end-to-end target by itself.

## Handset hangup propagation

The provider did not unexpectedly terminate this call. RustPBX logs show the
carrier's BYE and callee hangup at **14:32:14 UTC**. The adapter session remained
open afterward. Once the owner confirmed the phone was hung up, the adapter
container was stopped at **14:32:31 UTC** to close recordings. Its final
`terminated signal received` failure flag describes this cleanup, not another
spontaneous provider disconnect.

Call Journal retains the audio, ringback and 191 imported transcript fragments.
The missed PBX-to-adapter hangup needs SIP packet evidence; do not weaken BYE
source validation or label that propagation issue fixed without such evidence.
