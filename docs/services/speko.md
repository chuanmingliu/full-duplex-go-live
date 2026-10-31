# Speko GPT-Live service

This service invokes `gpt-live-1` through Speko's hosted speech-to-speech router:

The [SIP bridge runbook](speko-sip.md) covers Realtime/G.711 calls, secure API-key
setup on Mac or ThinkPad, and dedicated-service startup.

```text
GoLive native GPT-Live client
  → golive-speko /v1/live (or GoLive /v1/speko/live)
  → wss://router.speko.dev/v1/live
  → gpt-live-1
```

The implementation follows Speko's [speech-to-speech contract](https://docs.speko.ai/relay/s2s),
checked on 2026-10-31: bearer authentication, a fresh `Idempotency-Key` on each
WebSocket upgrade, and model selection in the first native `session.start` frame.
This route uses the vendor's GPT-Live protocol. The separate STT/TTS/LLM
`routing: {mode: "auto"}` envelope is not added to GPT-Live frames. This service
does not promise cross-model fallback or replay an interrupted conversation.

## Standalone

```sh
go build -o bin/golive-speko ./cmd/golive-speko
cp -n configs/speko.env.example .env.local
# Edit .env.local: set GOLIVE_AUTH_TOKEN to your own client credential.
# Add SPEKO_API_KEY when available. Never commit this file.
bin/golive-speko -env .env.local -addr 127.0.0.1:8095
```

If `.env.local` already exists, add the variables to it instead of replacing it.
The process starts without either key. `/healthz` returns 200; `/readyz` returns
503 and a configuration reason until both credentials are present. Restart
after adding or changing credentials. Environment variables take precedence
over the dotenv file.

| Setting | Purpose |
| --- | --- |
| `SPEKO_API_KEY` | Provider credential sent only to the upstream Router |
| `GOLIVE_AUTH_TOKEN` | Independent bearer credential accepted from your backend clients |
| `SPEKO_LIVE_URL` | Defaults to `wss://router.speko.dev/v1/live`; full WebSocket endpoint |
| `SPEKO_DELEGATION_MODE` | SIP bridge only: `responses` (default) or `client` (separate Speko text backend) |
| `SPEKO_RESPONSES_MODEL` | SIP backend model, default `gpt-6-luna` |
| `-max-sessions` | Concurrent session cap, default 32 |
| `-session-timeout` | Maximum connection lifetime, default `1h` |

The standalone listener defaults to loopback. Use an authenticated backend/BFF
for browser clients and TLS termination if serving across a network. This relay
accepts client authorization headers only, never URL tokens. It forwards neither
caller headers nor query parameters to Speko. Custom upstreams require `wss`,
except loopback `ws` for local testing.

## Embedded in GoLive

Set `GOLIVE_SPEKO_ENABLED=true` or `"speko_enabled": true` in a JSON profile,
and supply `GOLIVE_AUTH_TOKEN` plus, when available, `SPEKO_API_KEY`.

```sh
go run ./cmd/golive -profile configs/mock.json -env .env.local
```

The additional native endpoint is `/v1/speko/live`; authenticated readiness is
`GET /v1/speko/readyz`. The normal `/v1/live` and `/v1/realtime` endpoints continue
to use GoLive's local engine and its own configured providers. Startup still
preflights those providers; use the standalone command if only Speko is needed.
Global admission and per-IP limits also apply to embedded Speko sessions. The
relay has an additional cap of 32 when the global cap is unlimited, and a
one-hour lifetime when the local session duration is unlimited.

The existing global `/readyz` reflects shared capacity but does not gate the
local engine on Speko configuration. Check `/v1/speko/readyz` for the relay.
Readiness checks only local configuration/capacity: it does not verify balance,
model access, or upstream health and never opens a paid session.

## Client contract

Connect using `Authorization: Bearer <GOLIVE_AUTH_TOKEN>` and send:

```json
{
  "type": "session.start",
  "session": {
    "model": "gpt-live-1",
    "instructions": "Be concise and helpful.",
    "audio": {"format": {"type": "audio/pcm", "rate": 24000}}
  }
}
```

Omitting `model` inserts `gpt-live-1`. Other models and local `session.golive`
engine overrides are rejected before opening an upstream connection. An empty
`golive` object from existing clients is removed. Other session fields, including
delegation, are retained. Subsequent audio, transcript, delegation, response,
usage and unknown vendor events pass through without translating their payloads.

Clients must implement the selected native GPT-Live delegation/tool workflow.
Local GoLive business-tool execution, prompt defaults, phrase caches, recording,
attach/fork APIs and telemetry are not provided by the native relay. The SIP
adapter uses the separate `/v1/realtime` bridge, with `/v1/pool` and `/v1/opening`
for admission and answer-gated startup; the embedded versions are under
`/v1/speko/`. See the [bridge runbook](speko-sip.md) for its supported subset.

`golivectl` can target the standalone `/v1/live` or embedded `/v1/speko/live` URL
using its existing `GOLIVE_AUTH_TOKEN` support. Leave its ASR/LLM/TTS override
flags empty. Its synthetic-audio mode is a transport exercise, not a useful
spoken conversation; use a compatible real audio source for live validation.

## Lifecycle and verification

Setup and writes are bounded to 10 seconds; messages to 1 MiB. Backpressure is
applied directly between sockets rather than accumulating audio in a queue.
Disconnect, timeout, shutdown, and a native `session.closed` event close both
sockets and release capacity. No automatic redial occurs. SIGINT/SIGTERM closes
WebSockets explicitly before shutting down HTTP. No audio or transcript is
stored or logged by this service.

Upstream upgrade failures become sanitized native `error` events after the
client upgrade: `speko_auth_failed`, `speko_rate_limited`, or `speko_unavailable`.
Upstream native error events are forwarded unchanged.

```sh
go test -race ./internal/speko ./internal/config ./internal/server
go build ./cmd/golive-speko ./cmd/golive
```

Tests use local mock Router sockets; no Speko key, external request, deployment
or real phone call is involved. Once a key is supplied, check account access to
`gpt-live-1` in Speko's authenticated catalog and perform a live native session.
That remains necessary evidence for real model/audio interoperability.
