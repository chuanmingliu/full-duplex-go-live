# Isolated native SIP deployment on the box

Deployment: `/home/chuanming/golive-sip-ab/native-real-fb11e2c` on
`chuanming@192.168.71.9`. Compose project `golive-native-sip-fb11e2c` retains
the original GoLive providers, saved instructions, greeting and voice while
replacing Active Call only for an explicitly selected adapter test.

| Component | Identity | Address / role |
| --- | --- | --- |
| Original GoLive | `9547d22`, retained running process | `127.0.0.1:8765` |
| Original Active Call | Existing `active-call` container, unchanged | SIP registration / port 13050 |
| Original Journal | Retained | `192.168.71.9:8790` |
| Isolated GoLive | Same original executable `9547d22` | `127.0.0.1:9895` |
| Candidate adapter | `fb11e2c`, includes PCM16 support from `f758708` | SIP 15064, RTP 31200–31231 |
| Dedicated Journal | `f758708` binary | `192.168.71.9:9896` |

The [real-call retrospective](../../journal/retros/out-native-sip-20261101-01.md)
records measured results and remaining qualification gaps. The code commits
passed full CI before deployment; the call does not establish physical handset
latency or real handset-originated teardown.

## Configuration and credentials

The candidate copies the existing private adapter configuration and preserves
the recipient, PBX authentication, media allowlist and call limits. Explicit
experiment changes are `golive_url=http://127.0.0.1:9895`, SIP/RTP ports above,
`record_dir=/data/adapter`, `golive_audio_format=pcm16`, `codec=pcmu`, and
`contact_user=golive-sip-adapter`. The independent Contact avoids the simulator's
observed PBX collision with Active Call's registration. PCM16 preserves the
original engine's 16 kHz interface; the telephone side remains G.711 PCMU.

The engine profile changes address, capacity to one, local store/last-call paths,
opening hold, recording, and clears the legacy `hangup_url`. The adapter owns
this call's SIP teardown. Provider selection, saved conversation configuration,
VAD/endpointing and playback tuning remain from the original native setup.

Private `engine.env`, `providers.env`, `adapter.env`, `agent.json`, `adapter.json`
and saved-call JSON stay on the box. Never commit them or render resolved Compose
environment values. `SPEKO_API_KEY` and the Speko tunnel are not part of this path.

The image is `golive-sip-ringback:c3e049b`; actual executables are read-only mounts
under `/runtime`, so image tags alone do not identify the running code:

- Engine SHA-256: `e8648a7942d1e7c6a2c726033ab86efd786ca5801d302712d95cbc1a9bc7c493`.
- Adapter SHA-256: `314765664c46ca7eaf70402c749f117e5209df6bb7f197607fb543628e36fdc6`.

## Compose and startup

The [deployed Compose snapshot](../../scripts/local-ops/recorded/2026-11-01-native-real/compose.deployed.json)
uses host networking, UID 1000, read-only roots, tmpfs, dropped capabilities and
no-new-privileges. Engine and Journal restart unless stopped. The adapter is a
one-off service under profile `call`, with `restart: no`; starting the stack
does not dial.

The initial deployment failed readiness because host provider key files were
missing inside the engine container. The final Compose mounts DeepSeek,
SiliconFlow, OpenRouter and Fish key files **read-only at their original paths**,
as referenced by the private environment. It also retains the original working
directory and CRM/edge token-file mounts. After correction, engine readiness,
recording, Journal retention (720 hours), and the adapter's no-dial preflight
passed. Do not replay the initial historical deployment script as a ready recipe.

After inspecting current state, no-dial verification is:

```bash
cd /home/chuanming/golive-sip-ab/native-real-fb11e2c
docker compose -f compose.json ps
docker compose -f compose.json run --rm --no-deps adapter
```

Before any restart, prove the selected engine and adapter are idle. Preserve
`private/`, `state/`, the original services, and completed one-off containers.
For rollback, select the unchanged original call workflow; no migration back or
replacement of Active Call is needed. Plain `call` still means the original
workflow unless the owner explicitly selects the candidate.

## Call identity, launch and archive

Future explicitly authorized adapter calls must use `out-native-sip-<UUIDv4>`
from `uuid.uuid4()`, with the same value in the call file, receipt and journal.
The external console rejects date-based IDs as phone-like. Its deployed validator
accepts the UUID form. The [UUID real-call review](../../journal/retros/out-native-sip-a93db0f8-ff19-4b9d-95f3-09aca39541be.md)
verified 138 events sent without new failures; console retention remains separate
from acceptance.
Do not rename prior records or replay telemetry under a different identity.

Inspect live builds, pool occupancy, recording settings, configured recipient,
saved prompt/greeting/voice and existing receipts before constructing a fresh
runner job. Warm the native greeting without creating a session; release it only
after SIP answer. Use an exclusive receipt and detached one-off adapter. Never
retry an uncertain dial outcome. The [historical call source](../../scripts/local-ops/recorded/2026-11-01-native-real/README.md)
documents the completed attempt; it is not a reusable dial command.

Raw capture is under `state/store` and `state/adapter`; permanent recordings are
under `state/archive/calls/YYYY/MM/`. Journal reads sanitized notes from
`state/notes/retros` without restart. After the owner's `done`, verify closed
session, both speakers/channels, ringback, all manifest sizes/hashes, and canonical
latency records. Keep raw audio, transcripts and logs out of GitHub. A separate
[handset capture](../../HANDSET-LATENCY.md) is required for acoustic end-to-end timing.
