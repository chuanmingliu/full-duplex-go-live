# Box Aria originator integration

`codex/service-box-aria-integration` maintains golive's integration with the
ThinkPad's Aria outbound-call originator. It is a **deployment-integration
branch**. The locally installed Node/TypeScript Aria application is outside
this branch; this is not the native Go Aria application running on the Mac.

## Scope of this branch

| Path | Responsibility |
| --- | --- |
| `scripts/phone-brain.sh` | Existing caller-side real/simulated dial operations |
| `internal/crm/service.go` | CRM integration with the external originator |
| `deploy/services/box/core.compose.yml` | Explicit dependency on an external Aria originator |
| `docs/services/box.md` | Observed systemd ownership and unresolved source provenance |
| `docs/services/codebases.md` | Distinction between TypeScript and Go Aria codebases |

There is no new Aria container or imported Aria application source in this
branch. The existing systemd service remains its launch owner.

## Observed deployment

On 2026-10-31, `/etc/systemd/system/aria.service` was running as `root`, with
working directory `/home/chuanming/aria` and optional environment file
`/home/chuanming/aria/deploy/env.lan`.

The deployment directory contains Node/TypeScript source, package manifests,
startup scripts and deployment files, but the inventory found no accessible
Git metadata. A separate Mac checkout at `~/phone/aria` has no configured
remote; it is not sufficient evidence of the box's exact source revision.

The existing golive/CRM integration addresses the originator on
`http://127.0.0.1:3000`. It supplies saved call instructions and coordinates
opening playback with golive, while Active Call owns the SIP/media endpoint.
The current call workflow retains the configured allowed destination.

## Maintenance

- Inspect the installed unit and its effective startup command before changing
  ownership. Preserve private environment files without copying their contents
  into Git or command output.
- Recover a source/build manifest for the installed application before an
  application upgrade; a directory name is not deployment provenance.
- Check originator availability and golive's idle state independently.
- Preserve call IDs, opening coordination and duplicate-dial protection when
  changing CRM or runner integration.
- Do not use a real outbound call as an unattended health probe. Use the
  existing simulator path for isolated integration checks.

A future container migration must carry the existing private settings and
persisted application state explicitly. Keep the original unit, executable
and configuration available for rollback; never run two originators against
the same listener or assume the Mac Go Aria is an interchangeable binary.

See [adjacent deployment ownership](docs/services/box.md#adjacent-aria-stacks-and-host-ingress),
[the two Aria codebases](docs/services/codebases.md#aria-typescript-and-aria-go-are-separate-codebases),
and [the branch registry](docs/services/branches.json).
