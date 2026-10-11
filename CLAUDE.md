# Working on golive

Notes for whoever picks this up next. State as of 2026-10-11, branch
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

The figure measured so far is the caller's last sound → first audio leaving
golive (`+pause` / "last sound → first audio" in `scripts/call-latency.sh`). The
phone leg — Active Call, RustPBX, the GSM gateway, the carrier, both ways — is on
top and has not been measured. Until it is, treat ~900–950 ms at golive as the
working target.

## Where it stands

| What | Figure |
| --- | --- |
| Real providers on the test box, no phone, 15 turns (`phone-brain.sh bench`) | p50 1159, p90 1395, min 927, max 1448 ms |
| Same, previous build, 15 turns | p50 1248 ms |
| Simulated providers (`make bench-sim`), before → after PR #2 | 1755 → 1324 ms |
| Phone calls through golive | 2 placed 2026-10-11; audio fine both ways; per-turn figures not yet read from the log |

Stage medians of the 1159 ms run, in ms from the turn being judged over (which
is `vad.min_silence_ms` = 380 after the last sound):

| answer begun | ASR final | LLM first token | first clause to TTS | TTS first audio |
| --- | --- | --- | --- | --- |
| 0 | 103 | 450 (its own ttft: 450; probe alone: 385) | +94 | +293 (probe alone: ~240) |

So the path is: 380 pause + 0 + 450 + 94 + 293 ≈ 1217.

## What to work on, in order

1. **Turn-taking on a real line.** On the second call the caller spoke five
   times in ten seconds before getting an answer, and one answer came ~6.6 s
   after the question began. Not yet diagnosed. Suspects: each new utterance
   cancelling an answer that was about to start (`on_new_query: cut`), and
   golive hearing its own voice back from the line as speech. Read the engine's
   lines for that call first (`phone-brain.sh log 6000 '<regex>'`).
2. **Speculation rarely starts mid-pause with the real recognizer.** In 12 of
   17 bench turns the answer was begun at the close of the turn or at the ASR
   final, not during the pause; the 5 that did start early (~160 ms) were the
   fastest. Tencent's partials lag the audio, so the hypothesis is not still for
   `speculative_settle_ms` until the stream is ended. Idea not yet built: end
   the recognizer's stream early in the pause to get the final sooner, and open
   a new one if the caller resumes. Estimated worth 150–200 ms; unmeasured.
3. **The chat model's first token** (385–450 ms) is the largest single term.
   `scripts/phone-brain.sh llm -try 'label|base|model|KEY_ENV'` compares
   candidates from the box. `deepseek-v4-flash` on the same key is untested.
4. **Smaller things seen in logs:** a doubled phrase in one transcript
   ("…明天上午去上海的，明天上午去上海的航班…", clip or engine unknown); a segment
   that was only a closing quote sent to the synthesizer; MiniMax's per-minute
   request limit reached by `ttsprobe` and once by the bench.

Not worth trying again: MiniMax's bidirectional endpoint (≈2 s slower on a
clause that does not end a sentence; no better with flush). Shortening the
pause (the LLM, not the pause, is on the critical path while speculation
holds).

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
- `scripts/phone-brain.sh` is the whole procedure: `status`, `golive`, `bench`,
  `net`, `log`, `llm`, `tts`, `sync`, `restore`. It needs `sudo` for docker.
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
