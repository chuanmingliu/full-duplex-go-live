# Active Call audio fault during quotation-recovery trial

Call `out-mwg5x8a3-pkz0` used the original GoLive Active Call route on
2026-11-14, with test build `87b9b92-quote-recovery`. The build includes the
deployed RealtimeVAD integration and enables quotation interruption recovery.
The previous `5ea1c7d` executable and original profile are retained on the box
under `codex-latency/quote-recovery-live-v2-20261114/rollback`.

## Outcome

The owner reported mechanical-sounding audio from the opening greeting.
In the later review, the owner confirmed that the Call Journal recording
does not contain the mechanical noise heard on the phone. The session lasted 23 seconds,
including the pre-answer interval. It contains one assistant greeting and zero
recognized caller transcript entries. Quotation recovery, normal-response
latency and conversational interruption behavior were not exercised.

## Verified evidence

- The five archive files match their manifest hashes.
- The stereo PCM16 archive is 16 kHz and 23.138 seconds long. Both channels
  contain signal. This does not prove intelligibility or a two-sided transcript.
- Plugin analysis refused conversation verification because the caller
  transcript is missing. There are no qualifying normal-response samples.
- MiniMax opening preparation hit its connection deadline before answer.
- Tencent ASR reported a connection deadline failure during the answered call.
- The first greeting segment began about 961 ms after the answer-release event.
  Its reported audio duration was 3,237 ms. The second segment's first audio
  arrived about 1,014 ms after that first segment's nominal audio end. This is
  a server-side scheduling calculation, without handset-playout confirmation.
- The second TTS vendor, Fish, was requested twice and spoke zero segments.
  MiniMax supplied the greeting. A secondary connection also logged a resync
  timeout. These logs do not establish a voice substitution.
- Active Call negotiated PCMA at 8 kHz. Its three available inbound RTCP
  snapshots report zero packets lost. The final snapshot contains 759 inbound
  packets and 342 outbound packets. No packet capture or remote outbound-loss
  report was retained, so exact RTP cadence and handset delivery remain unknown.
- Later unauthenticated TCP/TLS checks succeeded three times for each speech
  provider. Tencent TLS handshakes took 129–211 ms and MiniMax took 60–86 ms.
  Those checks establish recovered connectivity at check time, not successful
  authenticated recognition/synthesis or the earlier call's network condition.

## Follow-up provider checks

Isolated checks completed at approximately 17:09 China time on the same day,
through the existing Mac runner. The probe used the saved greeting, live
profile and launch environment, and the same voice-resolution function as
the engine. Audio samples remain private on the box. These checks did not
place a call or restart a service.

- Both greeting segments in the failed call selected MiniMax account 1.
  There was no account switch between those segments.
- The realtime client's voice name resolved to the configured GoLive voice.
- Two direct synthesis requests per configured account succeeded. Account 1
  produced first audio 228–295 ms after the synthesis request; account 2 took
  256–278 ms. Connection setup took 117–432 ms separately.
- Four saved PCM samples contain 6.4–7.0 seconds of audio each, with zero
  full-scale clipped samples. This signal check does not establish perceived
  voice quality.
- Three authenticated Tencent WebSocket connections received successful
  initial provider responses in 191–220 ms. Recognition accuracy was not
  tested in these handshake checks.
- The direct probe submitted the complete greeting as one segment. Its
  first-audio times exclude phone transport, engine segmentation and playout,
  so they cannot establish a call-latency improvement over the failed call.

## Interpretation and follow-up

The observed speech-provider timeouts and greeting gap are confirmed faults.
Later authenticated checks succeeded, supporting a transient service or
connectivity problem during this call; the precise failing network component
is unknown.
The owner's clean-recording comparison shifts the mechanical-noise investigation
to the path after the recorded GoLive output, including Active Call audio
conversion and pacing, RustPBX forwarding, and the carrier/handset leg. The
provider timeouts remain separate observations; their role in the mechanical
noise has not been established. Inbound packet-loss snapshots cannot validate
outbound delivery. The exact failing hop remains unresolved.

A subsequent authorized call should capture RTP timing on both the Active
Call-to-RustPBX and RustPBX-to-carrier legs, with a narrow call-specific filter.
Compare decoded outbound RTP against the Journal assistant track and measure
payload sizes, sequence gaps, timestamp increments and cumulative pacing drift
per continuous speech interval. If both outgoing legs preserve the source
audio and timing, continue with the carrier/handset path. Keep packet captures
private because they contain reconstructable call audio.

The later deployment check found the same running Active Call and RustPBX
containers that started on November 6, both with zero container restarts.
That check excludes a container replacement at the test time, while leaving
per-call media negotiation and transient delivery faults open.

Quotation calculation timing is separate and unavailable in this attempt.
The recovery feature has simulation coverage but has not passed a practical
conversation test. No new real call was placed after the owner's done signal.
Raw audio and transport logs remain private on the box.
