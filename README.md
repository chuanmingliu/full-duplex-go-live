# RustPBX integration

`codex/service-rustpbx-integration` maintains the ThinkPad PBX's deployment
contract, state recovery and golive routing documentation. It is a
**deployment-integration branch**, not a fork of the upstream
[restsend/rustpbx](https://github.com/restsend/rustpbx) codebase.

RustPBX routes SIP calls between the GSM gateway and Active Call. Its routing
and media settings must remain compatible with the existing telephone path.

## Scope of this branch

| Path | Responsibility |
| --- | --- |
| `deploy/services/box/rustpbx.staged.compose.yml` | Staged deployment after state recovery |
| `deploy/services/box/rustpbx.env.example` | Explicit recovered-state and private-config locations |
| `deploy/services/validate.py` | Offline model validation |
| `docs/services/box.md` | Observed image, configuration and persistence discrepancy |

The staged model is default-off. It is not a command to replace the running
container directly from an image or an empty data directory.

## Observed deployment

On 2026-10-31, container `rustpbx` was standalone Docker, with no Compose owner
and restart policy `no`. Its image label was `ghcr.io/restsend/rustpbx:latest`;
the actual image ID is recorded in the catalog. The deployed source commit
has not been established from that mutable label.

The container uses host networking and working directory `/app`. Its inspected
configuration selects HTTP `:8080`, SIP UDP `5060`, RTP `20000–23999`, and
`database_url = "sqlite://rustpbx.sqlite3"`.

Only `/app/config.toml` is mounted. The container's writable layer contains
`/app/storage` and `/app/config/recorders`, including recordings. The relative
SQLite configuration also requires a consistent database-state recovery.

## State recovery before adoption

1. Locate the actual database and take a consistent backup, including required
   SQLite sidecars. Preserve the original container and private configuration.
2. Recover and verify storage/recording trees and any other required writable
   state. A fresh volume is not a substitute for those files.
3. Prepare a copied configuration with the staged model's absolute database
   location under `/state`; verify that the deployed binary accepts it.
4. Test the recovered deployment on isolated ports: database integrity,
   registrations, routes, console access, recordings and media.
5. Switch the live owner only after calls are idle and rollback is prepared.

Run `python3 deploy/services/validate.py` for structural Compose checks. It
does not recover the database or verify live SIP/media behavior.

Rollback restores the retained container/configuration and reconciles the
latest state. Do not recreate the old service with an empty database or revert
recordings produced after migration.

See [PBX state findings](docs/services/box.md#active-call-and-rustpbx-codebases),
[the staged migration contract](deploy/services/README.md#telephony-and-pbx-persistence),
and [the branch registry](docs/services/branches.json).
