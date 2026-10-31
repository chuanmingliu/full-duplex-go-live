# Realtime mini trial through Speko

The owner requested a Realtime mini comparison after call
`out-speko-20261031-06`. No real number was dialed for this trial, and the
existing call command mappings and deployed SIP configurations were preserved.

## Route and compatibility

On 2026-10-31, the authenticated Router catalog listed
`gpt-realtime-2.1-mini` at `/v1/realtime` with 24 kHz PCM input and output.
The literal older name `gpt-realtime-mini` was rejected with
`capability_unsupported`. The current mini session returned its exact requested
model name; no fallback model was accepted.

[Speko's Router documentation](https://docs.speko.ai/relay/s2s) describes the
vendor-native Realtime WebSocket route. This differs from the existing
GPT-Live `/v1/live` protocol. The platform/MCP model list and platform session
bootstrap are not the authoritative Router catalog for this experiment; query
[`GET /v1/models`](https://docs.speko.ai/relay/models) with the Router key.

The working request uses:

- `wss://router.speko.dev/v1/realtime?model=gpt-realtime-2.1-mini`.
- Private Speko bearer credential and a fresh `Idempotency-Key` header.
- GA `session.update`, `session.type: realtime`, `output_modalities: [audio]`.
- `audio/pcm` at 24000 Hz in both directions, voice `marin`.
- Server VAD: threshold 0.5, prefix 300 ms, silence 300 ms; response creation
  and interruption enabled.
- Chinese `gpt-4o-mini-transcribe` input transcription.
- Explicit `response.create` for the opening after the simulated ringing hold.

The route rejected `audio/pcma` settings despite initially emitting a
`session.updated` event. That first update is unsolicited setup, not the
acknowledgment of our configuration. A subsequent isolated test established
that `response.create` itself works with supported PCM settings. The replay
now waits for an update containing the actual supplied instructions.

## First 90-second replay

`replay-06c293b48d444ce7` used the same private instructions and caller WAV
as call 06, starting 11.176445 seconds into that WAV (the SIP answer boundary).
The original 8 kHz input was linearly interpolated to 24 kHz. The session
prepared during an eight-second simulated ringing hold. The saved greeting
was requested unchanged. All recordings and transcript events remain private.

The opening's first packet with signal arrived **1184 ms** after release.
Eight automatically generated replies produced these diagnostic observations:

| Caller signal ended | First reply packet with signal | Gap |
| ---: | ---: | ---: |
| 8.861 s | 10.912 s | 2052 ms |
| 28.183 s | 30.373 s | 2190 ms |
| 43.683 s | 45.721 s | 2038 ms |
| 58.325 s | 60.261 s | 1936 ms |
| 62.741 s | 66.428 s | 3687 ms |
| 68.243 s | 70.171 s | 1928 ms |
| 76.803 s | 79.009 s | 2206 ms |
| 85.264 s | 87.369 s | 2106 ms |

Median **2079 ms**, range **1928–3687 ms**. Signal detection uses RMS 300;
caller times are recorded 20 ms frame-send times. Replies are joined to the
preceding VAD-stop notification and subsequent response creation. A packet
with signal can contain initial silence, and packet arrival is not playback.
All nine responses, including the greeting, completed generation. This does
not establish semantic answer correctness or that a caller heard them fully.

VAD-stop notifications arrived about 0.65–0.88 seconds after the detected
signal end for seven of eight turns; the other was about 3.45 seconds. These
intervals include silence classification, transport and local observation.
Do not attribute them all to the configured 300 ms VAD silence setting.

## Repeat with identical settings

`replay-c0238d07f0fe4316` used matching input and instruction hashes. Opening
signal arrived in **1283 ms**. Its eight diagnostic reply gaps were 1595, 1489,
1909, 1663, 1384, 2037, 2403, and 1595 ms: median **1629 ms**, range
**1384–2403 ms**. Eight of nine generated responses completed before the
90-second capture cutoff; the final response had started audio but had not
finished generation when the harness closed it.

The variation between runs is substantial. Neither run had a reply gap below
1200 ms, even at this earlier packet-arrival boundary. These are repeated
observations of eight recorded utterances, not sixteen independent handset
turns. No interruption-to-silence or automatic-hangup result is claimed.

## Shorter end-of-speech pause trial

`replay-7434b7c4ee4d4b52` requested 100 ms server-VAD silence with the same
recording, instructions and other settings. It detected eight speech ends,
the same count as the 300 ms runs. Its eight reply gaps were 2053, 1719, 1747,
1492, 1560, 1448, 1550, and 1604 ms: median **1582 ms**, range **1448–2053 ms**.
Opening signal arrived in 1347 ms. The final response again extended beyond
the capture cutoff. A separate 12-second protocol check
(`replay-333e1e231bae4487`) verified the provider acknowledged PCM24 and the
100 ms VAD setting; it completed with output audio.

This small comparison does not establish a significant improvement over the
better 300 ms run (1629 ms median), and no reply met 1200 ms. Eight detected
ends do not prove that arbitrary future sentences would avoid premature
endpointing. The harness exposes `--vad-silence-ms` for explicit experiments;
its default remains 300 ms and the deployed GPT-Live configuration is unaffected.

## Interpretation and phone integration boundary

This demonstrates a working mini audio session, not a sub-1200 ms phone path.
The client captured packet arrivals without an actual paced speaker, SIP leg,
carrier or handset. The earlier GPT-Live call used different output timing
boundaries, so subtracting these medians would not prove a model latency gain.
The fixed caller recording also cannot adapt to different assistant replies.

Changing only a model string in `cmd/golive-speko` would be incorrect. A real
mini call needs Realtime protocol handling, PCM/G.711 conversion, paced output,
clearing queued audio on barge-in, truncating unheard assistant context,
answer-gated opening, and explicit hangup handling. The existing GPT-Live
client-delegation backend is not part of this mini route. The separate
carrier-BYE propagation problem is not fixed by choosing another voice model.

The reusable no-dial harness is
[`probe-speko-latency.py`](../../scripts/local-ops/probe-speko-latency.py), with
`--router-model gpt-realtime-2.1-mini`. Use
[`analyze-realtime-replay.py`](../../scripts/local-ops/analyze-realtime-replay.py)
on the completed capture to reproduce the diagnostic gaps. The replay harness
without `--router-model` still exercises the existing local GPT-Live bridge. Do not reinterpret `call gpt-live-1` as a mini
call.
