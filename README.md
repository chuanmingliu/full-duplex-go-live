# OpenShip edge proxy integration

`codex/service-edge-proxy-integration` maintains the ThinkPad's existing
OpenShip HTTP edge-proxy boundary. It is a **deployment-integration branch**,
not an upstream proxy fork. This ingress proxy is separate from golive's
`edge` speech-model gateway on port 8030.

## Scope of this branch

| Path | Responsibility |
| --- | --- |
| `docs/services/box.md` | Observed container ownership and persistent mount contract |
| `docs/services/catalog.json` | Image identity, mounts and service classification |
| `docs/services/branches.json` | Maintenance branch identity |
| `deploy/services/README.md` | Boundary between application Compose and existing ingress |

No new proxy Compose service or private site configuration is included here.
The running proxy remains independently managed until its effective sites,
certificates and lifecycle have been mapped for an explicit migration.

## Observed deployment

On 2026-10-31, `openship-edge` was a standalone Docker container using
`ghcr.io/oblien/openship-edge:0.6.6`, host networking and restart policy
`unless-stopped`. Its actual image ID is recorded in the service catalog.

| Host path | Container path | Purpose |
| --- | --- | --- |
| `/var/lib/openship/edge/acme` | `/var/www/acme` | ACME challenge files |
| `/etc/letsencrypt` | `/etc/letsencrypt` | Certificates and renewal-related state |
| `/opt/openship/static` | `/opt/openship/static` | Static site content |
| `/var/lib/openship/edge/sites-enabled` | `/usr/local/openresty/nginx/conf/sites-enabled` | Enabled site configuration |

All four mounts were writable in the observed container. The initial inventory
did not audit individual virtual hosts, TLS policy or ownership of every HTTP
listener. Do not derive those details from the image name alone.

## Maintenance

- Preserve image identity and all mounted state before recreating the proxy.
- Inspect effective site targets without publishing certificate private keys
  or credential-bearing configuration.
- Check each changed origin directly, then test its virtual host and TLS
  behavior through the proxy.
- Preserve ACME challenge reachability when changing routes or listeners.
- Keep proxy deployment separate from conversation/model services; changing
  the speech edge gateway does not require replacing this container.

For rollback, restore the prior proxy image and site configuration while
retaining current certificate/ACME state. Do not blindly revert renewed
certificates or replace mount directories with empty volumes. Verify the
restored route reaches the expected application after restart.

See [host ingress ownership](docs/services/box.md#adjacent-aria-stacks-and-host-ingress),
[deployment boundaries](deploy/services/README.md), and
[the branch registry](docs/services/branches.json).
