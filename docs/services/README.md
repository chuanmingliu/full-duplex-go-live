# Service operations: Mac and ThinkPad

This is the maintenance entry point for the golive ecosystem. The inventory was observed on **2026-10-31, Asia/Shanghai**. It is a snapshot, not a live dashboard. No production service was restarted or migrated while preparing these files.

| Guide | Contents |
| --- | --- |
| [Mac services](mac.md) | Native Aria, Metal Talker, runner, health and local source trees |
| [ThinkPad services](box.md) | Phone stack, models, telephony, simulator and existing experimental containers |
| [Codebase guide](codebases.md) | Responsibilities, interfaces, source identity, build and lifecycle |
| [Compose deployment models](../../deploy/services/README.md) | Separate core, telephony, staged PBX and optional Mac development configurations |
| [Machine-readable catalog](catalog.json) | 21 logical services, current ownership and observed container identities |
| [Branch registry](branches.json) | A maintenance branch for every catalog service |
| [Reusable service-maintainer agent](../../.cursor/agents/golive-service-maintainer.md) | Inventory, documentation, branch and migration workflow |

## Maintenance branches

The integration branch is `codex/service-operations`. Services in the Go monorepo share source and may share an image, but have separate maintenance branches and independent runtime lifecycles. Merge reviewed service changes through the appropriate integration/release branch; do not deploy arbitrary divergent service branches as one release. Existing phone and simulator workstreams are retained.

For third-party packages and the local-only TypeScript originator, the branch maintains **our integration**. It is not a fork of the upstream implementation. Exact application-source imports or new forks need their own provenance review; existing private runtime trees are not published by this task.

| Service | Branch | Ownership |
| --- | --- | --- |
| `phone` | [codex/telephony-latency](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/telephony-latency) | source |
| `browser-demo` | [codex/service-browser-demo](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-browser-demo) | source |
| `crm` | [codex/service-crm](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-crm) | source |
| `quotesvc` | [codex/service-quotation](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-quotation) | source |
| `journal` | [codex/service-call-journal](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-call-journal) | source |
| `desk` | [codex/service-desk](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-desk) | source |
| `edge-gateway` | [codex/service-edge-gateway](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-edge-gateway) | source |
| `target-speaker` | [codex/service-speaker-verification](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-speaker-verification) | source |
| `diarization` | [codex/service-diarization](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-diarization) | source |
| `audio8-asr` | [codex/service-audio8-asr-integration](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-audio8-asr-integration) | deployment-integration |
| `audio8-tts` | [codex/service-audio8-tts-integration](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-audio8-tts-integration) | deployment-integration |
| `active-call` | [codex/service-active-call-integration](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-active-call-integration) | deployment-integration |
| `rustpbx` | [codex/service-rustpbx-integration](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-rustpbx-integration) | deployment-integration |
| `simulator` | [codex/inbound-user-simulator](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/inbound-user-simulator) | source |
| `simulator-activecall` | [codex/service-simulator-gateway](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-simulator-gateway) | source |
| `aria-originator` | [codex/service-box-aria-integration](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-box-aria-integration) | deployment-integration |
| `cloudflared` | [codex/service-cloudflared-integration](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-cloudflared-integration) | deployment-integration |
| `openship-edge` | [codex/service-edge-proxy-integration](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-edge-proxy-integration) | deployment-integration |
| `mac-talker` | [codex/local-talker](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/local-talker) | source |
| `mac-aria` | [codex/service-mac-aria](https://github.com/chuanmingliu/openai-gpt-live/tree/codex/service-mac-aria) | source |
| `mac-runner` | [codex/service-local-runner](https://github.com/chuanmingliu/full-duplex-go-live/tree/codex/service-local-runner) | source |

Historical stopped containers are recorded as deployment instances, not assigned invented source repositories. Existing Aria/CX experiments retain their own Compose ownership and are not automatically retired.

## Validate and operate

Run `python3 deploy/services/validate.py` from the repository root. It resolves fixture-only Compose models and checks persistence, profile boundaries and resistance to inherited environment overrides. It requires Docker Compose, not a running Docker daemon or credentials.

The production models are **staged definitions**. Migration is a separate operational step described in their runbook. Important remaining work is explicit: recover RustPBX SQLite/storage/recordings from its writable layer; reconcile all private configuration paths and native overrides; build and smoke-test images; verify idle call state and rollback before switching process ownership. The current native launch commands continue to own the live core services.

Keep the simulator project and named volumes intact. Keep Mac Metal inference native. Treat published branch state, built artifact identity and observed running version as three different facts.

## Updating this inventory

For each service change, update its source revision and dependency/health/state notes, validate the corresponding deployment definition, and record whether a live change actually happened. Store provider secrets, phone numbers, prompt content, recordings and weights outside Git. Read-only host inventory can be collected through the established [runner](../../scripts/local-ops/README.md); do not re-run historical deployment jobs to refresh inventory.
