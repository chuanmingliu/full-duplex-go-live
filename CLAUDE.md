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

**End-to-end latency within 1200 ms, on a phone call.** This is the thing to
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

Stage medians of the 943 ms run, from the caller's last sound:

| final asked for | final back, answer begun | LLM first token | first clause to TTS | first audio |
| ---: | ---: | ---: | ---: | ---: |
| 120 | ~211 | +415 | +70 | +216 |

Phone calls through golive: placed on the morning of 2026-10-11, all before
the early final. The 12 turns read from the log: last sound → first audio p50 1210, p90 1334; turn
judged over → first audio p50 830, p90 954 (pause 380). **No call has been
placed on the current build yet.**

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
- **The latency console refuses a batch for one unknown key in `extra`.** It
  takes `coverage`, `endpoint`, `port`, `revision`; anything else is "Invalid
  canonical field: extra" and nothing in the batch is kept. golive sent
  `agent` on every event, so none of its turns were ever there. Fixed in
  8382c52; not yet seen working on a call.
- **The phone gateway sends no turn detection golive acts on.** Its
  `session.update` is 31 KB of instructions; calls run on the profile's
  `vad.min_silence_ms`. The originator's "requested silence 150 ms" setting
  belongs to the other agent. `realtime: session.update` now logs the shape.
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

1. **A call on the current build.** Read the turns with
   `scripts/call-latency.sh`, check the console shows golive's turns
   (`telemetry: the latency console took the first events`), and listen for the
   two line problems above.
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
