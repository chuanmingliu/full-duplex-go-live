# golive on a phone line

How to put golive behind a SIP media gateway, place a call through it, and read
the latency of that call. Written for the stack on `192.168.71.9`
(GSM gateway → RustPBX → Active Call → agent, originated by Aria), but nothing
in golive is specific to it.

```
PSTN ──GSM──▶ gateway ──SIP──▶ RustPBX ──▶ Active Call :8081
                                               │  OpenAI Realtime JSON
                                               ▼
                               golive  ws://127.0.0.1:8765/v1/realtime
                                  ▲            │ turn boundaries
        POST /v1/opening (dial, pickup)        ▼
              Aria :3000                 latency console :8768/ingest
```

Active Call connects to whatever is at `ws://127.0.0.1:8765/v1/realtime`, and
Aria's "Local" brain points there. golive in `telephony` mode listens exactly
there, so neither needs changing.

## Start it

```bash
# :8765 must be free. If the s2s container holds it:
docker stop s2s-backend
ss -lntp | grep ':8765 ' || echo ":8765 is free"

# provider keys come from an existing .env.local
ENVFILE=~/golive/.env.local ./start.sh telephony
```

Check it from another shell:

```bash
curl -s http://127.0.0.1:8765/healthz
curl -s http://127.0.0.1:8765/v1/pool          # sessions in progress; empty for now
```

To go back: `Ctrl-C`, then `docker start s2s-backend`.

## Place a call

Aria's brain must be `local` (`GET /api/realtime/brain` says which). Then dial
from the Aria page, or:

```bash
curl -s -X POST http://127.0.0.1:3000/api/outbound/dial \
     -H 'content-type: application/json' \
     -d '{"to":"<the number Aria is locked to>"}'
```

What should happen, and where to look if it does not:

| Moment | In `golive-telephony.log` |
| --- | --- |
| Dial | `realtime: opening line ready` — the greeting was synthesized while the phone rings |
| INVITE | `session started … format=audio/pcm rate=16000`, then `realtime: holding the session until the call is answered` |
| Pickup | `realtime: call answered; session opened`, then the greeting plays |
| Each turn | `turn: response latency … first_audio_out_ms=…` |
| Hangup | `session: response latency summary …` |

No `session started` line means Active Call never reached `:8765`. A session
that starts and is never opened means nothing posted `/v1/opening` at pickup —
it starts listening by itself after `realtime.hold_timeout_seconds`.

## Read the latency

```bash
scripts/call-latency.sh golive-telephony.log
```

```
time      turn     spec     asr    llm    seg    tts   audio  +pause
16:14:06  item_2   true      86    222    320    481     481     861
```

`audio` is the turn judged over → first audio written towards the gateway.
`+pause` adds the silence that judgement waited through, so it is the caller's
last sound → first audio leaving golive. The latency console on `:8768` shows
the same boundaries as they happen, next to the SIP side of the call.

Neither includes the path to the handset: Active Call's 20 ms pacing, RustPBX's
media proxy, the gateway's jitter buffer and the GSM leg, in both directions.
That part is only measurable from a recording made at the handset end.

## The levers, in the order they are worth trying

1. **`speculative=false` on most turns** means the pause is being wasted. Look
   for `think: answered without speculating` and its `reason`.
2. **`llm` dominates.** The backend's time to first token is the largest term
   and the only one a config change can halve: a faster provider
   (`GOLIVE_LLM=cerebras`), a shorter prompt, or `duplex.backend_prefix_warm`
   on a provider that caches prefixes.
3. **`seg − llm`** is time spent waiting for a first clause to end.
   `duplex.stream_first_chunk_chars` and `stream_first_chunk_deadline_ms`.
4. **`tts − seg`** is the synthesizer's time to first byte. See BENCHMARK.md,
   *Is the synthesis provider holding your first syllable?*
5. **The pause itself.** `vad.min_silence_ms` is what `+pause` adds. Lower it
   only with `duplex.semantic_endpointing` on, which gives it back to callers
   who stop mid-thought.
