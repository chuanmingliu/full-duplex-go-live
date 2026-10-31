# Active Call integration

`codex/service-active-call-integration` maintains the production telephone
adapter's golive deployment configuration. It is a **deployment-integration
branch**, not a fork of Active Call. The existing adapter terminates the SIP
media leg and exchanges streaming audio/events with golive's realtime endpoint.

## Scope of this branch

| Path | Responsibility |
| --- | --- |
| `deploy/services/box/telephony.compose.yml` | Proposed independent Active Call deployment model |
| `deploy/services/box/telephony.env.example` | Paths to the existing private configuration, playbooks and models |
| `deploy/services/validate.py` | Offline Compose structure validation |
| `docs/services/box.md` | Observed image identity, mounts and ownership |

The upstream source project is
[miuda-ai/active-call](https://github.com/miuda-ai/active-call). The existing Mac
build recipe is `~/telephony/Dockerfile.patched`, outside this branch. It carries
handshake, event/schema compatibility, RTP framing and pacing fixes. Recover
its exact source revision before replacing the known working patched image.

## Observed deployment

The 2026-10-31 inventory found container `active-call`, owned by the existing
`telephony` Compose project, using the local image `active-call:patched`.
Its actual image ID is recorded in the service catalog. A local image ID is not
a registry manifest digest.

Active Call uses host networking: HTTP control on `:8081`, SIP UDP `13050`,
and configured RTP range `25000–29999`. It depends on RustPBX/GSM routing and
golive at `127.0.0.1:8765`. Its mounts supply `/app/config.toml`, `/app/config`
and `/app/models` from the private `~/telephony/active-call` installation.

The proposed Compose model is default-off and does not adopt the existing
container automatically. RustPBX remains an external dependency.

## Maintenance

- Preserve the patched image, private configuration and all three mounts.
- Keep `--conf /app/config.toml` when setting codec arguments; replacing the
  command without it can silently select default configuration and ports.
- Validate Compose with `python3 deploy/services/validate.py`; this checks model
  structure without starting a call or a container.
- Test control connectivity, SIP answer/hangup, paced audio and interruption
  clearing using an isolated replay before a live cutover.
- Establish idle call state and stop the old owner before activating a new
  owner on the same ports. Container liveness alone does not prove audio works.

Rollback restores the retained image, configuration, mounts and prior Compose
owner. Keep transport replacement separate from conversation-engine changes so
that the cause of a media regression remains identifiable.

See [transport ownership](docs/services/box.md#active-call-and-rustpbx-codebases),
[the cutover contract](deploy/services/README.md#telephony-and-pbx-persistence),
and [the branch registry](docs/services/branches.json).
