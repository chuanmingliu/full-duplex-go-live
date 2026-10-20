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

## The simulated customer

A second service that answers a call as a synthetic customer, so the
assistant can be exercised without a person holding a phone. It is Codex's
work on the branch `codex/inbound-user-simulator` (`README.simulator.md`
there is the project's own README; `deploy/simulator/README.md` the runbook;
`docs/simulator-carrier-acceptance.md` what is deliberately not done yet).
Nothing from it is merged into this branch: it is two commands of its own,
`golive-simulator` (the receiver: an HTTP control API, a workbench page at
`/sim/`, and the customer's own ASR → LLM → TTS engine behind a media
socket) and `golive-simctl` (a suite runner and an independent judge), and
it never touches the phone line's golive.

How a test goes: a *run* is reserved for a scenario (persona, synthetic
facts, goal, scheduled actions such as an interruption or a hangup, what the
assistant is expected to do); the reservation returns a run id and the SIP
target `sip:<run id>@simulator.invalid`; the assistant side has 120 s to
send its INVITE to the simulator's **own** gateway (a pinned Active Call in
`deploy/simulator/compose.yml`, SIP on UDP 25060 inside a Docker network of
its own); the gateway answers, the customer speaks and listens through the
media socket, and when the call ends a report is written: answer time,
greeting, each side's speech boundaries, interruption, who hung up, and the
failures seen. Nothing dials from the simulator's side, and a reservation
never attaches to "the current call" — the run id in the called extension is
what binds them, so a late INVITE cannot consume the next reservation.
Transcripts are kept only when a scenario asks; audio is never kept.

On the box:

```bash
scripts/sim-bundle.sh                  # here: builds the two commands from the branch into dist/golive-sim.tar.gz
tar -C ~ -xzf golive-sim.tar.gz        # on the box: ~/golive-sim/{bin,scenarios,configs,deploy,docs,scripts}
S=~/golive-sim/scripts/sim-brain.sh
$S init                                # three private tokens (tokens.env, mode 600), the data dir,
                                       # a providers profile and an empty providers.env to fill in
$S start mock                          # the receiver on 127.0.0.1:18865 with synthetic providers
$S status                              # livez 204; readyz 503 until its gateway is up
$S scenarios                           # what is shipped: cooperative, confused, busy, appointment-correction, cancel-before-answer
$S prepare cooperative                 # reserve a call; prints the run id and the SIP target
scripts/phone-brain.sh call sim        # dial it from the phone line's golive with the kept prompt
$S run | events | report | stop-run    # the run as it goes; the report once it has ended
$S suite synthetic.json                # one scenario after another, each waiting to be called
```

**Two words, two kinds of call.** `phone-brain.sh call` places the real
call — the one number the originator allows, a person on a phone — and
`phone-brain.sh call sim` places the simulated one. Nothing else tells them
apart: to golive both are a session from Active Call. So `call sim` writes
one line per dial to `~/golive-sim/data/dials.jsonl` (`{"at","call","run"}`:
the originator's call id and the simulator's run id), and that file is what
the journal reads (`-sim-dials`) to mark a call **sim** in its list and head
it "Simulated call of …", and what the desk reads to pair the two.

**Following a simulated call** (the desk, `#/calls`): every dial, newest
first, with the simulator's side (phase, scenario, how it ended, its checks
and failures, its measure of the assistant's response gaps) beside golive's
(turns, p50, worst or p95, turns over 1200) and, embedded, the journal's
view of the same call — the recording with the customer on the left and the
assistant on the right, the turns, the gate — so what the customer said and
what the assistant answered can be played back together. The report in full
is a click away; the desk fetches it from the simulator with the control
token server side, and the page never sees that token. Recording must be
on for the audio (`phone-brain.sh record on`, then a restart of golive).

**Writing a scenario** (the desk, `#/scenarios`): the shipped scenarios
(read-only) and the owner's own (`~/golive-sim/scenarios/custom/`), each
as a form — persona, goal, behaviour line by line in the order of the
conversation, invented facts, the customer's first words, how many
assistant turns and how long (the simulator caps a call at 300 s and 100
turns), what to expect of the assistant, scripted actions as JSON — with
*Validate* (the simulator's own check), *Save* (refused if the simulator
would refuse it) and *Prepare a run* (reserves the call and writes
`data/last-run`, so `phone-brain.sh call sim` on the box dials it).

**A customer in the image of a real one**: *Draft from a real call* takes
a real call from the journal (never a simulated one), reads the caller's
side on the box, and asks the chat model — with the simulator's own key —
for a synthetic customer who talks like that over a long conversation:
persona, goal, eight to twelve behaviour lines, facts with every
particular invented, a greeting, and expectations of the assistant drawn
from what it did well or badly on that call. The transcript goes to the
model that heard the call in the first place and nowhere else; the draft
comes back synthetic, marked with the call it came from, and is only a
draft until it is saved.

**A set from the journal, with the things callers do** (*Synthesize a
set* on the same page): several real calls at once, each drafted and saved
plain and with each *effect* ticked, plus a suite manifest
(`~/golive-sim/scenarios/suites/<prefix>.json`) for `golive-simctl suite`.
The effects are the simulator's scheduled actions built from lines the
draft asked the model for in the caller's own voice:

| effect | what the customer does | what it tests |
| --- | --- | --- |
| `barge_in` | cuts in over the 2nd and 5th answers, 700 and 500 ms in | the answer must stop quickly, and the cut-in be answered |
| `backchannel` | a listening sound ("嗯", "对") over the 3rd and 6th answers | the answer must **not** stop |
| `rapid` | cuts in over the 2nd, 3rd and 4th answers, 400 ms in | turn-taking under pressure; abandoned guesses |
| `busy` | says it has a minute, short replies, hangs up at 85 s | short turns; a clean end |
| `abrupt` | hangs up at 60 s without a word | teardown and the report |

The same persona plain and with each effect makes the runs comparable: a
figure that moves between them moved because of the effect. Background
noise is not an effect the simulator can script (its actions are speech
and hangup only; it refuses silence and keeps no audio) — the subway and
the show stay on the real line for now.

**After every simulated call, a retrospective**, as for a real one:
`sim-brain.sh retro <call id> [build]` runs `scripts/sim-retro.py`, which
reads golive's log and the customer's report and writes
`journal/retros/<call id>.md` — the stages of every turn, the chat model's
and synthesizer's distributions, guesses held and abandoned, answers cut
by the customer, silence filled, the receiver's checks and failures — with
an *Open* section to fill in by hand: what the slow turns were laid to and
what to try. Numbers only; the journal shows it on the call.

`start providers` runs the customer on real speech: Tencent, DeepSeek and
MiniMax with **its own** accounts, written by the owner into
`~/golive-sim/providers.env` (the phone line's keys are the phone line's;
their quotas are already spoken for). With the mock profile the customer
"speaks" tones — enough to prove the call reaches it and the report comes
back, not for a conversation.

What is in place as of 2026-10-16 and what is not:

- The receiver runs on the box (source `6269dd2`, mock profile, loopback).
  A run was reserved and stopped through it. Its tests, `go vet`, the
  package's Python checks and `check_workbench.py` pass in the workspace.
- **Its gateway is not up.** The Docker package needs `sudo docker compose`
  on the box and builds a pinned Active Call image; the runner has no sudo.
  When it is started, stop the host binary first (`sim-brain.sh stop`; the
  package's controller container takes 127.0.0.1:18865 and the same tokens:
  `set -a; . ~/golive-sim/tokens.env; set +a` before `docker compose`).
- **No route carries the INVITE.** The phone line's Active Call dials
  through RustPBX to the GSM gateway. `simulator.invalid` is a logical
  name; RustPBX (or Active Call) needs a route for that domain to the
  simulator gateway's address on its Docker network, port 25060, with RTP
  24000–24019 reachable from the host. Both run as root in containers whose
  configuration this account cannot read; the owner sets the route.
- **The originator is locked to one callee.** `phone-brain.sh call sim`
  posts the dial with `to: sip:<run id>@simulator.invalid`; the originator
  will refuse it until that callee is allowed, and the refusal is what the
  command prints. The alternative — dialling the gateway's own API,
  bypassing the originator — would lose the kept prompt and the opening
  posted at pickup.
- The scenarios are zh-CN appointment tasks with fictional facts. One for
  the owner's own business (synthetic facts only) is a short JSON file;
  `sim-brain.sh validate` checks it without reserving a call.
- The independent judge (`golive-simctl evaluate`) needs a chat-model
  endpoint of its own in `SIM_EVAL_BASE_URL/MODEL/API_KEY`, set in the
  invoking shell; a DeepSeek key not used by the phone line would do.

The receiver's engine is the branch's: the duplex engine as of its
merge-base (`6626293`), without the focus channel, the floor tracking or
anything since. That is the customer's side and does not matter for what is
being tested; it means the two services do not share a binary.

## The desk: where Claude and Codex work in the owner's sight

Two coding agents now work on this project — Claude on `telephony-realtime`,
Codex on `codex/inbound-user-simulator` — and the simulator test needs them
to hand things to each other: a run reserved, a gateway started, a report
read. The desk is where that happens, in writing, with the owner watching.
`cmd/desk`, `internal/desk`; on the box `phone-brain.sh desk start`, port
8792 on this machine and the home network, data in `~/golive-desk`.

What it is:

- **Notes** in threads: `message`, `task`, `question`, `result`, `status`,
  from one participant to another or to all, with refs (a commit, a run id,
  a call id, a link). A task or a question stays **open** until the
  addressee, the author or the owner closes it.
- **One token per participant** — `owner.token`, `claude.token`,
  `codex.token` in `~/golive-desk`, made on the first start, mode 600 —
  so a note's author is who the token says, not who the note claims. The
  owner copies `codex.token` to wherever Codex runs; Claude reads
  `claude.token` on the box.
- **An append-only checksummed log** (`notes.jsonl`, the lead service's
  chain): a line changed by hand breaks the chain from there on and
  `desk verify` says where.
- **The owner's switches.** *Pause*: the agents may read but not write
  (HTTP 423) until *resume*; the owner always may. *Mode* `manual | auto`:
  written down for the scheduler that does not exist yet, read by nothing
  but the page.
- **The page** at `/`: who was last heard from and when, what is open for
  whom, the switches, threads on the left, the thread on the right, a
  compose box, live over the event stream (`/api/events`, server-sent).
- **Scenarios** at `#/scenarios`: the simulated customer's scenarios
  written, checked and reserved from here, or drafted from a real call
  ("Writing a scenario" under "The simulated customer"). `GET/PUT
  /api/scenarios/{name}`, `POST /api/scenarios/validate`, `POST
  /api/scenarios/{name}/prepare`, `POST /api/scenarios/draft {call, turns}`,
  `GET /api/scenarios/calls`.
- **Simulated calls** at `#/calls`: the calls placed to the simulated
  customer, each paired with its run's report and the journal's call, the
  recording playable in place ("Following a simulated call" under "The
  simulated customer"). `GET /api/calls`, `GET /api/calls/{run}/report`.

```bash
scripts/phone-brain.sh desk start                  # on the box; tokens are made
scripts/desk.sh me                                 # as claude by default (DESK_AS, DESK_TOKEN_FILE, DESK_URL)
scripts/desk.sh post codex task "Start the gateway" body.md b8aa681
scripts/desk.sh inbox                              # open tasks and questions for me
scripts/desk.sh reply n1 result - < report.txt     # into n1's thread, to its author
scripts/desk.sh done n1
scripts/desk.sh watch                              # the stream, as it comes
DESK_AS=owner scripts/desk.sh pause                # or the button on the page
```

The API, every request with `Authorization: Bearer <token>`: `GET /api/me`,
`GET /api/state`, `GET /api/notes?after=&thread=&to=me|all|…&from=&kind=
&open=1&limit=`, `POST /api/notes {to, kind, subject, body, refs,
reply_to}`, `POST /api/notes/{id}/done`, `PUT /api/control {paused, mode}`
(owner), `GET /api/events`. Bodies are text: numbers, shapes and ids; no
transcripts, keys or phone numbers, the same rule as everywhere else here.

What it is for later: when the two agents are to take turns on a schedule
— Codex prepares a run, Claude dials and reads the log, each writes what
it found and asks the next thing — the desk is the record they take turns
in and the pause is the owner's hand on it. A scheduler would read `mode`
and the open tasks per agent; nothing does yet, and no agent is started by
the desk.

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
  left out, or the service's default (`crm greeting 'LINE'`, `PUT
  /api/greeting`) when both are; the default is also filled in as `start`
  when the template reads it and the request does not give it. `nowDateTime` is filled in with the time of the render when the
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

### The panel

The service serves a page at its root — on the box `http://192.168.71.9:8791/`
on the home network — over the same API: templates and their versions (read,
save a new version), the configs (`quote-rules`, `quote-api`, any other, as
JSON, in versions), render a template with a lead's variables and read the
prompt, a dry run, placing the call, the default opening line, the quotation
tool as the assistant would be answered, and every call placed with the
prompt it ran on and a link to its journal page. The page asks once for the
token (`cat ~/golive-crm/api.token`) and keeps it in that browser; the API is
not opened to anything without it. Like the journal, the page has no login of
its own, so it is for the home network, not the tunnel.

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
| Audio | `~/golive-calls/<session>/` (golive's store) until the call is archived | for good, in the archive; golive's working copy goes after `CALLS_TTL_HOURS`, a month since 10-15 |
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

## A second synthesizer: Fish Audio

`internal/provider/fish` speaks to Fish Audio's HTTP endpoint (`FISH_TTS_API_KEY`,
`FISH_TTS_VOICE_ID`, `FISH_TTS_MODEL`: `s2.1-pro` or `drama-3`; `tts: "fish"`
in a profile uses it alone). One request a segment, raw PCM streamed back, on a
connection the client keeps open; nothing to resynchronize after an
interruption. Measure it against MiniMax before deciding anything:

```bash
scripts/phone-brain.sh addkey fish                                   # the key, pasted unseen
scripts/phone-brain.sh tts -fish s2.1-pro,drama-3 -runs 12           # both models beside MiniMax, same rounds, same texts
scripts/phone-brain.sh tts -fish s2.1-pro,drama-3 -fish-only -call 20 -every 6s   # each paced like a call: first audio, refusals
```

The question is first audio on the short opening clause (what a caller waits
through), whether it refuses at a call's pace, and how the voice sounds; only
then whether it belongs in the pool beside the MiniMax accounts.

Measured on 10-14 at 22:32 from the box (the VPN route; a warm, empty round
trip to `api.fish.audio` is 190 ms, which is the floor): first audio at the
median / p95, MiniMax 216 / 309; Fish drama-3 with `latency: balanced` 423 /
709, s2.1-pro 562 / 940; with the default `normal` 612 / 844 and 1040 / 1578.
Paced like a call (drama-3, 20 turns at 20 a minute): p50 424, p90 637, max
748, none of 40 refused. So Fish is ~200 ms behind at the median and more at
p95, and is not raced on every turn; what it is good for is the thing a
second MiniMax connection is not — the vendor stalling for seconds, once in
each of three runs. **`tts_fallback: "fish"`, `tts_fallback_after_ms: 320`**
(`provider.FallbackTTS`): the primary is asked alone; only when it has
produced nothing for 320 ms (p95 is 309) is Fish asked for the same segment,
and whichever speaks first is heard, the other cancelled. A later segment of
an answer gets three times the wait. A stall then costs about 320 + 424 ms
rather than 3–8 s. On the box the env has `FISH_TTS_MODEL=drama-3` and
`FISH_TTS_LATENCY=balanced`; the log says `tts fallback: asking the second
vendor too` and `the second vendor spoke first` when it happens, and at the
end of a call how many times it was asked and how many times it spoke.

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

**The noise sits above the bar.** A subway car (the 19:41 call on 10-15, the
owner's target scenario) is louder than the bar the whole time, and the floor
cannot learn it: it adapts only on frames it believes are not speech, and it
believed every one of these was. The recognizer does no better — it transcribes
the noise, a new hypothesis every half second, so the settled close above never
sees unchanged words either. That call held one utterance open for the full
`vad.max_speech_ms` (30 s) and the assistant said nothing for 35 s. Speech has
gaps between words that fall to the line's own level; noise does not. So the
gate now tracks the quietest frame of the last `vad.floor_track_ms` (2000) of an
open utterance, and when none of it dipped under the bar, raises the floor to
that quietest frame (bounded by `max_noise_floor_db`, −28):

```
listen: the line never dipped under the bar; the floor is raised to its quietest  from_db=-60 to_db=-37.4 over_ms=2000
listen: a line that never dipped under the bar  floor_raises=3 noise_floor_db=-35.1
```

The frames at the noise's level stop reading as voiced, and the utterance
closes on the next 380 ms that the *caller* is quiet. The floor comes back down
by itself afterwards (the release is fast on quiet frames). The cost: a caller
who talks for two seconds without one gap near the room's level — a loud,
continuous voice in a quiet room — has the floor raised to the quietest part of
their own voice, and a soft trailing word can then close the turn early. The
tests in `internal/audio/vad_test.go` hold both sides: a steady rumble closes
within 3.5 s, a four-second sentence is heard to its end. `-1` turns it off.

**The caller's own quiet** (`duplex.focus_quiet_close_ms`, 420, veto mode
only). The floor only helps when the noise is steady and below the voice;
a room with a second voice, a radio, dishes, keeps the gate open at the
caller's own level and the recognizer keeps finding words in it, so the
settled close never sees the words stand still either. The 09:37 call on
10-19: one utterance open for nine seconds with half its voiced time
another source's, three guesses made and two thrown away as the "words"
changed, the turn taken by the 900 ms patience and the caller answered
1525 ms after their last sound for an answer ready in 625. The focus
channel labels every window as the caller's or not, so the turn is now
taken when the caller's voice has been absent for 420 ms by the labels
while the line stayed open — once the utterance has been attributed to
the caller (160 ms of their voice), and only on the range the labels have
reached, so their lag (tens of ms with the target-speaker service) is paid
once and no guess is made on unlabelled audio:

```
listen: the caller's voice has been quiet though the line has not; taking the turn  caller_quiet_ms=420 others_ms=410 labelled_to_ms=64020
```

Counted as `quiet_closes` in `focus: this call`. The pause this gives is
the labels' own 420, a little more than the detector's 380, and it is
judged on the caller, not the room. Test: `TestTheTurnIsTakenWhenTheCallerGoesQuietThoughTheLineDoesNot`.

**Seeing what the gate saw.** Every 100 ms the gate keeps a sample — the
loudest and quietest frame, the floor, the bar, how much was judged voiced,
whether it was open, whether the assistant was speaking, whether the floor was
raised (`vad.gate_trace_ms`) — in the stored session's record (`gate` in
`session.json`, so the archive has it). The call journal draws it as the third
lane of the player, "Gate": the level as a band, the floor as a line, the bar
dashed, the openings shaded (red when opened over the assistant), a bar at the
end of an opening that ran into the 30 s cap or was taken by the settled close,
and an arrow where the floor was raised. Hover for the figures. On the subway
call it shows the whole story in one glance: the band sitting above the dashed
line for thirty seconds. `/api/calls/<id>/gate` is the data.

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

## The edge test group

Speech models run on the box itself, served through an OpenAI-compatible
API by `cmd/edge` on port 8030: `scripts/phone-brain.sh edge start|status|
models|probe|say`. What is served, how to call it, what was measured and how
the runtimes were set up are all in `EDGE.md`. The Audio8-ASR model is
licensed for non-commercial use only.

## Keeping to one voice

A callee is rarely alone — a television, a colleague, a passenger — and the
recognizer transcribes all of it. `duplex.focus` runs a speaker-activity
model (Nemotron-3-Diarization, on the box, through the edge gateway) beside
the caller's audio and uses what it says in the one way its lateness allows:
the labels arrive ~700 ms after the sound, too late to decide whether a turn
is over (the final transcript is in hand ~210 ms after the last sound) but
early enough to abandon an answer, which takes ~650 ms more to become audio.
The audio the recognizer hears is never changed.

- The primary speaker is whoever first says 600 ms of speech after the
  greeting — the person who answered. The assistant's own voice coming back
  down the line is masked out by the moments the assistant was speaking (plus
  a 600 ms tail). Another voice becomes the primary only after carrying the
  conversation alone for 4 s with the primary silent: the phone handed over.
- Every turn begun on the caller's words is judged once most of its audio is
  labelled: `focus: utterance attributed` in the log, `golive.focus` as an
  event (primary share, others' share, voiced ms, coverage, whether it was
  another voice, whether its answer was dropped), and a line per call
  (`focus: this call`: judged, vetoed, too late, primary speaker, hand-overs).
- `"focus": "observe"` judges and logs only; `"veto"` also abandons the
  answer to an utterance in which another voice was active for at least
  `focus_veto_share` (0.7) of the voiced frames with the primary silent, and
  at least `focus_min_voiced_ms` (240) was voiced — before the first audio
  when the labels come in time, with `focus: … answered before the labels
  came` in the log when they do not. The telephony profile is on `observe`
  until a call has shown the labels arriving in time.
- The journal shows it: a badge on each answered turn (caller's voice /
  other voice / dropped / too late, with the shares and the labels' lag), a
  note on a caller's line whose answer was dropped, a marker on the
  recording's timeline, a "Speaker focus" count group and a dot plot of how
  far behind the audio the labels were at each judgment.
- `focus_url` is the gateway's WebSocket, the token comes from
  `GOLIVE_FOCUS_TOKEN_FILE` (`phone-brain.sh` sets it to the gateway's), and
  a gateway or model that is down is a call without focus, logged once.

### A sound over the answer

The floor hold (`user_backchannel_hold_ms`) used to have one witness: the
recognizer. Words that were not an acknowledgement cut the answer. On a quiet
line that is the caller interrupting; with a show playing in the background
(the 21:59 call on 10-15) the recognizer made words of the show, and of the
assistant's own voice returning at −28 dB, and cut the answer thirty times in
four minutes. The labels are the second witness. The tracker tells the
assistant's return apart — the voice it hears mostly while the assistant
speaks — and reads the sound over the answer with that voice taken out:

- the caller's voice (160 ms of it) yields, as always;
- another voice, or nothing but the echo, keeps the floor: the rest of that
  utterance goes by without a turn, unless the caller joins in;
- labels not in yet are waited for, up to `focus_barge_in_wait_ms` (900) from
  the hold's start; then the words decide as before.

```
focus: a sound over the answer was judged  verdict=keep why="another voice" primary_ms=0 others_share=1 voiced_ms=480 coverage=0.84 echo_speaker=2 waited_ms=450
focus: this call  ... barge_ins_judged=9 barge_ins_kept=7 echo_speaker=2
```

In `observe` mode the verdict is logged and the words still decide. The
telephony profile is in `veto` since 10-15. `-1` turns the wait off.

### Only the primary voice: the target-speaker service

The owner's rule (10-15): target my voice; a second voice is ignored. The
diarizer cannot keep that promise on a CPU under continuous background
speech — on the 22:29 call its labels were 1–1.2 s behind within a minute,
and 37 of 38 sounds over the answer were decided on the recognizer's words
because the labels had not come. The question is narrower than "who are all
the speakers": it is "is this window the caller", and a speaker-verification
embedding answers it in 10 ms.

`edge/tsv_server.py` (CAM++ zh/en, ONNX, from the sherpa-onnx model set;
runtime under `~/edge/tsv`, port 8032; `phone-brain.sh edge start` starts it
with the others) speaks the diarizer's wire, so `focus_url` simply points at
it (`ws://127.0.0.1:8032/verify`) and the tracker and every judgment stay as
they are. What is different:

- **Enrolment.** The engine tells the service the first utterance the caller
  made alone (≥ 600 ms voiced, not over the assistant, nothing of the
  assistant's in it), and extends it with the next few the labels call the
  caller's (`focusEnrolments`, 5), up to 8 s of voice. Before enrolment every
  frame is unknown and nothing is judged.
- **Scores.** Every 100 ms, the 800 ms window ending there is scored against
  the enrolment: voiced and over `--threshold` (0.35) → the caller (speaker
  0); voiced and under → another voice; not voiced → nobody. Speaker 0 is
  therefore always the caller, and the tracker enrols it as the primary on
  its own rule. The similarity is in the message (`sim`).
- **The open utterance is judged as it is said** (`judgeUtteranceFocus`):
  from 300 ms of labels in, another voice (≥ 240 ms voiced, ≥ 70% others) or
  nothing voiced for 800 ms of an open gate means the utterance is let go by:
  no guess, no turn, no interruption, no transcript shown — unless the
  caller's voice joins in (160 ms), which takes it up at once. In observe
  mode the verdict is logged and nothing is let go.

```
focus: enrolling the caller's voice  from_ms=7820 to_ms=10280 voiced_ms=1480 enrolment=1
focus: the caller's voice is enrolled  voiced_ms=1480
focus: the open utterance is not the caller's  why="another voice" others_share=1 voiced_ms=640 span_ms=900 mode=veto acting=true
focus: the utterance went by; no turn made of it
```

**A kept profile.** One utterance does not represent a voice on a phone
line (the 09:41 call on 10-16: the owner's own later utterances scored
0.07–0.25 against an enrolment from their first one, and the channel
inverted — ISS-038). With `focus_profile: owner` the service keeps the
caller's embedding across calls (`~/edge/tsv/profiles/owner.npy`; 192
numbers, not audio) and every call starts enrolled from it before the
caller has spoken; a line with no profile yet saves its first enrolment as
one. The profile on the box was made from 28 s of the owner's voice over 14
utterances of two quiet calls (`.runner` job 267), and recognises every
utterance of the 09:41 call at 0.33–0.61. The tracker is pinned to speaker
0 on enrolment, and an enrolment that the next eight seconds of voice
contradict (under 30% caller) puts the channel in observe for the call.

**Whoever picked up is the caller, learned from the call by agreement.**
One window at the start of a call is not a reliable sample of a voice: on
the 09:41 call on 10-16 the caller's own later utterances scored 0.07–0.25
against that call's 1.3 s enrolment and 0.54–0.59 against the previous
call's, and with the tracker free to pick the primary by its own rule it
picked "not the caller" and three of the caller's answers were vetoed. So
each utterance the caller makes alone (≥ 600 ms, the assistant silent) is
*offered* to the service, up to six; the enrolment is the largest group of
offered utterances that agree with each other (`--agree`, 0.4). One alone
enrols tentatively; the next ones confirm, extend or outvote it; once two
or more agree, a further one joins only if it agrees — a voice in the
background cannot creep in. This is the general case: a line that reaches
anybody. `focus_profile` names a kept profile (`~/edge/tsv/profiles/
<name>.npy`, an embedding, not audio) for a line that always reaches one
person or a known contact; the call then starts enrolled, and offered
utterances only extend it. The caller is pinned as the primary on this
channel (`Tracker.Pin`): no re-enrolment, no handover. And when the first
eight seconds of voice after an enrolment are under 30% "the caller", the
enrolment is distrusted and the channel observes only (`focus: the
enrolment does not match what followed; observing only`).

**What a mixture is.** With a show playing *under* the caller's voice every
window is a mixture, and the mixture scores like a stranger (the 23:20
call: 1,130 voiced windows, 83 over 0.35, none over 0.5; four of the
caller's own answers dropped as "another voice"). So no verdict acts on a
mixture: an answer is vetoed, the floor kept, an utterance let go by only
when the span has **no caller voice at all** (`focus_veto_share` 0.95 and a
zero-primary rule), and the enrolment is the first utterance only — a later
one is a mixture too. What that leaves is the show cutting in sometimes;
taking it out of the audio is a different model (target-speaker extraction)
and the next thing to evaluate.

Measured on this line's archive (`.runner` jobs 249–251): against a 2 s
enrolment from one call, the caller's own later utterances score 0.45–0.68
(p10 0.45), the assistant's voice on the caller track 0.11–0.16, the
assistant's own channel 0.14–0.21, the lines of the 21:59 call with the show
on 0.18–0.35 (p90). A 0.9 s enrolment is not enough (the caller's later
utterances 0.13–0.24): hence the extension. Phone audio of the same voice on
different days scores lower than on one call; the threshold is 0.35 and the
journal's similarities are what to set it from.

## Hanging up

On a phone call somebody has to hang up. Left to themselves two models say
goodbye to each other for as long as the line stays up — the simulated
customer showed it on the first long conversations, which ran to their
limits on pleasantries. So the assistant can end the call: `tools_end_call`
(on in the telephony profile) gives the model `end_call`, to be called when
the conversation is over — after its own farewell, when the customer has
said goodbye, or when the business rules say to end politely (a second
refusal). The engine then waits for whatever is being said to finish
playing (the farewell usually arrives in the same response as the call),
gives the last audio 700 ms to reach the line, and closes the session; the
gateway sends BYE when its agent goes away, and that is the hangup. The
journal records the close reason `assistant_hangup`.

**The cases, on the panel.** Which situations end a call is business
knowledge, so it lives where the owner edits it: the lead service's config
`hangup-rules` (the panel's Configs page, versioned like the quote rules;
`internal/tools/hangup.go`). golive reads it at the start of every call
(`hangup_rules: true`, through `tools_base_url` with the tools' token) and
falls back to the built-in set if the service cannot be reached. A rule is
one JSON object:

```json
{"id": "wrong_number", "name": "打错电话", "kind": "model",
 "when": "对方表示不是本人、打错了", "farewell": "抱歉打扰了，再见。"}
{"id": "complaint", "kind": "keyword", "pattern": "投诉|别再打", "farewell": "好的，不再打扰您，再见。"}
{"id": "silence", "kind": "silence", "seconds": 25, "farewell": "听不到您的声音，我稍后再联系您，再见。"}
{"id": "too_long", "kind": "duration", "seconds": 900, "farewell": "今天先聊到这里，再见。"}
```

`model` rules are written into `end_call`'s description, numbered, each
with its farewell: the model says the farewell and calls the tool in the
same breath. An `assistant` rule (`"pattern": "再见|拜拜|不打扰您了"`) is the
net under that: when a reply the assistant has just finished saying
matches, the call ends — on the 09:59 call of 10-20 the model said goodbye
twice and never called the tool, and the line stayed up until the caller
hung up (ISS-047). `keyword`, `silence` and `duration` rules the engine judges
itself (`checkHangupRules`, on the tick and on each final transcript): it
says the farewell as an opening line would be said, drops any answer in
flight, and hangs up when the farewell has played — the same path as the
model's `end_call`, reason = the rule's id. `"enabled": false` keeps a
case without using it; a bad pattern is refused on save. The default set
(goodbye, refused twice, wrong number, done, complaint, 25 s silence, 15
min) is seeded on a lead service that has none. Log lines: `hangup: rules
from the lead service version=N model=K engine=M`, `hangup: a rule applies
rule=…`. Tests: `TestTheSilenceRuleSaysItsFarewellAndHangsUp`,
`TestTheKeywordRuleEndsTheCallOnTheCallersWords`.

**The farewell is said whole.** Once `end_call` is accepted nothing
interrupts it: a sound over it does not yield the floor, nothing heard
after the goodbye is answered, and the hangup waits for the last word
(ISS-046: on a metro-station call the farewell was cut at four characters
by an announcement and the line dropped mid-word; the owner's rule is
"say the last response completely, then hang up").

Guards: `end_call` in the first ten seconds of a call is refused and the
model told to carry on; a farewell that never begins does not keep the
call up (20 s bound); the request is honoured once. The tool is described
to the model in Chinese with the rules it must respect; the owner's prompt
already says when to hang up, and the tool is how.

Seen working on 10-17 at 18:14, on a simulated call: the customer said
goodbye, the model answered with a six-character farewell and `end_call`,
the farewell played in full and the line dropped 770 ms later, 155 s into
the call. Two things that call taught (ISS-041): an accepted `end_call`
ends the response — the backend is *not* resumed with the tool's output,
because the model, resumed, says goodbye again and the hangup cuts it; and
the grace is followed by a second look at the player, so a farewell that
begins late is still played out. The simulated customer saw our BYE as its
control link failing (`failed: Active Call control disconnected`, 20 s
later) rather than the call ending; that is Codex's (ISS-042).

On the real line, closing the socket did **not** hang up the phone (10-19
23:16: the farewell played, `session: the assistant hung up`, and the owner
had to hang up himself). The gateway's BYE comes from its controller, not
from its agent going away. So the session now tells the gateway directly
(ISS-045): `hangup_url` (`http://127.0.0.1:8081/command/{call}` in the
telephony profile) is Active Call's command route for the call's dialog —
the call id the originator gave golive at session open — and golive POSTs
`{"command":"hangup","reason":"normal","initiator":"system","refer":false}`
there before closing the socket (the same thing Codex's receiver sends on
its side; he told us the shape). `session: the gateway was told to hang
up … status=200` is what to look for; a 404 `not_found` means the id was
not the dialog's. Test: `TestTheAssistantsHangupIsSentToTheGateway`.

## Working memory, and work that takes longer than a turn

A policy conversation goes round in circles — "三者改成三百万", "座位险不要了",
"那一共多少钱" — and pricing takes seconds. Two things in the telephony
profile deal with that; neither touches the turn's latency.

**Working memory** (`tools_memory: true`). The model keeps what the call has
settled — the coverage plan, the vehicle, the person's choices — in a JSON
object it edits through the builtin `update_memory(patch)` (keys set, `null`
deletes, nested objects merge). Every edit is a version. The memory is
rendered into the model's context on every request as 【工作记忆 vN】, *after*
the instructions, so the cached prefix is unchanged and the latest plan is
always in view; `golive.memory` events carry each edit.

**Tools that run in the background** (`tools`, `async: true`). A tool call
is carried out by golive itself: a quick one (`async: false`) runs and its
output goes back to the model in the same turn; a slow one is submitted to
a pool (`tools_workers`) and the model is answered at once with
`{"status":"running","task_id":…,"eta_s":…}` — so it tells the caller to
hold on and goes on answering whatever else they ask. The tool's service
is posted the call's memory and its version (`internal/tools.Request`). When
the result comes, it is delivered as a turn of its own at the first gap —
nobody speaking, no answer in flight — or, after `tools_deliver_wait_ms`
with no gap, over whatever is going on. The note the model gets says what
the result was computed on and whether the memory has changed since, so it
reports "按您刚才的方案…" or asks whether to recompute. A newer call of the
same tool supersedes an older one still running; a result nobody wants any
more (the call ended) is dropped. `golive.task` events: running → done →
delivered, with the wait.

**The quotation** is the first such tool: `quote_total` → the lead service's
`POST /api/tools/quote`, which takes the plan under `quote` in the memory as
the coverage selection and the rest as variables, prices it under the
versioned `quote-rules` config (`phone-brain.sh crm config quote-rules
rules.json` to replace the placeholder tables with the business's), records
every quote as `quote.computed` (choices and total, never the person), and
can be made slow for testing (`simulate_delay_ms` in the rules). Log lines:
`tools: memory updated`, `tools: task submitted`, `tools: task done`,
`tools: delivering a task's result`, and `tools: this call` at the end.

**What a quotation must know first** (`quote-plans`, on the panel;
`internal/crm/plans.go`). The owner's rule (10-20): essential fields are
collected the way a form would collect them, one question at a time, and a
quotation is made only when every must field is known. A *plan* is a
combination — its coverages and its fields, each field saying which
coverages need it (`"for": ["车损险"]`, `["*"]` for all), the question to ask
when it is unknown, what to do if the customer cannot answer (`skip` the
coverages that need it, or `stop`), and which way of quoting reads it
(`source`: `rules`, `api` or any — the business service looks the car up by
its plate and does not need a price). The default plan is `standard`; a
call names another with `quote_plan` in its working memory (a lead's own
plan, once leads are built), so leads can carry different combinations.
A coverage that comes in tiers lists them under `options`
(`"驾乘险": ["0", "30万", "50万", "80万"]`, the owner's tiers of 10-20); chosen
without one of them it is asked for as a `choice` (recorded under
`quote.<coverage>`), and `"0"` or `不要` means none.
`quote_total` runs the intake before anything else: with a must field
missing it answers at once

```json
{"status":"need","plan":"standard","missing":[{"field":"vehiclePrice","for":["车损险"],"question":"您这辆车当时的购置价大概是多少？","if_unknown":"skip"}],"known":["vehicleAge"],"instruction":"…"}
```

and the tool's description tells the model to ask the first question,
record the answer with `update_memory`, and ask for the quote again; a
customer who does not know gives `"unknown"`, and a `skip` field then drops
its coverages, named in the answer's `skipped`. Only with nothing missing
does the quotation run (the background path, "please hold", as before).
ISS-048 was the case: 车损险 with no price, the service refusing with a 422
the model never spoke. Tests: `TestAQuotationAsksForItsMustFieldsBeforeItRuns`.

**Seeing a quotation in the journal.** golive logs each step in numbers
and names only — `tools: memory updated … keys=vehiclePrice` (which
fields were recorded, never their values), `tools: task submitted`,
`tools: task result … status=need missing=vehiclePrice,ncd` or
`status=quote total=3542.89 lines=3 skipped=座位险 source=rules` or
`status=error`, `tools: delivering a task's result … waited_for_gap_ms` —
and the journal lists them on the call's page under **Quotation**, each
with its time (click to hear the moment), with markers on the player for
a need, a quote and a failure. The whole loop of a call is read there:
ask → need (which fields) → recorded → ask → quote → told the model.

**The business's quotation service.** The owner's interface document
(2026-07-21) describes an asynchronous inquiry
(`POST …/intranet/vehicle/innerProcess/vehicleApplyInquiryPriceAsync`, answers
"ok" at once) and a query of the latest quote by phone and plate
(`POST …/intranet/vehicle/simpleProcess/queryLastQuoteDetailByPhone`). The
lead service is the adapter (`internal/crm/quoteapi.go`): it notes the quote
that is there before, submits the inquiry with the plan in the service's
terms (coverage codes, 不计免赔, sums in yuan from "300万"; 交强险 as
`isInsureCompelInsurance`), polls the query every `poll_every_ms` until a
quote with a new id or flow code appears, and returns it normalized: total,
商业险 / 交强险 / 车船税, the lines with amounts and premiums, the discount,
the periods, the added services, the difference against last year, how long
it is valid, and anything in the plan the service does not know (`skipped`).
A failed quote comes back as an error with the service's reason, which the
model says. The versioned config `quote-api` selects it: with a `base_url`
quotes go to the service, without one to the placeholder rules. The config
holds the intranet address, channel ids, the coverage-name → code map, and
a `default_phone` / `default_vehicle_no` for the test line; it is the
business's and stays on the box:

```bash
scripts/phone-brain.sh crm config quote-api            # what is set (v1 has no base_url)
scripts/phone-brain.sh crm config quote-api api.json   # save a new version
scripts/phone-brain.sh crm quote '{"tool":"quote_total","memory":{"quote":{"交强险":true,"车损险":true,"三者险":"300万"},"carNum":"浙A12345"}}'
```

**Where the pieces run.** The quotation service is on the business's
intranet, so the lead service runs there too, next to it, and golive on the
box reaches the lead service over the network. Two addresses are therefore
deployment settings, not profile or config content:

- the lead service's address for golive: `tools_base_url` in the profile
  (`http://127.0.0.1:8791` by default), overridden by `GOLIVE_TOOLS_BASE_URL`;
  tool URLs in the profile are paths (`/api/tools/quote`) joined with it.
  The intranet lead service's `api.token` is copied to the box and named by
  `GOLIVE_TOOLS_TOKEN_FILE`. `phone-brain.sh restart` passes both through.
- the quotation service's address for the lead service: `-quote-api` /
  `CRM_QUOTE_API_BASE` (`http://<host>/intranet`), which overrides the
  `quote-api` config's `base_url`; the config keeps what is the business's
  (channel ids, the coverage map, the test identity) and is saved on the
  intranet instance with `crm config quote-api api.json`.

On the intranet host: `bin/crm-linux-amd64 -addr 0.0.0.0:8791 -dir
~/golive-crm -quote-api http://<quote-host>/intranet -golive ""` (the
originator flags matter only where calls are placed from). The quote tool's
`eta_s` is 20 in the profile and its timeout 120 s, which is what the
service is expected to take; `tools: task done … took_ms` in the log says
what it did take.

Not yet: seeding the memory from the lead's record (phone, plate, last
year's plan) when the lead service places the call, writing the final
memory back as the contact's summary, the task events in the journal.
