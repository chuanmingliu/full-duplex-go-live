# golive

A full-duplex realtime audio service in Go.

For the inbound simulated-customer project, see **[Inbound Voice User Simulator](README.simulator.md)**: a receiving voice agent, configuration workbench, scenario suites and independent post-call evaluation.

It speaks the **gpt-live-1 event protocol** over a WebSocket, and simulates
full duplex with a concurrent ASR → LLM → TTS cascade. We do not have a genuine
full-duplex speech model, so golive builds the *behaviour* out of ordinary
parts: several channels that run at the same time and never gate each other. A
client written for gpt-live-1 — continuous audio in, no turn commands, the
service decides when to speak — is the client this is built to serve.

Out of the box it runs on mock providers with no credentials. Point it at
Tencent realtime ASR, a DeepSeek (or any OpenAI-compatible) backend and MiniMax
T2A and it is a production-shaped voice service.

```
./start.sh             # http://localhost:8080 — open it and talk
./start.sh real        # the same, against Tencent + DeepSeek + MiniMax
./start.sh prod        # auth required, json logs, no demo page
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
| Answering fast | No cascade to wait on | The backend starts on a guess during the pause and is dropped if the caller carries on; a tiny first TTS segment gets a syllable out early; fixed phrases play from a cache |
| Knowing you are not finished | It hears you trailing off | The words so far are read as well as the silence: "我想问一下…" is given longer before the turn is taken |
| "Mm-hmm" while you talk | Natural | A backchannel channel that synthesizes short phrases during your turn |
| *You* say "mm-hmm" while it talks | Natural — it keeps going, having learned that a murmur is not a request to stop | It cannot hear the difference, so it is given a list: `duplex.user_backchannel_phrases`. Hearing one keeps the floor; the first syllable that rules them all out yields it |
| Hard reasoning | Delegates to a backend model | The same delegation split, in both `responses` and `client` modes |

The honest gap: floor control here is thresholds, timers and a word list, not
acoustics. A real model hears prosody — it knows "对" and "对?" are different
acts, and that you are winding down before you stop. That shows up as occasional
late barge-in detection, speculation that sometimes guesses wrong, and a
backchannel list that judges by spelling. All of them cost a cancelled
generation or a beat of delay, never a wrong answer.

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
| `internal/live` | gpt-live-1 event types, session config, extension events, Responses → chat tool translation |
| `internal/server` | WebSocket session state machine, sidebands, fork, HTTP routes, auth, admission, demo hosting |
| `internal/duplex` | The engine: turn tracking and speculation (`listen.go`), backend and delegation (`think.go`), history, paced player, phrase cache, backchannel |
| `internal/endpoint` | Reads a partial transcript as finished, unfinished or neither |
| `internal/store` | Sessions started with `store: true`: recording and conversation |
| `internal/metrics` | Process-wide Prometheus text (no client library) |
| `internal/audio` | PCM conversion, G.711, resampling, framing, energy VAD, WAV |
| `internal/segment` | LLM text → speakable segments (CJK + Latin aware) |
| `internal/provider` | ASR / LLM / TTS contracts and registry |
| `internal/provider/{tencent,deepseek,minimax,mock}` | Adapters |
| `cmd/golive` | The service |
| `cmd/golivectl` | CLI harness: stream audio in, save audio out, interrupt on cue |
| `cmd/golivebench` | A/B latency harness; drives this service and an OpenAI Realtime one |
| `web/` | The browser demo, served at `/` — a single self-contained page |
| `configs/` | JSON profiles: `mock.json` (no credentials), `sim.json` + `sim.env` (mocks with realistic stage latencies), the real stack, and `telephony.json` (the real stack behind a SIP gateway) |
| `bin/` | Prebuilt binaries for shipping (gitignored; `make dist` fills it) |

---

## What it takes to pass for gpt-live-1

gpt-live-1 is one model: it hears and speaks on the same stream, so "when is it
my turn" is something it simply knows. golive has a recognizer, a chat model and
a synthesizer, none of which knows the other two exist. The protocol is the same
either way, and the protocol makes promises — audio is sent continuously and the
service decides when to speak; a transcript fragment is never taken back; a
delegation, once announced, is real. Each of those is free for a single model
and has to be built for a cascade.

| What a gpt-live-1 client relies on | Why a cascade does not get it for free | What golive does |
| --- | --- | --- |
| No turn commands. Audio in, audio out, the service decides. | Somebody has to decide the caller has finished, and a timer alone is either slow or rude. | VAD silence, read together with the words so far: a pause after "我想问一下" is given longer (`duplex.patient_silence_ms`); a finished question starts the backend sooner. |
| It answers as soon as you stop. | The backend's time to first token starts when it is called, and it cannot be called before the turn is over — unless you guess. | The backend and the synthesizer are started on a guess, in the pause, and everything they produce — events and audio — is **held** until the turn is confirmed. If the caller carries on, the guess is dropped without a trace and another is made at the next pause. |
| `session.input_transcript.delta` is append-only. | A streaming recognizer revises words it already emitted. | Only text two hypotheses agree on is sent as a delta. The live, revisable hypothesis is a separate extension event. |
| `session.output_transcript.delta` is what was said. | The chat model drafts text well ahead of the audio. | Each sentence is reported when its first audio is played, so an interrupted reply leaves its unspoken remainder out. |
| `session.delegation.created` is a commitment; there is no cancel. | Speculation makes work that may be thrown away. | A delegation — and any function call — is announced only once the turn is certain. Before that, `golive.delegation.speculative` offers the head start to applications that want it. |
| The conversational model chats while the backend works, and results are spoken when they fit. | A cascade has one thing to say at a time. | Function calls do not block the conversation; a result that arrives while the caller is talking waits for them to stop. |
| Sidebands, stored sessions, forks, G.711. | Plumbing. | All there: `/attach`, `store: true` + `/content`, `/fork`, `audio/pcmu` and `audio/pcma`. |

What it cannot do is listed under [Differences from gpt-live-1](#differences-from-gpt-live-1).

---

## Protocol

Endpoint: `ws://host:8080/v1/live/sessions` (`/v1/live` is the same thing, kept
for clients written before the rename). Every message is one JSON event with a
`type`. Names, fields and lifecycle follow OpenAI's
[GPT-Live guides](https://developers.openai.com/api/docs/guides/live), and the
suite checks them as raw JSON rather than through golive's own structs.

| Route | What it is |
| --- | --- |
| `WS /v1/live/sessions` | A session. Send `session.start` first. |
| `WS /v1/live/sessions/{id}/attach` | A sideband on a running session: receives everything, including the caller's audio as reflected `session.input_audio.append`; may send any command except audio, and does not send `session.start`. At most four per session. |
| `WS /v1/live/sessions/{id}/fork` | A new session that starts from a stored one's conversation. Send `session.start`; it may set `store`, `audio.format` and `delegation.responses`, and inherits the rest. |
| `GET /v1/live/sessions/{id}/content` | A stored session's recording: stereo WAV, caller left, assistant right. `409` while the session is still running. |

`/fork` and `/content` need `store_dir` set on the server and `store: true` on
the session.

**Client → service**

| Event | Notes |
| --- | --- |
| `session.start` | Once per connection. Model, instructions, voice, `audio.format`, delegation. Immutable afterwards. |
| `session.update` | Only `delegation.responses` may change, and omitted settings keep their values. Anything else is `immutable_field_update`. |
| `session.input_audio.append` | `audio`: base64 in the session format. Send continuously; there is nothing to commit. Binary frames are accepted as a shortcut. |
| `session.input_audio.mute` / `.unmute` | Acknowledged with `session.input_audio.muted` / `.unmuted`. |
| `session.instructions.append` | Trusted system-level guidance mid-session. |
| `session.thinking.append` | Context for a delegation that informs the conversation and is not spoken. |
| `session.commentary.append` | Text to speak for a delegation. This is how client delegation answers. |
| `response.item.create` | A `function_call_output`, or a `message` with `input_text` parts — typed input. |
| `response.create` | Continue the backend with the function results, or answer the typed input. |
| `session.close` | Graceful end. |

**Service → client.** Every event carries its own `event_id`; an event that
answers a command carries that command's `event_id` as `client_event_id`.

| Event | Notes |
| --- | --- |
| `session.started` / `session.updated` | The resolved session object. |
| `session.input_transcript.delta` | `delta`, `start_ms`, `end_ms`. Append-only. |
| `session.output_transcript.delta` | `delta`, `start_ms`, `end_ms`. One sentence at a time, as it starts to play. |
| `session.output_audio.delta` | `delta` is base64 audio; `start_ms` / `end_ms` place it on the session clock. There is no "done" event. |
| `session.delegation.created` | `offset_ms` and `delegation: {id, type: "delegation", target, response_id}`. |
| `response.event` | `delegation_id` and `event`: the backend's stream in Responses form — `response.created`, `response.output_text.delta`, `response.output_item.done`, `response.completed` / `response.failed`. A function to run is an `output_item.done` whose item is a `function_call`. |
| `session.thinking.appended` / `commentary.appended` / `instructions.appended` | Acknowledgements, with `delegation_id`. |
| `session.input_audio.muted` / `.unmuted` | Acknowledgements. |
| `session.usage.updated` | `usage.seconds` and `context_window.usage_ratio`; golive adds audio seconds and backend tokens, cached ones included. |
| `session.closed` | `reason`, final `usage`, and the `session` object. Reasons: `close_requested`, `expired`, `remote_hangup`, `connection_lost`, `server_shutdown`, `rate_limited`. |
| `error` | `error: {type, code, message, client_event_id}`. |

**Audio formats** (`session.audio.format`): `audio/pcm` at 24000 or 16000 Hz, as
gpt-live-1, plus 8000 and 48000; `audio/pcmu` and `audio/pcma`, which are G.711
and always 8000 Hz. A telephony leg can be bridged with no transcoding on either
side.

**golive extensions** (namespaced; ignore them and the session still works)

| Event | Why it exists |
| --- | --- |
| `golive.input_transcript.updated` | The recognizer's whole current hypothesis for an utterance, corrections included: `item_id`, `text`, `final`. Replace, do not append. For captions that keep up. |
| `golive.delegation.speculative` | Work has started on a guess at the caller's turn. Retractable — if no `session.delegation.created` with the same id follows, it was dropped. |
| `golive.channel.state` | Which of listen / transcribe / think / speak are live *right now* — the simulation made visible. |
| `golive.speech.started` / `.stopped` | VAD turn boundaries, with a `barge_in` flag. |
| `golive.output_audio.truncated` | `played_ms`, `total_ms` and the exact text the listener heard before the cut. |
| `golive.backchannel` | A short acknowledgement was spoken during your turn. |
| `golive.input_backchannel` | *You* said something that was heard, recognised as an acknowledgement, and deliberately not treated as an interruption. |
| `golive.turn.metrics` | Per-stage latency for one turn, every stage measured from VAD close — including `first_audio_out_ms`, the only latency a caller experiences. |

Transcript and audio events also carry `item_id`, which groups fragments into
turns for a client that would rather not do it from timestamps.

### Minimal client

```js
const ws = new WebSocket('ws://localhost:8080/v1/live/sessions');
ws.onopen = () => ws.send(JSON.stringify({
  type: 'session.start',
  session: {
    model: 'gpt-live-1',
    audio: { format: { type: 'audio/pcm', rate: 24000 }, output: { voice: 'marin' } },
    delegation: { type: 'responses' },
  },
}));
ws.onmessage = (e) => {
  const ev = JSON.parse(e.data);
  if (ev.type === 'session.output_audio.delta') play(atob(ev.delta));
  if (ev.type === 'golive.output_audio.truncated') stopPlaybackNow();
};
// then stream base64 PCM16 with { type: 'session.input_audio.append', audio }
```

`voice: 'marin'` works because a gpt-live-1 voice name with no entry in
`voice_aliases` falls back to the configured voice rather than being handed to a
vendor that has never heard of it. The same goes for
`delegation.responses.model`: map OpenAI model ids to your provider's in
`backend_models`, and an unmapped one uses `backend_model`.

### On a phone line: `/v1/realtime`

A SIP media gateway terminates the call and reaches its voice agent over a
WebSocket in the one protocol it knows, which is OpenAI's Realtime API, not
gpt-live-1. `ws://host/v1/realtime` is the same session and the same engine in
that protocol, so golive can be the voice on a telephone line with nothing in
between. It was written against [Active Call](https://github.com/miuda-ai/active-call)'s
realtime client and carries the subset a gateway uses:

| Direction | Events |
| --- | --- |
| in | `session.update` (instructions, voice, tools — starts the session), `input_audio_buffer.append`, `response.cancel`, `conversation.item.create`, `response.create` |
| out | `session.created`, `session.updated`, `response.created`, `response.audio.delta`, `response.audio_transcript.delta`, `response.done`, `input_audio_buffer.speech_started` / `speech_stopped`, `conversation.item.input_audio_transcription.delta` / `.completed`, `response.function_call_arguments.done`, `error` |

Three things about it are specific to telephony:

* **The audio rate is the gateway's, and it does not say what that is.** "pcm16"
  means 24 kHz to OpenAI and 16 kHz to Active Call. `realtime.sample_rate` sets
  it; `?rate=` on the URL overrides it for one connection, as does the newer
  `session.audio.input.format`. `g711_ulaw` and `g711_alaw` work as named.
* **Barge-in stays the engine's decision.** A gateway flushes its playback the
  instant it is told `input_audio_buffer.speech_started`. golive sends that when
  the caller speaks into silence, and otherwise only once it has actually
  yielded the floor — so "嗯" over the assistant does not cut it off — and
  ignores the `response.cancel` the gateway sends straight back.
* **An outbound call connects its agent before anyone answers.** With
  `realtime.hold_for_opening`, a new session is deaf and silent until it is
  released:

  ```bash
  # on dial: synthesize the opening line while the phone rings (poll until ready)
  curl -s localhost:8765/v1/opening -d '{"greeting":"您好，我是小翠儿。","warm":true}'
  # on answer: release every waiting session and say it, from the cache
  curl -s localhost:8765/v1/opening -d '{"greeting":"您好，我是小翠儿。","warm":false}'
  ```

`GET /v1/pool` lists the sessions in progress, which is how a monitor finds one
to attach to: a sideband on a Realtime session still speaks gpt-live-1, so a
phone call can be watched — transcripts, both directions of audio,
`golive.turn.metrics` — without being in its audio path.

`./start.sh telephony` runs this arrangement: `configs/telephony.json`, bound to
`127.0.0.1:8765`, where a gateway on the same host expects its agent. With
`telemetry_url` set, each turn's boundaries (speech stop, final transcript,
first token, first audio) are posted to an external latency console as they
happen.

### Differences from gpt-live-1

* **WebSocket only.** No WebRTC and no SIP; `POST /v1/live/sessions` answers
  501. Bridge telephony through the WebSocket with G.711.
* **Function tools only.** Hosted tools (`web_search`, `file_search`, MCP) need a
  backend that runs them. They are dropped at `session.start` and reported with
  an `unsupported_tool` error rather than ignored quietly.
* **Text and audio only.** No image input.
* **Field names changed in this version.** Output audio moved from `audio` to
  `delta`, and transcripts from `content` + `replace` to append-only `delta`.
  `duplex.legacy_audio_field: true` also sends audio under the old key while a
  client is being updated.
* **One credential, no per-session scoping.** Anyone holding the server's
  token who learns a session id can attach to that session, fork it or download
  it — as with an API key. Session ids are random and are only ever sent to the
  session's own connections; treat them accordingly, and put tenant isolation
  in whatever issues your callers their tokens.
* **It is a cascade.** Floor control is thresholds, timers and word lists; see
  the gap described above.

---

## Delegation

The gpt-live-1 split — a light conversational layer in front of a heavier
backend — is the part worth keeping even in a simulation, because it is what
lets the voice layer stay responsive while real work happens elsewhere.

### `responses`

golive runs the backend itself, against the configured OpenAI-compatible
endpoint, with the settings in `delegation.responses`: `instructions`, `tools`,
`tool_choice`, `parallel_tool_calls`, `max_output_tokens`, `model`. Tools are
written the Responses way, flat:

```json
{"type":"session.start","session":{"delegation":{"type":"responses","responses":{
  "model":"gpt-5.5",
  "instructions":"只回答保单问题。",
  "tools":[{"type":"function","name":"lookup_policy",
            "parameters":{"type":"object","properties":{"id":{"type":"string"}}}}]}}}}
```

When the backend calls one, the call arrives inside a `response.event`:

```json
{"type":"response.event","delegation_id":"item_3.0","event":{"type":"response.output_item.done",
  "item":{"type":"function_call","call_id":"call_1","name":"lookup_policy","arguments":"{\"id\":\"A17\"}"}}}
```

Run it and send the result back, then ask for the response to continue:

```json
{"type":"response.item.create","item":{"type":"function_call_output","call_id":"call_1","output":"有效期到明年三月"}}
{"type":"response.create"}
```

The conversation does not stop while the function runs. Whatever the model said
alongside the call ("好的，我查一下") is spoken; the caller can go on talking and
be answered; and when the result arrives it is read back at the next moment
that would not talk over them. `response.create` before every call has its
output is an error, `tool_outputs_missing`. A function output is sent to the
backend whole and is limited to 16,000 characters: return what the model needs
to answer, not the record.

### `client`

Your application is the backend. golive announces work with
`session.delegation.created` (`target: "client"`), and the application replies:

```json
{"type":"session.commentary.append","delegation_id":"item_3.0","content":"已经帮你订好了，周四下午两点。"}
```

…or, for progress that should inform but not be spoken:

```json
{"type":"session.thinking.append","delegation_id":"item_3.0","content":"正在查周四的空档，还没有下单。"}
```

Who decides that a turn needs the application is `duplex.client_delegation`
(or `golive.client_delegation` on `session.start`):

| Mode | Behaviour | Cost |
| --- | --- | --- |
| `always` (default) | Every caller turn is delegated; golive says nothing of its own. | No model call in front of your application: the fast one. |
| `auto` | The configured backend plays gpt-live-1's conversational model. It answers small talk itself and calls a built-in `delegate` function — announced to you as a delegation with the model's summary in `delegation.request` — when the request needs real work. | Closer to gpt-live-1; one model call slower on every delegated turn. |

There is no cancellation event, by design. Track active delegations by id,
ignore late results, and check outcomes before repeating an action —
interrupting speech does not cancel backend work. An append naming a delegation
that was never announced is refused with `unknown_delegation_id`. Commentary is
spoken in the order it arrives, one at a time, and at most 32 may be waiting.

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
| `duplex.speculative_stable_ms` | How long the caller must be audibly quiet before golive starts the backend early. Measured from the first frame of the pause, so the head start is `vad.min_silence_ms` minus this. Lower = faster and more abandoned generations. A guess costs backend tokens and nothing else: its events are held until the turn is confirmed. |
| `duplex.speculative_eager_ms` | Replaces the above when the words so far read as a finished thought — a question, a closed reply. `0` disables. |
| `duplex.speculative_settle_ms` | How long the transcript must have been still before a guess, when it changed during the pause. Raise it if the log shows guesses abandoned because the transcript moved on. |
| `duplex.semantic_endpointing` / `duplex.patient_silence_ms` | Read the partial transcript as well as the silence. A pause after something nobody stops on is given `patient_silence_ms` instead of `vad.min_silence_ms`. It only ever lengthens the wait. |
| `duplex.client_delegation` | `always` or `auto` — who decides a turn needs the application, in client delegation. See Delegation. |
| `duplex.asr_predial` | Open the recognizer's connection when speech is first suspected rather than confirmed, so the handshake overlaps the detector instead of following it. |
| `duplex.tts_native_rate` | Ask the synthesizer for the session's own sample rate instead of resampling from 16 kHz. |
| `duplex.phrase_cache` | Keep synthesized audio for the greeting, backchannels and holding fillers, and warm it at boot. |
| `duplex.backend_prefix_warm` | Send the backend a one-token request with the conversation so far as the caller starts speaking, to load a provider's prefix cache. Off: it spends tokens, and whether it buys anything depends on the provider. Measure first. |
| `history_max_chars` / `history_turns` | Bound the prompt. History is trimmed in steps rather than to the limit on every turn, so the front of the prompt stays byte-identical across requests and a prefix cache can hit. |
| `voice_aliases` / `backend_models` | Map the names a gpt-live-1 client sends (`marin`, `gpt-5.5`) to your providers' own. |
| `realtime.sample_rate` / `realtime.dialect` / `realtime.hold_for_opening` | The OpenAI-Realtime-compatible endpoint: the rate "pcm16" means on it (24000; 16000 for Active Call), `beta` or `ga` event names for assistant audio, and whether a session waits for `POST /v1/opening` before it listens. |
| `telemetry_url` | Where to post each turn's boundary events for an external latency console. Asynchronous and lossy by design. |
| `store_dir` / `store_ttl_hours` | Where `store: true` sessions are kept, and for how long. Unset disables storage, fork and `/content`. |
| `duplex.legacy_audio_field` | Also send output audio under `audio`, for clients written before it moved to `delta`. |
| `instructions` / `greeting` | The conversational prompt, and what the assistant says unprompted when a session opens. Both overridable per session; the demo page exposes them under **Session config**. |
| `duplex.reset_tts_on_interrupt` | Drop the synthesis connection after every interruption. Only needed for a provider that cannot resynchronize an abandoned stream; see TESTING.md. |
| `duplex.on_new_query` | `cut`, `finish_sentence` or `queue` — what happens to an answer still in flight when the caller speaks again. |
| `duplex.user_backchannel_phrases` | Things the caller can say without taking the floor — "嗯", "对", "uh huh". Hearing one keeps the answer running. Every entry is a phrase the caller can no longer interrupt with, so keep it to noises. Empty restores plain any-speech-interrupts. |
| `duplex.user_backchannel_hold_ms` | How long the assistant may keep talking while the transcript decides what it just heard. Two questions, not one: whether it was an acknowledgement, and whether it was speech at all. An utterance the recognizer finds no words in — a cough, a door, the assistant's own voice through a speaker — never takes the floor, which is only possible because the interrupt is deferred rather than performed. Applies with no phrase list configured. Zero restores interrupt-on-sound. |
| `vad.barge_in_margin_db` | Raise if the assistant interrupts itself through a speakerphone. Browsers with AEC need very little. |
| `vad.min_silence_ms` | How long a pause must be before the turn is considered over. The single biggest lever on "it cuts me off". |
| `vad.snapshot_ms` | How often an open utterance's audio is handed to the recognizer. Every hypothesis runs this far behind the speaker, so it is latency: `40`. |

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

### Making the MiniMax voice sound less like a machine

Four settings do most of the work, all in `.env.example`:

* **`MINIMAX_TTS_PRONUNCIATION`** — the highest-value one for most deployments.
  An agent says the same handful of proper nouns, product names and
  initialisms hundreds of times a day, and mispronouncing them is the most
  noticeable unnaturalness there is. Comma-separated `written/spoken` pairs.
* **`MINIMAX_TTS_EMOTION`** — leave it *unset*. MiniMax then picks per
  sentence, which reads as more natural across a call than pinning every
  sentence to one mood. Set it only for an agent with a fixed register.
* **`MINIMAX_TTS_CONTINUOUS_SOUND`** — the one real fluency-versus-latency
  dial. golive cuts a deliberately tiny first segment to get a syllable out
  early; continuous inference carries prosody across that seam so the sentence
  sounds like one sentence, and costs latency because segments can no longer
  synthesize concurrently. Off by default — answering late sounds worse than a
  slightly flat first phrase. `speech-2.8` only.
* **Paralinguistic tags** — `speech-2.8` speaks `(laughs)`, `(sighs)`,
  `(breath)` and similar, and `<#0.4#>` inserts a pause in seconds. These go in
  the text, so they belong in `instructions`, not here. Use them sparingly:
  a laugh in the wrong place is far more jarring than no laugh at all.

Two more are latency, not naturalness, and are on by default:
`MINIMAX_TTS_CANCEL_ON_ABANDON` cancels an interrupted synthesis rather than
draining it, and `MINIMAX_TTS_FLUSH_PARTIAL_SEGMENTS` stops the server sitting
on a segment that has no sentence-ending punctuation — which is exactly the
short first chunk and the holding fillers.

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
make dist     # cross-compile bin/ for darwin-arm64, darwin-amd64, linux-amd64, linux-arm64
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
* the wire format, read as raw JSON with gpt-live-1's field names: `delta`,
  `start_ms`, `end_ms`, `client_event_id`, the nested delegation and error
  objects, `session.closed` with usage — and nothing from the turn-based
  protocol;
* a guess made in a mid-sentence pause is abandoned when the caller carries on,
  leaves no delegation and no function call behind, and a second guess is made
  at the real end of the turn;
* a function call round trip produces a conversation every chat backend accepts
  — the call, then its result, with the question still in front of them — and
  the caller can be answered while a function is still running;
* the caller's transcript is append-only even when the recognizer corrects
  itself, and the assistant's holds only what was spoken;
* a G.711 session, a sideband that steers a call it does not carry, and a stored
  session that is downloaded and forked;
* a phone call as a media gateway conducts it, in the OpenAI Realtime protocol:
  ringback ignored until the call is answered, the opening line played from
  the cache at pickup, the gateway told to stop playing only when the engine
  has yielded, and each turn's boundaries delivered to a latency console;
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

* **Production profile.** `configs/production.json` turns auth on, serves no
  demo page, logs JSON, caps concurrent sessions, and refuses client provider
  overrides. Boot it with `GOLIVE_AUTH_TOKEN` set:

  ```
  ./start.sh prod
  # or
  docker compose up --build
  ```

  Endpoints: `/livez` (liveness), `/readyz` (admission), `/metrics`
  (Prometheus; authenticated when a token is set unless `metrics_public` is
  on), `ws://host/v1/live/sessions`.

* **Stored sessions.** `store_dir` holds one directory per `store: true`
  session: two raw PCM tracks and a JSON record of the conversation, removed
  after `store_ttl_hours`. It is audio and transcripts of your callers, written
  unencrypted with mode 0700 — put it on a volume you would put call
  recordings on, and size it: there is a per-session cap but no total quota. `/attach`, `/fork` and `/content` take the same credentials as
  a session.

* **Auth.** `GOLIVE_AUTH_TOKEN` is required by the production profile. Clients
  send `Authorization: Bearer …` or, for browsers, `?token=`. golivectl and
  golivebench take `-token`. `GOLIVE_ALLOWED_ORIGINS` is the WebSocket Origin
  allow-list; with auth and no list, only same-host origins are accepted.
  Failed logins from one IP are 401 up to `auth_fail_burst` (default 30) and
  429 after that. `GOLIVE_METRICS_TOKEN` is an optional scrape credential so
  Prometheus does not have to hold the session secret.

* **Admission and drain.** `max_sessions` / `max_sessions_per_ip` refuse
  upgrades with 503 before a socket is open. SIGTERM sets `/readyz` to
  draining, sends `session.closed` with reason `server_shutdown`, and waits
  `shutdown_timeout_seconds` for the map to empty.

* **Idle and flood.** `duplex.idle_timeout_seconds` (default 300) closes a
  silent session. `duplex.max_events_per_second` and `duplex.max_audio_kbps`
  (set in the production profile) close a client that is flooding the
  socket, with reason `rate_limited`.

* **Health.** `/livez` is always 200 if the process is up. `/readyz` is 503
  while draining or at the session cap, which is what a load balancer should
  honour. `/metrics` is Prometheus text: session gauges, reject reasons,
  barge-ins, provider retries/errors, time-to-first-audio, and
  `golive_speculations_total{outcome="held"|"abandoned"}` — the ratio that says
  whether the speculation windows are set right for your callers.

* **Build environments without a module proxy.** `GOPROXY=direct` fetches
  straight from the source hosts. Checksum verification (`GOSUMDB`) stays on.
* **Shipping it.** `make dist` fills `bin/` with binaries for macOS (both
  architectures) and Linux amd64/arm64; the whole directory then runs anywhere
  those platforms are, with or without Go. `docker compose up --build` is the
  same service, non-root, read-only rootfs, healthcheck on `/livez`.
* **One session, one engine, ~8 goroutines.** Cost is dominated by provider
  sockets, not CPU. The VAD is energy-based on purpose: a neural detector per
  concurrent session is a different machine.
* **Backpressure.** A client that cannot drain its socket is disconnected rather
  than allowed to stall the engine — on a realtime audio path, queueing only
  makes the lag worse.
* **Trusting proxies.** `trust_proxy` reads `X-Forwarded-For` for the per-IP
  cap. Only turn it on behind a proxy that overwrites that header; otherwise
  a caller can spoof the address and bypass the cap.

## License

MIT.
