# Working on golive

Notes for whoever picks this up next. State as of 2026-10-11 midday, branch
`telephony-realtime` (PR #3, stacked on PR #2 `perf-and-live-parity`, stacked on
PR #1 `prod-hardening`).

## What this is

A Go service that speaks the gpt-live-1 protocol (`/v1/live`) and the OpenAI
Realtime protocol (`/v1/realtime`). There is no single full-duplex model behind
it: a streaming recognizer, a chat model and a synthesizer are run as a cascade
and made to behave like one. That simulation is the point of the project.

Providers in use: Tencent realtime ASR, DeepSeek (`deepseek-chat`), MiniMax
(`speech-2.8-turbo`, standard WebSocket endpoint).

## The goal

**End-to-end latency within 1200 ms at p95, on a phone call.** The owner said
so in as many words: the figure is a p95, not a median. This is the thing to
optimise; everything else is in service of it.

Two definitions are in use. Quote both, and say which.

- **Last sound → first audio**: the caller's last sound to the first audio
  leaving golive. `+pause` in `scripts/call-latency.sh`; what the bench
  measures client-side. This is what a caller waits through, less the phone leg.
- **Turn judged over → first audio**: the owner's latency console (test box,
  `:8768`) calls this the reply: `vad.speech_stopped → phone.audio_sent`. It is
  the `audio` column, and it does not include the pause that ended the turn.

The phone leg — Active Call, RustPBX, the GSM gateway, the carrier, both ways —
is on top of either and has not been measured.

## Where it stands

Real providers on the test box, no phone, 15 measured turns each
(`phone-brain.sh bench`), last sound → first audio:

| Build | What changed | p50 | p95 |
| --- | --- | ---: | ---: |
| d45e519 | first deploy | 1248 | |
| 9da7702 | pause 380 | 1159 | 1395 (p90) |
| 2e2457b | pause 280; a slow hour for the chat model | 1229 | 1572 |
| 42b5f54 | early final + two copies of each completion; pause 380 | **943** | 1244 |
| 42b5f54 | same, one copy per completion (minutes later) | 952 | 1431 |
| ea3a478 | no guess on trailing words while a final is pending | 978 | 1123 (max 1191) |
| b8883c6 | four copies; fixes from the 13:09 call; 30 turns | 922 | 1202 (max 1237) |
| dcf68a8 | shorter first clause; 30 turns | 940 | 1181 (max 1202) |
| ea5d26d | backend warmed at session open; fixes from the 14:22 call; 20 turns | 904 | 1118 (max 1184) |
| c2fbf96 | answer prepared during a mid-thought wait; fixes from the 18:34 call; 20 turns | 878 | 1067 (max 1262) |

Stage medians of the 943 ms run, from the caller's last sound:

| final asked for | final back, answer begun | LLM first token | first clause to TTS | first audio |
| ---: | ---: | ---: | ---: | ---: |
| 120 | ~211 | +415 | +70 | +216 |

Phone calls through golive, last sound → first audio, from the log:

| When | Build | Turns | p50 | worst | over 1200 |
| --- | --- | ---: | ---: | ---: | ---: |
| 10-11 morning | before the early final, pause 380 | 12 | 1210 | 1334 (p90) | about half |
| 10-11 13:09 | ea3a478 | 9 | 862 | 1382 | 2 |
| 10-11 14:22 | dcf68a8, the owner's 13k-character prompt | 19 | 972 | 1561 | 3 |
| 10-11 18:34 | ea5d26d, same prompt | 15 | 926 | 1481 | 1 |

By the console's definition: 13:09 p50 482, worst 1002; 14:22 p50 592, p95
1181; 18:34 p50 546, p95 805. The one slow turn of the 18:34 call was a
fragment the engine waited 700 ms on as a mid-thought and then answered from
nothing; the answer is now prepared during that wait. **No call yet on the
build after it.**

On samples this small p95 is the worst turn or the one before it: one turn in
fifteen over 1200 is a miss. Pool the calls made on one build before saying the
target is met.

## What was learned, so it is not learned again

- **The pause was being waited through for nothing.** The recognizer's partials
  trail the audio by 300 ms and more, so the words in hand during the pause were
  short of the sentence, the guess made on them was thrown away at the final,
  and the answer began a round trip after the turn closed. What the recognizer
  does do promptly is finish: told the audio has ended, the final is back in
  ~95 ms. `duplex.early_final_ms` ends the recognition 120 ms into the pause,
  begins the answer on that final, and opens a continuation to hear anything
  that follows. The pause length then stops mattering to latency until it
  exceeds the time the answer takes to prepare — choose it for turn-taking.
- **It also makes semantic endpointing work.** "你好，" followed by a 400 ms
  pause used to be taken as a turn, because "你好" had not arrived as a partial
  when the pause ran out. With the final in hand 215 ms in, the engine knows it
  is an opener and waits.
- **The chat model's first token is the largest term and is not one number**:
  170–880 ms for the same request within one run. `llm_hedge: 2` takes the
  faster of two. It bought ~70 ms at the median and ~190 ms at p95.
- **Copies of a completion are independent within a minute, not across
  them.** One copy is over 600 ms nearly half the time and two left one turn in
  eight slow, as predicted. Four should have left one in a hundred and left one
  in ten: the model has slow spells in which every copy is slow. Each of four
  copies wins about equally often. More copies will not fix the tail; a second,
  independent route to a model would.
- **The latency console refuses a batch for one unknown key in `extra`.** It
  takes `coverage`, `endpoint`, `port`, `revision`; anything else is "Invalid
  canonical field: extra" and nothing in the batch is kept. golive sent
  `agent` on every event, so none of its turns were ever there. Fixed in
  8382c52 and seen working on the 13:09 call (61 events, none refused).
- **The phone gateway does send turn detection, in camelCase**
  (`silenceDurationMs: 150`), which read as nothing until the shape of its
  `session.update` was logged. Both spellings are read now; the telephony
  profile declines it (`honor_turn_detection: false`) because 150 ms on an
  energy detector answers every hesitation and, since the early final, buys no
  latency.
- **A final asked for in a pause ends in "。" whatever was said.** Read as the
  recognizer's opinion it made "就是。", "呃。" and "我想问你一下啊。" finished
  turns on a real call. Ignored now for deciding whether the caller is done.
- **An interruption was being weighed on words that arrive late.** The early
  final used to be skipped while a floor hold was undecided; the slowest turn
  of the 13:09 call was three syllables said over the assistant.
- **Every bench turn is a first turn.** The bench opens a session per turn, so
  the slow first completion was in every bench figure before ea5d26d; warming
  at session open took the bench's median first token from ~430 to 342 ms.
- **A long prompt costs nothing once cached, and the first completion of a
  call is slow regardless.** 13,116 characters of instructions: ~7k prompt
  tokens served from the provider's cache on every turn, median first token
  444 ms — the same as with a 70-character prompt. But the first completion
  took 1101 ms with the cache already holding the prompt.
  `duplex.backend_prefix_warm_start` sends a one-token request while the phone
  rings and again as the greeting starts.
- **The owner's prompt lives in the originator's web page**, in that page's
  browser storage, and is sent only by a dial made from the page. A dial from
  the box (`to` + `greeting`) runs on the profile's own short instructions.
  For a test of the real prompt the owner dials from the page.
- **A bare "呃" is not a turn.** It was being waited on for 700 ms as a
  mid-thought and then answered anyway. `duplex.ignore_hesitations` leaves it
  with the caller.
- **A mid-thought wait was costing a whole backend round on top of itself.**
  Words that read as unfinished get 700 ms before the turn is taken, and the
  answer was begun only then: 700 + ~780. `duplex.speculate_while_patient`
  begins it on the early final, during the wait.
- **A deadline on the first clause will flush one character** if that is all
  the model has produced: "加", then "上了吗？". The flush now waits for
  `stream_first_chunk_chars`.
- **Event ids must survive a restart.** They were boot id + component + a
  count from one, so each restart reissued the ids of the process before it and
  the console refused them as duplicates. The count now starts at the clock.
- **Dialling from the box**: `POST /api/outbound/dial` needs `to`; the locked
  number is `ALLOWED_CALLEE` in the originator's env file on the box. A job can
  read it there and dial without it ever being printed. Never copy it out.
- **The box reaches its providers through a VPN client's tunnel** (names
  resolve to 198.18.x.x; TCP connects in 1 ms because the client terminates it
  locally) and over Wi-Fi. Domestic traffic is sent on directly — TLS to the
  chat model is 30 ms — but every provider round trip passes through that
  process, and one morning it sent the recognizer's traffic abroad. What it
  costs per round trip could not be measured without root.
- **A phone line is never quiet, and returns the assistant's voice.** The
  second call held a finished question open for 3.3 s on line noise and threw
  the answer away eight times. See "A line that is never quiet" in
  `TELEPHONY.md`: `duplex.settled_close_ms`, `vad.echo_tail_ms`,
  `vad.barge_in_margin_db`. Written from that one call's log; **not yet
  verified on a line.**
- `deepseek-v4-flash` on the same key: 8 of 10 completions came back empty and
  the rest took over a second. MiniMax's bidirectional endpoint is ~2 s slower
  on a clause that does not end a sentence.
- Bench clips are synthesized afresh on each run, so a pause inside one
  ("好的，谢谢。") is sometimes longer than the end-of-turn silence and the clip
  is, correctly, heard as two turns. A first-audio figure of a few hundred ms
  is that.

## What to work on, in order

1. **A call on the current build**, dialled by the owner from the page, to see
   whether p95 holds with the three causes from the 14:22 call removed.
   `scripts/call-latency.sh` prints p95 and how many turns were over 1200.
1b. **The chat model's slow spells are what is left of the tail.** Options, all
   the owner's to choose: a second route to the same model (another host of
   DeepSeek needs its own key) raced against the first; a different model;
   or racing the first synthesis request as well (the synthesizer's first audio
   is 200 ms at the median and 300+ one turn in seven).
2. **Recognition on phone audio is poor** (wrong words in several turns of the
   real calls). The recognizer is run with its 16 kHz engine on audio that is
   8 kHz band-limited by the phone network. `TENCENT_ASR_ENGINE=8k_zh` exists
   but the adapter would have to send 8 kHz audio. Not tried.
3. **The remaining terms**, largest first: LLM first token (415), TTS first
   audio (216), final round trip (95), first clause (70).
4. The phone leg has never been measured. The console has the events to do it
   once golive's are accepted.

## Build and test

```bash
make vet test          # go vet, go test ./...   (internal/duplex takes ~3 min)
make race              # with the race detector
make run-sim           # mock providers with realistic stage latencies
make bench-sim         # measure a server started with run-sim
make bundle            # dist/golive-telephony.tar.gz for a box with no Go
```

`internal/duplex` has failed once in a full run and passed on rerun; the test
was not identified. Treat a single failure there as worth a second look, not as
noise.

For every fix: a test that fails without it. Check that it does.

## The test box

A LAN machine runs the phone path; see `TELEPHONY.md` for the diagram and the
runbook. Things the runbook does not say:

- It is not reachable from a cloud shell. The owner copies the bundle over and
  runs commands; outbound calls are originated by Aria, the call originator on
  that box, which is also reachable at its public hostname.
- `runner.sh` (kept in `~/golive` on the owner's Mac, not in this repo) runs
  job files left in `~/golive/.runner/in` on the box over ssh and writes their
  output to `.runner/out`. With it running, a session that can write that
  folder can deploy and bench without the owner pasting anything. No sudo:
  use `phone-brain.sh restart`, not `golive`.
- `scripts/phone-brain.sh` is the whole procedure: `status`, `golive`, `restart`,
  `bench`, `net`, `log`, `llm`, `tts`, `sync`, `restore`. It needs `sudo` for docker.
  golive runs under a keep-alive loop it starts; it is not a system service, so
  after a reboot run `phone-brain.sh golive` again.
- The GSM gateway changes its SIP port when it re-registers (seen three times
  in two days) and once changed its address. RustPBX's trunk must point at the
  current one or every outbound call ends in `408 Request Timeout` after 32 s
  with the phone never ringing. A timer on the box (`gateway-port-sync`) keeps
  it in step; it was given the gateway's new address on 2026-10-11.
- Provider reachability from that box has been unreliable once (MiniMax
  handshake timeouts, Tencent refusing a recognition as cross-border) and was
  fine after a reboot. Cause unknown. `phone-brain.sh net` shows the egress.

## Rules of the road

- This repository is public. No keys, phone numbers, SIP accounts or passwords
  in files, commit messages or PR text. Provider keys live in an env file on the
  box and are never printed; the scripts print key *names* only.
- Latency claims come with how they were measured and on how many turns. A
  figure from mock providers is labelled as such.
- Debug logs contain transcripts.
