# UUID native SIP real-call review

2026-11-01, 12:01:59–12:05:09 Asia/Shanghai. Real configured handset through
adapter `fb11e2c` and original GoLive engine `9547d22`, with original saved
instructions, greeting, voice and native provider settings.
Call `out-native-sip-a93db0f8-ff19-4b9d-95f3-09aca39541be`,
session `sess_1802121cae900205da1e35cf`.
[Call Journal](http://192.168.71.9:9896/#/call/out-native-sip-a93db0f8-ff19-4b9d-95f3-09aca39541be).

## Outcome and capture

The assistant initiated hangup; SIP BYE succeeded and the one-off adapter exited
0. Both native engines were idle afterward. Active Call remains running for
rollback. This call again tests assistant-initiated teardown, not handset-originated
BYE routing.

All seven permanent archive files passed byte-size and SHA-256 verification:
`audio.wav`, `call.json`, `golive.log`, `ringback.json`, `ringback.wav`,
`session.json`, and `transcript.txt`. The closed session contains a 190.34-second
stereo PCM16 recording with nonzero signal in both channels, 24 caller transcript
entries and 26 assistant entries. Carrier ringback is retained separately:
8.167875 seconds with signal. Signal checks do not establish intelligibility.
No separate acoustic handset capture was verified.

## Latency and comparison

Canonical scope: caller last sound to GoLive player output. Gate arguments:
`--gate --target-ms 1200 --metric both --min-turns 20`.
Nearest-rank percentiles include interrupted answers; quotation results remain
linked to their original caller rather than becoming extra caller samples.

| Measurement | Samples | Median | p95 | Maximum |
| --- | ---: | ---: | ---: | ---: |
| First audio, including fillers | 21 | 380 ms | 381 ms | 663 ms |
| First content | 21 | 1024 ms | 1235 ms | 1238 ms |
| Actual linked quotation readout | 3 | 1055 ms | 7530 ms | 7530 ms |

**The strict gate fails.** Sample count is sufficient, but first-content p95
exceeds 1200 ms and the linked tool-result/readout p95 is 7530 ms. There are no
missing or unverified output/readout records, invalid links, malformed records,
duplicates or fallback-content measurements. Ten measured answers were interrupted.

The previous real call had 13 replies, median/p95 content 1051/1198 ms and
quotation readouts 1198/1073 ms. This call has more replies, different utterances
and more interruptions; these are descriptive comparisons, not a controlled
performance improvement/regression measurement. Neither call proves acoustic
handset latency. First-content can be a generic lead-in; quotation readout is
reported separately so an early acknowledgment cannot conceal the actual wait.

## Slow quotation breakdown

Task `t3`, origin `item_12`, result `item_14`, took 7530 ms from the caller's last
sound to deterministic readout. The other two readouts were 1055 and 1037 ms.

| Stage | Duration | Evidence |
| --- | ---: | --- |
| Caller endpoint silence | 380 ms | Canonical origin record |
| Endpoint to task submission | Approximately 1214 ms | Timestamp difference; overlaps speculative model work and speech |
| Tool request execution | 4162 ms | `tools: task done` |
| Ready result waiting for spoken acknowledgment | 1773 ms | `waited_for_gap_ms` |
| Result start to prepared readout output | 0 ms | Explicit readout-presence stamp |

Stage sums differ by about 1 ms because individual log timestamps are rounded.
The tool result became ready at 04:03:06.323Z. Its acknowledgment had started at
04:03:05.713Z and finished at 04:03:08.095Z; delivery/readout followed at
04:03:08.096Z. Source review confirms `maybeDeliver` intentionally protects an
already-speaking scripted acknowledgment. This is not the earlier fixed bug
where an entirely unplayed clause blocked a ready result.

The measured tool interval does not separate CRM queueing, upstream calculation
and HTTP time. Further correlation is needed before assigning it to any one
component. Removing all 1773 ms of acknowledgment wait would still leave about
5757 ms: transport tuning alone cannot meet the goal for this request.

All three result deliveries omitted the redundant posthook; task `t3` directly
exercised omission after its acknowledgment naturally completed (ISS-077).
Potential next work is a bounded acknowledgment that can end at a natural phrase
boundary, plus matched cache-hit/miss tool measurements. Preserve caller priority,
result freshness, actual spoken history and interruption accounting. No untested
engine behavior or original configuration was changed for this review (ISS-091).

## Transport, interruptions and telemetry

SIP answer to first outbound RTP was 2.919 ms. Across 26 response starts,
enqueue-to-first-send was median 0.076 ms, maximum 19.555 ms. These are adapter
packet timings, not audible speech at the handset. Peak output queue was 367 ms,
not an added delay on every reply. There were 24 playback clears, 161 cleared
frames and no stale audio events. Accepted-stream counters show no detected loss
or late/duplicate packets; 190 rejected packets have no recorded reason.

One echo-like barge-in diagnostic occurred; its presence does not establish a
missed interruption. A MiniMax warning says bidirectional-only options were
ignored on the configured non-bidirectional endpoint. Neither observation alone
justifies changing the original provider or interruption configuration.

The prefixed UUID resolved the console's call-ID rejection (ISS-090). Engine
telemetry counters changed from sent=0/failed=88 to sent=138/failed=88, with no
dropped events. The first HTTP response explicitly accepted one event, but also
reported `retention_discarded` for `agent.connected`. This verifies ingestion
compatibility, not permanent retention of every console event. Call Journal's
independently verified archive remains the durable evidence.
