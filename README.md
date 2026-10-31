# golive collaboration Desk

`codex/service-desk` maintains the shared operator/agent workspace: notes,
tasks, control state, event updates and selected experiment panels.

## Functionality and interfaces

- Stores attributed notes and task-completion events.
- Exposes operator control state, including the owner's pause controls.
- Streams updates through `/api/events` and serves the Desk browser UI.
- Links simulator calls to Call Journal and manages simulator scenario files.
- Provides the configured Talker playground without making it the phone engine.

The observed box service listens on loopback and LAN port 8792. `/healthz` is
public to that listener; operational API access uses a participant token.

## Owned source

| Path | Responsibility |
| --- | --- |
| [cmd/desk](cmd/desk) | Process flags, state paths and service lifecycle |
| [internal/desk](internal/desk) | Notes, controls, events, scenarios and Talker integration |
| [internal/desk/ui](internal/desk/ui) | Desk browser UI |
| [scripts/desk.sh](scripts/desk.sh) | Authenticated operator/agent CLI access |

This branch changes Desk functionality and necessary shared-code integration
with coordinated review. Other services remain in the shared checkout and
keep their own maintenance workstreams.

## Build and isolated check

Use the Go version in [go.mod](go.mod), from the repository root:

```sh
go test ./internal/desk
go build -o /tmp/golive-desk ./cmd/desk
DESK_DEV_DIR=$(mktemp -d)
/tmp/golive-desk -addr 127.0.0.1:18792 -dir "$DESK_DEV_DIR" \
  -sim-dials '' -talker ''
```

This fresh instance disables simulator-call integration and the Talker panel.
Check it from another terminal:

```sh
curl --fail http://127.0.0.1:18792/healthz
```

`desk -dir <state-directory> -verify` checks the Desk log. Keep participant
tokens private rather than pasting authenticated command lines into reports.

## Dependencies and durable state

The live `~/golive-desk` directory contains the event log and participant tokens.
When enabled, simulator integration also reads credentials and writes scenarios
and last-run metadata in the simulator directory. Talker uses its configured
Mac endpoint; Journal must use its actual LAN URL on this deployment.

Preserve logs, attribution, tokens and simulator state across upgrades. Desk is
an independent native service; a phone restart does not constitute a Desk update.
The [Desk Compose service](deploy/services/box/core.compose.yml) is staged.
Follow [migration requirements](deploy/services/README.md) before adopting it,
and check authentication, note history and enabled panels after replacement.
