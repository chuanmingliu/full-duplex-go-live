# Rust implementation map

Branch: `codex/telephony-realtime-rust`.
Base: `telephony-realtime` at `ad70a1c1a8010877c3a8a130a277e8566ec14ad5`.

The Rust service is additive. No existing Go source, profile, deployment script,
or production service is replaced. It runs on loopback port 8766 by default.
See [the Rust README](../../rust/README.md) for launch commands, verification,
and explicit compatibility differences.

| Existing Go responsibility | Native Rust implementation |
| --- | --- |
| `cmd/golive`, `internal/config` | `rust/src/main.rs`, `config.rs` |
| `internal/audio` | `audio.rs`, `vad.rs` |
| `internal/endpoint`, `internal/segment` | `endpoint.rs`, `segment.rs` |
| `internal/duplex` | `engine.rs`, `engine/` |
| `internal/provider` | `provider/` |
| `internal/server`, `internal/live` | `server.rs`, `server/session.rs`, `protocol.rs` |
| `internal/tools` | `engine/tools.rs` |
| `internal/focus` | `focus.rs` |
| latency-console transport | `telemetry.rs` |

One actor owns conversation state. Recognition and response workers carry
cancellation tokens. Generation IDs discard stale work after interruption;
playback waits for a confirmation gate before any speculative audio or tool
side effect. Provider, engine, socket, and background-tool queues are bounded.

Server tool executor configuration is immutable across session updates. A
client may provide function schemas and function results, but cannot supply
executor URLs or redirect the server's tool bearer token. Secrets stay in the
environment snapshot and are excluded from debug output and resolved profiles.

Validation uses the unchanged Go implementation as an audio/VAD oracle and
local scripted HTTP/WebSocket peers for vendor protocols. These tests establish
selected behavioral contracts, not complete parity. Live provider/gateway
validation and the advanced capabilities listed in the README remain open.
