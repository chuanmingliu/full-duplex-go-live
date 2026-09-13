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

The assistant also **holds the floor while the backend works**: if a delegation
runs longer than `duplex.holding_filler_after_ms` (1500 ms) with nothing being
said, it says something short rather than going silent. Turn it off with
`holding_filler: false` — it costs one extra synthesis and can delay a slow
answer by however long the filler takes to speak.

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
| `speak:` | Per-segment synthesis time. `chars=11 ms=180` on the first segment is your real time-to-first-syllable. |
| `speak: turn truncated` | `played_ms` vs `emitted_ms`, and `heard_text` — the exact prefix that entered history. |
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
| It takes too long to start replying | lower `vad.min_silence_ms`, or lower `duplex.speculative_stable_ms` |
| First syllable is slow | lower `duplex.stream_first_chunk_chars` (8 → 5) |
| It interrupts itself through the speaker | raise `vad.barge_in_margin_db` (6 → 12) |
| It ignores me when I interrupt | lower `vad.barge_in_margin_db`, or `vad.barge_in_min_speech_ms` |
| Audio stutters on a bad network | raise `duplex.playback_lead_ms` (300 → 600) |

Leave `duplex.playback_paced` on. Turning it off makes the service look faster
and quietly breaks truncation accounting — history starts recording sentences
nobody heard.
