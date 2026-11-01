# golive-rs

A native Rust voice service alongside the Go `cmd/golive` service. It uses
Tokio tasks and bounded channels for concurrent recognition, generation, and
speech playback; it does not launch Go, call a Go backend, or use an FFI shim.

This branch starts at `telephony-realtime` commit
`ad70a1c1a8010877c3a8a130a277e8566ec14ad5`. The existing Go source, profiles,
commands, and deployment are unchanged. Rust defaults to a separate loopback
port, **8766**.

The main conversation paths are implemented and covered by local tests. This
is **not yet a complete feature-parity replacement**: the differences below
are explicit, and real vendor/phone-call latency has not been measured.

## Run

Install a current stable Rust toolchain with Cargo, rustfmt, and Clippy. From
this directory:

```sh
cargo run --locked -- --addr 127.0.0.1:8766
```

This starts deterministic mock ASR/LLM/TTS with no credentials. Mock synthesis
produces tones, not intelligible speech. For the existing browser UI, run from
the repository root so `web_root: "web"` resolves correctly:

```sh
cargo run --locked --manifest-path rust/Cargo.toml -- \
  --profile configs/mock.json --addr 127.0.0.1:8766
```

Open `http://127.0.0.1:8766`, or connect a client to
`ws://127.0.0.1:8766/v1/live/sessions` or `/v1/realtime`.

To use the real telephony provider configuration as a separate process:

```sh
cargo run --locked --release --manifest-path rust/Cargo.toml -- \
  --profile configs/telephony.json \
  --env /path/to/provider.env \
  --addr 127.0.0.1:8766
```

The explicit address keeps the existing Go service's port available. Credentials
come from the selected dotenv file and environment; process environment wins.
Supported command flags include the Go-style `-profile`, `-env`, `-addr`,
`-print-config`, and `-version`, as well as their double-dash forms.
`--print-config` redacts credential fields. Missing selected-provider credentials
fail startup with variable names rather than values.

Provider credentials use the same names as Go:

| Adapter | Environment |
| --- | --- |
| Tencent ASR | `TENCENT_ASR_APP_ID`, `TENCENT_ASR_SECRET_ID`, `TENCENT_ASR_SECRET_KEY` |
| DeepSeek | `DEEPSEEK_API_KEY` |
| MiniMax TTS | `MINIMAX_TTS_API_KEY`, `MINIMAX_TTS_VOICE_ID` |
| Fish fallback | `FISH_TTS_API_KEY`, `FISH_TTS_VOICE_ID` |
| OpenAI-compatible LLM | `OPENAI_API_KEY`, explicit provider/base URL/model configuration |

The optional target-speaker service, tool executors, and latency console remain
separate services at the URLs in the profile. The external Active Call / RustPBX
stack continues to handle SIP/RTP. Merely starting Rust does not change any
existing gateway route or deployed service.

## Implemented behavior

- Live WebSockets at `/v1/live` and `/v1/live/sessions`; Realtime beta and GA
  event dialects at `/v1/realtime`.
- PCM16 mono at 8/16/24/48 kHz and G.711 PCMU/PCMA at 8 kHz, with a 16 kHz
  internal audio pipeline.
- Streaming ASR, early finalization with continuation, semantic pauses,
  speculative response preparation gated until the caller's turn ends,
  silence-suppressed line filling, barge-in, and paced playback.
- Progressive played-text history, stale-generation cancellation, sentence
  segmentation, bounded phrase caching, and warm opening lines.
- Tencent WebSocket recognition, DeepSeek/OpenAI-compatible SSE (including
  tool calls), persistent MiniMax WebSockets, delayed Fish fallback, LLM hedge
  lanes and connection prewarming. Provider calls are cancellable.
- Client delegation and commentary queues; seeded conversation input;
  server-side HTTP tools, bounded asynchronous tools, call memory,
  client-managed function results, and guarded `end_call` after playback.
- Speaker-focus side channel with enrollment, observation/veto, echo masks,
  and caller-quiet closure. Recognizer audio stays unmodified.
- Opening prewarm/release and pool inspection, health/readiness, basic metrics,
  bearer/subprotocol authentication, origin checks, session/input limits,
  graceful shutdown, and latency-console telemetry.

## Differences still requiring a port or validation

- Durable recordings/session storage, attach/fork/content, and last-call file
  persistence are not implemented. Storage requests return explicit errors.
- Same-vendor TTS account rotation, shared RPM limits, hedging, and refusal
  cooldown are not implemented; startup warns when requested. Delayed
  cross-vendor fallback is supported. Interrupted MiniMax sessions reconnect.
- `finish_sentence` currently queues until the whole answer ends; `cut` and
  `queue` are supported. Automatic client-delegation selection, automatic
  spoken backchannels, and holding filler phrases are not implemented.
- The native pipeline remains 16 kHz; it does not request session-native TTS
  rates. Some Go latency optimizations, including speculative generation from
  stable partials and one-token backend prefix warming, are not implemented.
- Spoken-text timing uses a duration-based approximation; neither PCM audio
  nor the current provider streams supply word-level alignment.
- Go pprof and an in-process TLS listener are unavailable. Configured TLS
  files are rejected rather than silently serving plaintext; use an existing
  TLS reverse proxy when required.
- CRM, desk, journal, edge model servers, CLI probes, and packaging scripts
  remain the existing independent Go/Python components.

Local tests do not establish live-vendor correctness, phone interoperability,
or the 1200 ms p95 target. Keep the Go deployment available while comparing
both services against the same gateway and provider conditions.

## Verify

```sh
cd rust
cargo fmt --check
cargo clippy --locked --all-targets -- -D warnings
cargo test --locked
cargo build --locked --release
```

Tests use mocks and loopback HTTP/WebSocket fixtures with fake keys. They do
not place phone calls or use real provider accounts. `tests/audio_parity.rs`
checks Go-generated VAD vectors, including every decision timestamp and
snapshot/utterance sample. Other tests cover codecs, endpointing, segmentation,
provider framing/reuse/cancellation, playback history, speculative gating,
Realtime event ordering, authentication, openings, focus, telemetry, and tools.

The independent `.github/workflows/rust.yml` checks this service. The existing
Go workflow is unchanged. `Cargo.lock` is committed; build output is ignored.
