# Isolated simulator SIP gateway

This branch maintains the **dedicated Active Call gateway used by the simulated
customer**. It accepts a SIP call on the simulator's private Docker network,
notifies the simulator controller, and bridges call audio to the simulator's
authenticated realtime WebSocket.

**Scope:** `codex/service-simulator-gateway` in `full-duplex-go-live`.
The owned package is [deploy/simulator/activecall](deploy/simulator/activecall/README.md).
The customer engine, scenarios and evaluation remain documented in
[README.simulator.md](README.simulator.md).

This gateway is different from the patched Active Call container on the real
phone path. Do not replace production `active-call:patched` with this image.

## Source and dependencies

| File | Responsibility |
| --- | --- |
| `deploy/simulator/activecall/Dockerfile` | Fetch pinned upstream source and build the dedicated runtime |
| `deploy/simulator/activecall/Cargo.lock` | Locked Rust dependency graph |
| `deploy/simulator/activecall/handshake.patch` | Correct the custom realtime WebSocket handshake and suppress token-bearing URL logging |
| `deploy/simulator/activecall/render_config.py` | Write a new private gateway configuration without overwriting an existing one |
| `deploy/simulator/activecall/sip_fixture.py`, `fixtures/` | Independent synthetic SIP checks and lifecycle fixtures |

Upstream Active Call is pinned to
`eaf7ebc8f39193a289348989f6a09b0ace287da7` (0.3.100). The Dockerfile pins its base
images and builds with `cargo --locked`; normal builds do not regenerate the
lockfile. Docker and network access for the pinned source/dependencies are needed.

## Build and validate

Build only this gateway image from the repository root:

```sh
docker build -t golive-user-simulator-activecall:eaf7ebc-handshake-v1 \
  deploy/simulator/activecall
python3 -m unittest discover -s deploy/simulator -p 'test_*.py'
```

[The simulator deployment runbook](deploy/simulator/README.md) describes package
validation, controller configuration and the isolated SIP fixture. A successful
image build does not establish SIP readiness or bidirectional audio.

## Runtime contract

The existing parent Compose project is `golive-user-simulator`, defined in
[deploy/simulator/compose.yml](deploy/simulator/compose.yml). Its `config` service
creates the private configuration before `activecall` starts. The gateway runs
as UID 65532 with a read-only root filesystem and temporary `/tmp`.

Inside the dedicated network, the gateway uses HTTP 8080, SIP UDP 25060 and RTP
24000–24019. These ports are not published to the host. Only the simulator's
control listener is exposed on host loopback 18865. The simulator reports its
process health at `/livez` and gateway readiness at `/readyz`.

For maintenance, use the existing project name and **all deployed Compose
files/private overlays**. Address only service `activecall` when replacing or
stopping the gateway; never run a broad container cleanup. Restart aborts any
in-progress test call. Preserve the `gateway-config` and `run-data` named volumes,
private tokens/provider settings, current image ID and configuration for rollback.
Do not use `down --volumes`.

Each call must carry the exact prepared simulator run ID in the called SIP
extension. Webhook, control and media credentials are distinct. Keep token-bearing
URLs and raw transcripts out of logs and Git. Synthetic SIP success is evidence
of the isolated transport only; it does not measure the GSM network or handset.
