# golive

A full-duplex realtime audio service in Go.

It speaks the **gpt-live-1 event protocol** over a WebSocket, and simulates
full duplex with a concurrent ASR → LLM → TTS cascade. We do not have a genuine
full-duplex speech model, so golive builds the *behaviour* out of ordinary
parts: several channels that run at the same time and never gate each other.

Out of the box it runs on mock providers with no credentials. Point it at
Tencent realtime ASR, a DeepSeek (or any OpenAI-compatible) backend and MiniMax
T2A and it is a production-shaped voice service.

```
./start.sh             # http://localhost:8080 — open it and talk
./start.sh real        # the same, against Tencent + DeepSeek + MiniMax
make demo              # drive a turn from the CLI, interrupt it, save the audio
```

`start.sh` rebuilds from source when a Go toolchain is present and otherwise
uses the prebuilt binary for your platform in `bin/`, so the project runs as
shipped on a machine with no Go installed. **[TESTING.md](TESTING.md)** is the
hands-on guide — what to try, how to read the log, what to tune when it feels
wrong — and **[BENCHMARK.md](BENCHMARK.md)** covers measuring response latency
and comparing against another stack.

---

## Why this is not just a fast cascade

A classic cascade is half duplex whether it means to be or not: it stops
listening while it speaks, because its own output would otherwise come back
through the microphone. Everything downstream of that one decision feels wrong —
you cannot interrupt, the assistant cannot acknowledge you mid-sentence, and the
turn boundary is a hard gate rather than a negotiation.

golive removes the gate and pays for it explicitly:

| Duplex behaviour | How a real model does it | How golive does it |
| --- | --- | --- |
| Listening while speaking | One model, one audio stream | Listen path never pauses; the VAD raises its threshold during playback (`barge_in_margin_db`) so echo is not mistaken for speech |
| Interrupting | The model yields the floor from prosody | Energy VAD + minimum-speech gate opens a barge-in, a generation counter invalidates everything in flight; `duplex.on_new_query` picks between cutting mid-word, finishing the sentence, or queueing |
| Knowing what you heard | Trivially, it is one process | The player paces output in real time and reports `played_ms` plus the exact spoken prefix |
| Answering fast | No cascade to wait on | Speculative turns start from a stable partial transcript; a tiny first TTS segment gets a syllable out early |
| "Mm-hmm" while you talk | Natural | A backchannel channel that synthesizes short phrases during your turn |
| Hard reasoning | Delegates to a backend model | The same delegation split, in both `responses` and `client` modes |

The honest gap: floor control here is thresholds and timers, not acoustics. That
shows up as occasional late barge-in detection and speculation that sometimes
guesses wrong — both of which cost a cancelled generation, never a wrong answer.

---

## Architecture

```
                        ┌──────────── session (WebSocket) ────────────┐
   browser / SIP ──────▶│  session.start, session.input_audio.append  │
                        └───────────────────┬─────────────────────────┘
                                            ▼
   ╔═══════════════════════════ duplex engine ════════════════════════════╗
   ║                                                                      ║
   ║  LISTEN ──▶ resample 16k ──▶ VAD ──┬──▶ TRANSCRIBE (streaming ASR)   ║
   ║   (never gated by playback)        │         │ partial / final       ║
   ║                                    │         ▼                       ║
   ║                                    │    turn tracker ──▶ THINK       ║
   ║                                    │    (speculative)     (LLM or    ║
   ║                                    │                   delegation)   ║
   ║                                    │                      │ deltas   ║
   ║                                    │                      ▼          ║
   ║                                    │              sentence segmenter ║
   ║                                    │                      │          ║
   ║                                    │                      ▼          ║
   ║                                    │                 SPEAK (TTS)     ║
   ║                                    │                      │          ║
   ║                                    ▼                      ▼          ║
   ║                             BACKCHANNEL ──────▶ paced player ────────╫──▶ session.output_audio.delta
   ║                                                   (played_ms,        ║
   ║                                                    truncation)       ║
   ╚══════════════════════════════════════════════════════════════════════╝
                    generation counter cancels every box at once
```

Package map:

| Package | Responsibility |
| --- | --- |
| `internal/live` | gpt-live-1 event types, session config, extension events |
| `internal/server` | WebSocket session state machine, HTTP routes, demo hosting |
| `internal/duplex` | The engine: turn tracking, generation/cancel scope, paced player, backchannel |
| `internal/audio` | PCM conversion, resampling, framing, energy VAD, WAV |
| `internal/segment` | LLM text → speakable segments (CJK + Latin aware) |
| `internal/provider` | ASR / LLM / TTS contracts and registry |
| `internal/provider/{tencent,deepseek,minimax,mock}` | Adapters |
| `cmd/golive` | The service |
| `cmd/golivectl` | CLI harness: stream audio in, save audio out, interrupt on cue |
| `cmd/golivebench` | A/B latency harness; drives this service and an OpenAI Realtime one |
| `web/` | The browser demo, served at `/` — a single self-contained page |
| `configs/` | JSON profiles: `mock.json` (no credentials) and the real stack |
| `bin/` | Prebuilt binaries for shipping (gitignored; `make dist` fills it) |

---

## Protocol

Endpoint: `ws://host:8080/v1/live`. Every message is one JSON event with a
`type`. The names and lifecycle follow OpenAI's GPT-Live guides, so a client
written for `gpt-live-1` works unchanged.

**Client → service**

| Event | Notes |
| --- | --- |
| `session.start` | Once per connection. Sets model, instructions, greeting, audio format, delegation mode. |
| `session.update` | Only `delegation.responses` may change. Model, voice, audio format and delegation type are fixed at startup. |
| `session.input_audio.append` | `audio` is base64 PCM16 at the session rate. Binary frames are also accepted as a shortcut. |
| `session.input_audio.mute` / `.unmute` | Stops consuming audio without tearing the listen channel down. |
| `session.instructions.append` | Trusted system-level guidance mid-session. |
| `session.thinking.append` | Backend context that informs the conversation but is never spoken. |
| `session.commentary.append` | Text to speak. This is how client delegation answers. |
| `response.item.create` | A `function_call_output` for the backend. |
| `response.create` | Resume delegated work after tool results. |
| `session.close` | Graceful end. |

**Service → client**

| Event | Notes |
| --- | --- |
| `session.started` / `session.updated` | Echoes the resolved session object. |
| `session.input_transcript.delta` | Fragments with `start_ms` / `end_ms`; `final` closes the row. |
| `session.output_transcript.delta` | What the assistant is saying. |
| `session.output_audio.delta` | Base64 PCM16. There is deliberately **no** "done" event — generation end and playback end are different moments. |
| `session.delegation.created` | Backend work is needed; `target` is `client` or `responses`. |
| `response.event` | A Responses-backend event wrapped with its `delegation_id`. |
| `session.thinking.appended` / `commentary.appended` / `instructions.appended` | Acknowledgements. |
| `session.usage.updated` | Cumulative voice seconds and backend tokens. |
| `session.closed` | `close_requested`, `expired`, `remote_hangup`, `connection_lost`. |
| `error` | `client_event_id` is set when a specific command was rejected. |

**golive extensions** (namespaced; ignore them and the session still works)

| Event | Why it exists |
| --- | --- |
| `golive.channel.state` | Which of listen / transcribe / think / speak are live *right now* — the simulation made visible. |
| `golive.speech.started` / `.stopped` | VAD turn boundaries, with a `barge_in` flag. |
| `golive.output_audio.truncated` | `played_ms`, `total_ms` and the exact text the listener heard before the cut. |
| `golive.backchannel` | A short acknowledgement was spoken during your turn. |
| `golive.turn.metrics` | Per-stage latency for one turn, every stage measured from VAD close — including `first_audio_out_ms`, the only latency a caller experiences. |

Two protocol details are worth calling out because they are easy to get wrong:

* **`session.input_transcript.delta` carries `replace`.** Streaming recognizers
  revise words they already emitted. Extending hypotheses arrive as fragments;
  a correction, and the final result, arrive as the whole row with `replace`
  set. A client that ignores the field still converges.
* **There is no audio-done event.** Track playback state client-side. golive
  tracks the server-side half itself, which is what makes truncation honest.

### Minimal client

```js
const ws = new WebSocket('ws://localhost:8080/v1/live');
ws.onopen = () => ws.send(JSON.stringify({
  type: 'session.start',
  session: {
    audio: { format: { type: 'audio/pcm', rate: 24000 } },
    delegation: { type: 'responses' },
  },
}));
ws.onmessage = (e) => {
  const ev = JSON.parse(e.data);
  if (ev.type === 'session.output_audio.delta') play(atob(ev.audio));
  if (ev.type === 'golive.output_audio.truncated') stopPlaybackNow();
};
// then stream base64 PCM16 with { type: 'session.input_audio.append', audio }
```

---

## Delegation

The gpt-live-1 split — a light conversational layer in front of a heavier
backend — is the part worth keeping even in a simulation, because it is what
lets the voice layer stay responsive while real work happens elsewhere.

**`responses`** (default): golive runs the backend itself against the configured
OpenAI-compatible endpoint. Tool calls are surfaced to the client wrapped in
`response.event`; answer with `response.item.create` then `response.create`.

**`client`**: golive announces `session.delegation.created` with
`target: "client"` and then says nothing on its own. Your application does the
work and replies with:

```json
{"type":"session.commentary.append","delegation_id":"item_3.0","content":"已经帮你订好了，周四下午两点。"}
```

…or, for progress that should inform but not interrupt:

```json
{"type":"session.thinking.append","delegation_id":"item_3.0","content":"正在查周四的空档，还没有下单。"}
```

There is no cancellation event, by design. Track active delegations by
`delegation_id`, ignore late results, and check outcomes before repeating an
action — interrupting speech does not cancel backend work.

---

## Configuration

Three layers, in order: compiled defaults → JSON profile → environment.
Credentials live **only** in the environment, which is what makes a profile
committable.

```
bin/golive -profile configs/tencent-deepseek-minimax.json -env .env.local
bin/golive -print-config            # resolved settings, then exit
```

Start from `.env.example`. The knobs that change how the thing feels:

| Setting | Effect |
| --- | --- |
| `duplex.playback_paced` | Off = burst audio at the client. Fast, but truncation accounting becomes meaningless. Leave it on. |
| `duplex.playback_lead_ms` | Jitter budget. Higher survives worse networks; lower makes cuts more precise. |
| `duplex.stream_first_chunk_chars` | Smaller = earlier first syllable, slightly worse prosody. `8` is a good default for Chinese. |
| `duplex.speculative_stable_ms` | How long a partial must stop changing before golive commits to guessing. Lower = faster and more wasted generations. |
| `instructions` / `greeting` | The conversational prompt, and what the assistant says unprompted when a session opens. Both overridable per session; the demo page exposes them under **Session config**. |
| `duplex.on_new_query` | `cut`, `finish_sentence` or `queue` — what happens to an answer still in flight when the caller speaks again. |
| `vad.barge_in_margin_db` | Raise if the assistant interrupts itself through a speakerphone. Browsers with AEC need very little. |
| `vad.min_silence_ms` | How long a pause must be before the turn is considered over. The single biggest lever on "it cuts me off". |

---

## Providers

Adapters register themselves by name; a factory only runs when a session asks
for it, so an unconfigured vendor is not a start-up failure — but the selected
ones are constructed once at boot so a missing credential fails immediately,
with the variable's name.

| Name | Kind | Credentials |
| --- | --- | --- |
| `tencent` | ASR | `TENCENT_ASR_APP_ID`, `TENCENT_ASR_SECRET_ID`, `TENCENT_ASR_SECRET_KEY` |
| `deepseek` | LLM | `DEEPSEEK_API_KEY` (+ `GOLIVE_BACKEND_BASE_URL`, `GOLIVE_BACKEND_MODEL`) |
| `openai-compatible` | LLM | `OPENAI_API_KEY`, same overrides |
| `minimax` | TTS | `MINIMAX_TTS_API_KEY`, `MINIMAX_TTS_VOICE_ID` |
| `mock` | all three | none |

Adding one is a file and an `init()`:

```go
func init() {
    provider.RegisterTTS("myvendor", func() (provider.TTS, error) { return New() })
}
```

Implement `Open` returning a stream. Keep the connection open across segments —
on a conversational cadence, connection setup dominates time-to-first-audio.

---

## Testing

```
make test     # unit + wire-level integration, all on mock providers
make race     # the same under -race; the engine is heavily concurrent
make dist     # cross-compile bin/ for darwin-arm64, darwin-amd64, linux-amd64
```

[TESTING.md](TESTING.md) covers driving it by hand. Below is what the automated
suite pins down.

What the suite actually pins down, beyond "it compiles":

* the VAD opens on speech, tolerates the gaps between syllables, ignores a
  60 ms click, and rejects leaked playback while raising a real interruption;
* VAD snapshots concatenate to exactly the utterance, so a streaming recognizer
  and a batch one see the same audio;
* the segmenter never splits `3.5` or `Dr.`, always reassembles to its input,
  and still flushes text that contains no punctuation at all;
* the Tencent signature is recomputed independently from the emitted URL;
* over a real WebSocket: a full turn produces transcript, delegation and audio;
  events before `session.start` are rejected by name; immutable fields are
  refused; `session.close` is acknowledged;
* a barge-in produces a truncation whose `played_ms` never exceeds what was
  emitted and whose text is a strict prefix of the reply.

The CLI harness is the fastest way to inspect timing by hand:

```
bin/golivectl -speak-ms 2200 -barge-in-at 1400 -record session.wav -v
```

`session.wav` is stereo: your input on the left, the assistant on the right —
the same layout gpt-live-1 uses for stored sessions, so overlap is visible in
any waveform viewer.

---

## Operational notes

* **Build environments without a module proxy.** `GOPROXY=direct GOSUMDB=off`
  fetches straight from the source hosts. The Makefile and `start.sh` set both.
* **Shipping it.** `make dist` fills `bin/` with binaries for macOS (both
  architectures) and Linux amd64; the whole directory then runs anywhere those
  platforms are, with or without Go.
* **Put an authenticating proxy in front of it.** The WebSocket accepts any
  origin, because a voice session carries no ambient credentials and the demo
  page needs it. That is a deliberate choice, not an oversight to inherit.
* **One session, one engine, ~8 goroutines.** Cost is dominated by provider
  sockets, not CPU. The VAD is energy-based on purpose: a neural detector per
  concurrent session is a different machine.
* **Backpressure.** A client that cannot drain its socket is disconnected rather
  than allowed to stall the engine — on a realtime audio path, queueing only
  makes the lag worse.

## License

MIT.
