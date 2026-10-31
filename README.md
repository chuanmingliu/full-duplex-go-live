# GoLive SIP/RTP adapter

`codex/sip-rtp-adapter` implements an experimental outbound phone transport:

**GoLive Realtime ↔ Go SIP/RTP adapter ↔ RustPBX ↔ GSM gateway ↔ handset**

It replaces Active Call for explicitly requested test calls. GoLive still owns
streaming ASR, LLM, TTS, turn-taking and business tools. The production Active
Call path stays available for comparison.

## This branch owns

| Part | Responsibility |
| --- | --- |
| [cmd/golive-telephony](cmd/golive-telephony/main.go) | One-call command; readiness check by default, explicit `-dial` for a real call |
| [internal/telephony](internal/telephony) | SIP dialog, direct G.711 audio, 20 ms RTP pacing, cancellation and recording |
| [Example configuration](configs/sip-adapter.example.json) | Fixed destination, media allowlist, independent ports and call limits |
| [Profile preparation](scripts/prepare_sip_adapter.py) | Create a separate agent profile without changing running services |
| [Adapter guide](docs/telephony/sip-adapter.md) | Architecture, operation, limitations and A/B procedure |
| [Latency report](docs/telephony/sip-adapter-latency.md) | Synthetic pacing results and expected handset benefit |
| [CI](.github/workflows/sip-adapter.yml) | Regression tests and Linux binary; no automatic deployment or dialing |

Shared engine source remains here to build and test the transport. Changes on
this branch should stay focused on the adapter and its integration contract.

## Build and validate

```sh
go build -o /tmp/golive-telephony ./cmd/golive-telephony
go test -race ./internal/telephony
```

Read the [setup guide](docs/telephony/sip-adapter.md) before connecting a PBX.
Tests use local simulated SIP peers and the actual GoLive Realtime server with
mock speech providers. **Live RustPBX/GSM interoperability and handset latency
are not yet measured.**

The expected first-packet scheduling saving is roughly **0–20 ms**, about
**10 ms on average** for uniform arrival phases. It is one transport improvement
toward the **<1200 ms handset response target**, not proof of reaching that target.
