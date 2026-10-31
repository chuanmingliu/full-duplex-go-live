# GoLive service operations

`codex/service-operations` maintains the deployment layout and service ownership map for the
Mac and ThinkPad box. It is the integration point for Compose definitions,
host inventories, migration procedures and service maintenance branches.

## Branch scope

| Owned files | Responsibility |
| --- | --- |
| [deploy/services/](deploy/services/README.md) | Container build definition, separate Compose projects, example configuration and migration procedures |
| [docs/services/](docs/services/README.md) | Host inventories, codebase descriptions and service-to-branch registry |
| [.cursor/agents/golive-service-maintainer.md](.cursor/agents/golive-service-maintainer.md) | Reusable inventory and service-maintenance agent |
| [scripts/check_service_catalog.py](scripts/check_service_catalog.py) | Ownership, branch coverage and documentation-link validation |
| [.github/workflows/services.yml](.github/workflows/services.yml) | CI for service definitions and documentation |

Keep changes on this branch focused on deployment and cross-service integration.
Service implementation changes belong on their [respective maintenance
branches](docs/services/branches.json). Shared application source remains in the
repository so builds and integration checks have their dependencies.

## Deployment groups

| Group | Contents |
| --- | --- |
| ThinkPad core | Phone engine, CRM, Call Journal and Desk; optional browser demo, quotation service and edge gateway |
| ThinkPad telephony | Patched Active Call; a separately staged RustPBX migration |
| User simulator | Existing independent simulator project, private overlays and named volumes |
| Mac | Native Aria, Metal Talker and SSH runner; optional containerized mock development service |

The Compose definitions are **staged, not a completed live migration**. All owned
Compose services require explicit profiles. The [deployment runbook](deploy/services/README.md)
covers image builds, private configuration, state preservation, cutover and rollback.
RustPBX's database and recordings must be recovered from its current container
before recreation. Mac Metal inference retains native supervision.

## Validate

From the repository root, with Python 3 and the Docker Compose CLI installed:

```sh
python3 scripts/check_service_catalog.py
python3 deploy/services/validate.py
```

These checks use fixture configuration and do not start containers or call a
phone. They validate the catalog, local documentation links, Compose profiles,
mount contracts and isolation from inherited shell settings. They do not prove
provider readiness or live audio behavior.

## Maintenance workflow

1. Identify the service and source branch in the [branch registry](docs/services/branches.json).
2. Update its host inventory, dependencies, configuration and state contract when they change.
3. Validate deployment definitions and preserve the existing runtime before a cutover.
4. Record the actual deployed artifact and health result separately from branch HEAD.

Start with the [Mac runbook](docs/services/mac.md), [ThinkPad runbook](docs/services/box.md)
or [codebase guide](docs/services/codebases.md). Private environment files,
recordings, prompts, tokens and model weights stay outside Git.
