# Inbound simulated customer, v1

Start with the **[project README](../../README.simulator.md)** for the purpose, architecture and first-run workflow. This document is the deployment and API runbook.

Claude's voice service calls this receiver. The receiver plays a synthetic human customer; its conversation with the assistant passes through audio only. A separate authenticated API prepares scenarios, returns run IDs and retrieves reports.

This package runs on branch `codex/inbound-user-simulator`. It does not modify an existing service, deployment, gateway route or provider account. The ordinary `golive` binary does not enable simulator capabilities.

The simulator includes an embedded configuration workbench at `/sim/`, task-oriented scenarios, a sequential inbound suite runner and an independent post-call evaluator. See [Workbench and regression testing](WORKBENCH.md) for the end-to-end workflow and [CLI reference](../../cmd/golive-simctl/README.md) for command options and exit codes.

## Evidence and limits

The default profile uses synthetic ASR/LLM/TTS. It proves control and audio transport, not recognizable speech or conversational quality. Real provider accounts and carrier/handset tests are separate acceptance steps. Reports label timings `application_audio_boundary`; they are not handset playout or carrier latency. Both directions use a 200 ms speech-end debounce and one receiver-local clock. Failed, missing and overlapping responses remain marked invalid. Base run reports keep semantic checks `indeterminate`; the opt-in independent evaluator produces a separate artifact from retained transcript evidence.

One reservation/call is allowed at a time. Run reservations expire after 30 minutes by default; setup is bounded at 25s, calls at 300s, script holds at 5s, and retained history at 128 runs. Restart aborts unfinished runs without replaying answer/speech. IDs and default reports omit telephone numbers, credentials and raw transcripts/audio. Transcript retention requires explicit scenario opt-in; audio retention and scripted silence actions are rejected in v1. Use only synthetic personas/facts.

## Local binary

From the repository root, generate three independent private tokens and set them in the process environment. Do not reuse an existing service's credentials. The command deliberately does not load `.env.local`.

```sh
export SIM_CONTROL_TOKEN="$(openssl rand -hex 32)"
export SIM_WEBHOOK_TOKEN="$(openssl rand -hex 32)"
export SIM_MEDIA_TOKEN="$(openssl rand -hex 32)"
go build -o /tmp/golive-simulator ./cmd/golive-simulator
/tmp/golive-simulator -profile configs/simulator/mock.json -addr 127.0.0.1:18865 -data /tmp/simulator-runs
```

`/livez` checks the HTTP process. `/readyz` additionally probes the dedicated Active Call control service. Configure `SIM_ACTIVECALL_URL`, `SIM_REALTIME_URL`, `SIM_SIP_DOMAIN`, `SIM_DESTINATION` and `SIM_GATEWAY_ID` only for the new receiver. Defaults are `http://127.0.0.1:18881`, `ws://127.0.0.1:18865/v1/realtime`, `simulator.invalid`, `simulator-receiver` and `isolated-activecall`.

## Isolated Docker package

External image dependencies and Active Call source are pinned. The gateway image contains a minimal custom-WebSocket handshake patch and suppresses logging the token-bearing media URL. Existing images or containers are not replaced.

```sh
export SIM_SOURCE_COMMIT="$(git rev-parse HEAD)"
python3 deploy/simulator/validate_package.py
# Run only after validation succeeds; these commands name this project exactly.
docker compose -p golive-user-simulator -f deploy/simulator/compose.yml build
docker compose -p golive-user-simulator -f deploy/simulator/compose.yml up -d
```

The init service writes only the new gateway-config volume. Both services run as UID 65532. Controller HTTP is bound to host loopback 18865; SIP 25060 and RTP 24000–24019 remain inside the dedicated network. No host Docker socket, existing volumes, host network or production bind mounts are used. HTTP control and media share this private listener but use different secrets and disjoint handlers. Native Active Call cannot send media auth headers, so its media URL uses a token query; keep it on the dedicated network and do not enable URL/access debug logging.

This package is intentionally **not a carrier deployment manifest**. Exposing SIP/RTP requires a dedicated host/address/NAT plan and separately reviewed ports. The validator rejects external SIP/RTP mappings and production mounts in this local package.

Stop only this project, preserving reports:

```sh
docker compose -p golive-user-simulator -f deploy/simulator/compose.yml down
```

## Prepare, call, inspect

```sh
curl --fail -sS http://127.0.0.1:18865/sim/v1/runs \
  -H "Authorization: Bearer $SIM_CONTROL_TOKEN" \
  -H 'Idempotency-Key: trial-001' -H 'Content-Type: application/json' \
  --data-binary @scenarios/simulator/cooperative.json
```

Use the returned `run_id` as the **called SIP extension**, e.g. `sip:<run_id>@simulator.invalid`. The assistant-side caller sends the INVITE to the dedicated gateway at UDP25060. Never substitute “the current run”: that would let a late call consume the next reservation. V1 directly supports this per-run SIP routing. A fixed carrier telephone number needs a verified route that preserves the reservation identity; the existing household gateway is not assumed to support it.

PBX forwarding may replace the Request-URI host with the gateway hostname or an
IP resolved from `SIM_ACTIVECALL_URL`, optionally with a SIP port. The receiver
accepts those gateway addresses as well as `SIM_SIP_DOMAIN`, while requiring the
exact reservation user part. Its inbound media path fills quiet gaps after
100 ms with 20 ms PCM16 silence frames on the wall clock; existing profiles need
no override. See the [project README](../../README.simulator.md#connect-the-assistant-side-caller)
and [RTP latching notes](activecall/README.md#rtp-source-address-and-latching).

Native webhook: `POST /active-call/incoming` with `Authorization: Bearer $SIM_WEBHOOK_TOKEN`, using Active Call's `event:"invite"`, `dialogId` and `callee`. The adapter attaches its control WebSocket before scheduling acceptance, observes `answer`/`hangup`, and supplies an exact authenticated media binding. The generic `/sim/v1/incoming` API is only a normalized trusted-adapter interface; do not point a native webhook there.

```sh
curl --fail -sS "http://127.0.0.1:18865/sim/v1/runs/$RUN_ID" -H "Authorization: Bearer $SIM_CONTROL_TOKEN"
curl --fail -sS "http://127.0.0.1:18865/sim/v1/runs/$RUN_ID/events?after=0" -H "Authorization: Bearer $SIM_CONTROL_TOKEN"
curl --fail -sS "http://127.0.0.1:18865/sim/v1/runs/$RUN_ID/report" -H "Authorization: Bearer $SIM_CONTROL_TOKEN"
curl --fail -sS -X POST "http://127.0.0.1:18865/sim/v1/runs/$RUN_ID/stop" -H "Authorization: Bearer $SIM_CONTROL_TOKEN"
```

`report` is available after termination; private state/report files are written atomically with 0600 permissions. Prepare is idempotent for the same key/body; reusing a key with different content conflicts. There is no outbound dialing endpoint or old `/v1/opening` broadcast route.

Authenticated `GET /sim/v1/runs` lists retained snapshots. `POST /sim/v1/validate` validates scenario JSON without reserving a call. Terminal `GET /sim/v1/runs/$RUN_ID/transcript` returns an identity-bound envelope only when transcript retention was enabled for that run. The workbench uses these APIs and keeps the control token only in page memory.

Optional scenario `goal` and `behavior` fields guide the simulated customer; `expectations` define independent assistant-task checks and are excluded from the live persona prompt. `expected_outcome: "cancel_before_answer"` selects explicit cancellation checks; it does not make the receiver cancel or dial a call. The independent caller must produce that event. Existing schema-version-1 scenarios and default conversation checks remain supported.

## Validation

```sh
go test -race ./internal/simulator ./internal/server -run 'Simulator|ActiveCall|Controller|Scenario|Observer|State|Report'
go test -race ./internal/duplex -run Simulator
go vet ./...
python3 -m unittest discover -s deploy/simulator -p 'test_*.py'
go build -o /tmp/golive-simulator ./cmd/golive-simulator
python3 deploy/simulator/check_workbench.py --binary /tmp/golive-simulator
```

The workbench check starts only a temporary loopback receiver with mock providers and a fake readiness gateway. It checks authentication, metadata, non-mutating validation, reservation identity, reports and cleanup without a call or provider request.

After starting the local Compose project, run `python3 deploy/simulator/run_sip_checks.py --allow-isolated-sip --output /tmp/simulator-sip-results` for cooperative, interruption and pre-answer cancellation checks. This places calls only inside the dedicated Docker network and never retries a call.

The independent SIP fixture lives in `activecall/sip_fixture.py`. Run it in a peer container on `golive-user-simulator_simulator`; its emitted PCM/PCMU tones are synthetic evidence only. See `VALIDATION.md` for the actual tested revision/results, including baseline test failures and any unrun external checks.

To exercise recognizable speech, use `configs/simulator/providers.example.json` with **dedicated** Tencent ASR, DeepSeek LLM and MiniMax TTS account credentials/quotas. The example uses one request per turn (no hedging). Run cooperative and clarification scenarios with independently recorded human utterances, listen to both call tracks, and check facts manually. This is an opt-in external test; mock-tone success never satisfies it.
