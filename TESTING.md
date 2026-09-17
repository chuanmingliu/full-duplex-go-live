# Testing golive

```bash
./start.sh
```

`start.sh` rebuilds from source when a Go toolchain is present, and otherwise
falls back to the prebuilt binary for your platform in `bin/` — so this works
on a machine with no Go installed.

Then open **http://localhost:8080** and click **Connect & talk**.

The browser will ask for microphone permission. `localhost` counts as a secure
origin, so this works over plain HTTP — you do not need TLS.

---

## What to try, and what to watch

The page shows four channel lamps: **listen · transcribe · think · speak**.

1. **Say something, then stop.** listen and transcribe light while you talk;
   think lights when the turn is delegated; speak lights when audio comes back.
   "first audio" in the stat row is time from your speech ending to the first
   syllable out.

2. **Talk over the assistant while it is speaking.** This is the one that
   matters. **listen and speak light at the same time** — a half-duplex cascade
   cannot do that. Playback cuts, and the transcript row for that turn is
   rewritten to only the part you actually heard, with a red note saying how
   many milliseconds landed. That truncated prefix is what goes into
   conversation history; the rest never happened as far as the model is
   concerned.

3. **Tick "backchannel" and keep talking for ~3 seconds.** The assistant says
   "嗯" while you still hold the floor — speak and listen lit together again,
   this time without an interruption.

4. **Mute mid-turn.** The listen lamp goes out but the session stays up;
   unmuting is instant because the channel was never torn down.

On mock providers the recognizer always "hears" the same sentence, revealed
progressively as you talk, and the reply is a fixed long paragraph — deliberately
long so there is something to interrupt.

---

## Session config

**Session config** in the header opens two fields, both saved in your browser and
applied on the next connect:

* **System prompt** — sent as `session.instructions`. Empty uses the server's,
  shown as the placeholder. Keep it short: the conversational layer has a small
  context window, and detailed procedure belongs in the backend prompt.
* **Greeting** — spoken the moment the session opens, before you say anything.
  Empty uses the server's; **Clear both** sends an explicit empty string, which
  suppresses it so the assistant waits to be spoken to. The two are different
  instructions and the protocol distinguishes them.

* **On new query** — what happens to an answer still in flight when you start
  speaking again. `cut` stops mid-word; `finish_sentence` completes the sentence
  and then yields; `queue` says everything first, which is what a half-duplex
  cascade does and is kept only for comparison.

A greeting is an ordinary assistant turn: you can talk over it, and it is
truncated honestly if you do. It is excluded from the latency summary, because
nobody was waiting for it.

### The three things that are off by default

Three behaviours make the assistant speak, or keep speaking, when it was not
asked something. All three are implemented, tested and switchable in one
boolean, and all three ship **off**, because in practice each was heard as the
opposite of what it was for.

| Switch | What it does | Why it is off |
| --- | --- | --- |
| `duplex.backchannel` | says "嗯" while you are still talking | it lands on top of the caller |
| `duplex.holding_filler` | says "我看一下" while a delegation runs long | heard as the agent having nothing to say — and it costs a synthesis and can delay the answer it was covering |
| `duplex.user_backchannel_phrases` | the words that do **not** take the floor | it decides on the caller's behalf that something they said was not worth answering |

The phrase lists for the first two stay populated, so switching one back on is
one boolean, not a retyped list. `user_backchannel_phrases` ships empty, with a
suggested set in a comment in `config.go`; empty means any word the recognizer
hears takes the floor at once.

If you do turn the filler on, note that it needs two things to be true, not one:
the wait must pass `duplex.holding_filler_after_ms` (1500 ms), *and* it must be
slow **for this call** — half again this session's own median. A filler's whole
meaning is "this is taking longer than usual", so on the first delegation of a
call, when there is no usual yet, it says nothing and measures instead.

### The one that stays on: `user_backchannel_hold_ms`

This arrived with the phrase list and is not the same feature, so it did not go
off with it. What it actually does is wait for the transcript before yielding
the floor — and the case it was written for has no phrase list in it at all.

A cough, a door, or your own assistant coming back through a speakerphone opens
the VAD. The recognizer finds no words. With the hold at 0 the assistant stops
anyway, which is the symptom of the greeting being cut mid-name, every time, at
the same word:

```
speak: turn truncated  played_ms=1056 emitted_ms=1180 heard_text=您好，我是众安保险的车险管家小翠
```

That line is from `TestNoiseDoesNotCostTheAssistantItsTurn` with the hold
switched off. If you are seeing it in a real call, check `user_backchannel_hold_ms`
first, then whether the noise is your own audio — the `listen: this barge-in
looks like the assistant's own voice returning` warning names that case.

The hold's cost is paid only while the transcript is ambiguous. With the phrase
list empty a real interruption releases it on the first recognized word, so what
it delays is silence, not speech.

To feel the difference in **On new query**, ask something that produces a long
answer, wait until the assistant is a sentence or two in, and interrupt. Under
`queue` you will hear the rest of the old answer before the new one — including
in the silence between two sentences, which is the case that used to leak
through regardless of the setting.

Server-side, both live in the profile so they apply to every client, not just
the browser:

```json
{ "instructions": "…", "greeting": "你好，我是语音助手，有什么可以帮你的？" }
```

`GOLIVE_INSTRUCTIONS` and `GOLIVE_GREETING` override them.

---

## The interrupted sentence plays under or before the new answer

Symptom: you cut the assistant off, it starts answering the new question, and
the old answer is still audible underneath or ahead of it.

Three different things produce this, in the order they are worth ruling out.

**The page was still holding scheduled audio.** Playback is the client's own
state, and it always lags the server by up to `duplex.playback_lead_ms` (300 ms)
because the server deliberately emits ahead of the playhead. The page drops what
it has scheduled when a truncation arrives, and that is the normal path — but it
now also drops it the moment audio for a *different* `item_id` shows up, because
two turns playing at once is never right whatever else did or did not arrive.
The log line is `· dropped N scheduled chunk(s) of item_X`. Seeing it means the
guard did its job; seeing it *often* means a truncation is going missing and is
worth reporting.

**The policy is not the one you think.** `finish_sentence` is supposed to let
the sentence in flight complete before yielding, and `queue` says everything
first. Check the `On new query` dropdown and `duplex.on_new_query` — the page's
"server default" is whatever the profile says.

**The synthesis provider is replaying an abandoned task.** If the server log
shows `truncated at N ms of M ms` for the old turn, the engine did its job — it
stopped generating and cleared its queue. The leftover is then coming from the
provider, not from golive.

A provider that holds one long-lived connection keeps generating audio for text
it has already been given. Abandon a synthesis mid-sentence and those frames
stay in the socket; reuse the connection without draining them and the *next*
request reads the old tail first. The audio arrives on the new turn's channel,
correctly formed and completely wrong, which is why nothing in the engine's own
accounting looks amiss.

The bundled MiniMax adapter drains an abandoned task before reusing the
connection. Confirm it is working:

```bash
grep -E 'resynced an abandoned task|did not drain in time' golive.log
```

`resynced an abandoned task discarded_frames=N` is the healthy line. If you see
`did not drain in time`, or you still hear the leak, force a clean connection
after every interruption:

```json
{ "duplex": { "reset_tts_on_interrupt": true } }
```

That costs a reconnect on the turn after each barge-in — a few hundred
milliseconds — which is why it is not the default.

---

## The answer arrives in pieces, or the page shows it twice

Two different faults produce this, and they are told apart by where you notice
it — in your ears, or on the page.

**In your ears.** You hear the first sentence of an answer and nothing after it,
and the next turn continues as though the whole thing had been said. Look for

```bash
grep 'produced no audio' golive.log
```

Those segments were synthesized to silence. Until this was fixed the engine
closed them anyway, which wrote them into conversation history as spoken — so
the model answered its own unheard sentences, and every later turn inherited the
drift. They are now dropped from history instead, and the WARN names each one.
The cause is upstream: a synthesis whose context was already cancelled returns an
empty stream, so check for a barge-in logged at the same millisecond. If those
barge-ins are your own assistant coming back through a speakerphone, see
*Tuning* below.

If that grep is empty and the answer still stops partway — always at the same
word, because that is the first segment boundary — check which MiniMax endpoint
you are on, and then measure rather than guess:

```bash
grep -E 'nothing to terminate it|WEBSOCKET_ENDPOINT' golive.log
./bin/ttsprobe -trace -env .env.local
```

`-trace` sends scripted sequences to **both** endpoints and prints what ended
each segment. Measured against one live account:

| scenario | `/ws/v1/t2a_v2` | `/ws/v1/t2a_v2_bidi` |
| --- | --- | --- |
| one segment, no flush | `is_final` | `is_final` |
| one segment, flush | `is_final` | `is_final` |
| two / three segments on one task | `is_final` each | `is_final` each |
| `task_cancel`, then another segment | **2202 invalid event**, task dead | `task_canceled`, next segment fine |

Three things follow.

**Both endpoints finalise every `task_continue`.** The obvious theory — that a
bidirectional stream would end a segment only on an explicit `task_flush` — is
false, and so is anything built on it.

**`task_cancel` really is bidirectional-only, and getting it wrong is fatal
rather than untidy.** On the plain endpoint it returns `2202 invalid event` and
the task is finished; every later segment on that connection is dead. That is
what `bidiOnly` gates, and the trace is the proof that the gate has to be there.

**The bidirectional endpoint sends frames the adapter had never seen** —
`sentence_start` before the audio, `sentence_end` *after* `is_final`. The second
one is the important shape: a frame that arrives behind the terminator the
reader already returned on stays in the socket for whoever reads next. That is
how a `task_flushed` from one segment came to end the following one with no
audio, which is the greeting stopping at the first segment boundary. A
terminator now only counts for the segment that asked for it.

### Switching the backend

`GOLIVE_LLM` (or the `LLM` dropdown on the page) picks one of `deepseek`,
`cerebras`, `inception`, `openai-compatible`, `mock`. All of them speak
OpenAI's `/v1/chat/completions`, so the only difference is the key, the base URL
and the model id — see `.env.example`.

Each provider has its own `GOLIVE_<NAME>_MODEL` and `GOLIVE_<NAME>_BASE_URL`,
which outrank the shared `GOLIVE_BACKEND_*` pair. That is what lets two
backends coexist in one process; before, the second one silently inherited the
first one's model id.

Worth trying because the backend is the dominant term: `llm_first_token_ms` is
2441–3385 ms on the persona prompt, against 259–546 ms on a short one, while
synthesis is 250–360 ms per segment. Compare with

```bash
grep 'think: first token' golive.log
```

### Which probe answers which question

`-trace` speaks the protocol directly, so it describes the **server** and is
unaffected by anything in golive. Running it again after a fix to the adapter
produces the same output by construction — that is not a regression, and not a
confirmation either.

`-adapter` puts `minimax.TTS.Synthesize` in the path and runs the shape that
broke — a flushed opening fragment, then sentences — on both endpoints, printing
the bytes each segment produced:

```bash
./bin/ttsprobe -adapter -env .env.local
```

Every segment non-zero is the fix working. A zero is the fault, and the exit
status is non-zero so it can be wired into a check.

`ttsprobe -trace` includes a **flush, then another segment** row for exactly
this, and it is the confirmation:

```
/ws/v1/t2a_v2_bidi  flush, then another segment  [is_final task_flushed]  audio=[85450 0]
```

Segment one, 85 KB, ends on `is_final` at 452 ms. `sentence_end` at 452 ms.
`task_flushed` at 452 ms. Segment two ends on that leftover acknowledgement with
**zero bytes**.

Two rows in that output are the probe being deliberately naive rather than a
fault: `flush, then another segment` and `cancel, then another segment` both
return `2202 invalid event` on the *plain* endpoint, because the probe sends
those events unconditionally and the plain endpoint has neither. golive gates
both on the endpoint, which is what `bidiOnly` is for — and the 2202 shows why
it matters, since the task is finished afterwards and every later segment on
that connection is dead.

`/ws/v1/t2a_v2` remains the default — it is no slower, and it has hours of
working calls behind it.

**On the page.** The greeting appears doubled — `您好，我是众安保险的您好，我是众安
保险的车险管家小翠儿` — or a filler merges into the line above it. That is the demo
page, not the engine: item ids are unique within a session and start again at
`item_1` on the next socket, so a transcript kept across a reconnect gets written
into by both calls. `connect()` now clears it. The check runs as part of `make
test`:

```bash
node web/transcript_test.mjs
```

---

## Hearing nothing

Work down this list; each step rules out a layer rather than a guess.

1. **Press "Test sound" on the page.** One second of 440 Hz through the exact
   AudioContext, scheduler and destination the assistant's audio uses. Silent
   tone → the tab or the output device is at fault and nothing below matters.
2. **Check the "audio context" stat.** Anything but `running` means the browser
   suspended playback. It is resumed automatically on every chunk, so a stuck
   `suspended` points at autoplay policy or an interrupted device.
3. **Check the "audio out" stat while talking.** Counting up means audio reached
   the tab — the problem is downstream, in routing or volume.
4. **Switch the "Output" picker to the built-in speakers.** On macOS, opening
   the microphone can flip a Bluetooth headset into its call profile, which
   silences playback with no error anywhere. This is the single most common
   cause of "the CLI works but the browser is silent". Chrome can retarget the
   page's audio; Safari cannot, so change the system output instead.
5. **Bypass the browser entirely.** `golivectl` writes what the server actually
   emitted, so `afplay check.wav` separates the service from the client in one
   command:

   ```bash
   ./bin/golivectl -url ws://127.0.0.1:8080/v1/live -speak-ms 1800 -wait-ms 14000 -out check.wav
   afplay check.wav
   ```

   Audible → the server and all three providers are fine; stay in the browser.
   Silent → go to the log, below.

---

## Reading the log

Everything goes to the terminal and to `golive.log`. One turn looks like this:

```
listen: utterance opened      start_ms=0 active_ms=200 barge_in=false noise_floor_db=-60
asr: stream opened            provider=tencent run=1 item=user_1
asr: partial                  run=1 text=你好，帮我查
asr: final                    run=1 text=你好，帮我查一下明天的天气 elapsed_ms=1980
think: delegating             turn=item_1 target=responses reason=final_transcript history_messages=0
think: first token            turn=item_1 provider=deepseek model=deepseek-chat ms=310
speak: tts session opened     provider=minimax rate=16000 ms=240
speak: first audio for segment turn=item_1 chars=11 ms=180
speak: segment synthesized    turn=item_1 text=好的，我听到你说 audio_ms=1210 ms=190
speak: turn truncated         turn=item_1 played_ms=1904 emitted_ms=2210 heard_text=好的，我听到你说…
barge-in: generation invalidated generation=2
```

What each prefix tells you:

| Prefix | Read it for |
| --- | --- |
| `listen:` | Did the VAD hear you at all? `noise_floor_db` is the adaptive floor; if it sits near your speech level, the mic is too hot or the room too loud. `barge_in=true` means it opened while the assistant held the floor. |
| `asr:` | Partial hypotheses as they arrive, and `elapsed_ms` on the final — the recognizer's real latency. |
| `think:` | Which turn was delegated and why (`final_transcript`, `speculative`, `revision`), then time to first token. `history_messages` is how much context went with it. |
| `think: answered without speculating` | INFO, one line per turn that paid the backend's full time-to-first-token when it did not have to. `reason` says what stopped it — too few characters, no pause, or a backchannel still being judged. If every turn carries this line, speculation is switched on and doing nothing. |
| `speak:` | Per-segment synthesis time. `chars=11 ms=180` on the first segment is your real time-to-first-syllable. |
| `speak: turn truncated` | `played_ms` vs `emitted_ms`, and `heard_text` — the exact prefix that entered history. |
| `speak: the segment produced no audio` | WARN. The synthesis stream accepted the sentence and returned not one frame. The sentence is deliberately kept out of history, so the answer is short by that much rather than the model believing it said something you never heard. Repeated lines mean the TTS connection is being cut mid-answer — look upward for a barge-in at the same millisecond. |
| `server event` | The full JSON of every outbound protocol event. Audio chunks are counted, not printed, or they would bury everything. |

`grep` is your friend:

```bash
grep -E 'listen:|asr:|think:|speak:' golive.log     # the turn pipeline only
grep 'truncated' golive.log                          # every interruption
grep 'server event' golive.log                       # replay the protocol stream
```

---

## Without a browser

`golivectl` drives a session from the terminal, which is the only sane way to
test timing repeatably — it can interrupt at an exact millisecond.

```bash
./bin/golivectl \
    -speak-ms 2200 \
    -barge-in-at 1400 \
    -record session.wav \
    -v
```

It streams synthetic voiced audio at real time, waits 1400 ms after the first
assistant audio, then talks over it. `session.wav` is stereo — your input on the
left, the assistant on the right — so the overlap is visible in any waveform
viewer. `-v` prints every server event.

(Without a Go toolchain, use the platform-suffixed binary instead:
`./bin/golivectl-darwin-arm64`.)

Useful flags: `-in yourfile.wav` to send real speech, `-rate 8000|16000|24000`,
`-delegation client`, `-asr/-llm/-tts` to override providers per session.

---

## Switching to the real providers

```bash
cp .env.example .env.local
# fill in: DEEPSEEK_API_KEY, TENCENT_ASR_{APP_ID,SECRET_ID,SECRET_KEY},
#          MINIMAX_TTS_API_KEY, MINIMAX_TTS_VOICE_ID
./start.sh real
```

The server constructs all three providers at boot, so a missing credential
fails immediately and names the variable rather than dying on the first caller.

Two things that commonly bite on first contact:

* **MiniMax host.** `api.minimaxi.com` for mainland-platform keys,
  `api.minimax.io` for global ones. The wrong one authenticates fine and then
  fails at `task_start`.
* **`MINIMAX_TTS_VOICE_ID` has no default.** Without it, session start is
  refused with a message saying so.

---

## Tuning, once you hear it

In `configs/tencent-deepseek-minimax.json`:

| If it feels like… | Change |
| --- | --- |
| It cuts me off mid-sentence | raise `vad.min_silence_ms` (380 → 600) |
| It takes too long to start replying | lower `vad.min_silence_ms`, or lower `duplex.speculative_stable_ms` (keeping it well under `min_silence_ms` — the gap between them *is* the head start) |
| `think: speculation missed` on nearly every turn | raise `duplex.speculative_stable_ms`: it is guessing before the recognizer has caught up, and each miss costs a cancelled generation |
| First syllable is slow | lower `duplex.stream_first_chunk_chars` (8 → 5); on MiniMax check `MINIMAX_TTS_FLUSH_PARTIAL_SEGMENTS` is on, or the server sits on that short chunk waiting for more text |
| One turn in a long call is randomly slow | the synthesis socket was dropped for idleness. Set `MINIMAX_TTS_KEEPALIVE_S` (the vendor sends no pings and closes at ~120 s) |
| Assistant sounds clipped or robotic across sentence boundaries | try `MINIMAX_TTS_CONTINUOUS_SOUND=true`, and expect to pay for it in TTFA |
| It mispronounces your product name every single time | `MINIMAX_TTS_PRONUNCIATION=YourName/how it should sound` |
| It interrupts itself through the speaker | raise `vad.barge_in_margin_db` (6 → 12) |
| It ignores me when I interrupt | lower `vad.barge_in_margin_db`, or `vad.barge_in_min_speech_ms` |
| `listen: this barge-in looks like the assistant's own voice returning` | the log is telling you the caller is on a speakerphone or has echo cancellation off. Raise `vad.barge_in_margin_db` (6 → 15–20) and compare `echo_floor_db` against `noise_floor_db` on the `utterance opened` lines. The warning wants 8 dB of gap *and* a barge-in sitting exactly on `barge_in_min_speech_ms`; it is the coincidence that is diagnostic, not either number alone |
| Nothing I put in the backchannel list ever matches | check the `· backchannel words:` line the page logs on connect. The list is matched after normalization against the recognizer's own output, so it has to be in the language the recognizer is transcribing — a Latin letter in a list matched against Chinese partials will never fire |
| The greeting (or an answer) plays half and stops, and I said nothing | a noise opened the VAD and the recognizer found no words in it — look for a `golive.speech.stopped` with no delegation behind it, and an empty row in the transcript. `duplex.user_backchannel_hold_ms` is what defers the interrupt long enough to find that out; set it to 0 only if you want the old interrupt-on-sound behaviour |
| It stops dead when I just say "嗯" | add that word to `duplex.user_backchannel_phrases` |
| The caller asks three times and only ever hears "让我查一下" | the backend is slower than their patience: each turn is superseded before it speaks. Only one filler now fires between answers, but the fix is `llm_first_token_ms` — check `think: answered without speculating`, then the prompt length and `history_turns` |
| It said "我看一下" on the very first thing I asked | it no longer can. A filler means *this is slower than usual*, and on the first delegation of a call there is no usual — `backchannel: holding filler suppressed; this session has no answer to compare against yet` is the line to grep for. If you see it on a turn that really did stall, that is the deliberate trade: one longer silence at the start, in exchange for never opening a call with a tic |
| It says "稍等一下" before every single answer | that is the holding filler, and a filler on every turn means the backend is uniformly slow rather than occasionally slow. The engine now also requires the turn to be slow *for this session*, so this should be rare — if it is not, look at `llm_first_token_ms`: a long system prompt or a large `history_turns` is the usual cause |
| Every delegation says `final_transcript`, never `speculative` | check the `think: answered without speculating` lines; the commonest answer is `speculative_min_chars` being above the length of a short question |
| It ignores a real interruption that starts with one of those words | it shouldn't — the check runs on every partial, so "嗯，等一下" yields at 等. If it doesn't, the recognizer is not emitting partials; check for `asr: partial` lines |
| Interrupting feels a beat slower than it used to | that is `duplex.user_backchannel_hold_ms`, but only when the transcript stays ambiguous. Lower it, or empty the phrase list to switch the behaviour off |
| Audio stutters on a bad network | raise `duplex.playback_lead_ms` (300 → 600) |

Leave `duplex.playback_paced` on. Turning it off makes the service look faster
and quietly breaks truncation accounting — history starts recording sentences
nobody heard.
