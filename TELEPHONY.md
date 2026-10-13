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
speaks first (`llm hedge: a copy answered first` at debug level). It costs the
losing copies' input tokens (they are cancelled at the winner's first word).

Copies only help if they are slow independently, and on one key they were not:
one turn in ten had every copy slow. So the copies go down **lanes** — each a
key and a connection of its own — and the keys are a pool:

```bash
scripts/phone-brain.sh addkey      # once per key; pasted unseen, kept in a 0600 file (addkey minimax: a synthesis account; addkey siliconflow, addkey openrouter: candidate chat-model vendors)
scripts/phone-brain.sh keys        # how many the pool has (counts, never the keys)
scripts/phone-brain.sh restart     # golive reads them at start
scripts/phone-brain.sh llm -pool   # what they are worth: a key per lane against all lanes on one key
```

Four keys give each of four copies an account of its own. A session keeps the
same lanes for its whole length, because the vendor's prompt cache belongs to
an account; the warm-up request at the start of a call goes to every lane and
is allowed to finish on each. A key the vendor refuses (wrong, out of credit)
is rested for ten minutes and the log says which by position: `llm: a key was
refused; resting it key=2 of=4`.

The first completion of a call is slower than the rest whatever the prompt
(1101 ms on a call whose others were near 450). `duplex.backend_prefix_warm_start`
spends the ringing on it: a one-token request with the session's instructions
when the session opens and again as the opening line starts
(`think: prefix warmed reason=...`).

## Placing the same call again, from the box

The prompt for a call is written in the originator's web page and kept in that
browser, so a call placed from anywhere else carries none of it. golive keeps
what the last answered call was given — its instructions and its opening line
— in a file only its own user can read (`last_call_file`; `phone-brain.sh`
sets it to `~/golive-last-call.json`), whenever a call that brought
instructions is answered. Then:

```bash
scripts/phone-brain.sh call show   # what is kept: when, how long, a hash. Not the words.
scripts/phone-brain.sh call        # dial it: same instructions, same opening line
```

`call` reads the one number the originator allows from the originator's env
file and posts the dial to it locally; neither the number nor the prompt is
printed. Dial once from the page after changing the prompt, and that is what
`call` places from then on. `instructions_sha` on the `realtime:
session.update` log line says whether two calls were given the same prompt.

## A call on a rendered prompt: the lead service

`scripts/phone-brain.sh crm start` runs a small service (`bin/crm`, port 8791
on this machine and on the home network) that keeps prompt templates, renders
one for a set of variables, and places the call with the result. It is the
first part of the lead store; templates and calls are what it has so far.

A template is the prompt as it is written for a Python renderer — Jinja2, with
`{% if x is defined and x is not none and x != "" %}`, `{% set years =
(months | int // 12) %}`, `{{ when.split(' ')[0] if ' ' in when else when }}`
and the `-` of whitespace control. It is rendered here by `internal/jinja`,
which is checked against Jinja2 itself on 1,300 cases and must agree to the
character; a construct it does not have (macros, includes, block sets) is
refused when the template is saved, with where it is, never guessed at.

```bash
scripts/phone-brain.sh crm put car-renewal ~/prompt.j2 "first version"   # a new version each time the text changes
scripts/phone-brain.sh crm templates
scripts/phone-brain.sh crm render car-renewal '{"carNum":"…","vehicleAge":"38","is_sh":true,"start":"您好…"}'
scripts/phone-brain.sh crm call   car-renewal '{…}' dry      # everything but the dial
scripts/phone-brain.sh crm call   car-renewal '{…}'          # rings the originator's locked line
scripts/phone-brain.sh crm calls
```

Or over HTTP, from anything on the box or the home network, with the token in
`~/golive-crm/api.token` (`crm token` prints it, to a terminal only):

```bash
T="Authorization: Bearer $(cat ~/golive-crm/api.token)"
curl -H "$T" -X PUT --data-binary @prompt.j2 http://127.0.0.1:8791/api/templates/car-renewal
curl -H "$T" -d '{"template":"car-renewal","vars":{"carNum":"…","start":"…"}}' http://127.0.0.1:8791/api/render
curl -H "$T" -d '{"template":"car-renewal","vars":{…},"lead":"L000123"}'      http://127.0.0.1:8791/api/call
```

| Request | What it does |
| --- | --- |
| `PUT /api/templates/{name}` | the body is the template; saved as the next version (the same text again is the same version) |
| `GET /api/templates`, `GET /api/templates/{name}?version=n` | what is saved; one version with its text |
| `POST /api/render` `{template, version?, vars}` | the rendered prompt, with what was supplied, what the template reads that was not (`undefined`), and what was supplied that it never reads (`unused`: a misspelt name) |
| `POST /api/call` `{template, version?, vars, greeting?, lead?, dry_run?}` | renders, checks no call is in progress, and dials; answers with the call id, which is the journal's |
| `GET /api/calls`, `GET /api/renders/{sha}` | the calls placed; the exact prompt one ran on |

Things to know:

- **The opening line** is `greeting`, or the variable `start` when that is
  left out. `nowDateTime` is filled in with the time of the render when the
  template reads it and the request does not give it.
- **A missing variable renders as Jinja2 renders it**: as nothing, or as the
  branch the template takes for it. `undefined` in the answer lists them. A
  template that does arithmetic on a missing value fails, as it does in Python.
- **Every call rings the originator's one locked number**, whichever lead it
  is for. The number is read from the originator's env file as the call is
  placed and is in no answer and no log.
- **What is kept** (`~/golive-crm`, the owner's alone): every version of every
  template in a file nothing rewrites; the exact prompt of every call
  (`renders/<sha>.txt`); and `events.jsonl`, one line for every save and every
  call, each carrying a checksum of itself and all before it. A line edited,
  removed or added by hand breaks the chain and the service will not start on
  it (`crm verify`). `crm.log` is the operational log: requests and outcomes,
  never a lead's details.
- **The prompt cache.** `static_chars` is how much of a template comes before
  its first variable. The chat model keeps a prompt's beginning between calls;
  what follows the first thing that differs is read afresh for each lead.
  golive warms the cache with the call's own prompt while the phone rings, so
  this costs the first answer little — but a template whose variables come
  late keeps more of it warm.

## The call journal

Every call golive answers, kept where the next round of work can look at it:

```bash
scripts/phone-brain.sh journal start     # prints the address, e.g. http://192.168.71.9:8790/
scripts/phone-brain.sh journal status | stop
```

It is a page on the home network, served by its own small process
(`bin/journal-linux-*`), so changing it never restarts golive. It reads
golive's log and, when calls are recorded (`record on`), golive's store. It has
no login. It listens on one address, the one this box has on the home network
(the interface the default route leaves by) — not the VPN tunnel, not the
container bridges. `JOURNAL_ADDR` overrides it. Since 10-12 the owner also
publishes it as `robotgo.liuchuanming.ai` through the Cloudflare tunnel; see
"The browser demo, and what the public hostnames point at" for what then
protects it. The one thing the page can write is the
owner's own note on a call.

What it shows:

- **Overview**: the target, the current build's pooled figures with the 95%
  interval for the share of turns over target, a mark per call over time, the
  builds pooled, and the list of calls. A call with a recording has a play
  button in its row: it plays there, without opening the call, and a line
  under the row shows how far it has got.
- **A call**: the recording as two lanes (caller, assistant) with each turn's
  wait drawn on it; the conversation turn by turn with the wait between a
  question and its answer; a waterfall of every turn's stages, which add up to
  its total; where the median turn's time goes; what else happened (guesses
  dropped, interruptions, which copy won, the synthesizer's first audios);
  warnings; the retrospective; the owner's rating and note.
- **Issues**: everything found so far — symptom, cause, fix, commit, the call
  that verified it — and **Decisions**: the options that need the owner.

Where its content comes from:

| | Source | Kept |
| --- | --- | --- |
| Figures, stages, conversation | `golive-telephony.log`, re-read when it grows | for good, in the archive (below) |
| Audio | `~/golive-calls/<session>/` (golive's store) until the call is archived | for good, in the archive; golive's working copy goes after `CALLS_TTL_HOURS`, a week |
| Retrospectives, issues, decisions | `journal/` in this repository, shipped in the bundle | with the code |
| The owner's notes | written from the page | `~/golive-journal/feedback/` |

After a call, a retrospective is a file: `journal/retros/<call id>.md`, a few
headed paragraphs — what happened, the slow turns and their causes, what was
changed, what is open. It contains no transcript and nothing personal; the
repository is public. The journal's figures for a call are the ones
`scripts/call-latency.sh` prints (a test holds them to that).

## The browser demo, and what the public hostnames point at

```bash
scripts/phone-brain.sh demo on        # a second golive of this build, with the test page, on 127.0.0.1:9080
scripts/phone-brain.sh demo status | off
```

`golive.liuchuanming.ai` is routed by the tunnel to port 9080. What used to be
there was a September build started by hand in another directory; it crashed
on 10-10, the box rebooted on 10-11, nothing brought it back, and the address
answered 502 until 10-12. The demo is now this bundle's own binary, run apart
from the phone line's golive: its own port, the browser profile
(`configs/tencent-deepseek-minimax.json`), its own log (`golive-demo.log`), no
recording, no telemetry, nothing in the journal or the archive. Once turned on
it is started whenever golive is, and kept running by the same kind of loop.

It shares the provider accounts with the phone line, and the page is public
with no login: anyone with the address can talk to it on the owner's keys.
`DEMO_MAX_SESSIONS` (2) bounds how many can at once; a Cloudflare Access
policy on the hostname is what keeps strangers out altogether.

The tunnel's rules are in `/etc/cloudflared/config.yml` on the box (locally
managed; root's to edit) and DNS records are made in the Cloudflare dashboard,
because the box has no `cert.pem`:

| Hostname | Goes to | What it is |
| --- | --- | --- |
| `liuchuanming.ai`, `www` | `localhost:3000` | Aria, the call originator's page |
| `golive.` | `localhost:9080` | the browser demo above |
| `fullduplex.` | `localhost:3100` | the owner's other service |
| `robotgo.` | `192.168.71.9:8790` | the call journal (added by the owner on 10-12) |

The journal has no login of its own and shows every call's conversation and
recording. Published like this, Cloudflare Access on `robotgo.` is the only
thing between it and anyone who has the address.

## The archive: every call, kept for good

Everything else that holds a call has a clock on it: golive's store deletes a
recording after a week, the log is one file, the journal's copy of a call is
its parser's reading of that log on the day. `~/golive-archive` on the box has
no clock. Nothing deletes from it. (Owner, 10-12: "a persistent storage system
to save all recording data, including text and audio".)

```
~/golive-archive/                       mode 700
  calls/2026/10/2026-10-12_124921_<call id>/
      audio.wav        the recording: stereo, caller left, assistant right
      transcript.txt   the conversation, to read: who, when, the wait before each answer
      call.json        everything measured: turns, stages, events, the conversation
      session.json     golive's own record of the session (the history the model saw)
      golive.log       this call's lines of golive's log: what the rest was derived from
      manifest.json    what is here, how big, each file's SHA-256
  prompts/<hash>.txt   the instructions a call ran on, once per distinct text
  index.jsonl          one line per call, for finding things
```

```bash
scripts/phone-brain.sh archive              # what it holds, in numbers; free disk
scripts/phone-brain.sh archive verify       # read every file again and compare with its manifest
scripts/phone-brain.sh archive mirror DEST  # a second copy: a directory on another disk, or host:/path (rsync)
```

- **The journal feeds it**, on a clock of its own (every 20 s, and only when
  golive's log has been still for 8 s, so never during a call). A call is
  archived when it has ended; its recording when golive has closed it. So the
  journal must be running: `phone-brain.sh golive` and `restart` now start it
  with golive, and `archive` says so if it is not.
- **Recording must be on** (`phone-brain.sh record on`, as it is) for there to
  be audio. A call made with it off is archived as text.
- **Audio, session.json and golive.log are written once** and never
  rewritten. The text files are replaced when a newer journal reads the call
  differently; golive.log is what lets it.
- **Size**: a call is about 3.8 MB a minute (two 16 kHz tracks, uncompressed,
  so that any player and any tool can read it). An hour of calls a day is 85 GB
  a year; the box has 3.2 TB free.
- **It is one disk in one laptop.** `archive mirror` is the second copy;
  where to is the owner's to say, and nothing runs it on a schedule yet.
- **Both sides of a recording are on one clock.** The phone side has been seen
  to deliver the caller's audio faster than time passes: a 20 ms frame every
  19.4 to 19.6 ms on nine evening calls, every 19.97 to 20.02 on four daytime
  ones. Appended as it came, the caller's side of a five-minute call ended six
  seconds behind the assistant's. The recorder now leaves out a chunk that is
  the previous chunk over again — the frame a quiet line repeats — while the
  caller's track is ahead of the clock; nothing that differs from what came
  before it is dropped (`input_on_clock` and `input_dropped_ms` in the
  session's record). For a recording made before that, the journal does the
  same as it plays it, from the log's `client audio` marks, says how much it
  left out, and offers the file exactly as recorded (`audio.wav?raw=1`). The
  cause upstream — gateway, PBX or phone bridge — is not known.
- The journal plays a call from the archive, so old calls stay playable, and
  each call's page has a "Download audio" button on the player — both sides as
  one stereo WAV (caller left, assistant right), or the caller or the assistant
  alone as mono, the samples as golive stored them — and "Download transcript".
  A recorded call's row in the list has the same download beside its play
  button. (`/api/calls/<id>/audio.wav?download=1`, with `&side=caller` or
  `&side=assistant` for one side.)

The archive holds what was said on calls and the prompt they ran on. It stays
on the box, mode 700, and nothing in this repository or any report quotes it.

## Measuring the phone leg

Nothing outside golive reports a timestamp, so the gateway, the PBX and the
phone network have never been timed. The line does return the assistant's own
voice, and golive times that. At the end of a call:

```
listen: echo return time n=14 p50_ms=420 min_ms=380 max_ms=640
```

(The figures here are made up to show the shape of the line.) Each one is the
time from golive starting to send an answer's audio to the first sound arriving
back on the input. One is taken only when the line had been quiet for 200 ms
and the caller was not mid-utterance when the answer began, and only if the
sound came back within 1.5 s. Each is also logged at debug as `listen: the
assistant's voice came back`, with `ms` and `level_db`.

What it spans depends on whose echo it is:

- The far handset's: the assistant's voice went out through the gateway and
  the network to the phone and came back the same way. That is the phone leg,
  both directions.
- The gateway's own: the voice turned round inside the box and never reached
  the network. That is the local part only.

The size says which. Under about 150 ms suggests the gateway; several hundred
suggests the whole round trip.

It reads 20–60 ms long, because speech begins softly and the first frame loud
enough to count is not the first frame of the sound. A caller who starts
speaking in the same second is timed as an echo, so read `p50_ms`, not
`max_ms`. No line is logged when nothing was measured, which is what a line
with echo cancellation gives.

An attempt that comes to nothing says why, at debug: `listen: the assistant's
voice coming back was not timed`, with `reason` (the line had not been quiet,
the caller was mid-utterance, nothing came back, a sound in the very first
frame), `quiet_before_ms`, `level_db` and `noise_floor_db`. The summary line
counts them as `not_timed`, and is printed with `n=0` when every attempt
failed.

**On the test line it does not give a usable figure.** One three-minute call:
four measurements — 60, 100, 300 and 600 ms, at levels from −49 to −22 dB,
which are a noise, two candidates and the caller's own voice — and 49 attempts
not timed: 34 because the line had not been quiet (its idle noise sits a few dB
over the bar), 11 with the caller mid-utterance, 2 in which nothing came back
in 1.5 s on a line that was digitally silent. The line returns the
assistant's voice some of the time and not others. Do not quote its `p50_ms`
as the phone leg. A recording of the call made beside the handset, lined up
against golive's log, is the way to measure what a caller actually waits.

### From a recording of the call

golive can keep a call's audio and measure the line from it, which survives
noise that the first-sound estimate above does not:

```bash
scripts/phone-brain.sh record on    # then: scripts/phone-brain.sh restart
# place a call
scripts/phone-brain.sh echo         # numbers only
scripts/phone-brain.sh record off   # and restart, when done
```

`record on` makes the Realtime endpoint keep every call as a stored session
(`realtime.store_calls`): the caller as received and the assistant as sent,
two PCM tracks on one timeline, in `~/golive-calls` (mode 700), swept after
`store_ttl_hours` (a day). It is the owner's voice and whoever is near them;
it stays on the box, and `echo` prints only numbers.

`echo` runs `echoprobe` on the last call: it correlates the pattern of onsets
in the assistant's track against the caller's track at every delay up to 1.5 s
and reports the delays at which the line returns the assistant's voice, each
with how sure it is, or says that it found none. On synthetic calls it found
an echo 25 dB down under noise 5 dB louder than the echo in 90 s of call, and
reported no echo where there was none in 2,700 trials; under noise 10 dB over
the echo it needs about five minutes. It has not yet been run on a real call:
a real echo has been through a codec twice and perhaps an echo canceller.

A second line at the end of a call is for choosing `duplex.early_final_ms`:

```
listen: pauses inside utterances n=31 ms_60_79=12 ms_80_99=8 ms_100_119=5 ms_120_plus=6
```

These are pauses of 60 ms or more that the caller carried on from, counted by
length; the pause that ended the turn is not among them. Lowering
`early_final_ms` begins every answer sooner by the difference, and every pause
at least that long costs a recognition split in two. At 120, that is the pauses
under `ms_120_plus`. At 80, it is those and the two columns before it. Each
pause is logged at debug as `listen: the caller paused inside the utterance`,
with `pause_ms`.

## The synthesizer's first audio

About 180 ms at the median and over 300 one turn in ten, and on an answer's
first segment all of it is silence. `tts_hedge: 2` gives each session a second
connection and asks both for that first segment, playing whichever produces
audio first. On the bench it took p95 from ~1070 to ~1010 ms.

**Asking both every time is more than the synthesizer account on the test box
can afford.** Paced like one phone call (`phone-brain.sh tts -call 36 -every 5s
-hedge 2`: 36 requests a minute), the account began refusing requests after 65
seconds ("status 1002: rate limit exceeded(RPM)") and went on refusing. That
fits an allowance of about 20 synthesis requests a minute with a burst of about
20 — and a refused request is an answer the caller never hears. A nine-minute,
70-turn call without hedging ran at 16–19 a minute: under it, not by much.

**So the profile asks the second connection only when the first is slow**:
`tts_hedge_after_ms: 260`. A synthesis that produces audio inside 260 ms — all
but one in twenty — is one request, as before. One that has not is put to the
second connection as well, and the caller hears whichever speaks. Later
segments are covered too, with three times the wait, since they play behind
audio already going out; a first connection that fails outright is followed by
the second at once. `tts_hedge_rpm: 4` bounds what this adds: at most four
second requests (and second connections) a minute. It was first a bound on the
account's total, at 18, and a call that made 22 requests a minute by itself
never had room: all four of its slow syntheses went unhedged. The log says when it happens
(`tts hedge: asking the second connection too`, then `a copy produced audio
first`).

**What it does not cure is the synthesizer stalling as a whole.** On one call
its first audio took 1284 and 1763 ms on consecutive turns, with a second
segment that had not arrived after 3.3 s, while the chat model and the
recognizer answered as usual. Paced like a call with the second connection
asked after 220 ms (`phone-brain.sh tts -call 20 -every 7s -hedge 2 -after
220ms`), the one stall in twenty turns — 1847 ms — was a stall on both
connections: the second, asked 1.6 s before the first finally spoke, had
nothing either. And at 220 ms the second connection was asked on nine turns of
the twenty and won none: the first answered within ten milliseconds each time.
So the wait is 260, the second connection is insurance against one connection
going bad, and the stalls are not a matter of load either — over two calls
the slow syntheses came at 13–17 requests in the minute before them, and
thirty-eight made at 20–31 a minute were as quick as any.

**A second synthesis account is a pool of two.** On since 10-12 22:35. Measured
first (`tts -pool 30 -every 8s`, both accounts asked at the same moment): first
audio p50 265, p90 336, worst 689 ms on the first account alone; 248, 328, 337
for the faster of the two. A first audio of 400 ms or more on 4 of 30 rounds,
never on both at once. The second account's copy of the voice speaks about 9%
faster and 3 dB louder; the owner heard both and keeps them as they are (both
are his voice, and the variation is wanted). Its settings are in the env file,
beside the first account's, as a numbered pair:

```bash
MINIMAX_TTS_API_KEY=…         # the first account, as it has always been
MINIMAX_TTS_VOICE_ID=…
MINIMAX_TTS_API_KEY_2=…       # the second account
MINIMAX_TTS_VOICE_ID_2=…      # what the second account calls the same voice
```

```bash
scripts/phone-brain.sh keys                                  # counts only
scripts/phone-brain.sh tts -pool 30 -every 8s -save ~/golive-voices   # measure, and save a sample from each
scripts/phone-brain.sh restart
```

The voice is why it is a pair. A cloned or designed voice belongs to the
account that made it, and its id means nothing to another account; the owner
makes the voice again on the second account, and that copy has an id of its
own. golive asks each account for the configured voice by that account's name
for it. (A key with no numbered voice is asked for the first account's id —
right for one of the vendor's own voices. `addkey minimax` adds such a key to
a file, unseen. `_3` to `_9` work the same way.) **Two clones are two voices,
however alike**: the pool keeps an answer on one account where it can, but
which account speaks changes from turn to turn. Clone from the same recording,
and listen to the two files `-save` writes before a caller hears both.

With two accounts, the session's second connection is opened on the other one,
and the rules for two connections that are apart are different from the frugal
ones above, because the second request no longer draws on the allowance the
first needs:

- **Every request to an account is counted** over the last minute, against
  that account's own allowance, written beside its key: `MINIMAX_TTS_RPM=20`
  and `MINIMAX_TTS_RPM_2=8` on the box. The vendor's rate-limit page (T2A v2,
  as the owner quoted it on 10-12) gives 10 requests a minute to a free
  account and 20 to one that has been topped up, and the two accounts behave
  like one of each. The first has carried 22 to 31 requests in a minute
  unrefused (36 a minute sustained was refused): 20 with some slack. The
  second, paced alone at 30 a minute, refused 42 of 76 requests from the 28th
  second on: 10, whatever its balance says. An account with nothing written
  gets the profile's `tts_account_rpm: 10`. If the second account moves up a
  tier, pace it again and raise its figure.
- **The first segment of an answer is asked of both accounts at once**
  (`tts_pool_after_ms: 0`) whenever the other account has room: the faster of
  two draws on every turn, not insurance on one in twenty.
- **A session whose account has had its minute moves to the other** while that
  one has room — and, when both have had theirs, to the roomier of the two,
  so a busy call ends up on the account that will actually take it. A request
  the session has to make is made regardless; only the optional second one
  waits for room.
- **A request an account refuses is asked of the other at once**, room or no
  room, and the refusing account is treated as full for the minute.

Three things `tts -pool` is there to find out, in this order:

1. **Whether the second account can speak in the voice, and whether it sounds
   the same.** An account asked for a voice it does not hold is refused at
   `task_start` ("you don't have access to this voice_id"). golive then rests
   that key for ten minutes, says so once (`tts: an account was refused;
   resting it`), and carries on with one account exactly as before. The probe
   says which id each account was asked for (never the id itself).
2. **How fast the faster of the two is**, against the first alone in the same
   rounds: that difference is what a turn gains.
3. **Whether they are slow together.** "rounds … on every account at once" is
   the count that matters for the stalls: if both accounts stall in the same
   round the stall is the vendor's or the path's, and a second account does
   not cure it.

To find a new account's allowance, pace it alone until it refuses
(`tts -account N -call 38 -every 4s -hedge 1` is 30 requests a minute) and
write a figure under what it took beside its key. Do not take it from the
vendor's page, and do not assume it from another account.

**What does help is not asking.** `duplex.phrase_cache_learn_chars: 12` keeps
the audio of any segment of an answer up to twelve characters long and plays
it from memory the next time those words come up in that voice: "好的，",
"是的，", "您说，我听着呢。", "再见。". On two calls one answer in eight opened
with words already said on them. Such a turn's first audio does not wait on
the synthesizer at all (`speak: segment played from the phrase cache`). The
cache is the process's, so it carries from call to call until a restart.

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

## What the caller can say without stopping the assistant

`duplex.user_backchannel_phrases` in `configs/telephony.json` is the owner's
list (10-12, 33 entries): murmurs ("嗯", "哦", "啊", "哈哈"), agreement ("对",
"是的", "没错", "确实", "好", "行", "没问题") and go-ons ("然后呢", "你接着说",
"这样啊", "原来如此", "真的吗"). To change it, edit the list in the repository
and deploy; the copy on the box is replaced by every bundle.

What it does, and only this: when the caller makes a sound over the assistant,
the assistant keeps talking for up to `user_backchannel_hold_ms` (600) while
the transcript says what the sound was. Nothing but listed phrases, repeated or
combined ("嗯嗯嗯", "嗯，对") — it carries on, and the utterance is neither
answered nor put in the history. Anything else stops it, at the first character
no listed phrase goes on with: "嗯，等一下" at 等, "对了" at 了, "好的" at 的
(好 is listed, 好的 is not). No words at all — a cough, the line — and it
carries on.

What each entry costs: the caller cannot stop the assistant with that phrase
alone, and a reply said over the end of the assistant's question ("对" before
the question has finished playing) is taken for a murmur and not answered. On
a phone the leg's delay makes that rare, since the caller hears the question
late; it is the thing to listen for on calls with this list.

What it no longer costs: a listed word said into a silence is the caller
answering, and its answer is begun on the early final like any other short
turn's (`acknowledging` in `internal/duplex/listen.go`). Before 10-12 a word on
the list lost that head start wherever it was said.

The assistant's own murmurs while the caller talks (`duplex.backchannel`,
`backchannel_phrases`) are off: on a phone line they land on top of the caller.

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
