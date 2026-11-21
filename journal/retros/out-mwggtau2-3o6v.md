# Active Call RTP comparison and revised quotation confirmation

Call `out-mwggtau2-3o6v` ran on 2026-11-14 from 21:52:24 to 21:56:48 China time,
using original saved GoLive settings through Active Call. The live engine
identified itself as `82b6a50-mcp`. The owner confirmed that quotation
interruption recovery worked and reported that the first few assistant replies
felt rushed.

## Archive verification

The GoLive plugin verified all five archive file hashes, session closure, 29
caller transcript entries and 34 assistant entries. The stereo PCM16 archive
contains 263.588 seconds of audio with signal on both directions. This verifies
stored chat and media. Acoustic handset audio was not captured.

## RTP and Journal comparison

A dedicated capture recorded the Active Call and RustPBX RTP ranges on the box.
It stopped automatically after this call ended. A marked three-packet local
fixture verified both capture files before dialing and was excluded from the
call analysis. Raw packets, recordings and transport logs remain private on the
box under `codex-latency/rtp-d3efe800-3593-4336-b593-c1134b01d1dd`.

- Active Call emitted 9,083 PCMA audio packets, each with a 160-byte payload.
  RustPBX forwarded 9,083 packets with identical payloads in the same order.
  Both outgoing streams had no missing or duplicate sequence numbers.
- Paired-packet forwarding delay was 0.089 ms median, 0.143 ms p95 and
  2.979 ms maximum at the host capture boundary.
- Packet arrival intervals were approximately 20 ms during speech. Across
  18 continuous speech blocks, wall time divided by declared audio duration
  was approximately 1.000–1.003. The first three blocks showed the same behavior.
  This call provides no evidence of a sustained transport-induced speed-up.
- After decoding PCMA and matching against the downsampled Journal assistant
  track, the median one-second-window correlation was approximately 0.999.
  32 of 40 sampled windows exceeded 0.95 correlation. The eight weaker windows
  were examined in 200 ms pieces. Most pieces matched above 0.98; a few boundary
  pieces remained weaker, and silent pieces cannot support reliable correlation.
  These measurements are not a perceptual sound-quality certification.
- Journal output is placed using playback timestamps and may contain gaps.
  Consequently, alignment offsets can be negative and must not be reported as
  handset latency or treated as a single fixed transport delay.
- Incoming carrier RTP contained 250 repeated sequence numbers and 25 missing
  unique sequences. The PBX-to-Active-Call stream contained seven missing unique
  sequences. These input-direction anomalies do not establish the cause of the
  assistant's previously reported mechanical sound.

The capture supports correct sustained outgoing pacing and byte-preserving PBX
forwarding for this call. The earlier problematic call had no RTP capture, and
this capture ends before carrier and handset playout. The precise cause of the
earlier mechanical sound remains unresolved.

The original profile sets speech speed to 1.2. That setting and conversational
handoff timing are plausible contributors to the rushed impression. The RTP
comparison provides no reason to change codec rate or packet timing.

## Latency

The plugin used analyzer SHA-256
`c5df3bf2a120d4bcc241f75a1e2c28b3205a903dd94b33516fa21e11ea549a50`.

| Measurement | Samples | Median | p95 | Maximum |
| --- | --- | --- | --- | --- |
| Normal first content at server output | 18 | 1,027 ms | 4,977 ms | 4,977 ms |
| First audio including preset openers | 23 | 380 ms | 440 ms | 4,174 ms |
| VAD-open to server playback cut | 14 | 456 ms | 823 ms | 823 ms |
| Quotation result completion | 5 | 3,028 ms | 5,050 ms | 5,050 ms |
| Quotation readout output | 5 | 3,978 ms | 5,050 ms | 5,050 ms |

Normal-content p90 was 1,245 ms. The 1,200 ms goal did not pass; there were also
fewer than the required 20 normal-response samples. Server playback cuts exclude
pre-VAD detection time and downstream queued audio.

Four actual quote submissions produced five linked result deliveries because
one request was coalesced. Two tool operations finished in approximately 3 ms
and 7 ms. Two later operations took approximately 4,030 ms and 3,525 ms. These
spans include tool/backend orchestration and are not isolated calculation CPU
time. They remain outside the normal-response population. Calculation-only
instrumentation is needed to separate service execution from polling and queueing.
Two fixed waiting prehooks were emitted for those later requests. The plugin's
zero quotation-acknowledgment sample count therefore does not mean zero spoken
waiting messages; those scripted messages are a different measurement category.

## Requested behavior change

The owner wants revised quotations to collect all intended adjustments, ask a
natural confirmation question, and calculate only after the caller confirms the
selection. Confirmation and waiting wording should vary with context. The
implementation and rollout option are described in
`docs/architecture/12-quotation-confirmation.md`. Existing production settings
remain the reference for comparing the new behavior in isolated simulations.

## Follow-up simulation

The final isolated DeepSeek replay passed three baseline and nine candidate
turns. The candidate made zero pricing requests while collecting changes and
one request after each explicit approval. It retained pending changes across
clarification and removed them on cancellation. A contextual affirmative to
the question about further changes did not start pricing. Confirmation wording
varied. Initial questions began mock audio at 405–1,447 ms; acknowledgment
audio after approval began at 1,276–1,626 ms. These small synthetic samples do
not establish a real-call or handset improvement. The mock calculation delay
remained three seconds.
