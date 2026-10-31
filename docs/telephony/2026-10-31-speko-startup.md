# Speko startup failures on the first SIP call

Call `out-speko-20261031-01` answered, then ended 1.18 seconds later with
`agent reported an error`. No assistant audio or transcript was produced.
Call Journal retains the ringback and inbound audio. No handset latency can
be measured from that attempt.

## Reproduction and findings

An authenticated, no-phone Realtime client reproduced the failure after the
opening release. A direct Speko startup probe then exposed two independent
failures:

1. `audio/pcma` returned `invalid_request`: the Router requires `audio/pcm`.
   The authenticated model catalog advertises 16/24 kHz PCM for `gpt-live-1`.
2. Changing only the upstream audio format to 24 kHz PCM passed format
   validation, but managed `delegation.type=responses` returned
   `provider_unavailable` / `no eligible route satisfies the request`.
   Removing managed delegation allowed `session.started`. Instructions and
   greeting context each worked independently. Separate Speko LLM requests
   to `gpt-6-luna` also succeeded.

The original local tests used a permissive mock that accepted G.711 upstream,
so they could not catch the Router's actual format restriction. Local pool
readiness only checked configuration and capacity; it never validated provider
admission.

## Changes

The SIP side remains G.711 at 8 kHz. The Mac bridge now requests 24 kHz PCM
upstream and converts in both directions. Input uses causal interpolation;
output uses a three-sample averaging filter and preserves incomplete groups
across WebSocket frames. Both directions retain their 20 ms pacing and bounded
queues. This is a narrowband voice conversion, not a high-fidelity resampler.

`SPEKO_DELEGATION_MODE=client` selects client-managed backend work. The voice
model remains `gpt-live-1`; the bridge sends bounded conversation context to
Speko's `/v1/llm/responses`, explicitly selecting `gpt-6-luna`. Completed text
returns with the original delegation ID. No business tools are supplied.
Duplicate delegations do not repeat work; newer delegations cancel older work;
call termination cancels backend requests. Incomplete or failed results are
not presented as successful answers.

The deployed Mac bridge runs with this mode at `127.0.0.1:8095`; the existing
reverse tunnel and ThinkPad SIP settings are unchanged. The binary, PID, and
log are under `bin/speko-runtime/` in the Mac checkout, which is gitignored.
The previous bridge executable is retained there for rollback. It is a
background process and is not yet managed by a reboot supervisor.

## Validation

- Regression test failed against the previous G.711 startup and passed with PCM.
- PCM tests check sample counts and packet-boundary invariance for both laws.
- SIP mock tests exercise answer gating, conversion, pacing and teardown.
- Client backend test covers transcript context, authenticated routing,
  duplicate delegation, and correlated results; incomplete results are rejected.
- Standalone package race tests passed twice from a clean source snapshot;
  `go vet` and the standalone build passed.
- A live no-phone probe with the configured full prompt and greeting produced
  non-silent assistant audio. A separate live LLM probe returned a completed
  `gpt-6-luna` response.

These checks establish admission and audio transport, not real-handset latency.

References: [Speko voice protocol](https://docs.speko.ai/relay/s2s),
[model catalog](https://docs.speko.ai/relay/models),
[Speko text requests](https://docs.speko.ai/relay/llm/responses), and
[GPT-Live client delegation](https://developers.openai.com/api/docs/guides/live-delegation).

## Retry exposed a startup audio backlog

`out-speko-20261031-02` answered and ended after 2.70 seconds with no
assistant audio. A no-phone probe sending G.711 every 20 ms reproduced
`invalid or excessive caller audio`. Packets buffered during upstream setup
were being paced a second time at 20 ms, overflowing the two-second queue.
The earlier no-phone greeting probe did not send microphone frames.

Caller RTP is already paced. The bridge now forwards received caller chunks
immediately and only inserts clocked silence when input is absent. Assistant
playback remains paced and bounded. The startup backlog regression fails on
the old queue and passes on the fix; a live probe with continuous microphone
frames and the configured prompt produced non-silent assistant audio.

Completed external-engine calls can be imported without inventing native-engine
logs using `scripts/local-ops/import-adapter-call.py` on the ThinkPad. It keeps
raw adapter files, records transcript fragments as fragments, and generates
no latency claims. Restarting only the Journal reloads the imported call.

## Remaining intermittent disconnect

`out-speko-20261031-03` delivered 4.48 seconds of assistant RTP audio and
captured both caller and assistant transcript fragments, then ended with an
upstream session error after about 9.30 seconds connected. No RTP loss or
outgoing queue overflow occurred (maximum queue 20 ms). Its complete audio,
ringback, and available transcript are archived in Call Journal.

The upstream error body was discarded by that build. Sustained silence and
recorded-caller replays could succeed, so the remaining disconnect is not
claimed fixed. Structured server diagnostics now retain the upstream code
and a bounded message with the configured credentials redacted. The client
receives only the stage and error code. No raw provider message is exposed
in the client error.

## Reproduced recoverable input-rate failure

A burst-input live probe subsequently reproduced the same fatal bridge error
and captured `input_audio_rate_limit_exceeded`. The provider message says that
excess appends are dropped, the input must stay below 1.2 times real time, and
bursts must not exceed five seconds. The bridge incorrectly ended the call on
this recoverable warning. This reproduces a concrete remaining failure class;
the exact error body from the historical handset attempt was not retained.

The bridge now continues on this specific warning and records it once per
session. Other provider errors remain fatal. It also permits 40 ms of extra
arrival jitter before filling an input gap, preventing a slightly late RTP
frame from being added alongside unnecessary synthetic silence. A regression
fixture injects the warning followed by valid audio and requires that audio
to reach the client. A second fixture ensures 40 jittered caller frames do
not inflate into an excessive number of upstream frames.
