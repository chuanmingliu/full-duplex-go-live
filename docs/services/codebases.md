# Service codebases and maintenance ownership

Inventory date: 2026-10-31, Asia/Shanghai. Repository identity was checked with
`git rev-parse --show-toplevel`, not inferred from a directory's name. A runtime
folder, a checkout, a GitHub branch and a deployed executable are different
objects. Keep their identities separate when releasing a service.

## Source map

The branch column below records local branches observed during inventory.
It does not by itself establish that a branch exists remotely, is up to date,
or describes the running binary. The central service catalog records branch
creation/verification separately.

| Source repository | Mac checkout | Observed branch and revision | Services / scope |
| --- | --- | --- | --- |
| [full-duplex-go-live](https://github.com/chuanmingliu/full-duplex-go-live) | `~/Documents/ChatGPT/full-duplex-go-live` | `codex/telephony-latency`, `e44dd442ce7a49d7331bc562910614ef00f3fe08` | Phone engine, browser demo, CRM, Journal, Desk, quotation service, edge gateway, runner tooling |
| Same repository | `~/golive-local-talker` | `codex/local-talker`, `2dd9a0c486347cb765b5e0301b6cff342c137e5a` | Talker training/export/evaluation tooling; private runtime/model stored elsewhere |
| Same repository | `~/golive-inbound-user-simulator` | `codex/inbound-user-simulator`, `5128a8f120345b707ec75fdac2c9d96c169485b1` | Isolated simulated customer and its dedicated Active Call package |
| Same repository | `~/golive` | `golive-turn-detector`, `a52a228740252c25e255e59dbfecf22268ddbef3` | Separate checkout also housing the installed runner and private operation queue |
| [openai-gpt-live](https://github.com/chuanmingliu/openai-gpt-live) | `~/Documents/openai-gpt-live` | `codex/public-operator-proxy`, `26f5ca36f9df4565ac16874a47546e2cc8155f7c` | Native Go Aria and related Go voice infrastructure |
| Same repository | `~/Documents/openai-gpt-live-custom` | Detached `e8dea1d78206265ec37e019c8b648b9fbe8a2a2a` | Separate custom checkout; not proof of installed Mac Aria identity |
| [RustPBX upstream](https://github.com/restsend/rustpbx) | `~/rustpbx` | `main`, `4dcfb56aed681fb3e4c722b39c191724e9fc47c4` | SIP routing and media infrastructure; only upstream remote configured locally |
| Local repository, no remote configured | `~/phone/aria` | `main`, `2c070d083083ac4f3a869234b8ca1f87d0e29911` | TypeScript Aria originator/operator application; separate from Go Aria |
| [inference-response-api](https://github.com/chuanmingliu/inference-response-api) | `~/responsible-api` | `main`, `4ee9adf92782e573964b20ebde4f17d13c10980c` | Adjacent native vLLM/SGLang services; not a confirmed golive dependency |

`~/telephony`, `~/golive-desk`, `~/hojo-asr-api` and `~/qwen3-asr-ws` are not
independent Git repositories in this inventory. Git invoked there discovers an
unborn repository at the user's home directory. Do not create a service branch
there or accidentally stage the home directory.

The Talker checkout contains existing local edits and untracked experiment code.
They are not part of the revision listed above and must not be silently included
in maintenance commits. The main checkout's unrelated `playground/` is likewise
outside this change.

## Full-duplex Go application

This is a monorepo with several independently deployed commands. Keep the
shared engine/library code in one repository; per-service maintenance branches
do not require duplicating the codebase or splitting its Git history.

| Service | Code ownership | Interfaces and dependencies | Durable state / runbook |
| --- | --- | --- | --- |
| Phone engine | `cmd/golive`, `internal/server`, `internal/duplex`, ASR/LLM/TTS adapters | Realtime WebSocket on box 8765; provider APIs, CRM tools, target-speaker service, Active Call | Calls, saved last-call configuration; [TELEPHONY.md](../../TELEPHONY.md) and [CALL-OPERATIONS.md](../../CALL-OPERATIONS.md) |
| Browser demo | Same `cmd/golive`, separate profile and process | Box loopback 9080; shares provider accounts, not phone sessions | Separate log and limits; [deployment architecture](../architecture/05-development-and-deployment.md) |
| CRM | `cmd/crm`, `internal/crm` | Token-protected 8791; originator, engine, quotation integration | Private `~/golive-crm`; templates, tokens and business configuration |
| Call Journal | `cmd/journal`, `internal/journal` | LAN 8790; reads engine logs, call store and notes; probes Talker | Private `~/golive-journal`, permanent `~/golive-archive`; [evidence architecture](../architecture/04-recordings-and-evidence.md) |
| Desk | `cmd/desk`, `internal/desk` | Loopback/LAN 8792; Journal, simulator and Talker | Private `~/golive-desk`; participant tokens and append-only coordination records |
| Quotation service | `cmd/quotesvc`, `internal/quotesvc` | Loopback 8793; consumed by CRM | Private `~/golive-quotesvc`; vehicle/rating data and token |
| Edge gateway | `cmd/edge`, `internal/edge` | Loopback/LAN 8030; proxies self-hosted ASR/TTS/diarization runtimes | Private `~/golive-edge`; [EDGE.md](../../EDGE.md) |
| Target-speaker verifier | `edge/tsv_server.py` | Loopback 8032; receives audio windows from phone engine | Model and speaker profiles under box `~/edge/tsv`; [EDGE.md](../../EDGE.md) |

Build individual Go commands from a clean checkout with `go build ./cmd/<name>`;
cross-compilation and release identity are handled by the established release
workflow. `go test ./...` and the repository CI checks apply to shared changes.
The currently installed box release must be identified independently of the
documentation checkout's HEAD.

Existing service lifecycle commands are in `scripts/phone-brain.sh`: `journal`,
`crm`, `desk`, `quotesvc`, `edge`, `demo`, and the phone engine's own restart.
Read the command's scope before running it: a phone restart does not imply a
CRM replacement. Preserve provider settings, private business files, tokens,
recordings and archive manifests before a release. A successful `/healthz` or
`/livez` only verifies that service's advertised health; it is not a handset test.

## Talker tooling

The [Talker runbook](https://github.com/chuanmingliu/full-duplex-go-live/blob/codex/local-talker/tools/talker/README.md)
owns extraction, MLX training, GGUF export and CPU benchmarking. Local tools use
private `~/.local/share/golive-talker`; box experiments use `~/golive-talker`.
Do not commit call-derived datasets, adapters, weights or predictions.

Validate tooling with the branch's Python unit tests. Preserve the selected
checkpoint/export manifest and llama.cpp revision together; substituting a
different quantizer/runtime changes reproducibility. Serving and rollback on
the Mac are described in [Mac services](mac.md#talker-preserve-the-native-metal-runtime).

## Isolated user simulator

The [simulator runbook](https://github.com/chuanmingliu/full-duplex-go-live/blob/codex/inbound-user-simulator/deploy/simulator/README.md)
owns `cmd/golive-simulator`, `cmd/golive-simctl`, scenarios, provider profiles and
`deploy/simulator`. Its Compose project is `golive-user-simulator`; loopback
18865 is the control service. SIP/RTP remain in the dedicated Docker network.

Build from the simulator branch, run its package validator and workbench checks,
then use its own Compose files and private overrides. Preserve both dedicated
volumes (`run-data` and `gateway-config`) and the private provider/token files.
`down` without `-v` preserves named volumes; never use a broad prune as cleanup.
`/livez` checks the process, `/readyz` also checks its dedicated gateway. Synthetic
audio tests are not real handset latency measurements.

The simulator's Active Call image is built from pinned upstream commit
`eaf7ebc8f39193a289348989f6a09b0ace287da7`, plus its own handshake/logging patch.
This is a different build contract from the production `active-call:patched`
image; never replace one with the other because their executable names match.

## Production telephony infrastructure

`~/telephony/docker-compose.yml` and `Dockerfile.patched` on the Mac document
RustPBX and the patched production Active Call. They are deployment files, not
an independent versioned source repository. The legacy Dockerfile follows an
upstream branch by default; rebuilding it today is not proof of reproducing the
running image. Keep the observed image ID and the exact patch sources before
considering a rebuild.

RustPBX owns SIP routing/trunks and its private configuration/database. Active
Call owns the realtime media bridge to golive and explicit call termination.
Preserve the production Active Call `--conf /app/config.toml` command and its
RTP framing/pacing patches. A default upstream image can ring a phone while
failing to deliver usable audio.

The [phone path](../architecture/01-phone-call.md) and
[TELEPHONY.md](../../TELEPHONY.md) describe integration. For branch maintenance,
use an owned fork or track deployment patches in the golive repository; a local
upstream remote is not authorization or ability to push a branch to that
upstream. Inventory the deployed RustPBX state before container replacement:
its writable container layer may contain state absent from the listed mounts.

## Aria TypeScript and Aria Go are separate codebases

The TypeScript checkout at `~/phone/aria` contains React/TanStack UI, SIP
originator/control integration and Node deployment scripts. Its package scripts
include `npm run typecheck`, `npm test`, `npm run build`, `npm run start:lan`
and `npm run package:lan`. The build also invokes database migration, so it must
not be treated as a harmless production validation command. Use an isolated
build environment with private runtime state excluded.

Its existing deployment scripts target `aria.service` on the box and health
at `/api/health`. Preserve the installed application, private environment,
operator/database data and service unit for rollback. No Git remote is
configured in this local checkout; identify an existing owned remote before
creating a GitHub maintenance branch or publishing this source.

Native Go Aria lives in `openai-gpt-live`, with `cmd/live-aria` as the executable.
Its current checkout documents health routes and four logical interfaces in
`docs/aria-compatibility.md`; runbooks include `docs/aria-quickstart.md` and
`docs/aria-stage-two-pbx.md`. Build with `go build ./cmd/live-aria` and run that
repository's checks, preserving the installed private launch configuration.
The Mac binary identifies an older dirty revision, as described in
[Mac services](mac.md#native-go-aria-already-supervised-by-launchd). Neither the
current Go checkout nor the TypeScript checkout alone proves how that binary
was built.

## Maintenance rules

For every service change, record the source revision, branch URL, image/binary
identity, deployment configuration fingerprint, state directories, verification
results and rollback target. Record pending source provenance explicitly rather
than filling gaps with a guessed repository.

Reuse an existing suitable `codex/` branch after checking its remote identity.
When a new service branch is needed, create it in the actual owning repository
from a verified revision. Do not reset other service branches or automatically
merge independent experimental histories. Updating this catalog is part of the
same change as updating a service's build or launch configuration.
