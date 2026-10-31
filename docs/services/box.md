# ThinkPad service inventory and ownership

[Service catalog](catalog.json) · [GitHub branch registry](branches.json) · [Architecture](../architecture/README.md) · [Call operations](../../CALL-OPERATIONS.md)

Observed on **2026-10-31, Asia/Shanghai**, through the established Mac runner.
The inventory records Linux listeners, accessible process working directories,
Docker containers/mounts/Compose labels, running systemd units, selected runtime
arguments, repository metadata and container storage configuration. A listener
or `running` container is **not** an application health check. The first
inventory could not list the root-owned `~/telephony` directory; a successful
follow-up recovered selected configuration and source facts. Unknown source
identity remains explicitly marked.
No services were stopped, restarted, or migrated to produce this inventory.

## Operational groups

| Group | Existing owner | Purpose and boundary |
| --- | --- | --- |
| Conversation and business | Native processes launched through `scripts/phone-brain.sh` | Phone engine, browser demo, CRM, quotation service, Journal and Desk |
| Telephone transport | `telephony` Compose project for Active Call; standalone Docker for RustPBX | Real call signalling, codecs, RTP and realtime audio bridge |
| Speech model lab | Native Python virtual environments plus Go edge gateway | Local ASR/TTS/diarization experiments; target-speaker verification also serves the phone profile |
| User simulator | `golive-user-simulator` Compose project | Synthetic customer and its own isolated Active Call instance |
| Adjacent Aria deployments | `aria.service` plus several independent Compose projects | Originator/telemetry and older experimental stacks; preserve independent ownership |
| Host ingress | `cloudflared.service`, standalone `openship-edge`, host networking services | Tunnels, HTTP routing and machine access; not conversation logic |

The new Compose definitions are an organization and migration target. They do
not retroactively adopt existing processes or containers. Keep their startup
commands separate until a verified cutover has happened.

## Shared Go codebase: full-duplex-go-live

Source: [chuanmingliu/full-duplex-go-live](https://github.com/chuanmingliu/full-duplex-go-live).
The active maintenance checkout is `codex/telephony-latency`; the box runs a
release bundle under `/home/chuanming/golive-telephony`, not a Git checkout.
Phone, demo, CRM and Journal were deployed from `9547d22`; other binaries must
be identified separately before replacing them. See the machine-readable
catalog for service IDs and the [branch registry](branches.json) for each
service’s GitHub maintenance branch.

| Service | Observed listener | Code | Function / dependencies |
| --- | --- | --- | --- |
| Phone engine | `127.0.0.1:8765` | `cmd/golive`, `internal/server`, `internal/engine` | Receives Active Call audio; streams provider ASR/LLM/TTS; calls CRM tools; consults target-speaker service |
| Browser demo | `127.0.0.1:9080` | Same Go executable, separate profile and pool | Browser voice experiments; shares provider accounts, not the phone session |
| CRM | Loopback and `192.168.71.9:8791` | `cmd/crm`, `internal/crm` | Templates, prompt rendering, leads, tools and outbound requests through Aria |
| Quotation service | `127.0.0.1:8793` | `cmd/quotesvc`, `internal/quotesvc` | Local business quotation API; persists its own tables/token/log |
| Call Journal | `192.168.71.9:8790` | `cmd/journal`, `internal/journal` | Reads phone logs/recordings, archives completed calls and shows analysis; reads Talker status from Mac |
| Desk | Loopback and `192.168.71.9:8792` | `cmd/desk`, `internal/desk` | Coordination log, simulator controls and Talker playground; accesses Journal on LAN address |
| Edge gateway | Loopback and `192.168.71.9:8030` | `cmd/edge`, `internal/edge` | Authenticated model gateway in front of local ASR/TTS/diarization |

**Build.** Use the repository's pinned Go toolchain and build the relevant
`./cmd/<service>` package for Linux amd64. Set the existing version ldflags as
in the release build. A generic root `Dockerfile` builds only `golive`; it does
not package CRM, Journal, Desk, quotation service or edge gateway.

**Existing lifecycle.** Run commands from the deployed release directory.
`bash scripts/phone-brain.sh status` is read-only. `restart` replaces the phone
process and can also start enabled companions; it is not a narrow single-service
operation. Companion controls are `journal start|stop|status`,
`crm start|stop|status`, `quotesvc start|stop|status`, `desk start|stop|status`,
`demo on|off|status`, and `edge start|stop|status`. The edge stop operation also
stops model runtimes, including the target-speaker service. Do not use it as a
harmless gateway-only restart.

**Health.** Phone/demo expose `/livez`, `/healthz` and `/v1/pool`; check pool idle
state before a deployment. CRM, Desk, quotation service and edge expose
`/healthz`. Journal exposes `/api/overview`; discard its body when checking
availability because it can contain private call information. Journal listens
on the LAN address, so `localhost:8790` is not the correct current probe target.

**Persistence and logs.** The follow-up process inventory confirms the current
provider environment, phone/demo profiles, business data directories, Journal
inputs/archive, and simulator paths below. Some additional companion log paths
come from the launch script. Refresh these bindings during migration preflight.

| Data / logs | Existing path | Preservation rule |
| --- | --- | --- |
| Provider environment | `~/golive-telephony.env` | Private configuration, never committed or printed |
| Saved call instructions | `~/golive-last-call.json` | Atomically replaced by the engine; needs a writable directory, not a read-write bind of a single file |
| Working recordings | `~/golive-calls` | Current configured retention is 720 hours; do not substitute an empty volume |
| Permanent archive | `~/golive-archive` | Preserve all files/manifests; verify checksums before and after migration |
| Journal data | `~/golive-journal` | Preserve notes/state independently from source `journal/` |
| CRM / quotations | `~/golive-crm`, `~/golive-quotesvc` | Keep business tables, plans, logs and tokens together |
| Desk / edge | `~/golive-desk`, `~/golive-edge` | Keep existing participant/API tokens and append-only logs |
| Phone / demo logs | `~/golive-telephony/golive-telephony.log`, `golive-demo.log` | Journal consumes the phone log; container stdout alone is not an equivalent replacement |
| Companion logs | `journal.log`, `crm.out`, `quotesvc.out`, `desk.out`, `edge.out` under release | Application data directories may contain additional durable operational logs |

**Upgrade and rollback.** Preserve executable hashes, exact environment/profile
files, enabled-service markers, archive manifests and writable stores. Deploy
one service group at a time only when phone/demo pools are idle. On failure,
restore the previous binaries/configuration and previous supervisor; do not run
native and Compose owners simultaneously on the same ports or writable stores.
Verify actual executable identity and application health after switching.

Detailed contracts: [TELEPHONY.md](../../TELEPHONY.md),
[EDGE.md](../../EDGE.md), [CALL-OPERATIONS.md](../../CALL-OPERATIONS.md),
[streaming coordination](../architecture/07-streaming-coordination.md).

## Active Call and RustPBX codebases

Active Call translates the telephone media leg into golive's realtime protocol;
RustPBX routes the SIP call to/from the GSM gateway. They remain required by the
current production path. Discussing a custom adapter does not change this path.

| Property | Active Call | RustPBX |
| --- | --- | --- |
| Observed container | `active-call` | `rustpbx` |
| Image label | `active-call:patched` | `ghcr.io/restsend/rustpbx:latest` |
| Actual local image ID | `sha256:76b141e9b0f720d80f7d00998cacea95879393b8ce26f53a8bba14332a8b5993` | `sha256:dca6de8b7213d51f1d57af3424a712091d92c546dd7aff6bc29db4be294699d7` |
| Supervisor | Compose project `telephony` | Standalone Docker; no Compose labels |
| Networking | Host | Host |
| Restart policy | `unless-stopped` | `no` |
| Control / signalling | Observed config: HTTP `0.0.0.0:8081`, SIP UDP `:13050`, RTP `25000–29999` | Observed config: HTTP `0.0.0.0:8080`, SIP UDP `:5060`, RTP `20000–23999` |
| Configuration | `~/telephony/active-call/active-call.toml` → `/app/config.toml`, read-only | `~/telephony/rustpbx/config.toml` → `/app/config.toml`, read-write |
| Other observed mounts | `active-call/config` → `/app/config`; `active-call/data/models` → `/app/models` | **None** |

These IDs are local Docker image IDs, not registry manifest digests. Pinning a
registry image requires separately resolving its repository digest; do not
construct a fake `name@sha256:<local-image-id>` reference.

**Active Call source and build.** The Mac-side `~/telephony/Dockerfile.patched`
clones `https://github.com/miuda-ai/active-call.git` and applies WebSocket handshake,
GA event/schema compatibility, RTP framing and pacing patches. Its current
`ACTIVE_CALL_REF=main` and runtime base `latest` are mutable. The image’s OCI source label also names `miuda-ai/active-call`. This identifies
the source project/build recipe, not the source revision of the running image. Preserve the known
image and patch files; recover a pinned source revision before rebuilding.
Always retain `--conf /app/config.toml`; dropping it can silently select default
ports/configuration. Logs come from `docker logs active-call` and may contain
private call details. Verify an actual media handshake and controlled audio
replay in addition to HTTP availability.

**RustPBX persistence discrepancy.** The Mac-side example Compose mounts
`./rustpbx/data:/app/data`, but the running box container has no such mount.
The inspected configuration sets `database_url = "sqlite://rustpbx.sqlite3"`
with container working directory `/app`, implying a database path under `/app`
that must be verified before copying. The container diff directly confirms
writable-layer additions under `/app/storage` and `/app/config/recorders`,
including call recordings. Recreating the container from the example may lose
that state. Take a consistent database backup (including any required SQLite
sidecars), copy and verify storage/recordings, and retain the old container
before adopting Compose. Merely adding a new empty volume is not a migration.
The OCI source label names `restsend/rustpbx`; its deployed source revision
remains unknown, even though a separate local RustPBX checkout exists.

**Lifecycle and rollback.** Inspect `docker inspect` selectively for image ID,
mounts, labels and state. Use the existing `telephony` project only for the
Active Call service it owns. RustPBX currently requires standalone Docker
lifecycle management until a deliberate adoption. Retain exact old image IDs,
configuration and data backups. Avoid `docker compose down -v` and any image
prune while migration/rollback depends on them.

## Native speech model runtimes

| Service | Observed process directory | Observed listener | Interface / role |
| --- | --- | --- | --- |
| Audio8 ASR | `~/edge/asr` | `0.0.0.0:7860` | `/health`; whole-clip local transcription behind edge gateway |
| Audio8 TTS | `~/edge/Audio8_TTS/onnx_runtime_0_1b_int8` | `127.0.0.1:8024` | `/api/health`; local synthesis behind edge gateway |
| Diarization | `~/edge/diar` | `127.0.0.1:8031` | `/health`; streaming speaker labels |
| Target-speaker verification | `~/edge/tsv` | `127.0.0.1:8032` | `/health`, `/verify`; caller focus for phone conversations |

The observed ASR bind is broader than the loopback intent documented in
`EDGE.md`. Record that discrepancy; verify clients before changing it.

ASR/TTS ran under a managed Python 3.12 installation, while diarization/TSV used
`/usr/bin/python3.14`. Do not put all four behind an assumed interchangeable
virtual environment. `phone-brain.sh` starts them from individual `.venv`
directories and scripts. Preserve model weights, tokenizer/configuration,
voice profiles and package lock/freeze information before containerizing.

The Go edge gateway, `edge/tsv_server.py`, and `edge/diar_server.py` are maintained
in the main golive codebase. Audio8 runtime source/model bundles are adjacent installations. The follow-up
confirms `~/edge/Audio8_TTS` is a checkout of
[Audio8-AI/Audio8_TTS](https://github.com/Audio8-AI/Audio8_TTS) at
`07e40f5d0b03fc473635ef378654bfb581027ac3`; package/environment changes still
need their own provenance. `~/edge/asr` has no detected Git metadata and was
documented as a Hugging Face runtime download
(`Edge0/Audio8-ASR-0.1B-onnx-runtime`). Record its source manifest before creating
a maintained wrapper branch. `~/edge/diar` and `~/edge/tsv` are runtime
directories without detected Git metadata, not separate repositories. Model
and voice artifacts remain private runtime data, not source commits.

## Synthetic customer codebase

The `golive-user-simulator` project has three observed containers:

- `simulator`: running, publishes **only** `127.0.0.1:18865`, mounts existing
  `golive-user-simulator_run-data` at `/data` and a read-only profile.
- `activecall`: running on the private `golive-user-simulator_simulator` network;
  mounts `golive-user-simulator_gateway-config` read-only.
- `config`: exited one-shot initializer, owns the same gateway-config volume.
  An exited initializer is expected; do not treat it as a crashed daemon.

All three had restart policy `no` in the observed state. The container labels
identify these configuration files, in order:

1. `~/golive-sim/src/deploy/simulator/compose.yml`
2. `~/golive-sim/compose.providers.yml`
3. `~/golive-sim/compose.sim-domain.yml`

The running `activecall` label lists the base file only, while the latest
simulator/config labels list all three. Preserve the effective per-service
configuration and existing volume names. Changing the project name would select
new empty named volumes unless volumes are explicitly mapped.

The box source checkout `~/golive-sim/src` is verified as
`chuanmingliu/full-duplex-go-live` at
`5128a8f120345b707ec75fdac2c9d96c169485b1`. Its Mac worktree is
`~/golive-inbound-user-simulator`, branch `codex/inbound-user-simulator`. This is
a separate workstream of the same repository, not a repository named after the
directory. Its `deploy/simulator/README.md` describes setup/build/control. Build with that codebase's
`deploy/simulator/Dockerfile` and `deploy/simulator/activecall/Dockerfile`.
Operational entry points are `scripts/sim-brain.sh`, `scripts/sim-bundle.sh` and
`phone-brain.sh call sim` in the main golive release. Keep this gateway isolated
from the production SIP/GSM route. Do not use a real call as a health probe.

## Adjacent Aria stacks and host ingress

The following are **observed owners**, not a declaration that they are redundant
or safe to stop. Source provenance must be recovered before consolidating them.

| Owner | Observed state | Configuration source / next maintenance boundary |
| --- | --- | --- |
| `aria.service` | Running system service as `root` | `/etc/systemd/system/aria.service`; working directory `~/aria`; optional environment file `~/aria/deploy/env.lan` |
| `aria-full-duplex` | `aria` and `s2s` running; `unless-stopped` | `~/gpt-live-deploy/full-duplex-current/go/deploy/aria/docker-compose.full-duplex.yml` |
| `cx-latency-20261011r7` | `cx-aria` and `cx-s2s` running; restart `no` | `~/cx-latency-20261011r7/release/candidate/compose.yml` |
| `aria-latency-20261002r5` | Both containers exited | `~/gpt-live-deploy/aria-latency-20261002r5-cleanup-20261009/candidate/compose.yml` |
| `aria-latency-20260922r3` | Aria created, S2S exited | `~/gpt-live-deploy/aria-latency-20260922r3/compose.yml` |
| `aria-latency-20260922r2` | Both exited | `~/gpt-live-deploy/aria-latency-20260922r2/compose.yml` |
| `aria-latency-20260922` | Both exited | `~/gpt-live-deploy/aria-latency-20260922/compose.yml` |
| `s2s-backend`, `s2s-backend-before-bidi-20260903-002657` | Exited, standalone containers | Historical agent images; preserve as rollback artifacts until retirement is explicit |
| `qwen3-asr-ws` | ASR container created, not running | `~/qwen3-asr-ws/docker-compose.yml`; existing Hugging Face cache volume |
| `cloudflared.service` | Running | Host tunnel lifecycle; preserve existing private credentials and route policy |
| `openship-edge` | Running standalone container | Image `ghcr.io/oblien/openship-edge:0.6.6`; host certificates, ACME, static files and site configuration mounted |

The box `~/aria` installation contains Node/TypeScript source, `package.json`,
`Dockerfile`, `startup.sh`, and deployment files but no accessible Git metadata.
It must not be confused with the separately observed native Go `live-aria` on
the Mac. The `~/gpt-live-deploy/full-duplex-current/go` and CX candidate folders
are also release trees without detected Git metadata; use their release/source
manifests to recover exact provenance before assigning development branches.

Originator and telemetry ports `3000`, `8767`, `8768` are listening, as are
several experimental ports (including `8865/8868/8870` and `9565/9568/9570`).
The initial unprivileged listener inventory cannot attribute every root-owned
listener to a specific container. Do not assign ports to an Aria variant from
naming alone. Host SSH, VPN, desktop, printing and OS discovery services stay
outside the golive application Compose lifecycle.

## Refresh checklist

1. Repeat selective process/container/listener inventory and application probes;
   update the observation date and evidence source.
2. Match every production binary/image to a source commit or mark it unresolved.
3. Update each service's source repository, GitHub maintenance branch, launch
   owner, dependencies and persistent stores together.
4. Compare effective mounts and network binds against the proposed Compose
   configuration; resolve RustPBX's missing durable data mount before adoption.
5. Record actual migration/rollback outcomes separately from file validation.
