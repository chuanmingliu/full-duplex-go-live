# Handset validation after the RTP pacing fix

Call: `out-sip-32bc369-20261031-01`, running `32bc369` on the isolated box stack.
Answered 2026-10-31 13:25:22.933 UTC; ended 13:27:17.493 UTC.
Connected duration: **114.56 seconds**.

## Pacing and call completion

The sender delivered 3,330 RTP packets (66.6 seconds of outgoing audio) with
a maximum queue of **340 ms**, below the unchanged 500 ms limit. It received
5,728 packets with no detected inbound loss or late/duplicate packets. Thirty
queued frames were cleared during interruptions. The earlier cumulative queue
overflow did not recur.

GoLive logged `hangup: the assistant said goodbye; ending the call`, then
closed its agent connection. The adapter sent SIP BYE, which was acknowledged.
This was an intentional assistant hangup, not another queue overflow.

The adapter nevertheless recorded `failed: true` and `agent connection closed`.
Its reader discarded every WebSocket close code/reason. A regression reproduces
this with GoLive's normal `1000 / assistant_hangup` close frame. The follow-up
fix recognizes the three explicit normal GoLive close reasons only after SIP
answer, records `agent_hangup`, and reports completion. Unknown normal-close
reasons, server shutdown, error close codes, and unacknowledged SIP BYE remain
failures. Historical raw call artifacts are preserved rather than rewritten.

The seven close-classification scenarios passed with race detection, including
an unacknowledged BYE. Three shuffled runs of the full adapter suite passed on
an isolated source snapshot in 50.9 seconds; adapter vet and Linux build passed.
The original pacing-fix GitHub workflow also completed successfully.

## Latency evidence

These values are measured at **GoLive's server audio output**, including the
380 ms endpoint silence. They are not acoustic handset measurements.

| Measurement | Samples | Median | Maximum |
| --- | ---: | ---: | ---: |
| Verified caller reply, first audio | 7 | 1,013 ms | 1,105 ms |
| Verified caller reply, first content | 7 | 1,013 ms | 1,105 ms |
| Linked tool-result content | 1 | 1,664 ms | 1,664 ms |
| Actual quote readout | 1 | 2,802 ms | 2,802 ms |

The seven verified replies span 787–1,105 ms. Two records remain unverified and
two tool links are invalid; they are excluded from the verified distribution.
The quote-result turn was interrupted. Its observed readout time decomposes as
380 ms endpoint silence + 1,117 ms to result-turn start + 1,305 ms to readout.
The early result content began 1,138 ms before the actual quote readout, so
first audio alone understates time to the requested information.

Median reported LLM request-to-first-token stages total about 565 ms, with
174 ms TTS first-audio time. These stages overlap through speculation; summing
all stage medians does not reconstruct a measured turn.

All nine configured filler attempts were skipped with `phrase not cached`.
Thus the verified first-audio results were not artificially shortened by a
filler. One echo-like interruption warning and one rejected telemetry-export
warning also occurred. Those observations need separate investigation before
making claims about full production readiness.

## Recording and deployment

The Call Journal retained audio and 25 transcript lines. All five files in its
archive manifest passed size and SHA-256 checks. The raw adapter WAVs, events,
summary and journal archive remain on the box.

The test engine/journal use ports 9885/9886 under
`/home/chuanming/golive-sip-ab/32bc369`. Production and the original `cb6c639`
stack remain intact. During deployment, the readiness warmup caught unreadable
CA certificates before any call was placed; the image was rebuilt with
`COPY --chmod=0644` for the public CA bundle and then successfully warmed.

The RTP pacing fix has passed this real-call stability check. **The overall
below-1,200 ms handset target is not yet established:** carrier/handset playout
was not measured acoustically, tool readout exceeded the threshold, and the
unverified timing records cannot be counted as passes.
