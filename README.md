# Cloudflare tunnel integration

`codex/service-cloudflared-integration` maintains the boundary between the
ThinkPad's existing Cloudflare tunnel and golive services. It is a
**deployment-integration branch**, not a fork of cloudflared. Tunnel
credentials, DNS configuration and access policies are private external state.

## Scope of this branch

| Path | Responsibility |
| --- | --- |
| `docs/services/box.md` | Observed host ownership and ingress boundary |
| `docs/services/catalog.json` | Service inventory and dependency classification |
| `docs/services/branches.json` | Maintenance branch identity |
| `deploy/services/README.md` | Application deployment boundaries and current external dependencies |

This branch does not contain a new tunnel Compose service or a complete copy
of the installed tunnel configuration. Application Compose changes do not
implicitly migrate or restart the tunnel.

## Observed deployment

The 2026-10-31 inventory found `cloudflared.service` running as a host systemd
service on the ThinkPad. Selected public hostname routes have historically
pointed at Aria, the browser demo and the Call Journal, but the inventory did
not re-audit DNS, route destinations or Cloudflare Access policy.

Those details must be read from the actual private tunnel configuration before
changing an origin. Do not infer current exposure from an older hostname table
or from an application listening successfully on the LAN.

## Maintenance

- Inspect `systemctl status cloudflared` and the installed unit/configuration
  paths. Avoid copying a token-bearing startup command into public output.
- Check the intended application origin locally before testing its published
  hostname. Separate origin failures from tunnel or access-policy failures.
- Preserve the existing hostname-to-origin mappings during application moves.
  The Journal currently listens on the box's LAN address, not localhost.
- Verify access behavior as well as HTTP availability after a route change;
  the Journal does not provide its own application login.
- Keep tunnel lifecycle separate from optional model experiments and simulator
  startup. None of those should create public routes automatically.

## Change and rollback record

Record the exact origin/route change and the prior private configuration
location, without including credentials or customer data. Apply a tunnel
change only after the replacement origin has passed its own checks.

Rollback restores the prior route/configuration and confirms that it reaches
the retained application origin. A service restart alone cannot restore a
changed DNS or external access policy; track those changes separately.

See [host ingress ownership](docs/services/box.md#adjacent-aria-stacks-and-host-ingress),
[deployment boundaries](deploy/services/README.md), and
[the branch registry](docs/services/branches.json).
