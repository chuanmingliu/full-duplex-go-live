# Speko SIP profile on the existing test stack

Verified on 2026-10-31 at 22:01 Asia/Shanghai. No call was placed.

## Installed path

```text
ThinkPad SIP adapter c3e049b (host network)
  → ThinkPad 127.0.0.1:8095
  → SSH reverse tunnel
  → Mac 127.0.0.1:8095 / golive-speko
  → Speko / gpt-live-1
```

The existing stack is `/home/chuanming/golive-sip-ab/32bc369` on
`chuanming@192.168.71.9`. Its native engine remains on port 9885 and its Call
Journal remains on port 9886. The adapter image remains
`golive-sip-ringback:c3e049b`, including carrier-ringback recording support.

The setup helper created these files with mode `0600`:

- `private/adapter-speko.json`: a copy of `private/adapter.json` with only
  `golive_url` changed to `http://127.0.0.1:8095`. No `golive_api_base` field.
- `private/speko-adapter.env`: the local adapter token matching the Mac bridge.
- `compose.speko.json`: selects that configuration and token for the adapter.

The Speko API key stays in the Mac credential file
`~/.config/golive/speko.env`; it was not copied to the ThinkPad.

## Tunnel lifecycle

The tunnel was started on the Mac with:

```sh
ssh -fNT -o BatchMode=yes -o ExitOnForwardFailure=yes \
  -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
  -o ControlMaster=yes -o ControlPath=/tmp/golive-speko-sip-tunnel.sock \
  -R 127.0.0.1:8095:127.0.0.1:8095 chuanming@192.168.71.9
```

Check it from the Mac:

```sh
ssh -S /tmp/golive-speko-sip-tunnel.sock -O check chuanming@192.168.71.9
```

This is a background SSH process, not a supervised service. It must be restored
after a reboot or connection failure. Do not stop it during a Speko call.

## Verified behavior and limits

On the ThinkPad, this command succeeded:

```sh
cd /home/chuanming/golive-sip-ab/32bc369
docker compose -f compose.json -f compose.speko.json \
  run --rm --no-deps adapter
```

Result: `Ready: dedicated agent is idle. No call placed.` The authenticated
bridge pool reported capacity 1 and zero active sessions. Effective Compose
configuration was checked for the matching adapter token, host networking,
existing image, and correct configuration path.

Readiness validates the local bridge and tunnel, not Speko account/model access,
live audio, or handset latency. No opening or model session was initiated.
The deployed adapter releases `/v1/opening` only after SIP answer. Do not reuse
the native engine's pre-dial opening warmup procedure with this bridge.

The original adapter configuration, original Compose file, production profile,
all 13 running containers, and native production listener were unchanged.
The Active Call/CRM dial path was not redirected.

## Select a path / rollback

The Speko path is opt-in: include **both** Compose files when deliberately
launching a future adapter call. A call command must explicitly select
`/private/adapter-speko.json` if it overrides the service's command; using
`/private/adapter.json` would select the native engine instead. Use a fresh call
ID and the existing exclusive call-receipt procedure.

To use the native engine again, omit `-f compose.speko.json` and select the
original `/private/adapter.json`. No existing service needs restarting.
When idle and no longer needed, stop only this tunnel from the Mac:

```sh
ssh -S /tmp/golive-speko-sip-tunnel.sock -O exit chuanming@192.168.71.9
```

See also the [bridge protocol runbook](../services/speko-sip.md).
