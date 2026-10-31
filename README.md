# golive browser voice demo

`codex/service-browser-demo` maintains the browser microphone, playback and
transcript experience, and the separate golive process that serves it.
It is a browser voice client; it does not place telephone calls.

## Functionality

- Streams microphone audio to a golive session and plays streamed responses.
- Displays user and assistant transcript events, including session resets.
- Exercises conversation behavior without the GSM/SIP telephone transport.
- Supports mock providers for local UI work and a separately configured real-provider demo.

## Owned source

| Path | Responsibility |
| --- | --- |
| [web/index.html](web/index.html) | Browser UI, microphone, playback and transcript handling |
| [web/transcript_test.mjs](web/transcript_test.mjs) | Transcript/reconnection regression checks |
| [cmd/golive](cmd/golive) | Shared executable used by the demo |
| [configs/mock.json](configs/mock.json) | Credential-free development profile |
| [configs/tencent-deepseek-minimax.json](configs/tencent-deepseek-minimax.json) | Real-provider demo profile |

The shared server and conversation engine remain in this checkout. This branch
changes demo functionality and necessary shared-code integration with coordinated
review; it does not own unrelated telephone, CRM or model-service changes.

## Local development

Use the Go version specified in [go.mod](go.mod), plus Node.js for the UI check.
From the repository root:

```sh
node web/transcript_test.mjs
go build -o /tmp/golive-demo ./cmd/golive
/tmp/golive-demo -profile configs/mock.json -env /dev/null -addr 127.0.0.1:18080
```

Open `http://127.0.0.1:18080/` and allow microphone access. The mock profile
requires no cloud credentials. In another terminal, check process liveness:

```sh
curl --fail http://127.0.0.1:18080/livez
```

Real-provider operation requires the corresponding private environment file.
Provider accounts may be shared with the phone service, so session limits and
load tests must respect the phone workload.

## Deployment and state

The observed box demo is a separate native process on loopback port 9080,
limited to two sessions. Its normal deployment disables call recording and
telephony telemetry. It must retain its own listener and process lifecycle.

The `demo` service in [the staged Compose model](deploy/services/box/core.compose.yml)
is a migration target, not evidence that the native process has been replaced.
See [migration requirements](deploy/services/README.md) before changing ownership.
A running UI does not establish telephone or handset latency.

Browser changes should preserve audio cancellation, clean reconnection and
transcript separation between sessions. Recheck microphone and speaker behavior
in a real browser after the automated transcript test.
