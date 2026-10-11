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

golive's own figure is `vad.min_silence_ms` (380 in the telephony profile). A
Realtime client may ask for another in `turn_detection.silence_duration_ms`, and
golive takes it (`realtime.honor_turn_detection`), never going below
`realtime.min_silence_floor_ms` (200): its detector judges frames by energy,
and the stops inside a word are silence at that scale.

The phone gateway on the test box does ask — `silenceDurationMs: 150`, spelled
in camelCase, which golive reads as well — but the telephony profile sets
`honor_turn_detection: false` and keeps its own 380. That figure was chosen for
a speech model's detector; 150 (200 after the floor) on this one answers a
caller's every hesitation, and since the early final below it would buy no
latency. Each session logs what it was sent, without the content, and what it
did about it:

```
realtime: session.update bytes=554 session_keys=[...] turn_detection={"silenceDurationMs":150,...} ...
realtime: the client asked for an end-of-turn silence; keeping the server's asked_ms=150 using_ms=380
```

The pause is no longer what a caller waits through. `duplex.early_final_ms`
(120) asks the recognizer for its final that far into the pause, and the answer
is begun on it while the rest of the pause runs out; the turn is still taken
only when the pause has, and nothing is heard before then. In the log:

```
asr: asking for a final on the words so far; a continuation is listening
asr: final for the words so far ... took_ms=95 pause_ms=215
asr: the turn closed on words already final
```

So a longer pause costs nothing until it is longer than the answer takes to
prepare (about a second), and can be chosen for how rarely it cuts a caller
off. Each `turn: response latency` line carries the pause that turn ended on
(`end_silence_ms`), which is longer when the caller was judged to be mid-thought.

## The chat model's first token

The largest term, and not one number: the same request seconds apart has
measured anywhere from 170 to 880 ms, and nearly half of them over 600.
`llm_hedge: 4` sends each completion four times and uses whichever copy
speaks first (`llm hedge: a copy answered first`
at debug level). It costs the losing copies' input tokens (they are cancelled at the winner's
first word). Two copies left about one turn in eight over 1200 ms on a call;
the p95 target needs the tail gone. `llm_hedge_after_ms` delays
the second copy, to spend it only on slow turns; 0 sends both at once.

The first completion of a call is slower than the rest whatever the prompt
(1101 ms on a call whose others were near 450). `duplex.backend_prefix_warm_start`
spends the ringing on it: a one-token request with the session's instructions
when the session opens and again as the opening line starts
(`think: prefix warmed reason=...`).

## The synthesizer's first audio

About 180 ms at the median and over 300 one turn in ten, and on an answer's
first segment all of it is silence. `tts_hedge: 2` gives each session a second
connection and asks both for that first segment, playing whichever produces
audio first (`tts hedge: a copy produced audio first` at debug level). It costs
the segment's characters twice and one more request per turn against the
provider's rate limit; a session that cannot get a second connection carries
on without one.

## A line that is never quiet, and a line that returns the assistant's voice

Two things a phone line does that a headset does not. Both were read off the
second real call, where the caller spoke five times before getting an answer.

**The silence never comes.** Line noise sat on the detector's threshold, so the
utterance stayed open for 3.3 s after the caller's last word. golive now ends
the turn on the evidence it does have: the recognizer's words have not changed
for `duplex.settled_close_ms` (600) and less than half of that time was voiced.

```
listen: no new words and the line never went quiet; taking the turn
```

And sound resuming after a pause no longer throws away the answer begun in that
pause; only the *words* changing does.

**The assistant hears itself.** The end of its own sentence comes back through
the caller's handset after golive has finished sending it, and opens a turn.
`vad.barge_in_margin_db` (16) is how far above the room a sound must be to
interrupt, and `vad.echo_tail_ms` (600) keeps that bar up after the assistant
stops. If the log still says

```
this barge-in looks like the assistant's own voice returning
```

raise the margin towards what the line suggests (`echo_floor_db` on that line).

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
