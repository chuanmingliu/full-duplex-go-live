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
there, so neither needs changing. Aria appends `?call=<call id>` to that URL;
golive files the session under it, releases the opening to that call when
`POST /v1/opening` names it, and reports each turn to the console under it.

Anything else asked of `:8765` that golive does not serve is logged as
`request for an endpoint this server does not have` — worth a grep after the
first call, since golive is standing where another agent used to.

## Start it

On the box, from the unpacked bundle:

```bash
scripts/phone-brain.sh status     # what holds :8765, PBX and gateway state; changes nothing
scripts/phone-brain.sh golive     # stop the container on :8765, start golive there
scripts/phone-brain.sh restart    # replace a running golive with a new build; no sudo
scripts/phone-brain.sh bench      # this box's real-provider latency, no phone involved
scripts/phone-brain.sh restore    # stop golive, start that container again
```

`golive` finds whichever container is listening on the agent port, stops it,
writes its name down for `restore`, and starts golive on the address that
container was using. Provider keys are read from `./.env.local`,
`~/golive-telephony.env` or `~/golive/.env.local`, or from `ENVFILE=`. It uses
`sudo` for docker when the user is not in the docker group.

`bench` has golive say a handful of sentences with its own synthesizer and plays
them back to it as the caller, so a real recognizer has real speech to hear on a
machine with no recordings. It prints last sound → first audio for each turn and
the per-stage table below. This is the number to get right before a phone is
involved: the phone can only add to it.

By hand, the same thing is:

```bash
docker stop s2s-backend                       # :8765 must be free
ENVFILE=~/golive/.env.local ./start.sh telephony
curl -s http://127.0.0.1:8765/v1/pool         # "service":"golive"
```

## How long a pause ends a turn

A Realtime client says so in `turn_detection.silence_duration_ms`, and golive
takes it from there (`realtime.honor_turn_detection`), not from its own
`vad.min_silence_ms`. The call originator on the test box asks for 150 ms.
golive will not go below `realtime.min_silence_floor_ms` (200): its detector
judges frames by energy, and the stops inside a word are silence at that scale.
The log says what was asked and what is in force:

```
realtime: end-of-turn silence taken from the client's turn detection asked_ms=150 using_ms=200 floor_ms=200
```

This pause is the first thing a caller waits through on every turn. Each
`turn: response latency` line carries the one that turn actually ended on
(`end_silence_ms`), which is longer when the caller was judged to be mid-thought.

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
